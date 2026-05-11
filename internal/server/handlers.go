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
			"mode":   "pr",
			"pr_url": prURL,
			"branch": branch,
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
