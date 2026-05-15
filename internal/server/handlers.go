package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/tokenutil"
)

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
		resp["total_files"] = totalFiles
		resp["progress_pct"] = progressPct
		if totalFiles > 0 {
			resp["indexed_files"] = totalFiles * progressPct / 100
		} else {
			resp["indexed_files"] = 0
		}
	}

	writeJSON(w, 200, resp)
}

// ── /stats ─────────────────────────────────────────────────────────────────────

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	fileCount, _, lastIndexed, _, _, _ := s.indexer.Stats()

	// Get accurate chunk count from Qdrant (not in-process counter)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	chunkCount, err := s.qdrant.Count(ctx)
	cancel()
	chunkReliable := true
	if err != nil {
		s.logger.Warn("stats: qdrant count failed", "error", err)
		chunkCount = 0
		chunkReliable = false
	}

	writeJSON(w, 200, map[string]interface{}{
		"vault_path":        s.cfg.VaultPath,
		"indexed_files":     fileCount,
		"total_chunks":      chunkCount,
		"chunk_count_reliable": chunkReliable,
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
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64 KB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(w, 413, "INVALID_REQUEST", "request body exceeds 64 KB limit")
		} else {
			writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		}
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


// queryContext retrieves context text for /ask requests.
// Handles RAG retrieval, source dedup, auto-mode threshold, and full-mode fallback.
func (s *Server) queryContext(ctx context.Context, query string, mode string, topK int) (contextText string, sources []string, modeUsed string, err error) {
	modeUsed = mode

	if mode == "rag" || mode == "auto" {
		vector, err := s.embedder.EmbedSingle(ctx, query)
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("embedding failed: %w", err)
		}
		results, err := s.qdrant.Search(ctx, vector, uint64(topK), 0.0, "")
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("search failed: %w", err)
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

		if mode == "auto" && topScore >= float32(s.cfg.AutoThreshold) {
			modeUsed = "rag"
		} else if mode == "auto" {
			modeUsed = "full"
			contextText = "" // trigger full-vault load below
		}
	}

	if modeUsed == "full" && contextText == "" {
		vector, err := s.embedder.EmbedSingle(ctx, query)
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("embedding failed: %w", err)
		}
		results, err := s.qdrant.Search(ctx, vector, 50, 0.0, "")
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("search failed: %w", err)
		}

		// Collect top source files from RAG results
		topFiles := make(map[string]bool)
		var fileOrder []string
		for _, r := range results {
			if src, ok := r.Payload["source_file"]; ok {
				s := src.GetStringValue()
				if !topFiles[s] {
					topFiles[s] = true
					fileOrder = append(fileOrder, s)
				}
			}
		}

		// Load all chunks from those files via source_filter
		var b strings.Builder
		sourceSet := make(map[string]bool)
		for _, file := range fileOrder {
			if tokenutil.EstTokens(b.String()) > 8000 { // conservative context limit
				break
			}
			fileResults, err := s.qdrant.Search(ctx, vector, 100, 0.0, file)
			if err != nil {
				continue
			}
			for _, r := range fileResults {
				if src, ok := r.Payload["source_file"]; ok {
					s := src.GetStringValue()
					if !sourceSet[s] {
						sources = append(sources, s)
						sourceSet[s] = true
					}
				}
				if text, ok := r.Payload["text"]; ok {
					b.WriteString(text.GetStringValue())
					b.WriteString("\n\n")
				}
			}
		}
		contextText = b.String()
	}

	return contextText, sources, modeUsed, nil
}



func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	if !s.cfg.HasLLM() {
		writeError(w, 503, "LLM_NOT_CONFIGURED", "LLM not configured")
		return
	}

	var req askRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64 KB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(w, 413, "INVALID_REQUEST", "request body exceeds 64 KB limit")
		} else {
			writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		}
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

	// Use shared queryContext for RAG/auto/full modes (shared with MCP handler)
	contextText, sources, modeUsed, err := s.queryContext(ctx, req.Query, req.Mode, req.TopK)
	if err != nil {
		writeError(w, 502, "RETRIEVAL_ERROR", fmt.Sprintf("retrieval failed: %s", err))
		return
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
	Delete      bool   `json:"delete"`
}

func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	var req draftRequest
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024) // 10 MB for draft
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(w, 413, "INVALID_REQUEST", "request body exceeds 10 MB limit")
		} else {
			writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		}
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

	// Security: prevent path traversal — verify resolved path stays under vault root
	cleanPath := filepath.Clean(req.TargetPath)
	fullPath, err := safeVaultPath(s.cfg.VaultPath, cleanPath)
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
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
			"mode":   "pr",
			"pr_url": prURL,
			"branch": branch,
		})
		return
	}

	// Direct mode: write to filesystem
	fullPath = filepath.Join(s.cfg.VaultPath, cleanPath)

	if req.Delete {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			writeError(w, 500, "INTERNAL", fmt.Sprintf("delete failed: %s", err))
			return
		}
	} else if req.Content == "" {
		writeError(w, 400, "INVALID_INPUT", "content required unless delete=true")
		return
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

// ── /audit ─────────────────────────────────────────────────────────────────────

type auditRequest struct {
	StaleDays  int      `json:"stale_days"`
	Checks     []string `json:"checks"`
	SampleSize int      `json:"sample_size"`
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	var req auditRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64 KB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(w, 413, "INVALID_REQUEST", "request body exceeds 64 KB limit")
		} else {
			writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		}
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
			s.log(ctx).Error("audit: staleness check failed", "error", err)
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
