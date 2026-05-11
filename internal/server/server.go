package server

import (
	"context"
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
)

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
	mcpHandler  *mcp.Handler
	logger      *slog.Logger
	started     time.Time
	mu          sync.Mutex
	requestCounts map[string]map[string]int64 // endpoint -> status -> count
}

// New creates a new Server.
func New(cfg *config.Config, qc *qdrant.Client, ec *embedding.Client, lm *llm.Client, idx *indexer.Indexer, gp git.Provider, logger *slog.Logger) *Server {
	return &Server{
		cfg:           cfg,
		qdrant:        qc,
		embedder:      ec,
		llm:           lm,
		indexer:       idx,
		gitProvider:   gp,
		logger:        logger,
		started:       time.Now(),
		requestCounts: make(map[string]map[string]int64),
	}
}

// RegisterRoutes sets up all HTTP routes.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/version", s.handleVersion)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/recall", s.handleRecall)
	mux.HandleFunc("/ask", s.handleAsk)
	mux.HandleFunc("/draft", s.handleDraft)
	mux.HandleFunc("/audit", s.handleAudit)

	// MCP bolt-on
	s.mcpHandler = mcp.New(s.mcpTools(), s.mcpDispatch, s.logger)
	mux.Handle("/mcp", s.mcpHandler)
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
	fmt.Fprintf(&b, strings.Join([]string{
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
