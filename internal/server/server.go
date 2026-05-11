package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/git"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

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
	embedder    *embedding.Client
	llm         *llm.Client
	indexer     *indexer.Indexer
	gitProvider git.Provider
	ratelimit   *ratelimit.Limiter
	mcpHandler  *mcp.Handler
	logger      *slog.Logger
	started     time.Time
	mu          sync.Mutex
	requestCounts map[string]map[string]int64 // endpoint -> status -> count
}

// New creates a new Server.
func New(cfg *config.Config, qc *qdrant.Client, ec *embedding.Client, lm *llm.Client, idx *indexer.Indexer, gp git.Provider, rl *ratelimit.Limiter, logger *slog.Logger) *Server {
	s := &Server{
		cfg:           cfg,
		qdrant:        qc,
		embedder:      ec,
		llm:           lm,
		indexer:       idx,
		gitProvider:   gp,
		ratelimit:     rl,
		logger:        logger,
		started:       time.Now(),
		requestCounts: make(map[string]map[string]int64),
	}

	// Configure rate limits
	rl.SetLimit("/recall", cfg.RateLimitRecall)
	rl.SetLimit("/ask", cfg.RateLimitAsk)
	rl.SetLimit("/draft", cfg.RateLimitDraft)
	rl.SetLimit("/audit", cfg.RateLimitAudit)

	return s
}

// RegisterRoutes sets up all HTTP routes, wrapped with request ID tracing.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.withRequestID(s.handleHealth))
	mux.HandleFunc("/stats", s.withRequestID(s.handleStats))
	mux.HandleFunc("/version", s.withRequestID(s.handleVersion))
	mux.HandleFunc("/metrics", s.withRequestID(s.handleMetrics))
	mux.HandleFunc("/recall", s.withRequestID(s.withRateLimit("/recall", s.handleRecall)))
	mux.HandleFunc("/ask", s.withRequestID(s.withRateLimit("/ask", s.handleAsk)))
	mux.HandleFunc("/draft", s.withRequestID(s.withRateLimit("/draft", s.handleDraft)))
	mux.HandleFunc("/audit", s.withRequestID(s.withRateLimit("/audit", s.handleAudit)))

	// MCP bolt-on
	s.mcpHandler = mcp.New(s.mcpTools(), s.mcpDispatch, s.logger)
	mux.Handle("/mcp", s.mcpHandler)
}

// ── Request ID middleware ──────────────────────────────────────────────────────

// withRequestID wraps a handler with request ID tracing.
// Accepts X-Request-ID from the client, or generates a new UUID.
// Stores the ID in the request context and echoes it in the response.
func (s *Server) withRequestID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = newRequestID()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		next(w, r.WithContext(ctx))
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
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[5:7], b[7:9], b[9:11], b[11:16])
}

// log returns a logger with the request ID from ctx attached.
func (s *Server) log(ctx context.Context) *slog.Logger {
	if id := requestID(ctx); id != "" {
		return s.logger.With("request_id", id)
	}
	return s.logger
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
	fileCount, chunkCount, _, _, _, _ := s.indexer.Stats()

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
