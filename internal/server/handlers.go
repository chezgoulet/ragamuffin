package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
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

	qdrantStatus := "ok"
	if s.qdrant == nil {
		qdrantStatus = "connecting"
	} else {
		if err := s.qdrant.Health(ctx); err != nil {
			if s.QdrantReconnecting() {
				qdrantStatus = "reconnecting"
			} else {
				qdrantStatus = "down"
			}
		}
	}

	idx := s.indexerFor(r.Context())
	var indexing bool
	var progressPct, totalFiles int
	if idx != nil {
		_, _, _, indexing, progressPct, totalFiles = idx.Stats()
	}

	embeddingStatus := "unconfigured"
	if s.embedder != nil {
		embeddingStatus = "down"
		if err := s.embedder.Health(ctx); err == nil {
			embeddingStatus = "ok"
		}
	}

	llmStatus := "unconfigured"
	if s.llm != nil {
		llmStatus = "down"
		if err := s.llm.Health(ctx); err == nil {
			llmStatus = "ok"
		}
	}

	status := "ok"
	if qdrantStatus != "ok" || embeddingStatus == "down" || llmStatus == "down" {
		status = "degraded"
	}

	resp := map[string]any{
		"status":    status,
		"qdrant":    qdrantStatus,
		"embedding": embeddingStatus,
		"llm":       llmStatus,
		"indexing":  indexing,
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

	// ── Per-vault stats ──
	vaults := make(map[string]map[string]any)
	for _, name := range s.indexers.VaultNames() {
		vs := s.indexers.Stats(name)
		vaults[name] = map[string]any{
			"chunk_count":  vs.ChunkCount,
			"file_count":   vs.FileCount,
			"last_indexed": vs.LastIndexed.Format(time.RFC3339),
			"indexing":     vs.Indexing,
		}
	}
	if len(vaults) > 0 {
		resp["vaults"] = vaults
	}

	writeJSON(w, 200, resp)
}

// ── /v1/auth/check ────────────────────────────────────────────────────────────

func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	claims := auth.ClaimsFromContext(r.Context())
	result := map[string]any{
		"authenticated": claims != nil,
	}
	if claims != nil {
		result["access"] = claims.Access
		result["vaults"] = claims.Vaults
	}

	writeJSON(w, 200, result)
}

// ── /stats ─────────────────────────────────────────────────────────────────────

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	idx := s.indexerFor(r.Context())
	var fileCount int
	var lastIndexed time.Time
	if idx != nil {
		fileCount, _, lastIndexed, _, _, _ = idx.Stats()
	}

	// Get accurate chunk count from per-vault Qdrant client
	vaultName := vaultFromContext(r.Context())
	var qc qdrant.FactStore
	if vaultName != "" {
		qc = s.indexers.GetClient(vaultName)
	}
	if qc == nil {
		qc = s.qdrant
	}
	if qc == nil {
		writeJSON(w, 200, map[string]any{
			"status":        "degraded",
			"vault_path":    s.cfg.VaultPath,
			"qdrant_status": "connecting",
			"chunk_count":   0,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	chunkCount, err := qc.Count(ctx)
	cancel()
	chunkReliable := true
	if err != nil {
		s.logger.Warn("stats: qdrant count failed", "error", err)
		chunkCount = 0
		chunkReliable = false
	}

	vaultPath := s.cfg.VaultPath
	if vaultPath == "" && s.cfg.IsMultiTenant() {
		vaultPath = "multi-tenant (see /vaults)"
	}

	writeJSON(w, 200, map[string]any{
		"vault_path":           vaultPath,
		"indexed_files":        fileCount,
		"total_chunks":         chunkCount,
		"chunk_count_reliable": chunkReliable,
		"last_indexed":         lastIndexed.Format(time.RFC3339),
		"qdrant_collection":    s.cfg.QdrantCollection,
		"embedding_provider":   s.cfg.EmbeddingProvider,
		"uptime_seconds":       int(time.Since(s.started).Seconds()),
		"qdrant":               s.qdrantCollectionHealth(),
	})
}

// qdrantCollectionHealth queries Qdrant's REST API for collection-level
// health information (status, optimizer_status, segments_count).
// Returns nil on failure (non-fatal — just missing from the stats response).
func (s *Server) qdrantCollectionHealth() map[string]any {
	qdrantURL := s.cfg.QdrantURL
	if qdrantURL == "" {
		qdrantURL = "http://qdrant:6333"
	}

	// Build the collection name — use configured or default
	collectionName := s.cfg.QdrantCollection
	if collectionName == "" {
		collectionName = "ragamuffin_default"
	}

	restURL := fmt.Sprintf("%s/collections/%s", strings.TrimRight(qdrantURL, "/"), collectionName)
	// Qdrant gRPC default port is 6334, REST is 6333 — strip gRPC port
	restURL = strings.Replace(restURL, ":6334", ":6333", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, restURL, nil)
	if err != nil {
		return nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var qresp struct {
		Result struct {
			Status           string `json:"status"`
			OptimizerStatus  string `json:"optimizer_status"`
			SegmentsCount    int    `json:"segments_count"`
			PointsCount      uint64 `json:"points_count"`
			IndexedPoints    uint64 `json:"indexed_points"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &qresp); err != nil {
		return nil
	}

	info := map[string]any{
		"status":          qresp.Result.Status,
		"optimizer_status": qresp.Result.OptimizerStatus,
		"segments_count":  qresp.Result.SegmentsCount,
		"points_count":    qresp.Result.PointsCount,
		"indexed_points":  qresp.Result.IndexedPoints,
	}

	// Add a human-readable health summary
	switch {
	case qresp.Result.OptimizerStatus != "ok":
		info["health"] = "optimizing"
	case qresp.Result.Status != "green" && qresp.Result.Status != "grey":
		info["health"] = "degraded"
	default:
		info["health"] = "ready"
	}

	return info
}

// ── /recall ────────────────────────────────────────────────────────────────────

type recallFilters struct {
	PathPrefix string   `json:"path_prefix"`
	Tags       []string `json:"tags"`
	DateFrom   string   `json:"date_from"`
	DateTo     string   `json:"date_to"`
}

type recallRequest struct {
	Query          string         `json:"query"`
	Answer         bool           `json:"answer,omitempty"`
	TopK           int            `json:"top_k"`
	ScoreThreshold float64        `json:"score_threshold"`
	SourceFilter   string         `json:"source_filter"`
	Filters        *recallFilters `json:"filters,omitempty"`
	Detail         string         `json:"detail"`
	TimeFilter     string         `json:"time_filter,omitempty"` // active | active_at:date | all
}

type recallResult struct {
	ChunkID         string  `json:"chunk_id"`
	Text            string  `json:"text,omitempty"`
	FirstParagraph  string  `json:"first_paragraph,omitempty"`
	SourceFile      string  `json:"source_file"`
	Header          string  `json:"header"`
	ChunkIndex      int     `json:"chunk_index"`
	Score           float32 `json:"score"`
	FileLastUpdated string  `json:"file_last_updated"`
}

// recallFilter builds a Qdrant filter from the optional filters object.
// Returns nil if no new-style filters are set (falls through to legacy sourceFilter).
func recallFilter(req recallRequest) *pb.Filter {
	if req.Filters == nil {
		return nil
	}
	if req.Filters.PathPrefix == "" && len(req.Filters.Tags) == 0 && req.Filters.DateFrom == "" && req.Filters.DateTo == "" {
		return nil
	}

	var must []*pb.Condition

	// Path prefix → MatchText (Qdrant full-text prefix match)
	if req.Filters.PathPrefix != "" {
		must = append(must, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "source_file",
					Match: &pb.Match{
						MatchValue: &pb.Match_Text{
							Text: req.Filters.PathPrefix,
						},
					},
				},
			},
		})
	}

	// Tags → MatchKeyword for each tag (Must = AND)
	for _, tag := range req.Filters.Tags {
		if tag == "" {
			continue
		}
		must = append(must, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "tags",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{
							Keyword: tag,
						},
					},
				},
			},
		})
	}

	// Date range → DatetimeRange on file_last_updated
	if req.Filters.DateFrom != "" || req.Filters.DateTo != "" {
		dtr := &pb.DatetimeRange{}
		if req.Filters.DateFrom != "" {
			t, err := time.Parse(time.RFC3339, req.Filters.DateFrom)
			if err == nil {
				dtr.Gte = timestamppb.New(t)
			}
		}
		if req.Filters.DateTo != "" {
			t, err := time.Parse(time.RFC3339, req.Filters.DateTo)
			if err == nil {
				dtr.Lte = timestamppb.New(t)
			}
		}
		must = append(must, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key:           "file_last_updated",
					DatetimeRange: dtr,
				},
			},
		})
	}

	if len(must) == 0 {
		return nil
	}
	return &pb.Filter{Must: must}
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
	if req.Detail == "" {
		req.Detail = "l2"
	}
	if req.Detail != "l0" && req.Detail != "l1" && req.Detail != "l2" {
		writeError(w, 400, "INVALID_REQUEST", "detail must be one of: l0, l1, l2")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	out, topScore, err := s.doRecall(ctx, req)
	if err != nil {
		writeError(w, 502, "SEARCH_ERROR", err.Error())
		return
	}

	// Synthesize answer if requested (unique to REST — MCP doesn't expose answer mode)
	var answer string
	if req.Answer && len(out) > 0 {
		var b strings.Builder
		for i, r := range out {
			if i >= 5 {
				break
			}
			if r.Text != "" {
				b.WriteString(r.Text)
				b.WriteString("\n\n")
			} else if r.FirstParagraph != "" {
				b.WriteString(r.FirstParagraph)
				b.WriteString("\n\n")
			}
		}
		if b.Len() > 0 {
			ans, err := s.llmFor(ctx).Synthesize(ctx, req.Query, b.String())
			if err == nil {
				answer = ans
			} else {
				s.logger.Warn("answer synthesis failed", "error", err)
			}
		}
	}

	resp := map[string]any{
		"results":   out,
		"top_score": topScore,
	}
	if answer != "" {
		resp["answer"] = answer
	}

	// Enrich results with link index data (?enrich_links=true)
	if enrichLinksEnabled(r) {
		vault := vaultFromContext(r.Context())
		enriched, err := s.enrichChunksWithLinks(ctx, out, vault)
		if err == nil {
			resp["results"] = enriched
		} else {
			s.log(ctx).Warn("links: enrichment failed", "error", err)
		}
	}

	writeJSON(w, 200, resp)
}

// ── /v1/batch/recall ──────────────────────────────────────────────────────────

type batchRecallQuery struct {
	Query          string         `json:"query"`
	Vault          string         `json:"vault,omitempty"`
	TopK           int            `json:"top_k"`
	ScoreThreshold float64        `json:"score_threshold"`
	Detail         string         `json:"detail"`
	SourceFilter   string         `json:"source_filter,omitempty"`
	Filters        *recallFilters `json:"filters,omitempty"`
	TimeFilter     string         `json:"time_filter,omitempty"`
}

type batchRecallRequest struct {
	Queries []batchRecallQuery `json:"queries"`
}

type batchRecallEntry struct {
	QueryIndex int            `json:"query_index"`
	Results    []recallResult `json:"results"`
	TopScore   float32        `json:"top_score"`
	Error      string         `json:"error,omitempty"`
}

type batchRecallResponse struct {
	Results []batchRecallEntry `json:"results"`
}

func (s *Server) handleBatchRecall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	var req batchRecallRequest
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024) // 10 MB limit
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(w, 413, "INVALID_REQUEST", "request body exceeds 10 MB limit")
		} else {
			writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		}
		return
	}

	if len(req.Queries) == 0 {
		writeError(w, 400, "INVALID_REQUEST", "queries array is required")
		return
	}

	if len(req.Queries) > 100 {
		writeError(w, 400, "INVALID_REQUEST", "maximum 100 queries per batch request")
		return
	}

	// Validate all queries upfront — fail-fast on bad input
	for i, q := range req.Queries {
		if q.Query == "" {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("query[%d].query is required", i))
			return
		}
		if q.Detail != "" && q.Detail != "l0" && q.Detail != "l1" && q.Detail != "l2" {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("query[%d].detail must be one of: l0, l1, l2", i))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Second) // 5 min overall
	defer cancel()

	// Pre-allocate results slice by index for ordered output
	results := make([]batchRecallEntry, len(req.Queries))

	// Semaphore limits concurrent queries (10 parallel max)
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for i, q := range req.Queries {
		wg.Add(1)
		i, q := i, q // capture for goroutine

		go func() {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			// Per-query timeout: 30s alongside the 300s overall
			qCtx, qCancel := context.WithTimeout(ctx, 30*time.Second)
			defer qCancel()

			// Resolve vault
			vaultName := q.Vault
			if vaultName == "" {
				vaultName = "default"
			}

			// Get the Qdrant client — per-vault first, then context fallback
			qc := s.indexers.GetClient(vaultName)
			if qc == nil {
				qc = s.qdrantFor(r.Context())
			}

			// Resolve top_k
			topK := q.TopK
			if topK <= 0 {
				topK = 10
			}
			if topK > 100 {
				topK = 100
			}

			// Resolve detail default
			detail := q.Detail
			if detail == "" {
				detail = "l2"
			}

			// Get embedder — per-vault first, then context fallback
			ec := s.indexers.GetEmbedder(vaultName)
			if ec == nil {
				ec = s.embeddingFor(r.Context())
			}

			// Embed query
			vector, err := ec.EmbedSingle(qCtx, q.Query)
			if err != nil {
				results[i] = batchRecallEntry{
					QueryIndex: i,
					Error:      fmt.Sprintf("embedding failed: %s", err),
				}
				return
			}

			// Build filter (reuse recallFilter with a recallRequest wrapper)
			rr := recallRequest{
				Query:          q.Query,
				TopK:           topK,
				ScoreThreshold: q.ScoreThreshold,
				SourceFilter:   q.SourceFilter,
				Filters:        q.Filters,
				Detail:         detail,
				TimeFilter:     q.TimeFilter,
			}
			filter := recallFilter(rr)

			// Apply time filter
			if isTemporalRecall(q.TimeFilter) {
				dateTo := temporalRecallDate(q.TimeFilter)
				if dateTo != "" {
					conditions := []*pb.Condition{
						{
							ConditionOneOf: &pb.Condition_Field{
								Field: &pb.FieldCondition{
									Key: "file_last_updated",
									Range: &pb.Range{
										Lte: float64Ptr(parseRFC3339Unix(dateTo)),
									},
								},
							},
						},
					}
					if filter != nil {
						filter.Must = append(filter.Must, conditions...)
					} else {
						filter = &pb.Filter{Must: conditions}
					}
				}
			}

			// Search Qdrant
			searchResults, err := qc.Search(qCtx, vector, uint64(topK), float32(q.ScoreThreshold), q.SourceFilter, filter)
			if err != nil {
				results[i] = batchRecallEntry{
					QueryIndex: i,
					Error:      fmt.Sprintf("search failed: %s", err),
				}
				return
			}

			// Map results
			out := make([]recallResult, 0, len(searchResults))
			var topScore float32
			for _, r := range searchResults {
				payload := r.Payload
				res := recallResult{
					Score:   r.Score,
					ChunkID: r.Id.GetUuid(),
				}
				if r.Score > topScore {
					topScore = r.Score
				}
				if v, ok := payload["text"]; ok {
					res.Text = v.GetStringValue()
				}
				if v, ok := payload["first_paragraph"]; ok {
					res.FirstParagraph = v.GetStringValue()
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

				// Apply detail-level field filtering (same as single /recall)
				switch detail {
				case "l0":
					res.Text = ""
					res.FirstParagraph = ""
				case "l1":
					res.Text = ""
				}
				// l2: no change

				out = append(out, res)
			}

			results[i] = batchRecallEntry{
				QueryIndex: i,
				Results:    out,
				TopScore:   topScore,
			}
		}()
	}

	wg.Wait()

	writeJSON(w, 200, batchRecallResponse{Results: results})
}

// ── /v1/chunks/{chunk_id} ─────────────────────────────────────────────────────

func (s *Server) handleChunkGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	chunkID := r.PathValue("chunk_id")
	if chunkID == "" {
		writeError(w, 400, "INVALID_REQUEST", "chunk_id is required")
		return
	}

	if _, err := uuid.Parse(chunkID); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "chunk_id must be a valid UUID")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	resp, err := s.doGetChunk(ctx, chunkID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, 404, "NOT_FOUND", err.Error())
		} else {
			writeError(w, 502, "RETRIEVAL_ERROR", err.Error())
		}
		return
	}

	writeJSON(w, 200, resp)
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
		vector, err := s.embeddingFor(ctx).EmbedSingle(ctx, query)
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("embedding failed: %w", err)
		}
		results, err := s.qdrantFor(ctx).Search(ctx, vector, uint64(topK), 0.0, "", nil)
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
			src := ""
			if s, ok := r.Payload["source_file"]; ok {
				sv := s.GetStringValue()
				if !seenSources[sv] {
					sources = append(sources, sv)
					seenSources[sv] = true
				}
				src = sv
			}
			ci := int64(0)
			if c, ok := r.Payload["chunk_index"]; ok {
				ci = c.GetIntegerValue()
			}
			b.WriteString(fmt.Sprintf("[Source: %s | Chunk %d | Score: %.3f]\n", src, ci, r.Score))
			if text, ok := r.Payload["text"]; ok {
				b.WriteString(text.GetStringValue())
				b.WriteString("\n\n")
			}
		}
		contextText = b.String()

		// ── Fact retrieval for temporal context ──
		if s.facts != nil && contextText != "" {
			if factText := s.appendFactContext(ctx, vector, topK); factText != "" {
				contextText += "\n" + factText
			}
		}

		if mode == "auto" && topScore >= float32(s.cfg.AutoThreshold) {
			modeUsed = "rag"
		} else if mode == "auto" {
			modeUsed = "full"
			contextText = "" // trigger full-vault load below
		}
	}

	if modeUsed == "full" && contextText == "" {
		vector, err := s.embeddingFor(ctx).EmbedSingle(ctx, query)
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("embedding failed: %w", err)
		}
		results, err := s.qdrantFor(ctx).Search(ctx, vector, 50, 0.0, "", nil)
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("search failed: %w", err)
		}

		// Collect top source files from RAG results
		topFiles := make(map[string]bool)
		var fileOrder []string
		for _, r := range results {
			if src, ok := r.Payload["source_file"]; ok {
				sv := src.GetStringValue()
				if !topFiles[sv] {
					topFiles[sv] = true
					fileOrder = append(fileOrder, sv)
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
			fileResults, err := s.qdrantFor(ctx).Search(ctx, vector, 100, 0.0, file, nil)
			if err != nil {
				continue
			}
			for _, r := range fileResults {
				if src, ok := r.Payload["source_file"]; ok {
					sv := src.GetStringValue()
					if !sourceSet[sv] {
						sources = append(sources, sv)
						sourceSet[sv] = true
					}
				}
				var chunkSrc string
				if s, ok := r.Payload["source_file"]; ok {
					chunkSrc = s.GetStringValue()
				}
				chunkIdx := int64(0)
				if c, ok := r.Payload["chunk_index"]; ok {
					chunkIdx = c.GetIntegerValue()
				}
				b.WriteString(fmt.Sprintf("[Source: %s | Chunk %d]\n", chunkSrc, chunkIdx))
				if text, ok := r.Payload["text"]; ok {
					b.WriteString(text.GetStringValue())
					b.WriteString("\n\n")
				}
			}
		}
		contextText = b.String()

		// ── Fact retrieval for temporal context ──
		if s.facts != nil && contextText != "" {
			if factText := s.appendFactContext(ctx, vector, topK); factText != "" {
				contextText += "\n" + factText
			}
		}
	}

	return contextText, sources, modeUsed, nil
}

// appendFactContext retrieves facts relevant to a query and returns a formatted
// context block with temporal metadata (valid_from, valid_until).
func (s *Server) appendFactContext(ctx context.Context, vector []float32, topK int) string {

	factResults, err := s.factsQdrantFor(ctx).Search(ctx, vector, uint64(topK), 0.0, "", nil)
	if err != nil || len(factResults) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n── Facts ──\n\n")
	for _, fr := range factResults {
		payload := fr.GetPayload()
		key, _ := qutil.GetPayloadString(payload, "fact_key")
		value, _ := qutil.GetPayloadString(payload, "fact_value")
		validFrom, _ := qutil.GetPayloadString(payload, "valid_from")
		validUntil, _ := qutil.GetPayloadString(payload, "valid_until")
		createdAt, _ := qutil.GetPayloadString(payload, "created_at")

		b.WriteString(fmt.Sprintf("[Fact: %s | Created: %s", key, createdAt))
		if validFrom != "" {
			b.WriteString(fmt.Sprintf(" | Valid from: %s", validFrom))
		}
		if validUntil != "" {
			b.WriteString(fmt.Sprintf(" | Valid until: %s", validUntil))
		}
		b.WriteString(fmt.Sprintf(" | Score: %.3f]\n", fr.GetScore()))
		b.WriteString(value)
		b.WriteString("\n\n")
	}
	return b.String()
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	if !s.cfg.HasLLM() {
		writeError(w, 503, "SERVICE_UNAVAILABLE", "LLM not configured")
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

	answer, sources, modeUsed, err := s.doAsk(ctx, req.Query, req.Mode, req.TopK)
	if err != nil {
		writeError(w, 502, "ASK_ERROR", err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
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

	ctx := r.Context()

	result, err := s.doDraft(ctx, req)
	if err != nil {
		if strings.Contains(err.Error(), "write access required") {
			writeError(w, 403, "FORBIDDEN", err.Error())
		} else if strings.Contains(err.Error(), "git provider not configured") {
			writeError(w, 503, "GIT_NOT_CONFIGURED", err.Error())
		} else if strings.Contains(err.Error(), "PR creation failed") {
			writeError(w, 502, "GIT_PROVIDER_ERROR", err.Error())
		} else if strings.Contains(err.Error(), "delete failed") || strings.Contains(err.Error(), "mkdir failed") || strings.Contains(err.Error(), "write failed") {
			writeError(w, 500, "INTERNAL", err.Error())
		} else {
			writeError(w, 400, "INVALID_REQUEST", err.Error())
		}
		return
	}

	writeJSON(w, 200, result)
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

	resp := map[string]any{
		"checks_run": req.Checks,
	}

	checkSet := make(map[string]bool)
	for _, c := range req.Checks {
		checkSet[c] = true
	}

	vaultPath := s.vaultPathFromContext(r.Context())

	// ── Staleness check ──
	if checkSet["stale"] {
		staleFiles, err := s.checkStaleness(vaultPath, req.StaleDays)
		if err != nil {
			s.log(ctx).Error("audit: staleness check failed", "error", err)
		}
		resp["stale_files"] = staleFiles
	}

	// ── Gap check ──
	if checkSet["gap"] {
		gaps := s.checkGaps(vaultPath)
		resp["gaps"] = gaps
	}

	// ── Duplicate check ──
	if checkSet["duplicate"] {
		dupes := s.checkDuplicates(vaultPath)
		resp["duplicates"] = dupes
	}

	// ── Semantic conflict check ──
	if checkSet["semantic_conflict"] {
		if !s.cfg.HasLLM() {
			resp["semantic_conflicts"] = []any{}
			resp["llm_calls"] = 0
		} else {
			// Use vault-specific Qdrant client
			vaultName := vaultFromContext(r.Context())
			var auditQc qdrant.FactStore
			if vaultName != "" {
				auditQc = s.indexers.GetClient(vaultName)
			}
			conflicts, llmCalls := s.checkSemanticConflicts(ctx, auditQc, req.SampleSize)
			resp["semantic_conflicts"] = conflicts
			resp["semantic_conflict_llm_calls"] = llmCalls
		}
	}

	// ── Pruner health check ──
	if checkSet["pruner_health"] {
		if s.pruner != nil {
			health := s.pruner.Health()
			resp["pruner_health"] = health
		} else {
			resp["pruner_health"] = map[string]any{
				"enabled": false,
				"message": "pruner not configured",
			}
		}
	}

	// ── Fact conflict check ──
	if checkSet["fact_conflict"] {
		conflicts := s.checkFactConflicts(ctx)
		resp["fact_conflicts"] = conflicts
	}

	// ── Fact vs vault conflict check ──
	if checkSet["fact_vault_conflict"] {
		if !s.cfg.HasLLM() {
			resp["fact_vault_conflicts"] = []any{}
		} else {
			conflicts, llmCalls := s.checkFactVaultConflicts(ctx, req.SampleSize)
			resp["fact_vault_conflicts"] = conflicts
			resp["fact_vault_conflict_llm_calls"] = llmCalls
		}
	}

	writeJSON(w, 200, resp)
}

// ── /v1/refresh ───────────────────────────────────────────────────────────────

type refreshRequest struct {
	Vault string `json:"vault"`
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	var req refreshRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	vaultName := req.Vault
	if vaultName == "" {
		vaultName = "default"
	}

	ok := s.indexers.Reindex(vaultName)
	if !ok {
		if s.indexers.Get(vaultName) == nil {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
			return
		}
		writeError(w, 409, "CONFLICT", fmt.Sprintf("vault %q is already indexing", vaultName))
		return
	}

	writeJSON(w, 202, map[string]any{
		"status":  "accepted",
		"vault":   vaultName,
		"message": "Re-index started. Monitor progress via /health.",
	})
}

// ── /reindex ───────────────────────────────────────────────────────────────────

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	// Require write access
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	// Vault name comes from context (set by withVault middleware)
	vaultName := vaultFromContext(r.Context())
	if vaultName == "" {
		vaultName = "default"
	}

	ok := s.indexers.Reindex(vaultName)
	if !ok {
		// Check if vault doesn't exist (distinct from already indexing)
		if s.indexers.Get(vaultName) == nil {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
			return
		}
		writeError(w, 409, "CONFLICT", fmt.Sprintf("vault %q is already indexing", vaultName))
		return
	}

	writeJSON(w, 202, map[string]any{
		"status":  "accepted",
		"vault":   vaultName,
		"message": "Re-index started. Monitor progress via /health.",
	})
}

// ── Temporal recall helpers ────────────────────────────────────────────────

// isTemporalRecall returns true if the time_filter value is active or active_at:*.
// Validates the timestamp portion when active_at: is used.
func isTemporalRecall(mode string) bool {
	if mode == "active" {
		return true
	}
	if strings.HasPrefix(mode, "active_at:") {
		ts := strings.TrimPrefix(mode, "active_at:")
		if _, err := time.Parse(time.RFC3339, ts); err == nil {
			return true
		}
		if _, err := time.Parse("2006-01-02", ts); err == nil {
			return true
		}
	}
	return false
}

// temporalRecallDate extracts the date from "active_at:2006-01-02" or returns empty.
// Caller should verify isTemporalRecall first.
func temporalRecallDate(mode string) string {
	if strings.HasPrefix(mode, "active_at:") {
		return strings.TrimPrefix(mode, "active_at:")
	}
	if mode == "active" {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return ""
}

// parseRFC3339Unix parses RFC 3339 or YYYY-MM-DD and returns Unix seconds as float64.
func parseRFC3339Unix(s string) float64 {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return float64(t.Unix())
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return float64(t.Unix())
	}
	return 0
}
