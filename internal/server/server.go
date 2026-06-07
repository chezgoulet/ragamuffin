package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"runtime/debug"
	"sync"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/google/uuid"
	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/extraction"
	"github.com/chezgoulet/ragamuffin/internal/git"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/ingress"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
	"github.com/chezgoulet/ragamuffin/internal/pruner"
	"github.com/chezgoulet/ragamuffin/web"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
	pb "github.com/qdrant/go-client/qdrant"
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
	qdrant      qdrant.FactStore
	facts       qdrant.FactStore
	embedder    embedding.Embedder
	llm         llm.Synthesizer
	indexers    *indexer.Manager
	gitProvider git.Provider
	ratelimit   *ratelimit.Limiter
	watcher     watcher.Watcher
	logStore    *logstore.Store
	pruner      *pruner.Pruner
	extractor   *extraction.Extractor
	apiDriver   *ingress.APIIngestDriver
	emitter     *events.Emitter // webhook + SSE event publisher
	mcpHandler  *mcp.Handler
	broker      *events.Broker  // SSE subscriber registry
	logger      *slog.Logger
	started     time.Time
	mu          sync.Mutex
	requestCounts map[string]map[string]int64 // endpoint -> status -> count

	shutdownCtx    context.Context // cancelled by Shutdown() (#420)
	shutdownCancel context.CancelFunc

	qdrantReconnecting bool
	qdrantMu          sync.RWMutex
}

// New creates a new Server.
func New(cfg *config.Config, qc qdrant.FactStore, factsQc qdrant.FactStore, ec embedding.Embedder, lm llm.Synthesizer, idxm *indexer.Manager, gp git.Provider, rl *ratelimit.Limiter, w watcher.Watcher, logStore *logstore.Store, pr *pruner.Pruner, emitter *events.Emitter, br *events.Broker, logger *slog.Logger, ext *extraction.Extractor, apiDrv *ingress.APIIngestDriver) *Server {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
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
		pruner:        pr,
		extractor:      ext,
		apiDriver:     apiDrv,
		emitter:       emitter,
		broker:        br,
		logger:        logger,
		started:       time.Now(),
		requestCounts: make(map[string]map[string]int64),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
	}

	// Configure rate limits
	rl.SetLimit("/recall", cfg.RateLimitRecall)
	rl.SetLimit("/ask", cfg.RateLimitAsk)
	rl.SetLimit("/draft", cfg.RateLimitDraft)
	rl.SetLimit("/audit", cfg.RateLimitAudit)
	rl.SetLimit("/v1/facts", cfg.RateLimitFacts)
	rl.SetLimit("/v1/logs", cfg.RateLimitLogs)
	rl.SetLimit("/v1/snapshot", cfg.RateLimitSnapshot)
	rl.SetLimit("/reindex", cfg.RateLimitReindex)
	rl.SetLimit("/v1/ingest", cfg.RateLimitIngest)
	rl.SetLimit("/v1/documents", cfg.RateLimitIngest)
	rl.SetLimit("/v1/chunks", cfg.RateLimitIngest)
	rl.SetLimit("/v1/pruner/auto-tune", cfg.RateLimitAudit)
	rl.SetLimit("/v1/pruner/config", cfg.RateLimitAudit)
	rl.SetLimit("/v1/review", cfg.RateLimitReview)

	// Ensure payload indexes for facts lifecycle queries
	s.ensureFactIndexes()

	// Migrate existing facts — set defaults on missing lifecycle fields
	s.migrateFacts()

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
	mux.HandleFunc("/events", s.withRequestID(s.handleEvents))
	mux.HandleFunc("/graph", s.withRequestID(s.handleGraph))

	// Static file server (catch-all for web UI)
	staticHandler := http.FileServer(http.FS(web.FS))
	mux.Handle("/static/", s.with404Check(staticHandler))
	mux.Handle("/", s.with404Check(staticHandler))

	if s.cfg.IsMultiTenant() {
		// Vault-prefixed routes (multi-tenant mode)
		mux.HandleFunc("/vault/{name}/recall", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/recall", s.handleVaultRecall))))
		mux.HandleFunc("/vault/{name}/ask", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/ask", s.handleVaultAsk))))
		mux.HandleFunc("/vault/{name}/draft", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/draft", s.handleVaultDraft))))
		mux.HandleFunc("/vault/{name}/audit", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/audit", s.handleVaultAudit))))
		mux.HandleFunc("/vault/{name}/v1/facts", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/v1/facts", s.handleVaultFacts))))
		mux.HandleFunc("/vault/{name}/v1/facts/{key}/graph", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/v1/facts", s.handleFactGraph))))
		mux.HandleFunc("/vault/{name}/v1/logs", s.withRequestID(s.withVaultRateLimit("/v1/logs", s.handleVaultLogs)))
		mux.HandleFunc("/vault/{name}/v1/snapshot", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/v1/snapshot", s.handleVaultSnapshot))))
		mux.HandleFunc("/vault/{name}/reindex", s.withRequestID(s.withQdrant(s.withVaultRateLimit("/reindex", s.handleReindex))))
		mux.HandleFunc("/vault/{name}/graph", s.withRequestID(s.withVault(s.handleGraph)))
		mux.HandleFunc("/vault/{name}/inbox", s.withRequestID(s.withVault(s.withRateLimit("/inbox", s.handleInbox))))
		mux.HandleFunc("/vault/{name}/inbox/{id}", s.withRequestID(s.withVault(s.withRateLimit("/inbox", s.handleInbox))))
	} else {
		// Single-tenant routes (v0.1–v0.3 behavior)
		mux.HandleFunc("/recall", s.withRequestID(s.withQdrant(s.withRateLimit("/recall", s.handleRecall))))
		mux.HandleFunc("/ask", s.withRequestID(s.withQdrant(s.withRateLimit("/ask", s.handleAsk))))
		mux.HandleFunc("/draft", s.withRequestID(s.withQdrant(s.withRateLimit("/draft", s.handleDraft))))
		mux.HandleFunc("/audit", s.withRequestID(s.withQdrant(s.withRateLimit("/audit", s.handleAudit))))

		// Pruner endpoints (auto-tune, config)
		mux.HandleFunc("/v1/pruner/auto-tune", s.withRequestID(s.withQdrant(s.withRateLimit("/v1/pruner/auto-tune", s.handlePrunerAutoTune))))
		mux.HandleFunc("/v1/pruner/config", s.withRequestID(s.withQdrant(s.withRateLimit("/v1/pruner/config", s.handlePrunerConfig))))
		mux.HandleFunc("/reindex", s.withRequestID(s.withQdrant(s.withRateLimit("/reindex", s.handleReindex))))
	}

	// Facts
	mux.HandleFunc("/v1/facts", s.withRequestID(s.withQdrant(s.withRateLimit("/v1/facts", s.handleFacts))))
	mux.HandleFunc("/v1/facts/{key}/graph", s.withRequestID(s.withQdrant(s.withRateLimit("/v1/facts", s.handleFactGraph))))
	mux.HandleFunc("/v1/auth/check", s.withRequestID(s.handleAuthCheck))

	// Chunk retrieval
	mux.HandleFunc("/v1/chunks/{chunk_id}", s.withRequestID(s.withQdrant(s.withRateLimit("/v1/chunks", s.handleChunkGet))))

	// Review queue (stats MUST be registered before the prefix match)
	mux.HandleFunc("/v1/review/stats", s.withRequestID(s.withRateLimit("/v1/review", s.handleReviewStats)))
	mux.HandleFunc("/v1/review", s.withRequestID(s.withRateLimit("/v1/review", s.handleReview)))

	// Vault operations — clear, create, list
	mux.HandleFunc("/v1/vaults/", s.withRequestID(s.handleVaultClear))

	// Ingest — content and conversation ingestion
	mux.HandleFunc("/v1/ingest", s.withRequestID(s.withRateLimit("/v1/ingest", s.handleIngest)))
	mux.HandleFunc("/v1/ingest/conversation", s.withRequestID(s.withRateLimit("/v1/ingest", s.handleIngestConversation)))
	mux.HandleFunc("/v1/documents", s.withRequestID(s.withRateLimit("/v1/documents", s.handleDocuments)))

	// Agent session endpoints (v0.5+/#162)
	mux.HandleFunc("/v1/sessions/batch", s.withRequestID(s.withRateLimit("/v1/ingest", s.handleBatchSessions)))
	mux.HandleFunc("/v1/sessions", s.withRequestID(s.withRateLimit("/v1/ingest", s.handleSessions)))
	mux.HandleFunc("/v1/sessions/", s.withRequestID(s.withRateLimit("/v1/ingest", s.handleSessionByID)))

	// Extraction pipeline (v0.8)
	mux.HandleFunc("/v1/extraction/stats", s.withRequestID(s.withRateLimit("/v1/logs", s.handleExtractionStats)))

	// Inbox — low-friction intake for agent observations (#313)
	mux.HandleFunc("/inbox", s.withRequestID(s.withRateLimit("/inbox", s.handleInbox)))
	mux.HandleFunc("/inbox/{id}", s.withRequestID(s.withRateLimit("/inbox", s.handleInbox)))

	// Logs
	mux.HandleFunc("/v1/logs", s.withRequestID(s.withRateLimit("/v1/logs", s.handleLogs)))

	// Snapshot
	mux.HandleFunc("/v1/snapshot", s.withRequestID(s.withQdrant(s.withRateLimit("/v1/snapshot", s.handleSnapshot))))

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
		s.logger.Info("api_key auth enabled")
		return auth.NewAPIKeyAuthenticator(
			s.cfg.AuthReadKey,
			s.cfg.AuthWriteKey,
			s.indexers.VaultNames(),
			s.cfg.IsMultiTenant(),
		)
	case auth.ModeJWT:
		s.logger.Info("jwt auth enabled", "issuer", s.cfg.AuthJWTIssuer)
		return auth.NewJWTAuthenticator(
			s.cfg.AuthJWTIssuer,
			s.cfg.AuthJWTAudience,
			s.cfg.AuthJWKSURL,
			s.logger,
		)
	case auth.ModeOIDC:
		s.logger.Info("oidc auth enabled", "issuer", s.cfg.AuthOIDCIssuer)
		a := auth.NewOIDCAuthenticator(
			s.cfg.AuthOIDCIssuer,
			s.cfg.AuthOIDCClientID,
			s.logger,
		)
		// Eager discovery: non-fatal, logs failure and retries lazily (#410)
		discoveryCtx, discoveryCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer discoveryCancel()
		a.StartDiscovery(discoveryCtx)
		return a
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
// withQdrant returns 503 if Qdrant is reconnecting, otherwise passes through.
// Endpoints that depend on Qdrant should be wrapped with this middleware.
func (s *Server) withQdrant(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.QdrantReconnecting() {
			writeJSON(w, 503, map[string]any{
				"status": "degraded",
				"detail": "qdrant reconnecting",
			})
			return
		}
		next(w, r)
	}
}

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
	return uuid.New().String()
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

// vaultPathFromContext resolves the vault root path from the request context.
func (s *Server) vaultPathFromContext(ctx context.Context) string {
	if name := vaultFromContext(ctx); name != "" && s.cfg.Vaults != nil {
		if vc, ok := s.cfg.Vaults[name]; ok {
			return vc.Path
		}
	}
	return s.cfg.VaultPath
}

// qdrantFor returns the per-vault Qdrant client from context,
// falling back to the server-wide client (for single-tenant mode).
func (s *Server) qdrantFor(ctx context.Context) qdrant.FactStore {
	if name := vaultFromContext(ctx); name != "" {
		if qc := s.indexers.GetClient(name); qc != nil {
			return qc
		}
	}
	return s.qdrant
}

// factsCollectionFor returns the facts collection name for the vault in context.
// Falls back to the global FactsCollection config value (single-tenant compat).
func (s *Server) factsCollectionFor(ctx context.Context) string {
	if name := vaultFromContext(ctx); name != "" {
		return s.cfg.FactsCollectionFor(name)
	}
	return s.cfg.FactsCollection
}

// factsQdrantFor returns the per-vault facts Qdrant client from context,
// falling back to the server-wide facts client.
func (s *Server) factsQdrantFor(ctx context.Context) qdrant.FactStore {
	if name := vaultFromContext(ctx); name != "" {
		if fc := s.indexers.GetFactClient(name); fc != nil {
			return fc
		}
	}
	return s.facts
}

// llmFor returns the per-vault LLM client from context,
// falling back to the server-wide LLM client for backward compatibility.
// Returns nil if neither is configured.
func (s *Server) llmFor(ctx context.Context) llm.Synthesizer {
	if name := vaultFromContext(ctx); name != "" {
		if lm := s.indexers.GetLLM(name); lm != nil {
			return lm
		}
	}
	return s.llm
}

// embeddingFor returns the per-vault embedding client from context,
// falling back to the server-wide embedder.
func (s *Server) embeddingFor(ctx context.Context) embedding.Embedder {
	if name := vaultFromContext(ctx); name != "" {
		if ec := s.indexers.GetEmbedder(name); ec != nil {
			return ec
		}
	}
	return s.embedder
}

// ensureFactIndexes creates Qdrant payload field indexes needed for fact
// lifecycle queries (status filter, expiry scan, etc.). Errors are logged
// but non-fatal — queries still work without indexes (just slower).
func (s *Server) ensureFactIndexes() {
	if s.facts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	factsCollection := s.cfg.FactsCollection

	// Detect Qdrant schema mismatch: vector size changed between runs.
	// Most common cause: switching embedding models with a different dimension.
	if expected := s.cfg.FactsVectorSize; expected > 0 {
		actual, err := s.facts.GetVectorSize(ctx, factsCollection)
		if err != nil {
			s.logger.Error("facts: cannot probe vector size — Qdrant may be unreachable",
				"collection", factsCollection, "error", err)
		} else if actual > 0 && actual != expected {
			s.logger.Error("facts: Qdrant vector size mismatch — data was indexed with one embedding model and now the config expects a different size",
				"collection", factsCollection,
				"existing_size", actual,
				"configured_size", expected,
				"hint", "Delete the Qdrant collection or recreate it with the new vector size (qdrant_collection_recreate).")
		}
	}

	// Also check the main chunk collection (different client, same Qdrant)
	if s.qdrant != nil {
		mainCollection := s.cfg.QdrantCollection
		if mainCollection != "" && mainCollection != factsCollection {
			expectedChunk := uint64(s.cfg.EmbeddingDims)
			if s.cfg.ChunkVectorSize > 0 {
				expectedChunk = s.cfg.ChunkVectorSize
			}
			if expectedChunk > 0 {
				actual, err := s.qdrant.GetVectorSize(ctx, mainCollection)
				if err != nil {
					s.logger.Warn("main collection: cannot probe vector size",
						"collection", mainCollection, "error", err)
				} else if actual > 0 && actual != expectedChunk {
					s.logger.Error("main collection: Qdrant vector size mismatch",
						"collection", mainCollection,
						"existing_size", actual,
						"configured_size", expectedChunk,
						"hint", "Delete the Qdrant collection or recreate it after changing the embedding model.")
				}
			}
		}
	}


	indexes := map[string]string{
		"status":            "keyword",
		"source_type":       "keyword",
		"confidence":        "float",
		"expires_at":        "keyword",
		"expires_at_unix":   "float",
		"fact_key":          "keyword",
		"conflict_resolved": "bool",
		"fact_tags":         "keyword",
		"ttl_days":          "float",
		"supersedes":        "keyword",
		"version":           "float",
		"superseded_by":     "float",
	}

	for field, fieldType := range indexes {
		if err := s.facts.CreatePayloadIndex(ctx, factsCollection, field, fieldType); err != nil {
			s.logger.Warn("facts payload index not created (may already exist)",
				"field", field, "error", err)
		}
	}
}

// withVault wraps a handler to validate vault access. Extracts the vault name
// from the request path (set by Go 1.22+ pattern matching), validates it against
// the configured vaults, and stores it in request context.
// with404Check wraps the static file handler. If the request expects JSON
// (Accept header), returns a JSON 404 instead of the SPA HTML catch-all.
func (s *Server) with404Check(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeError(w, 404, "NOT_FOUND", "endpoint not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

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

		// Enforce vault scope from auth claims
		if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
			if !claims.HasVaultAccess(name) {
				writeError(w, 403, "FORBIDDEN", fmt.Sprintf("access to vault %q denied", name))
				return
			}
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
	switch r.Method {
	case http.MethodGet:
		s.handleListVaults(w, r)
	case http.MethodPost:
		s.handleCreateVault(w, r)
	default:
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
	}
}

func (s *Server) handleListVaults(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.IsMultiTenant() {
		// Single-tenant: return the single vault with real stats
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
		writeJSON(w, 200, map[string]any{
			"vaults": []map[string]any{
				{
					"name":          "default",
					"path":          s.cfg.VaultPath,
					"indexed_files":  fileCount,
					"total_chunks":   chunkCount,
					"last_indexed":   lastIndexedStr,
					"indexing":       indexing,
				},
			},
		})
		return
	}

	// Multi-tenant: list all configured vaults with per-vault stats
	var vaults []map[string]any
	s.indexers.ForEach(func(name string, idx *indexer.Indexer) {
		fileCount, chunkCount, lastIndexed, indexing, _, _ := idx.Stats()
		var lastIndexedStr *string
		if !lastIndexed.IsZero() {
			formatted := lastIndexed.Format(time.RFC3339)
			lastIndexedStr = &formatted
		}
		s.mu.Lock()
		vc := s.cfg.Vaults[name]
		s.mu.Unlock()
		path := ""
		if vc != nil {
			path = vc.Path
		}
		vaults = append(vaults, map[string]any{
			"name":          name,
			"path":          path,
			"indexed_files": fileCount,
			"total_chunks":  chunkCount,
			"last_indexed":  lastIndexedStr,
			"indexing":      indexing,
		})
	})

	writeJSON(w, 200, map[string]any{
		"vaults": vaults,
	})
}

// handleCreateVault creates a new runtime vault (POST /vaults).
// Only available in multi-tenant mode.
func (s *Server) handleCreateVault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Description string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if req.Name == "" || req.Path == "" {
		writeError(w, 400, "INVALID_REQUEST", "name and path are required")
		return
	}

	// Validate vault name (lowercase alphanumeric + hyphens, 1-32 chars)
	if !config.ValidVaultName(req.Name) {
		writeError(w, 400, "INVALID_INPUT", "invalid vault name: must be 1-32 lowercase alphanumeric characters with optional hyphens")
		return
	}

	// Validate path: must be absolute and clean (no traversal)
	if !filepath.IsAbs(req.Path) {
		writeError(w, 400, "INVALID_INPUT", "path must be absolute")
		return
	}
	cleaned := filepath.Clean(req.Path)
	if cleaned != req.Path {
		writeError(w, 400, "INVALID_INPUT", "path must be clean (no extra slashes or dot components)")
		return
	}

	// Validate path is within VaultsRoot (#413)
	if s.cfg.VaultsRoot != "" {
		root := filepath.Clean(s.cfg.VaultsRoot)
		if !strings.HasPrefix(cleaned+"/", root+"/") {
			writeError(w, 400, "INVALID_INPUT", "path must be within RAGAMUFFIN_VAULTS_ROOT")
			return
		}
	}

	// Check vault config access with lock — all existence checks inside one lock
	s.mu.Lock()
	if s.cfg.Vaults == nil {
		s.mu.Unlock()
		writeError(w, 400, "INVALID_REQUEST", "not in multi-tenant mode")
		return
	}
	if _, exists := s.cfg.Vaults[req.Name]; exists {
		s.mu.Unlock()
		writeError(w, 409, "CONFLICT", "vault already exists")
		return
	}
	if s.indexers.Get(req.Name) != nil {
		s.mu.Unlock()
		writeError(w, 409, "CONFLICT", "vault index already exists")
		return
	}
	s.mu.Unlock()

	// Create vault directory on disk (I/O outside lock)
	if err := os.MkdirAll(req.Path, 0755); err != nil {
		writeError(w, 500, "INTERNAL", fmt.Sprintf("failed to create vault directory: %s", err))
		return
	}

	// Register new vault config — re-check under lock to prevent double-create race
	s.mu.Lock()
	if _, exists := s.cfg.Vaults[req.Name]; exists {
		s.mu.Unlock()
		// Clean up stale directory created above
		os.RemoveAll(req.Path)
		writeError(w, 409, "CONFLICT", "vault already exists (concurrent creation)")
		return
	}
	s.cfg.Vaults[req.Name] = &config.VaultConfig{
		Path: req.Path,
	}
	s.mu.Unlock()

	s.logger.Info("vault created at runtime", "name", req.Name, "path", req.Path)

	writeJSON(w, 201, map[string]interface{}{
		"name": req.Name,
		"path": req.Path,
	})
}

// ── /v1/vaults/{name}/clear ───────────────────────────────────────────────────

type vaultClearRequest struct {
	Confirm bool `json:"confirm"`
}

type vaultClearResponse struct {
	Status          string `json:"status"`
	Vault           string `json:"vault"`
	ChunksDeleted   int64  `json:"chunks_deleted"`
	FactsDeleted    int64  `json:"facts_deleted"`
	SessionsDeleted int64  `json:"sessions_deleted"`
}

func (s *Server) handleVaultClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	// Extract vault name from path: /v1/vaults/{name}/clear
	path := strings.TrimPrefix(r.URL.Path, "/v1/vaults/")
	parts := strings.SplitN(path, "/", 2)
	vaultName := parts[0]
	if vaultName == "" || len(parts) < 2 || parts[1] != "clear" {
		writeError(w, 400, "INVALID_REQUEST", "expected /v1/vaults/{name}/clear")
		return
	}

	var req vaultClearRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if !req.Confirm {
		writeError(w, 400, "INVALID_REQUEST", "confirm must be true to clear vault")
		return
	}

	// Get the indexer for this vault
	idx := s.indexers.Get(vaultName)
	if idx == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
		return
	}

	// Get the Qdrant client for this vault
	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeError(w, 500, "INTERNAL", "vault has no database connection")
		return
	}

	// 1. Delete all Qdrant points (chunks) for this vault
	s.logger.Info("clearing vault chunks", "vault", vaultName)
	chunksBefore, _ := qc.Count(r.Context())
	collectionName := qc.Collection()
	if err := qc.DeleteFiltered(r.Context(), collectionName, &pb.Filter{}); err != nil {
		s.logger.Error("failed to delete vault chunks", "vault", vaultName, "error", err)
		writeError(w, 500, "INTERNAL", "failed to clear vault chunks")
		return
	}
	chunksDeleted := int64(chunksBefore)

	// 2. Delete all facts for this vault from the per-vault facts collection
	factsQc := s.indexers.GetFactClient(vaultName)
	var factsCollection string
	var factsDeleted int64
	if factsQc != nil {
		factsBefore, _ := factsQc.Count(r.Context())
		factsCollection = factsQc.Collection()
		if err := factsQc.DeleteFiltered(r.Context(), factsCollection, &pb.Filter{}); err != nil {
			s.logger.Warn("failed to delete vault facts", "vault", vaultName, "error", err)
		}
		factsDeleted = int64(factsBefore)
	}

	// 3. Delete all sessions for this vault from logstore
	sessionsDeleted, _ := s.logStore.DeleteSessionsByVault(r.Context(), vaultName)

	writeJSON(w, 200, vaultClearResponse{
		Status:          "ok",
		Vault:           vaultName,
		ChunksDeleted:   chunksDeleted,
		FactsDeleted:    factsDeleted,
		SessionsDeleted: sessionsDeleted,
	})
}

// ── Rate limit middleware ──────────────────────────────────────────────────────

// withRateLimit wraps a handler with per-endpoint rate limiting.
func (s *Server) withRateLimit(endpoint string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := s.ratelimit.Allow(endpoint)
		if !allowed {
			retrySeconds := int(time.Until(retryAfter).Seconds())
			if retrySeconds < 1 {
				retrySeconds = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySeconds))
			writeError(w, 429, "RATE_LIMITED",
				fmt.Sprintf("Too many requests to %s. Retry after %d seconds", endpoint, retrySeconds))
			return
		}
		next(w, r)
	}
}

// ── Error helpers ──────────────────────────────────────────────────────────────

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

	// ── Pruner metrics ─────────────────────────────────────────────────
	if s.pruner != nil {
		scanCounts, flaggedCount, resolvedCount := s.pruner.Metrics()
		b.WriteString("# HELP ragamuffin_pruner_scans_total Total pruner scan runs by type.\n")
		b.WriteString("# TYPE ragamuffin_pruner_scans_total counter\n")
		for scanType, count := range scanCounts {
			fmt.Fprintf(&b, "ragamuffin_pruner_scans_total{scan_type=\"%s\"} %d\n", scanType, count)
		}
		b.WriteString("# HELP ragamuffin_pruner_facts_flagged_total Total facts flagged for review.\n")
		b.WriteString("# TYPE ragamuffin_pruner_facts_flagged_total counter\n")
		fmt.Fprintf(&b, "ragamuffin_pruner_facts_flagged_total %d\n", flaggedCount)
		b.WriteString("# HELP ragamuffin_pruner_facts_resolved_total Total review queue resolutions.\n")
		b.WriteString("# TYPE ragamuffin_pruner_facts_resolved_total counter\n")
		fmt.Fprintf(&b, "ragamuffin_pruner_facts_resolved_total %d\n", resolvedCount)
	}

	w.Write([]byte(b.String()))
}

// SetQdrantReconnecting marks whether Qdrant is in reconnection mode.
func (s *Server) SetQdrantReconnecting(v bool) {
	s.qdrantMu.Lock()
	defer s.qdrantMu.Unlock()
	s.qdrantReconnecting = v
}

// QdrantReconnecting returns whether Qdrant is currently reconnecting.
func (s *Server) QdrantReconnecting() bool {
	s.qdrantMu.RLock()
	defer s.qdrantMu.RUnlock()
	return s.qdrantReconnecting
}

func (s *Server) qdrantHealth() int {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := s.qdrant.Health(ctx); err != nil {
		return 0
	}
	return 1
}

// Shutdown cancels the server's background goroutines (fact supersede,
// access tracking, linkFactToChunks, etc.). Call before httpServer.Shutdown() (#420).
func (s *Server) Shutdown() {
	if s.shutdownCancel != nil {
		s.shutdownCancel()
	}
}
