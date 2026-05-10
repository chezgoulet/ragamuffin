package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/git"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	pb "github.com/qdrant/go-client/qdrant"
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
}

// New creates a new Server.
func New(cfg *config.Config, qc *qdrant.Client, ec *embedding.Client, lm *llm.Client, idx *indexer.Indexer, gp git.Provider, logger *slog.Logger) *Server {
	return &Server{
		cfg:         cfg,
		qdrant:      qc,
		embedder:    ec,
		llm:         lm,
		indexer:     idx,
		gitProvider: gp,
		logger:      logger,
		started:     time.Now(),
	}
}

// RegisterRoutes sets up all HTTP routes.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/stats", s.handleStats)
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

// ── /health ────────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.qdrant.Health(ctx); err != nil {
		writeError(w, 502, "QDRANT_UNREACHABLE", fmt.Sprintf("Qdrant unavailable: %s", err))
		return
	}

	_, _, _, indexing, progressPct, totalFiles := s.indexer.Stats()

	resp := map[string]interface{}{
		"status":  "ok",
		"qdrant":  "reachable",
		"indexing": indexing,
	}
	if indexing {
		resp["status"] = "indexing"
		resp["indexed_files"] = totalFiles * progressPct / 100
		resp["total_files"] = totalFiles
		resp["progress_pct"] = progressPct
	}

	writeJSON(w, 200, resp)
}

// ── /stats ─────────────────────────────────────────────────────────────────────

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	fileCount, chunkCount, lastIndexed, _, _, _ := s.indexer.Stats()

	writeJSON(w, 200, map[string]interface{}{
		"vault_path":        s.cfg.VaultPath,
		"indexed_files":     fileCount,
		"total_chunks":      chunkCount,
		"last_indexed":      lastIndexed.Format(time.RFC3339),
		"qdrant_collection": s.cfg.QdrantCollection,
		"embedding_provider": s.cfg.EmbeddingProvider,
		"uptime_seconds":    int(time.Since(s.started).Seconds()),
	})
}

// ── /recall ────────────────────────────────────────────────────────────────────

type recallRequest struct {
	Query          string  `json:"query"`
	TopK           int     `json:"top_k"`
	ScoreThreshold float64 `json:"score_threshold"`
	SourceFilter   string  `json:"source_filter"`
}

type recallResult struct {
	Text            string  `json:"text"`
	SourceFile      string  `json:"source_file"`
	Header          string  `json:"header"`
	ChunkIndex      int     `json:"chunk_index"`
	Score           float32 `json:"score"`
	FileLastUpdated string  `json:"file_last_updated"`
}

func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	var req recallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if req.Query == "" {
		writeError(w, 400, "INVALID_REQUEST", "query is required")
		return
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.TopK > 100 {
		req.TopK = 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Generate query embedding
	vector, err := s.embedder.EmbedSingle(ctx, req.Query)
	if err != nil {
		writeError(w, 502, "EMBEDDING_API_ERROR", fmt.Sprintf("embedding failed: %s", err))
		return
	}

	// Search Qdrant
	results, err := s.qdrant.Search(ctx, vector, uint64(req.TopK), float32(req.ScoreThreshold), req.SourceFilter)
	if err != nil {
		writeError(w, 502, "QDRANT_UNREACHABLE", fmt.Sprintf("search failed: %s", err))
		return
	}

	// Map results
	out := make([]recallResult, 0, len(results))
	var topScore float32
	for _, r := range results {
		payload := r.Payload
		res := recallResult{Score: r.Score}
		if r.Score > topScore {
			topScore = r.Score
		}
		if v, ok := payload["text"]; ok {
			res.Text = v.GetStringValue()
		}
		if v, ok := payload["source_file"]; ok {
			res.SourceFile = v.GetStringValue()
		}
		if v, ok := payload["header"]; ok {
			res.Header = v.GetStringValue()
		}
		if v, ok := payload["chunk_index"]; ok {
			res.ChunkIndex = int(v.GetIntegerValue())
		}
		if v, ok := payload["file_last_updated"]; ok {
			res.FileLastUpdated = v.GetStringValue()
		}
		out = append(out, res)
	}

	writeJSON(w, 200, map[string]interface{}{
		"results":   out,
		"top_score": topScore,
	})
}

// ── /ask ───────────────────────────────────────────────────────────────────────

type askRequest struct {
	Query string `json:"query"`
	Mode  string `json:"mode"`
	TopK  int    `json:"top_k"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	if !s.cfg.HasLLM() {
		writeError(w, 503, "LLM_NOT_CONFIGURED", "LLM is not configured — set RAGAMUFFIN_LLM_PROVIDER and RAGAMUFFIN_LLM_API_KEY")
		return
	}

	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if req.Query == "" {
		writeError(w, 400, "INVALID_REQUEST", "query is required")
		return
	}
	if req.Mode == "" {
		req.Mode = "auto"
	}
	if req.TopK <= 0 {
		req.TopK = 8
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	modeUsed := req.Mode
	var contextText string
	var sources []string

	if req.Mode == "rag" || req.Mode == "auto" {
		vector, err := s.embedder.EmbedSingle(ctx, req.Query)
		if err != nil {
			writeError(w, 502, "EMBEDDING_API_ERROR", fmt.Sprintf("embedding failed: %s", err))
			return
		}
		results, err := s.qdrant.Search(ctx, vector, uint64(req.TopK), 0.0, "")
		if err != nil {
			writeError(w, 502, "QDRANT_UNREACHABLE", fmt.Sprintf("search failed: %s", err))
			return
		}

		seenSources := make(map[string]bool)
		var topScore float32
		var b strings.Builder
		for _, r := range results {
			if r.Score > topScore {
				topScore = r.Score
			}
			if src, ok := r.Payload["source_file"]; ok {
				s := src.GetStringValue()
				if !seenSources[s] {
					sources = append(sources, s)
					seenSources[s] = true
				}
			}
			if text, ok := r.Payload["text"]; ok {
				b.WriteString(text.GetStringValue())
				b.WriteString("\n\n")
			}
		}
		contextText = b.String()

		// Auto mode: if confidence is high enough, use RAG. Otherwise, fall through to full.
		if req.Mode == "auto" && topScore >= 0.75 {
			modeUsed = "rag"
		} else if req.Mode == "auto" {
			modeUsed = "full"
			contextText = "" // trigger full-vault load below
		}
	}

	if modeUsed == "full" {
		// Load all chunks (up to context limit)
		// Simple approach: load all chunks from indexed files
		if contextText == "" {
			// Use larger search for full mode
			vector, err := s.embedder.EmbedSingle(ctx, req.Query)
			if err != nil {
				writeError(w, 502, "EMBEDDING_API_ERROR", fmt.Sprintf("embedding failed: %s", err))
				return
			}
			results, err := s.qdrant.Search(ctx, vector, 50, 0.0, "")
			if err != nil {
				writeError(w, 502, "QDRANT_UNREACHABLE", fmt.Sprintf("search failed: %s", err))
				return
			}

			seenSources := make(map[string]bool)
			var b strings.Builder
			for _, r := range results {
				if src, ok := r.Payload["source_file"]; ok {
					s := src.GetStringValue()
					if !seenSources[s] {
						sources = append(sources, s)
						seenSources[s] = true
					}
				}
				if text, ok := r.Payload["text"]; ok {
					b.WriteString(text.GetStringValue())
					b.WriteString("\n\n")
				}
			}
			contextText = b.String()
		}
	}

	answer, err := s.llm.Synthesize(ctx, req.Query, contextText)
	if err != nil {
		writeError(w, 502, "LLM_API_ERROR", fmt.Sprintf("LLM call failed: %s", err))
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"answer":    answer,
		"sources":   sources,
		"mode_used": modeUsed,
	})
}

// ── /draft ─────────────────────────────────────────────────────────────────────

type draftRequest struct {
	Title       string `json:"title"`
	Content     string `json:"content"`
	TargetPath  string `json:"target_path"`
	Mode        string `json:"mode"`
	Description string `json:"description"`
}

func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	var req draftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if req.Title == "" {
		writeError(w, 400, "INVALID_REQUEST", "title is required")
		return
	}
	if req.TargetPath == "" {
		writeError(w, 400, "INVALID_REQUEST", "target_path is required")
		return
	}
	if req.Mode == "" {
		req.Mode = "direct"
	}

	// Security: prevent path traversal
	cleanPath := filepath.Clean(req.TargetPath)
	if strings.HasPrefix(cleanPath, "..") {
		writeError(w, 400, "INVALID_REQUEST", "target_path must not escape vault root")
		return
	}

	if req.Mode == "pr" {
		if !s.cfg.HasGit() {
			writeError(w, 503, "GIT_NOT_CONFIGURED", "PR mode requires git provider configuration")
			return
		}
		// PR mode: use git provider REST API
		prURL, branch, err := s.createPR(req.Title, req.Content, cleanPath, req.Description)
		if err != nil {
			writeError(w, 502, "GIT_PROVIDER_ERROR", fmt.Sprintf("PR creation failed: %s", err))
			return
		}
		writeJSON(w, 200, map[string]interface{}{
			"mode":    "pr",
			"pr_url":  prURL,
			"branch":  branch,
		})
		return
	}

	// Direct mode: write to filesystem
	fullPath := filepath.Join(s.cfg.VaultPath, cleanPath)

	if req.Content == "" {
		// Delete
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			writeError(w, 500, "INTERNAL", fmt.Sprintf("delete failed: %s", err))
			return
		}
	} else {
		// Write
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			writeError(w, 500, "INTERNAL", fmt.Sprintf("mkdir failed: %s", err))
			return
		}
		if err := os.WriteFile(fullPath, []byte(req.Content), 0644); err != nil {
			writeError(w, 500, "INTERNAL", fmt.Sprintf("write failed: %s", err))
			return
		}
	}

	writeJSON(w, 200, map[string]interface{}{
		"mode":    "direct",
		"path":    cleanPath,
		"written": true,
	})
}

func (s *Server) createPR(title, content, path, description string) (prURL, branch string, err error) {
	if !s.cfg.HasGit() {
		return "", "", fmt.Errorf("git provider not configured")
	}

	repo := s.cfg.GitRepos
	if idx := strings.IndexByte(repo, ','); idx != -1 {
		repo = repo[:idx] // first repo in list
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", "", fmt.Errorf("no git repos configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return s.gitProvider.CreatePR(ctx, repo, s.cfg.GitBaseBranch, title, content, path, description)
}

// ── /audit ─────────────────────────────────────────────────────────────────────

type auditRequest struct {
	StaleDays   int      `json:"stale_days"`
	Checks      []string `json:"checks"`
	SampleSize  int      `json:"sample_size"`
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	var req auditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if req.StaleDays <= 0 {
		req.StaleDays = 90
	}
	if len(req.Checks) == 0 {
		req.Checks = []string{"stale", "semantic_conflict", "gap", "duplicate"}
	}
	if req.SampleSize <= 0 {
		req.SampleSize = 50
	}
	if req.SampleSize > 200 {
		req.SampleSize = 200
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	resp := map[string]interface{}{
		"checks_run": req.Checks,
	}

	checkSet := make(map[string]bool)
	for _, c := range req.Checks {
		checkSet[c] = true
	}

	// ── Staleness check ──
	if checkSet["stale"] {
		staleFiles, err := s.checkStaleness(req.StaleDays)
		if err != nil {
			s.logger.Error("audit: staleness check failed", "error", err)
		}
		resp["stale_files"] = staleFiles
	}

	// ── Gap check ──
	if checkSet["gap"] {
		gaps := s.checkGaps()
		resp["gaps"] = gaps
	}

	// ── Duplicate check ──
	if checkSet["duplicate"] {
		dupes := s.checkDuplicates()
		resp["duplicates"] = dupes
	}

	// ── Semantic conflict check ──
	if checkSet["semantic_conflict"] {
		if !s.cfg.HasLLM() {
			resp["semantic_conflicts"] = []interface{}{}
			resp["llm_calls"] = 0
		} else {
			conflicts, llmCalls := s.checkSemanticConflicts(ctx, req.SampleSize)
			resp["semantic_conflicts"] = conflicts
			resp["semantic_conflict_llm_calls"] = llmCalls
		}
	}

	writeJSON(w, 200, resp)
}

func (s *Server) checkStaleness(staleDays int) ([]map[string]interface{}, error) {
	var stale []map[string]interface{}
	cutoff := time.Now().AddDate(0, 0, -staleDays)

	err := filepath.Walk(s.cfg.VaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
			stale = append(stale, map[string]interface{}{
				"path":          relPath,
				"last_updated":  info.ModTime().Format(time.RFC3339),
				"days_stale":    int(time.Since(info.ModTime()).Hours() / 24),
			})
		}
		return nil
	})
	return stale, err
}

func (s *Server) checkGaps() []string {
	var gaps []string

	filepath.Walk(s.cfg.VaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}

		// Check if directory has any indexable files
		entries, err := os.ReadDir(absPath)
		if err != nil {
			return nil
		}

		hasFiles := false
		for _, e := range entries {
			if !e.IsDir() {
				hasFiles = true
				break
			}
		}

		if !hasFiles && len(entries) == 0 {
			relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
			if relPath != "." {
				gaps = append(gaps, relPath+"/ — directory exists but is empty")
			}
		} else if !hasFiles && len(entries) > 0 {
			// Has subdirectories but no files
			hasIndexable := false
			filepath.Walk(absPath, func(subPath string, subInfo os.FileInfo, subErr error) error {
				if subErr != nil || subInfo.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(subPath))
				if ext == ".md" || ext == ".txt" || ext == ".org" || ext == ".rst" || ext == "" {
					hasIndexable = true
					return filepath.SkipAll
				}
				return nil
			})
			if !hasIndexable {
				relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
				if relPath != "." {
					gaps = append(gaps, relPath+"/ — directory exists but contains no indexable files")
				}
			}
		}
		return nil
	})
	return gaps
}

func (s *Server) checkDuplicates() []map[string]interface{} {
	// Simple filename-based duplicate detection
	seen := make(map[string]string) // filename → first path
	var dupes []map[string]interface{}

	filepath.Walk(s.cfg.VaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
		if first, exists := seen[name]; exists {
			dupes = append(dupes, map[string]interface{}{
				"filename": name,
				"path_a":   first,
				"path_b":   relPath,
			})
		} else {
			seen[name] = relPath
		}
		return nil
	})
	return dupes
}

type conflictResult struct {
	ChunkA  map[string]string `json:"chunk_a"`
	ChunkB  map[string]string `json:"chunk_b"`
	Summary string            `json:"summary"`
}

func (s *Server) checkSemanticConflicts(ctx context.Context, sampleSize int) ([]conflictResult, int) {
	// Get all chunks from Qdrant
	scrollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Simplified: do a large search with a generic query to get a sample
	// In production, this would use proper scroll/pagination
	vector := make([]float32, 1536) // zero vector as generic query proxy
	results, err := s.qdrant.Search(scrollCtx, vector, uint64(sampleSize*2), 0.0, "")
	if err != nil {
		s.logger.Error("audit: conflict search failed", "error", err)
		return nil, 0
	}

	if len(results) < 2 {
		return nil, 0
	}

	// Pair chunks that share semantic space but come from different files
	type pair struct {
		a, b *pb.ScoredPoint
	}
	var pairs []pair
	sourceMap := make(map[string][]*pb.ScoredPoint)

	for _, r := range results {
		src := ""
		if v, ok := r.Payload["source_file"]; ok {
			src = v.GetStringValue()
		}
		sourceMap[src] = append(sourceMap[src], r)
	}

	// Pair chunks from different files
	var allChunks []*pb.ScoredPoint
	for _, chunks := range sourceMap {
		allChunks = append(allChunks, chunks...)
	}

	// Shuffle and pair
	rand.Shuffle(len(allChunks), func(i, j int) {
		allChunks[i], allChunks[j] = allChunks[j], allChunks[i]
	})

	for i := 0; i < len(allChunks)-1 && len(pairs) < sampleSize; i += 2 {
		a, b := allChunks[i], allChunks[i+1]
		srcA := ""
		srcB := ""
		if v, ok := a.Payload["source_file"]; ok {
			srcA = v.GetStringValue()
		}
		if v, ok := b.Payload["source_file"]; ok {
			srcB = v.GetStringValue()
		}
		if srcA != srcB && srcA != "" && srcB != "" {
			pairs = append(pairs, pair{a, b})
		}
	}

	// LLM compare each pair
	var conflicts []conflictResult
	llmCalls := 0

	for _, p := range pairs {
		textA := ""
		textB := ""
		if v, ok := p.a.Payload["text"]; ok {
			textA = v.GetStringValue()
		}
		if v, ok := p.b.Payload["text"]; ok {
			textB = v.GetStringValue()
		}
		srcA := ""
		srcB := ""
		if v, ok := p.a.Payload["source_file"]; ok {
			srcA = v.GetStringValue()
		}
		if v, ok := p.b.Payload["source_file"]; ok {
			srcB = v.GetStringValue()
		}

		if textA == "" || textB == "" {
			continue
		}

		llmCalls++
		summary, err := s.llm.Compare(ctx, textA, textB, srcA, srcB)
		if err != nil {
			s.logger.Warn("audit: LLM compare failed", "error", err)
			continue
		}
		if summary != "" {
			conflicts = append(conflicts, conflictResult{
				ChunkA:  map[string]string{"source_file": srcA, "text": truncate(textA, 200)},
				ChunkB:  map[string]string{"source_file": srcB, "text": truncate(textB, 200)},
				Summary: summary,
			})
		}
	}

	return conflicts, llmCalls
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ── Startup coordination ───────────────────────────────────────────────────────

// ── MCP Tools ──────────────────────────────────────────────────────────────────

func (s *Server) mcpTools() []mcp.ToolDefinition {
	return []mcp.ToolDefinition{
		{
			Name:        "ragamuffin_recall",
			Description: "Semantic search across the vault. Returns ranked chunks with source paths, scores, and timestamps.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":           map[string]interface{}{"type": "string", "description": "Natural-language search query"},
					"top_k":           map[string]interface{}{"type": "integer", "description": "Max results (1-100, default 10)"},
					"score_threshold": map[string]interface{}{"type": "number", "description": "Minimum similarity score 0.0-1.0"},
					"source_filter":   map[string]interface{}{"type": "string", "description": "Restrict to files under this path prefix"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "ragamuffin_ask",
			Description: "Full-context synthesis via LLM. Returns a prose answer with source citations.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":  map[string]interface{}{"type": "string", "description": "The question to answer"},
					"mode":   map[string]interface{}{"type": "string", "description": "auto, rag, or full (default: auto)"},
					"top_k":  map[string]interface{}{"type": "integer", "description": "RAG results to retrieve (1-50, default 8)"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "ragamuffin_draft",
			Description: "Write a file to the vault. Direct mode writes immediately; PR mode opens a pull request.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       map[string]interface{}{"type": "string", "description": "PR title if PR mode"},
					"content":     map[string]interface{}{"type": "string", "description": "Complete file content. Empty string to delete."},
					"target_path": map[string]interface{}{"type": "string", "description": "Vault path relative to vault root"},
					"mode":        map[string]interface{}{"type": "string", "description": "direct or pr (default: direct)"},
					"description": map[string]interface{}{"type": "string", "description": "Optional PR body"},
				},
				"required": []string{"title", "content", "target_path"},
			},
		},
		{
			Name:        "ragamuffin_audit",
			Description: "Vault health check. Scans for staleness, semantic conflicts, gaps, and duplicates.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"stale_days":  map[string]interface{}{"type": "integer", "description": "Days since last update to flag as stale (default: 90)"},
					"checks":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Which checks to run: stale, semantic_conflict, gap, duplicate"},
					"sample_size": map[string]interface{}{"type": "integer", "description": "Chunk pairs to LLM-compare (1-200, default 50)"},
				},
			},
		},
	}
}

func (s *Server) mcpDispatch(toolName string, args map[string]interface{}) (interface{}, error) {
	switch toolName {
	case "ragamuffin_recall":
		return s.mcpRecall(args)
	case "ragamuffin_ask":
		return s.mcpAsk(args)
	case "ragamuffin_draft":
		return s.mcpDraft(args)
	case "ragamuffin_audit":
		return s.mcpAudit(args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}
}

// mcpRecall handles ragamuffin_recall tool calls by reusing the REST handler's logic.
func (s *Server) mcpRecall(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	topK := 10
	if v, ok := args["top_k"].(float64); ok {
		topK = int(v)
	}

	var scoreThreshold float32
	if v, ok := args["score_threshold"].(float64); ok {
		scoreThreshold = float32(v)
	}

	sourceFilter, _ := args["source_filter"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vector, err := s.embedder.EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	results, err := s.qdrant.Search(ctx, vector, uint64(topK), scoreThreshold, sourceFilter)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	out := make([]map[string]interface{}, 0, len(results))
	var topScore float32
	for _, r := range results {
		res := map[string]interface{}{
			"score": r.Score,
		}
		if r.Score > topScore {
			topScore = r.Score
		}
		if v, ok := r.Payload["text"]; ok {
			res["text"] = v.GetStringValue()
		}
		if v, ok := r.Payload["source_file"]; ok {
			res["source_file"] = v.GetStringValue()
		}
		if v, ok := r.Payload["header"]; ok {
			res["header"] = v.GetStringValue()
		}
		if v, ok := r.Payload["chunk_index"]; ok {
			res["chunk_index"] = int(v.GetIntegerValue())
		}
		if v, ok := r.Payload["file_last_updated"]; ok {
			res["file_last_updated"] = v.GetStringValue()
		}
		out = append(out, res)
	}

	return map[string]interface{}{
		"results":   out,
		"top_score": topScore,
	}, nil
}

func (s *Server) mcpAsk(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if !s.cfg.HasLLM() {
		return nil, fmt.Errorf("LLM not configured — set RAGAMUFFIN_LLM_PROVIDER and RAGAMUFFIN_LLM_API_KEY")
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "auto"
	}

	topK := 8
	if v, ok := args["top_k"].(float64); ok {
		topK = int(v)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	modeUsed := mode
	var contextText string
	var sources []string

	if mode == "rag" || mode == "auto" {
		vector, err := s.embedder.EmbedSingle(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("embedding failed: %w", err)
		}
		results, err := s.qdrant.Search(ctx, vector, uint64(topK), 0.0, "")
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}

		seenSources := make(map[string]bool)
		var topScore float32
		var b strings.Builder
		for _, r := range results {
			if r.Score > topScore {
				topScore = r.Score
			}
			if src, ok := r.Payload["source_file"]; ok {
				s := src.GetStringValue()
				if !seenSources[s] {
					sources = append(sources, s)
					seenSources[s] = true
				}
			}
			if text, ok := r.Payload["text"]; ok {
				b.WriteString(text.GetStringValue())
				b.WriteString("\n\n")
			}
		}
		contextText = b.String()

		if mode == "auto" && topScore >= 0.75 {
			modeUsed = "rag"
		} else if mode == "auto" {
			modeUsed = "full"
			contextText = ""
		}
	}

	if modeUsed == "full" && contextText == "" {
		vector, _ := s.embedder.EmbedSingle(ctx, query)
		results, _ := s.qdrant.Search(ctx, vector, 50, 0.0, "")
		seenSources := make(map[string]bool)
		var b strings.Builder
		for _, r := range results {
			if src, ok := r.Payload["source_file"]; ok {
				s := src.GetStringValue()
				if !seenSources[s] {
					sources = append(sources, s)
					seenSources[s] = true
				}
			}
			if text, ok := r.Payload["text"]; ok {
				b.WriteString(text.GetStringValue())
				b.WriteString("\n\n")
			}
		}
		contextText = b.String()
	}

	answer, err := s.llm.Synthesize(ctx, query, contextText)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	return map[string]interface{}{
		"answer":    answer,
		"sources":   sources,
		"mode_used": modeUsed,
	}, nil
}

func (s *Server) mcpDraft(args map[string]interface{}) (interface{}, error) {
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	targetPath, _ := args["target_path"].(string)
	mode, _ := args["mode"].(string)
	description, _ := args["description"].(string)

	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if targetPath == "" {
		return nil, fmt.Errorf("target_path is required")
	}
	if mode == "" {
		mode = "direct"
	}

	// Security: prevent path traversal
	cleanPath := filepath.Clean(targetPath)
	if strings.HasPrefix(cleanPath, "..") {
		return nil, fmt.Errorf("target_path must not escape vault root")
	}

	if mode == "pr" {
		prURL, branch, err := s.createPR(title, content, cleanPath, description)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"mode":   "pr",
			"pr_url": prURL,
			"branch": branch,
		}, nil
	}

	fullPath := filepath.Join(s.cfg.VaultPath, cleanPath)

	if content == "" {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("delete failed: %w", err)
		}
	} else {
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir failed: %w", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write failed: %w", err)
		}
	}

	return map[string]interface{}{
		"mode":    mode,
		"path":    cleanPath,
		"written": true,
	}, nil
}

func (s *Server) mcpAudit(args map[string]interface{}) (interface{}, error) {
	staleDays := 90
	if v, ok := args["stale_days"].(float64); ok {
		staleDays = int(v)
	}

	var checks []string
	if raw, ok := args["checks"].([]interface{}); ok {
		for _, c := range raw {
			if s, ok := c.(string); ok {
				checks = append(checks, s)
			}
		}
	}
	if len(checks) == 0 {
		checks = []string{"stale", "semantic_conflict", "gap", "duplicate"}
	}

	sampleSize := 50
	if v, ok := args["sample_size"].(float64); ok {
		sampleSize = int(v)
	}

	resp := map[string]interface{}{
		"checks_run": checks,
	}

	checkSet := make(map[string]bool)
	for _, c := range checks {
		checkSet[c] = true
	}

	if checkSet["stale"] {
		staleFiles, err := s.checkStaleness(staleDays)
		if err != nil {
			s.logger.Error("MCP audit: staleness check failed", "error", err)
		}
		resp["stale_files"] = staleFiles
	}

	if checkSet["gap"] {
		gaps := s.checkGaps()
		resp["gaps"] = gaps
	}

	if checkSet["duplicate"] {
		dupes := s.checkDuplicates()
		resp["duplicates"] = dupes
	}

	if checkSet["semantic_conflict"] {
		if !s.cfg.HasLLM() {
			resp["semantic_conflicts"] = []interface{}{}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			conflicts, llmCalls := s.checkSemanticConflicts(ctx, sampleSize)
			resp["semantic_conflicts"] = conflicts
			resp["semantic_conflict_llm_calls"] = llmCalls
		}
	}

	return resp, nil
}
