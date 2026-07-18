package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/chezgoulet/ragamuffin/internal/retrieval"
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
		"status":         status,
		"qdrant":         qdrantStatus,
		"embedding":      embeddingStatus,
		"llm":            llmStatus,
		"indexing":       indexing,
		"uptime_seconds": int(time.Since(s.started).Seconds()),
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
			"chunk_count": vs.ChunkCount,
			"file_count":  vs.FileCount,
			"indexing":    vs.Indexing,
		}
		if !vs.LastIndexed.IsZero() {
			vaults[name]["last_indexed"] = vs.LastIndexed.Format(time.RFC3339)
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
			Status          string `json:"status"`
			OptimizerStatus string `json:"optimizer_status"`
			SegmentsCount   int    `json:"segments_count"`
			PointsCount     uint64 `json:"points_count"`
			IndexedPoints   uint64 `json:"indexed_points"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &qresp); err != nil {
		return nil
	}

	info := map[string]any{
		"status":           qresp.Result.Status,
		"optimizer_status": qresp.Result.OptimizerStatus,
		"segments_count":   qresp.Result.SegmentsCount,
		"points_count":     qresp.Result.PointsCount,
		"indexed_points":   qresp.Result.IndexedPoints,
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
	Mode           string         `json:"mode,omitempty"`        // dense | sparse | hybrid (default dense)
	TimeFilter     string         `json:"time_filter,omitempty"` // active | active_at:date | all
	Vaults         string         `json:"vaults,omitempty"`      // cross-vault: comma-separated names
	All            bool           `json:"all,omitempty"`         // cross-vault: search all vaults
	Expand         bool           `json:"expand,omitempty"`      // associative recall: also search facts (#794)
	Rewrite        string         `json:"rewrite,omitempty"`     // off | hyde | stepback | multiquery (default: server config)
	Rerank         bool           `json:"rerank,omitempty"`      // listwise LLM rerank of fused top-k (requires RAGAMUFFIN_RERANK)
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
	Vault           string  `json:"vault,omitempty"` // set by cross-vault recall (#792)
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

	// Resolve retrieval mode. Config default is dense (unchanged behavior);
	// hybrid fuses dense semantic + in-process BM25 lexical via RRF.
	if req.Mode == "" {
		req.Mode = s.cfg.RecallMode
	}
	if req.Mode == "" {
		req.Mode = "dense"
	}
	if req.Mode != "dense" && req.Mode != "sparse" && req.Mode != "hybrid" {
		writeError(w, 400, "INVALID_REQUEST", "mode must be one of: dense, sparse, hybrid")
		return
	}

	// Validate optional per-request query-rewrite override. Empty falls back to
	// the server default in doRecall.
	if req.Rewrite != "" {
		if _, ok := retrieval.ParseRewriteMode(req.Rewrite); !ok {
			writeError(w, 400, "INVALID_REQUEST", "rewrite must be one of: off, hyde, stepback, multiquery")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var out []recallResult
	var topScore float32

	// Cross-vault recall (#792): search all vaults or a specific list.
	if req.All || req.Vaults != "" {
		var vaultNames []string
		if req.All {
			vaultNames = s.indexers.VaultNames()
		} else {
			for _, v := range strings.Split(req.Vaults, ",") {
				v = strings.TrimSpace(v)
				if v != "" {
					vaultNames = append(vaultNames, v)
				}
			}
		}

		// Embed once, share across all vault searches.
		ec := s.embeddingFor(ctx)
		if ec == nil {
			writeError(w, 502, "EMBEDDING_API_ERROR", "embedding not configured")
			return
		}
		vector, err := ec.EmbedSingle(ctx, req.Query)
		if err != nil {
			writeError(w, 502, "EMBEDDING_API_ERROR", err.Error())
			return
		}

		// Build filter chain once (applies to all vaults).
		filter := recallFilter(req)
		if isTemporalRecall(req.TimeFilter) {
			dateTo := temporalRecallDate(req.TimeFilter)
			if dateTo != "" {
				cond := &pb.Condition{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "file_last_updated",
							Range: &pb.Range{
								Lte: float64Ptr(parseRFC3339Unix(dateTo)),
							},
						},
					},
				}
				if filter != nil {
					filter.Must = append(filter.Must, cond)
				} else {
					filter = &pb.Filter{Must: []*pb.Condition{cond}}
				}
			}
		}

		var mu sync.Mutex
		var wg sync.WaitGroup
		errCh := make(chan error, len(vaultNames))
		for _, vn := range vaultNames {
			wg.Add(1)
			go func(vaultName string) {
				defer wg.Done()
				vCtx := context.WithValue(ctx, vaultNameKey, vaultName)
				qc := s.qdrantFor(vCtx)
				if qc == nil {
					errCh <- fmt.Errorf("vault %s: no database connection", vaultName)
					return
				}
				results, err := qc.Search(vCtx, vector, uint64(req.TopK), float32(req.ScoreThreshold), req.SourceFilter, filter)
				if err != nil {
					errCh <- fmt.Errorf("vault %s: %w", vaultName, err)
					return
				}
				mapped := make([]recallResult, 0, len(results))
				for _, r := range results {
					payload := r.Payload
					res := recallResult{
						Score:   r.Score,
						ChunkID: r.Id.GetUuid(),
						Vault:   vaultName,
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
					if req.Detail == "l0" {
						res.Text = ""
						res.FirstParagraph = ""
					} else if req.Detail == "l1" {
						res.Text = ""
					}
					mapped = append(mapped, res)
				}
				mu.Lock()
				out = append(out, mapped...)
				mu.Unlock()
			}(vn)
		}
		wg.Wait()
		close(errCh)

		// Collect errors but don't fail — partial results are better than none.
		var errs []string
		for err := range errCh {
			errs = append(errs, err.Error())
		}
		if len(errs) > 0 {
			s.log(ctx).Warn("cross-vault recall: partial errors", "errors", errs)
		}

		// Sort all results by score descending and cap at top_k
		sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
		if len(out) > req.TopK {
			out = out[:req.TopK]
		}
		if len(out) > 0 {
			topScore = out[0].Score
		}
	} else {
		var err error
		out, topScore, err = s.doRecall(ctx, req)
		if err != nil {
			writeError(w, 502, "SEARCH_ERROR", err.Error())
			return
		}
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

	// Associative recall (#794): when expand=true, also search the facts
	// collection for semantically related facts and merge them into results.
	if req.Expand && !req.All {
		factsQc := s.factsQdrantFor(ctx)
		if factsQc != nil {
			ec := s.embeddingFor(ctx)
			if ec == nil {
				return
			}
			vector, err := ec.EmbedSingle(ctx, req.Query)
			if err == nil {
				factResults, err := factsQc.Search(ctx, vector, uint64(req.TopK), 0, "", nil)
				if err == nil && len(factResults) > 0 {
					for _, fr := range factResults {
						payload := fr.GetPayload()
						if payload == nil {
							continue
						}
						result := recallResult{
							ChunkID:    fr.Id.GetUuid(),
							SourceFile: payload["fact_key"].GetStringValue(),
							Text:       payload["fact_value"].GetStringValue(),
							Score:      fr.Score,
						}
						if sf, ok := payload["source_file"]; ok && sf.GetStringValue() != "" {
							result.SourceFile = sf.GetStringValue()
						}
						if h, ok := payload["header"]; ok {
							result.Header = h.GetStringValue()
						}
						out = append(out, result)
					}
					sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
					if len(out) > req.TopK {
						out = out[:req.TopK]
					}
					if len(out) > 0 {
						topScore = out[0].Score
					}
				}
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

	// Emit query processed event (#802)
	if s.emitter != nil {
		vault := vaultFromContext(r.Context())
		s.emitter.Emit(events.TypeQueryProcessed, events.QueryProcessedData{
			Query:   req.Query,
			Results: len(out),
			Vault:   vault,
		})
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
			if ec == nil {
				results[i] = batchRecallEntry{
					QueryIndex: i,
					Error:      "embedding not configured",
				}
				return
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

// ── /vault/{name}/v1/chunks — Chunk listing and pruning (#790) ───────────────

type chunkSummary struct {
	ChunkID         string `json:"chunk_id"`
	SourceFile      string `json:"source_file"`
	Header          string `json:"header"`
	ChunkIndex      int    `json:"chunk_index"`
	FileLastUpdated string `json:"file_last_updated"`
}

func (s *Server) handleChunksList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleChunksListGET(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		s.handleChunksDelete(w, r)
		return
	}
	writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET and DELETE are accepted")
}

func (s *Server) handleChunksListGET(w http.ResponseWriter, r *http.Request) {
	vaultName := vaultNameFromRequest(r)
	if vaultName == "" {
		vaultName = "default"
	}

	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
		return
	}

	limit := uint32(100)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 500 {
			limit = uint32(v)
		}
	}

	ctx := r.Context()
	chunks := make([]chunkSummary, 0)
	var scrollOffset *pb.PointId

	for uint32(len(chunks)) < limit {
		need := limit - uint32(len(chunks))
		if need > 200 {
			need = 200
		}
		points, nextOffset, err := qc.Scroll(ctx, need, scrollOffset)
		if err != nil {
			writeError(w, 502, "SCROLL_FAILED", fmt.Sprintf("scroll failed: %v", err))
			return
		}
		for _, p := range points {
			payload := p.GetPayload()
			c := chunkSummary{
				ChunkID: p.Id.GetUuid(),
			}
			if v, ok := payload["source_file"]; ok {
				c.SourceFile = v.GetStringValue()
			}
			if v, ok := payload["header"]; ok {
				c.Header = v.GetStringValue()
			}
			if v, ok := payload["chunk_index"]; ok {
				c.ChunkIndex = int(v.GetIntegerValue())
			}
			if v, ok := payload["file_last_updated"]; ok {
				c.FileLastUpdated = v.GetStringValue()
			}
			chunks = append(chunks, c)
		}
		if nextOffset == nil {
			break
		}
		scrollOffset = nextOffset
	}

	if chunks == nil {
		chunks = []chunkSummary{}
	}

	writeJSON(w, 200, map[string]any{
		"vault":  vaultName,
		"count":  len(chunks),
		"chunks": chunks,
	})
}

func (s *Server) handleChunksDelete(w http.ResponseWriter, r *http.Request) {
	vaultName := vaultNameFromRequest(r)
	if vaultName == "" {
		vaultName = "default"
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
		return
	}

	ctx := r.Context()

	// Build filter from query params
	source := r.URL.Query().Get("source")
	var filter *pb.Filter
	if source != "" {
		filter = &pb.Filter{
			Must: []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "source_file",
							Match: &pb.Match{
								MatchValue: &pb.Match_Keyword{Keyword: source},
							},
						},
					},
				},
			},
		}
	}
	// If no source filter, require confirm=true
	if filter == nil {
		confirm := r.URL.Query().Get("confirm")
		if confirm != "true" {
			writeError(w, 400, "INVALID_REQUEST", "bulk delete without source filter requires confirm=true")
			return
		}
	}

	before := qc.Collection()
	if err := qc.DeleteFiltered(ctx, before, filter); err != nil {
		writeError(w, 502, "DELETE_FAILED", fmt.Sprintf("delete failed: %v", err))
		return
	}

	writeJSON(w, 200, map[string]any{
		"deleted": true,
		"vault":   vaultName,
	})
}

// ── /ask ───────────────────────────────────────────────────────────────────────

type askRequest struct {
	Query   string `json:"query"`
	Mode    string `json:"mode"`
	TopK    int    `json:"top_k"`
	Cite    bool   `json:"cite"`
	Rewrite string `json:"rewrite,omitempty"` // off | hyde | stepback | multiquery (default: server config)
	Rerank  bool   `json:"rerank,omitempty"`  // listwise LLM rerank of retrieved chunks (requires RAGAMUFFIN_RERANK)
}

// explanationEntry is a single chunk's contribution to an /ask response (#804).
type explanationEntry struct {
	SourceFile string  `json:"source_file"`
	ChunkIndex int     `json:"chunk_index"`
	Score      float32 `json:"score"`
	Included   bool    `json:"included"`
	Text       string  `json:"text,omitempty"`
}

// queryContext retrieves context text for /ask requests.
// Handles RAG retrieval, source dedup, auto-mode threshold, and full-mode fallback.
func (s *Server) queryContext(ctx context.Context, query string, mode string, topK int, rewrite string, rerank bool) (contextText string, sources []string, modeUsed string, err error) {
	modeUsed = mode

	if mode == "rag" || mode == "auto" {
		// Route through doRecall so /ask honors the full retrieval pipeline:
		// query rewrite (HyDE/step-back/multi-query) and listwise rerank.
		results, topScore, err := s.doRecall(ctx, recallRequest{
			Query:   query,
			TopK:    topK,
			Detail:  "l2",
			Rewrite: rewrite,
			Rerank:  rerank,
		})
		if err != nil {
			return "", nil, modeUsed, fmt.Errorf("search failed: %w", err)
		}

		seenSources := make(map[string]bool)
		var b strings.Builder
		for _, r := range results {
			if !seenSources[r.SourceFile] && r.SourceFile != "" {
				sources = append(sources, r.SourceFile)
				seenSources[r.SourceFile] = true
			}
			b.WriteString(fmt.Sprintf("[Source: %s | Chunk %d | Score: %.3f]\n", r.SourceFile, r.ChunkIndex, r.Score))
			if r.Text != "" {
				b.WriteString(r.Text)
				b.WriteString("\n\n")
			}
		}
		contextText = b.String()

		// ── Fact retrieval for temporal context ──
		if s.facts != nil && contextText != "" {
			if vector, verr := s.embeddingFor(ctx).EmbedSingle(ctx, query); verr == nil {
				if factText := s.appendFactContext(ctx, vector, topK); factText != "" {
					contextText += "\n" + factText
				}
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

	// Reconsolidation-on-recall / accessibility decay (B1/B5): the /ask fact
	// context is a recall path, so stamp the returned facts.
	stampFactsRecalled(s, ctx, factResults)

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

	cite := req.Cite || s.cfg.AskCiteDefault

	if cite {
		answer, sources, citations, err := s.doAskCited(ctx, req.Query, req.TopK)
		if err != nil {
			writeError(w, 502, "ASK_ERROR", err.Error())
			return
		}
		resp := map[string]any{
			"answer":    answer,
			"sources":   sources,
			"mode_used": "rag",
			"citations": citations,
		}
		if s.emitter != nil {
			vault := vaultFromContext(r.Context())
			s.emitter.Emit(events.TypeQueryProcessed, events.QueryProcessedData{
				Query: req.Query, Results: len(sources), Vault: vault,
			})
		}
		writeJSON(w, 200, resp)
		return
	}

	answer, sources, modeUsed, explanation, err := s.doAskWithExplanation(ctx, req.Query, req.Mode, req.TopK, req.Rewrite, req.Rerank)
	if err != nil {
		writeError(w, 502, "ASK_ERROR", err.Error())
		return
	}

	resp := map[string]any{
		"answer":    answer,
		"sources":   sources,
		"mode_used": modeUsed,
	}
	if len(explanation) > 0 {
		resp["explanation"] = explanation
	}

	// Emit query processed event (#802)
	if s.emitter != nil {
		vault := vaultFromContext(r.Context())
		s.emitter.Emit(events.TypeQueryProcessed, events.QueryProcessedData{
			Query: req.Query, Results: len(sources), Vault: vault,
		})
	}

	writeJSON(w, 200, resp)
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
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	var req auditRequest
	// GET requests (used by the web UI) run the default check set with no body.
	// Optional query parameters allow tuning without a JSON payload.
	if r.Method == http.MethodGet {
		if v := r.URL.Query().Get("stale_days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				req.StaleDays = n
			}
		}
		if v := r.URL.Query().Get("checks"); v != "" {
			req.Checks = strings.Split(v, ",")
		}
		if v := r.URL.Query().Get("sample_size"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				req.SampleSize = n
			}
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64 KB
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			if err.Error() == "http: request body too large" {
				writeError(w, 413, "INVALID_REQUEST", "request body exceeds 64 KB limit")
			} else {
				writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
			}
			return
		}
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

	// UI-friendly aliases: the web UI reads staleness/contradictions/gaps.
	if v, ok := resp["stale_files"]; ok {
		resp["staleness"] = v
	}
	if v, ok := resp["semantic_conflicts"]; ok {
		resp["contradictions"] = v
	} else if v, ok := resp["fact_conflicts"]; ok {
		resp["contradictions"] = v
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

	// Reindex is async; drop the stale lexical index so it rebuilds lazily.
	s.InvalidateLexicalIndex(vaultName)

	writeJSON(w, 202, map[string]any{
		"status":  "accepted",
		"vault":   vaultName,
		"message": "Re-index started. Monitor progress via /health.",
	})
}

// ── /v1/debt — Knowledge Debt (#806) ──────────────────────────────────────────

func (s *Server) handleDebt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	ctx := r.Context()
	resp := map[string]any{}

	// Aggregate vault stats from indexers
	totalFiles := 0
	totalChunks := 0
	vaultCount := 0
	s.indexers.ForEach(func(name string, idx *indexer.Indexer) {
		files, chunks, lastIdx, indexing, _, _ := idx.Stats()
		totalFiles += files
		totalChunks += chunks
		vaultCount++
		_ = lastIdx
		_ = indexing
	})
	resp["vault_count"] = vaultCount
	resp["total_files"] = totalFiles
	resp["total_chunks"] = totalChunks

	// Aggregate review queue data from facts collection
	if s.facts != nil {
		reviewFilter := &pb.Filter{
			Must: []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "status",
							Match: &pb.Match{
								MatchValue: &pb.Match_Keyword{Keyword: "needs_review"},
							},
						},
					},
				},
			},
		}
		reviewPoints, err := s.facts.ScrollFiltered(ctx, s.cfg.FactsCollection, reviewFilter, 1000, "")
		if err == nil {
			staleCount := 0
			conflictCount := 0
			lowConfCount := 0
			oldest := ""
			for _, p := range reviewPoints {
				payload := p.GetPayload()
				if reasons, ok := payload["review_reasons"]; ok {
					reasonStr := reasons.GetStringValue()
					if strings.Contains(reasonStr, "stale") {
						staleCount++
					}
					if strings.Contains(reasonStr, "contradiction") {
						conflictCount++
					}
					if strings.Contains(reasonStr, "low_confidence") {
						lowConfCount++
					}
				}
				if created, ok := payload["created_at"]; ok {
					if oldest == "" || created.GetStringValue() < oldest {
						oldest = created.GetStringValue()
					}
				}
			}
			resp["review_queue_size"] = len(reviewPoints)
			resp["review_by_reason"] = map[string]int{
				"stale":          staleCount,
				"contradiction":  conflictCount,
				"low_confidence": lowConfCount,
			}
			if oldest != "" {
				resp["oldest_review_item"] = oldest
			}
		} else {
			resp["review_queue_size"] = 0
			s.log(ctx).Warn("debt: review scan failed", "error", err)
		}
	}

	// Aggregate pruner health
	if s.pruner != nil {
		health := s.pruner.Health()
		resp["pruner"] = health
	} else {
		resp["pruner"] = map[string]any{"enabled": false}
	}

	writeJSON(w, 200, resp)
}

// ── /v1/gaps — Knowledge Gap Mapping (#807) ────────────────────────────────────

func (s *Server) handleGaps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	_ = r.Context()
	resp := map[string]any{
		"covered_topics":  []string{},
		"poorly_covered":  []map[string]any{},
		"recommendations": []string{},
	}

	// Identify vaults with few files — potential coverage gaps
	s.indexers.ForEach(func(name string, idx *indexer.Indexer) {
		files, chunks, _, _, _, _ := idx.Stats()
		if files < 10 {
			resp["poorly_covered"] = append(resp["poorly_covered"].([]map[string]any), map[string]any{
				"vault":    name,
				"files":    files,
				"chunks":   chunks,
				"severity": "low_coverage",
			})
		}
	})

	// Recommendations based on gap analysis
	if len(resp["poorly_covered"].([]map[string]any)) > 0 {
		resp["recommendations"] = []string{
			"Add documentation to vaults with fewer than 10 files",
			"Schedule regular indexing of new source materials",
		}
	}

	writeJSON(w, 200, resp)
}

// ── /v1/agents/stats — Agent Contribution Heatmap (#808) ────────────────────────

func (s *Server) handleAgentStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	ctx := r.Context()

	// Agent name is optional — if provided, return stats for a single agent
	agentName := r.PathValue("name")

	if agentName != "" {
		s.handleSingleAgentStats(w, r, ctx, agentName)
		return
	}

	// Aggregate over all facts via Scroll
	if s.facts == nil {
		writeJSON(w, 200, map[string]any{"agents": []map[string]any{}})
		return
	}

	type agentAccum struct {
		FactCount int
		ConfSum   float64
		FlagCount int
	}
	agents := map[string]*agentAccum{}
	var offset string
	const pageSize uint32 = 200

	for {
		points, err := s.facts.ScrollFiltered(ctx, s.cfg.FactsCollection, nil, pageSize, offset)
		if err != nil {
			break
		}
		if len(points) == 0 {
			break
		}
		for _, p := range points {
			payload := p.GetPayload()
			sourceStr := payload["source"].GetStringValue()
			if sourceStr == "" {
				sourceStr = "unknown"
			}
			conf := payload["confidence"].GetDoubleValue()
			status := payload["status"].GetStringValue()

			if agents[sourceStr] == nil {
				agents[sourceStr] = &agentAccum{}
			}
			agents[sourceStr].FactCount++
			agents[sourceStr].ConfSum += conf
			if status == "needs_review" {
				agents[sourceStr].FlagCount++
			}
		}
		// Use last point ID as cursor
		if len(points) > 0 {
			offset = points[len(points)-1].GetId().GetUuid()
		}
	}

	agentList := make([]map[string]any, 0, len(agents))
	for name, acc := range agents {
		avgConf := 0.0
		if acc.FactCount > 0 {
			avgConf = acc.ConfSum / float64(acc.FactCount)
		}
		flagRate := 0.0
		if acc.FactCount > 0 {
			flagRate = float64(acc.FlagCount) / float64(acc.FactCount) * 100
		}
		agentList = append(agentList, map[string]any{
			"agent":          name,
			"facts_written":  acc.FactCount,
			"avg_confidence": avgConf,
			"flag_count":     acc.FlagCount,
			"flag_rate_pct":  flagRate,
		})
	}

	if agentList == nil {
		agentList = []map[string]any{}
	}

	writeJSON(w, 200, map[string]any{"agents": agentList})
}

func (s *Server) handleSingleAgentStats(w http.ResponseWriter, r *http.Request, ctx context.Context, agentName string) {
	if s.facts == nil {
		_ = ctx
		writeJSON(w, 200, map[string]any{"agent": agentName, "facts_written": 0})
		return
	}
	writeJSON(w, 200, map[string]any{
		"agent":         agentName,
		"facts_written": 0,
		"detail":        "per-agent detail requires fact-source aggregation",
	})
}

// ── /v1/verify — Knowledge Validation (#810) ──────────────────────────────────

type verifyRequest struct {
	Fact string `json:"fact"`
	TopK int    `json:"top_k"`
}

type verifySource struct {
	SourceFile string  `json:"source_file"`
	Text       string  `json:"text"`
	Score      float32 `json:"score"`
}

type verifyResponse struct {
	Status          string         `json:"status"`
	Supporting      []verifySource `json:"supporting_sources"`
	Conflicting     []verifySource `json:"conflicting_sources"`
	Confidence      float64        `json:"confidence"`
	ConflictSummary string         `json:"conflict_summary,omitempty"`
	LLMUsed         bool           `json:"llm_used"`
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	var req verifyRequest
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			writeError(w, 413, "INVALID_REQUEST", "request body exceeds 256 KB limit")
		} else {
			writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		}
		return
	}
	if req.Fact == "" {
		writeError(w, 400, "INVALID_REQUEST", "fact is required")
		return
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.TopK > 50 {
		req.TopK = 50
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := s.doVerify(ctx, req)
	if err != nil {
		writeError(w, 502, "VERIFY_ERROR", err.Error())
		return
	}

	writeJSON(w, 200, resp)
}

// ── /v1/embedding/project — 2D Embedding Explorer (#809) ──────────────────────

type vaultProjection struct {
	proj *embedding.Projection2D
	ts   time.Time
}

var (
	projectCache    = make(map[string]*vaultProjection)
	projectCacheMu  sync.RWMutex
	projectCacheTTL = 24 * time.Hour
)

func (s *Server) handleEmbedProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET and POST are accepted")
		return
	}

	vaultName := vaultNameFromRequest(r)
	if vaultName == "" {
		vaultName = "default"
	}

	// Return cached if fresh for this vault
	projectCacheMu.RLock()
	if cached, ok := projectCache[vaultName]; ok && time.Since(cached.ts) < projectCacheTTL {
		proj := cached.proj
		projectCacheMu.RUnlock()
		writeJSON(w, 200, proj)
		return
	}
	projectCacheMu.RUnlock()

	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
		return
	}

	ctx := r.Context()

	// Scroll all chunks with vectors using ScrollWithVectors
	var vectors [][]float32
	var labels []string
	var sources []string
	const pageSize uint32 = 200
	var scrollOffset *pb.PointId

	for {
		points, nextOffset, err := qc.ScrollWithVectors(ctx, pageSize, scrollOffset)
		if err != nil {
			writeError(w, 502, "SCROLL_FAILED", fmt.Sprintf("scroll failed: %v", err))
			return
		}
		for _, p := range points {
			payload := p.GetPayload()
			if payload == nil {
				continue
			}
			label := ""
			if h, ok := payload["header"]; ok {
				label = h.GetStringValue()
			}
			src := ""
			if sf, ok := payload["source_file"]; ok {
				src = sf.GetStringValue()
			}
			// Extract the actual embedding vector
			vec := p.GetVectors()
			if vec == nil {
				continue
			}
			v := vec.GetVector()
			if v == nil || len(v.GetData()) == 0 {
				continue
			}
			vectors = append(vectors, v.GetData())
			labels = append(labels, label)
			sources = append(sources, src)
		}
		if nextOffset == nil {
			break
		}
		scrollOffset = nextOffset
	}

	if len(vectors) == 0 {
		writeJSON(w, 200, &embedding.Projection2D{Points: []embedding.ProjectionPoint{}})
		return
	}

	projection, err := embedding.ProjectPCA(ctx, vectors, labels, sources)
	if err != nil {
		writeError(w, 500, "PROJECTION_FAILED", fmt.Sprintf("PCA failed: %v", err))
		return
	}

	// Cache and return
	projectCacheMu.Lock()
	projectCache[vaultName] = &vaultProjection{proj: projection, ts: time.Now()}
	projectCacheMu.Unlock()

	writeJSON(w, 200, projection)
}

// ── /v1/digest — Daily Knowledge Change Digest (#824) ────────────────────────

type digestVaultEntry struct {
	Vault   string `json:"vault"`
	Created int    `json:"created"`
	Updated int    `json:"updated"`
	Deleted int    `json:"deleted"`
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	ctx := r.Context()
	if s.logStore == nil {
		writeError(w, 503, "NO_LOGSTORE", "log store is not configured")
		return
	}
	since := time.Now().Add(-24 * time.Hour)

	entries, _, err := s.logStore.List(ctx, logstore.Filter{
		Since: since.Format(time.RFC3339),
		Limit: 1000,
	})
	if err != nil {
		writeError(w, 502, "LOG_QUERY_FAILED", fmt.Sprintf("log query failed: %v", err))
		return
	}

	byVault := map[string]*digestVaultEntry{}
	for _, e := range entries {
		agent := e.Agent
		if agent == "" {
			agent = "system"
		}
		if byVault[agent] == nil {
			byVault[agent] = &digestVaultEntry{Vault: agent}
		}
		switch e.Type {
		case "fact_created", "created", "ingest":
			byVault[agent].Created++
		case "fact_updated", "updated":
			byVault[agent].Updated++
		case "deleted", "fact_deleted":
			byVault[agent].Deleted++
		}
	}

	vaults := make([]digestVaultEntry, 0, len(byVault))
	for _, v := range byVault {
		vaults = append(vaults, *v)
	}

	writeJSON(w, 200, map[string]any{
		"period_hours": 24,
		"total_events": len(entries),
		"vaults":       vaults,
	})
}

// ── /v1/contradictions — Cross-Vault Fact Conflicts (#823) ────────────────────

type contradictionEntry struct {
	Key        string `json:"key"`
	VaultA     string `json:"vault_a"`
	ValueA     string `json:"value_a"`
	VaultB     string `json:"vault_b"`
	ValueB     string `json:"value_b"`
	UpdatedAtA string `json:"updated_at_a,omitempty"`
	UpdatedAtB string `json:"updated_at_b,omitempty"`
}

func (s *Server) handleContradictions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	ctx := r.Context()
	vaultNames := s.indexers.VaultNames()
	if len(vaultNames) < 2 {
		writeJSON(w, 200, map[string]any{
			"contradictions": []contradictionEntry{},
			"note":           "need at least 2 vaults for cross-vault contradiction detection",
		})
		return
	}

	// Collect all fact keys per vault by scrolling facts
	type vaultFact struct {
		Value     string
		UpdatedAt string
	}
	vaultFacts := make(map[string]map[string]vaultFact)
	truncated := false

	for _, vn := range vaultNames {
		qc := s.indexers.GetFactClient(vn)
		if qc == nil {
			continue
		}
		facts := map[string]vaultFact{}
		collection := qc.Collection()
		var offset string
		const pageSize uint32 = 200
		const maxFactsPerVault = 10000

		for {
			points, err := qc.ScrollFiltered(ctx, collection, nil, pageSize, offset)
			if err != nil {
				break
			}
			if len(points) == 0 {
				break
			}
			for _, p := range points {
				payload := p.GetPayload()
				if payload == nil {
					continue
				}
				key := payload["fact_key"].GetStringValue()
				if key == "" {
					continue
				}
				facts[key] = vaultFact{
					Value:     payload["fact_value"].GetStringValue(),
					UpdatedAt: payload["updated_at"].GetStringValue(),
				}
				if len(facts) >= maxFactsPerVault {
					truncated = true
					break
				}
			}
			if truncated {
				break
			}
			if len(points) > 0 {
				offset = points[len(points)-1].GetId().GetUuid()
			}
		}
		if len(facts) > 0 {
			vaultFacts[vn] = facts
		}
	}

	// Compare: for each fact key present in multiple vaults, check value
	var contradictions []contradictionEntry
	vaultNamesList := make([]string, 0, len(vaultFacts))
	for vn := range vaultFacts {
		vaultNamesList = append(vaultNamesList, vn)
	}

	for i := 0; i < len(vaultNamesList); i++ {
		for j := i + 1; j < len(vaultNamesList); j++ {
			va := vaultNamesList[i]
			vb := vaultNamesList[j]
			for key, fva := range vaultFacts[va] {
				if fvb, ok := vaultFacts[vb][key]; ok {
					if fva.Value != fvb.Value {
						contradictions = append(contradictions, contradictionEntry{
							Key:        key,
							VaultA:     va,
							ValueA:     truncate(fva.Value, 200),
							VaultB:     vb,
							ValueB:     truncate(fvb.Value, 200),
							UpdatedAtA: fva.UpdatedAt,
							UpdatedAtB: fvb.UpdatedAt,
						})
					}
				}
			}
		}
	}

	if contradictions == nil {
		contradictions = []contradictionEntry{}
	}

	writeJSON(w, 200, map[string]any{
		"contradictions": contradictions,
		"count":          len(contradictions),
		"truncated":      truncated,
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
