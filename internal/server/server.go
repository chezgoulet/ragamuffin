package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"runtime/debug"
	"sync"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/git"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"
const vaultNameKey ctxKey = "vault_name"

var (
	Version   = "unknown"
	Commit    = "unknown"
	BuildDate = "unknown"
	GoVersion = "unknown"
)

// Server is the HTTP server.
type Server struct {
	cfg         *config.Config
	qdrant      *qdrant.Client
	facts       *qdrant.Client
	embedder    *embedding.Client
	llm         *llm.Client
	indexers    *indexer.Manager
	gitProvider git.Provider
	ratelimit   *ratelimit.Limiter
	watcher     watcher.Watcher
	logStore    *logstore.Store
	mcpHandler  *mcp.Handler
	logger      *slog.Logger
	started     time.Time
	mu          sync.Mutex
	requestCounts map[string]map[string]int64 // endpoint -> status -> count
}

// New creates a new Server.
func New(cfg *config.Config, qc *qdrant.Client, factsQc *qdrant.Client, ec *embedding.Client, lm *llm.Client, idxm *indexer.Manager, gp git.Provider, rl *ratelimit.Limiter, w watcher.Watcher, logStore *logstore.Store, logger *slog.Logger) *Server {
	s := &Server{
		cfg:           cfg,
		qdrant:        qc,
		facts:         factsQc,
		embedder:      ec,
		llm:           lm,
		indexers:      idxm,
		gitProvider:   gp,
		ratelimit:     rl,
		watcher:       w,
		logStore:      logStore,
		logger:        logger,
		started:       time.Now(),
		requestCounts: make(map[string]map[string]int64),
	}

	// Configure rate limits
	rl.SetLimit("/recall", cfg.RateLimitRecall)
	rl.SetLimit("/ask", cfg.RateLimitAsk)
	rl.SetLimit("/draft", cfg.RateLimitDraft)
	rl.SetLimit("/audit", cfg.RateLimitAudit)
	rl.SetLimit("/v1/facts", cfg.RateLimitFacts)
	rl.SetLimit("/v1/logs", cfg.RateLimitLogs)
	rl.SetLimit("/v1/snapshot", cfg.RateLimitSnapshot)

	return s
}

// Recovery wraps a handler to catch panics, log stack traces, and return 500.
func (s *Server) Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				s.logger.Error("handler panic recovered",
					"path", r.URL.Path,
					"method", r.Method,
					"panic", fmt.Sprintf("%v", rec),
					"stack", string(stack),
				)
				writeError(w, 500, "INTERNAL", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RegisterRoutes sets up all HTTP routes, wrapped with request ID tracing.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// Instance-wide routes (always registered)
	mux.HandleFunc("/health", s.withRequestID(s.handleHealth))
	mux.HandleFunc("/stats", s.withRequestID(s.handleStats))
	mux.HandleFunc("/version", s.withRequestID(s.handleVersion))
	mux.HandleFunc("/metrics", s.withRequestID(s.handleMetrics))
	mux.HandleFunc("/vaults", s.withRequestID(s.handleVaults))

	if s.cfg.IsMultiTenant() {
		// Vault-prefixed routes (multi-tenant mode)
		mux.HandleFunc("/vault/{name}/recall", s.withRequestID(s.withVaultRateLimit("/recall", s.handleVaultRecall)))
		mux.HandleFunc("/vault/{name}/ask", s.withRequestID(s.withVaultRateLimit("/ask", s.handleVaultAsk)))
		mux.HandleFunc("/vault/{name}/draft", s.withRequestID(s.withVaultRateLimit("/draft", s.handleVaultDraft)))
		mux.HandleFunc("/vault/{name}/audit", s.withRequestID(s.withVaultRateLimit("/audit", s.handleVaultAudit)))
		mux.HandleFunc("/vault/{name}/v1/facts", s.withRequestID(s.withVaultRateLimit("/v1/facts", s.handleVaultFacts)))
		mux.HandleFunc("/vault/{name}/v1/logs", s.withRequestID(s.withVaultRateLimit("/v1/logs", s.handleVaultLogs)))
		mux.HandleFunc("/vault/{name}/v1/snapshot", s.withRequestID(s.withVaultRateLimit("/v1/snapshot", s.handleVaultSnapshot)))
		mux.HandleFunc("/vault/{name}/reindex", s.withRequestID(s.withVault(s.handleReindex)))
	} else {
		// Single-tenant routes (v0.1–v0.3 behavior)
		mux.HandleFunc("/recall", s.withRequestID(s.withRateLimit("/recall", s.handleRecall)))
		mux.HandleFunc("/ask", s.withRequestID(s.withRateLimit("/ask", s.handleAsk)))
		mux.HandleFunc("/draft", s.withRequestID(s.withRateLimit("/draft", s.handleDraft)))
		mux.HandleFunc("/audit", s.withRequestID(s.withRateLimit("/audit", s.handleAudit)))
		mux.HandleFunc("/reindex", s.withRequestID(s.withRateLimit("/recall", s.handleReindex)))
	}

	// Facts
	mux.HandleFunc("/v1/facts", s.withRequestID(s.withRateLimit("/v1/facts", s.handleFacts)))

	// Logs
	mux.HandleFunc("/v1/logs", s.withRequestID(s.withRateLimit("/v1/logs", s.handleLogs)))

	// Snapshot
	mux.HandleFunc("/v1/snapshot", s.withRequestID(s.withRateLimit("/v1/snapshot", s.handleSnapshot)))

	// MCP bolt-on
	s.mcpHandler = mcp.New(s.mcpTools(), s.mcpDispatch, s.logger, Version)
	mux.Handle("/mcp", s.mcpHandler)
}

// authMiddleware returns the auth authenticator based on config.
func (s *Server) BuildAuth() auth.Authenticator {
	m, err := auth.ParseMode(s.cfg.AuthMode)
	if err != nil {
		s.logger.Warn("invalid auth mode, falling back to none", "mode", s.cfg.AuthMode, "error", err)
		return &auth.NoneAuthenticator{}
	}

	switch m {
	case auth.ModeNone:
		return &auth.NoneAuthenticator{}
	case auth.ModeAPIKey:
		// Issue #101: implement API key authenticator
		s.logger.Warn("api_key auth not yet implemented, falling back to none")
		return &auth.NoneAuthenticator{}
	case auth.ModeJWT:
		// Issue #102: implement JWT authenticator
		s.logger.Warn("jwt auth not yet implemented, falling back to none")
		return &auth.NoneAuthenticator{}
	default:
		return &auth.NoneAuthenticator{}
	}
}

// ── Request ID middleware ──────────────────────────────────────────────────────

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards Flush() to the underlying ResponseWriter if supported.
// Required for the streaming gzip snapshot handler — statusRecorder must
// satisfy http.Flusher so the gzip writer can flush incrementally.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withRequestID wraps a handler with request ID tracing and request counting.
// Accepts X-Request-ID from the client, or generates a new UUID.
// Stores the ID in the request context, echoes it in the response, and
// tracks request counts for /metrics.
func (s *Server) withRequestID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID != "" && len(reqID) > 256 {
			http.Error(w, "X-Request-ID header too long", http.StatusBadRequest)
			return
		}
		if reqID == "" {
			reqID = newRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)

		// Wrap in statusRecorder to capture the status code for metrics
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next(rec, r.WithContext(ctx))

		// Derive endpoint label from URL path
		endpoint := r.URL.Path
		s.countRequest(endpoint, rec.statusCode)
	}
}

// requestID extracts the request ID from a context.
func requestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func newRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	// Format as canonical UUID per RFC 9562: time_low-time_mid-time_hi_and_version-clock_seq-node
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// indexerFor returns the indexer for the vault in context, or the first/only
// indexer in single-tenant mode. Returns nil if no indexer is available.
func (s *Server) indexerFor(ctx context.Context) *indexer.Indexer {
	if s.indexers == nil {
		return nil
	}
	name := vaultFromContext(ctx)
	if name != "" {
		if idx := s.indexers.Get(name); idx != nil {
			return idx
		}
		return nil
	}
	// Single-tenant or no vault context: use first registered indexer
	for _, name := range s.indexers.VaultNames() {
		return s.indexers.Get(name)
	}
	return nil
}

// log returns a logger with the request ID from ctx attached.
func (s *Server) log(ctx context.Context) *slog.Logger {
	if id := requestID(ctx); id != "" {
		return s.logger.With("request_id", id)
	}
	return s.logger
}

// ── Vault middleware ───────────────────────────────────────────────────────────

// vaultNameFromRequest extracts the vault name from a PathValue pattern.
// Only called for routes registered as /vault/{name}/...
func vaultNameFromRequest(r *http.Request) string {
	return r.PathValue("name")
}

// vaultFromContext extracts the vault name previously stored in the context.
func vaultFromContext(ctx context.Context) string {
	if name, ok := ctx.Value(vaultNameKey).(string); ok {
		return name
	}
	return ""
}

// withVault wraps a handler to validate vault access. Extracts the vault name
// from the request path (set by Go 1.22+ pattern matching), validates it against
// the configured vaults, and stores it in request context.
func (s *Server) withVault(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := vaultNameFromRequest(r)
		if name == "" {
			writeError(w, 400, "INVALID_REQUEST", "missing vault name in path")
			return
		}
		if _, ok := s.cfg.Vaults[name]; !ok {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", name))
			return
		}
		ctx := context.WithValue(r.Context(), vaultNameKey, name)
		next(w, r.WithContext(ctx))
	}
}

// withVaultRateLimit combines vault validation with rate limiting. Uses the inner
// endpoint name (e.g., "/recall") for rate limit tracking, not the vault-prefixed path.
func (s *Server) withVaultRateLimit(endpoint string, next http.HandlerFunc) http.HandlerFunc {
	return s.withVault(s.withRateLimit(endpoint, next))
}

// ── Vault-prefixed handlers ────────────────────────────────────────────────────

// Each vault handler validates the vault, then delegates to the
// underlying single-vault handler. Per-vault resource separation
// (separate indexer, watcher, etc.) comes in issue #98.

func (s *Server) handleVaultRecall(w http.ResponseWriter, r *http.Request) {
	s.handleRecall(w, r)
}

func (s *Server) handleVaultAsk(w http.ResponseWriter, r *http.Request) {
	s.handleAsk(w, r)
}

func (s *Server) handleVaultDraft(w http.ResponseWriter, r *http.Request) {
	s.handleDraft(w, r)
}

func (s *Server) handleVaultAudit(w http.ResponseWriter, r *http.Request) {
	s.handleAudit(w, r)
}

func (s *Server) handleVaultFacts(w http.ResponseWriter, r *http.Request) {
	s.handleFacts(w, r)
}

func (s *Server) handleVaultLogs(w http.ResponseWriter, r *http.Request) {
	s.handleLogs(w, r)
}

func (s *Server) handleVaultSnapshot(w http.ResponseWriter, r *http.Request) {
	s.handleSnapshot(w, r)
}

// ── /vaults ─────────────────────────────────────────────────────────────────────

func (s *Server) handleVaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	if !s.cfg.IsMultiTenant() {
		// Single-tenant: return a single unnamed vault
		writeJSON(w, 200, map[string]interface{}{
			"vaults": []map[string]interface{}{
				{
					"name":         "default",
					"path":         s.cfg.VaultPath,
					"indexed_files": 0,
					"total_chunks":   0,
					"last_indexed":   nil,
					"indexing":       false,
				},
			},
		})
		return
	}

	// Multi-tenant: list all configured vaults with live stats
	idx := s.indexerFor(r.Context())
	var fileCount, chunkCount int
	var lastIndexed time.Time
	var indexing bool
	if idx != nil {
		fileCount, chunkCount, lastIndexed, indexing, _, _ = idx.Stats()
	}

	var lastIndexedStr *string
	if !lastIndexed.IsZero() {
		formatted := lastIndexed.Format(time.RFC3339)
		lastIndexedStr = &formatted
	}

	var vaults []map[string]interface{}
	for name, vc := range s.cfg.Vaults {
		vaults = append(vaults, map[string]interface{}{
			"name":          name,
			"path":          vc.Path,
			"indexed_files": fileCount,
			"total_chunks":  chunkCount,
			"last_indexed":  lastIndexedStr,
			"indexing":      indexing,
		})
	}

	writeJSON(w, 200, map[string]interface{}{
		"vaults": vaults,
	})
}

// ── Rate limit middleware ──────────────────────────────────────────────────────

// withRateLimit wraps a handler with per-endpoint rate limiting.
func (s *Server) withRateLimit(endpoint string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := s.ratelimit.Allow(endpoint)
		if !allowed {
			w.Header().Set("Retry-After", retryAfter.Format(time.RFC1123))
			writeError(w, 429, "RATE_LIMITED",
				fmt.Sprintf("Too many requests to %s. Retry after: %s", endpoint, retryAfter.Format(time.RFC3339)))
			return
		}
		next(w, r)
	}
}

// ── Error helpers ──────────────────────────────────────────────────────────────

type errResp struct {
	Error   bool   `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errResp{Error: true, Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ── /version ──────────────────────────────────────────────────────────────────

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]string{
		"version":    Version,
		"commit":     Commit,
		"build_date": BuildDate,
		"go_version": GoVersion,
	})
}

// ── /metrics ──────────────────────────────────────────────────────────────────

func (s *Server) countRequest(endpoint string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestCounts[endpoint] == nil {
		s.requestCounts[endpoint] = make(map[string]int64)
	}
	s.requestCounts[endpoint][fmt.Sprintf("%d", status)]++
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	idx := s.indexerFor(r.Context())
	var fileCount, chunkCount int
	if idx != nil {
		fileCount, chunkCount, _, _, _, _ = idx.Stats()
	}

	var b strings.Builder

	s.mu.Lock()
	b.WriteString("# HELP ragamuffin_requests_total Total HTTP requests by endpoint and status.\n")
	b.WriteString("# TYPE ragamuffin_requests_total counter\n")
	for endpoint, statuses := range s.requestCounts {
		for status, count := range statuses {
			fmt.Fprintf(&b, "ragamuffin_requests_total{endpoint=\"%s\",status=\"%s\"} %d\n", endpoint, status, count)
		}
	}
	s.mu.Unlock()

	b.WriteString("\n")
	fmt.Fprint(&b, strings.Join([]string{
		"# HELP ragamuffin_indexed_files Number of files in the index.",
		"# TYPE ragamuffin_indexed_files gauge",
		fmt.Sprintf("ragamuffin_indexed_files %d", fileCount),
		"",
		"# HELP ragamuffin_indexed_chunks Total chunks in the index.",
		"# TYPE ragamuffin_indexed_chunks gauge",
		fmt.Sprintf("ragamuffin_indexed_chunks %d", chunkCount),
		"",
		"# HELP ragamuffin_qdrant_health Qdrant connectivity (1 = healthy, 0 = down).",
		"# TYPE ragamuffin_qdrant_health gauge",
		fmt.Sprintf("ragamuffin_qdrant_health %d", s.qdrantHealth()),
		"",
	}, "\n"))
	w.Write([]byte(b.String()))
}

func (s *Server) qdrantHealth() int {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := s.qdrant.Health(ctx); err != nil {
		return 0
	}
	return 1
}
