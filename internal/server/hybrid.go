package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/qdrant/go-client/qdrant"
)

// hybridResult is one entry in a /v1/hybrid response.
type hybridResult struct {
	Kind    string   `json:"kind"`              // "chunk" or "fact"
	Score   float32  `json:"score,omitempty"`   // similarity score (chunks only)
	Content string   `json:"content,omitempty"` // chunk text (chunks only)
	Source  string   `json:"source,omitempty"`  // source file (chunks only)
	Header  string   `json:"header,omitempty"`  // section header (chunks only)
	Key     string   `json:"key,omitempty"`     // fact key (facts only)
	Value   string   `json:"value,omitempty"`   // fact value (facts only)
	Match   string   `json:"match,omitempty"`   // how fact matched: key|prefix|tag|vector (facts only)
	Tags    []string `json:"tags,omitempty"`    // fact tags
}

// handleHybrid returns document chunks AND facts for a query in a single typed response.
// GET /v1/hybrid?query=...&key=...&prefix=...&tag=...&limit=...&top_k=...
// GET /vault/{name}/v1/hybrid?query=...&key=...&prefix=...&tag=...&limit=...&top_k=...
func (s *Server) handleHybrid(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	key := r.URL.Query().Get("key")
	prefix := r.URL.Query().Get("prefix")
	tag := r.URL.Query().Get("tag")

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	topK := limit
	if tk := r.URL.Query().Get("top_k"); tk != "" {
		if v, err := strconv.Atoi(tk); err == nil && v > 0 && v <= 100 {
			topK = v
		}
	}

	var results []hybridResult

	// ── 1. Chunks (vector recall) ──
	if query != "" {
		chunks, _, err := s.doRecall(r.Context(), recallRequest{
			Query: query,
			TopK:  topK,
		})
		if err == nil {
			for _, c := range chunks {
				content := c.Text
				if content == "" {
					content = c.FirstParagraph
				}
				results = append(results, hybridResult{
					Kind:    "chunk",
					Score:   c.Score,
					Content: content,
					Source:  c.SourceFile,
					Header:  c.Header,
				})
			}
		}
	}

	// ── 2. Facts (vector search) — when query is present ──
	if query != "" {
		eb := s.embeddingFor(r.Context())
		qc := s.factsQdrantFor(r.Context())
		if eb != nil && qc != nil {
			vector, err := eb.EmbedSingle(r.Context(), query)
			if err == nil {
				factPoints, err := qc.Search(r.Context(), vector, uint64(topK), 0.0, "", nil)
				if err == nil {
					for _, p := range factPoints {
						key, _ := qutil.GetPayloadString(p.GetPayload(), "fact_key")
						value, _ := qutil.GetPayloadString(p.GetPayload(), "fact_value")
						tags := qutil.GetPayloadStringList(p.GetPayload(), "fact_tags")
						results = append(results, hybridResult{
							Kind: "fact", Key: key, Value: value,
							Tags: tags, Score: float32(p.GetScore()), Match: "vector",
						})
					}
				}
			}
		}
	}

	// ── 3. Facts (exact key, prefix, tag) — in priority order, deduped ──
	if key != "" || prefix != "" || tag != "" {
		facts := s.queryHybridFacts(r.Context(), key, prefix, tag, limit)
		results = append(results, facts...)
	}

	writeJSON(w, 200, map[string]any{"results": results})
}

// queryHybridFacts searches facts by key/prefix/tag and returns hybridResult entries.
// Priority order: exact key > prefix > tag. Deduplicates by key across all three passes.
func (s *Server) queryHybridFacts(ctx context.Context, key, prefix, tag string, limit int) []hybridResult {
	if s.facts == nil {
		return nil
	}
	var results []hybridResult
	seen := make(map[string]bool)

	// 1. Exact key match
	if key != "" {
		points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), factKeyFilter(key), 1, "")
		if err == nil && len(points) > 0 {
			factKey, _ := qutil.GetPayloadString(points[0].Payload, "fact_key")
			value, _ := qutil.GetPayloadString(points[0].Payload, "fact_value")
			tags := qutil.GetPayloadStringList(points[0].Payload, "fact_tags")
			results = append(results, hybridResult{
				Kind: "fact", Key: factKey, Value: value,
				Tags: tags, Match: "key",
			})
			seen[factKey] = true
		}
	}

	// 2. Prefix match (scroll with empty filter, prefix-match keys in Go)
	if prefix != "" && len(results) < limit {
		scrollLimit := uint32(min(limit*2, 200))
		points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), nil, scrollLimit, "")
		if err == nil {
			for _, p := range points {
				if len(results) >= limit {
					break
				}
				factKey, _ := qutil.GetPayloadString(p.Payload, "fact_key")
				if factKey == "" || seen[factKey] || !strings.HasPrefix(factKey, prefix) {
					continue
				}
				value, _ := qutil.GetPayloadString(p.Payload, "fact_value")
				tags := qutil.GetPayloadStringList(p.Payload, "fact_tags")
				results = append(results, hybridResult{
					Kind: "fact", Key: factKey, Value: value,
					Tags: tags, Match: "prefix",
				})
				seen[factKey] = true
			}
		}
	}

	// 3. Tag match
	if tag != "" && len(results) < limit {
		remaining := limit - len(results)
		tagFilter := &qdrant.Filter{
			Must: []*qdrant.Condition{{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "fact_tags",
						Match: &qdrant.Match{
							MatchValue: &qdrant.Match_Keyword{
								Keyword: tag,
							},
						},
					},
				},
			}},
		}
		points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), tagFilter, uint32(remaining+5), "")
		if err == nil {
			for _, p := range points {
				if len(results) >= limit {
					break
				}
				factKey, _ := qutil.GetPayloadString(p.Payload, "fact_key")
				if factKey == "" || seen[factKey] {
					continue
				}
				value, _ := qutil.GetPayloadString(p.Payload, "fact_value")
				tags := qutil.GetPayloadStringList(p.Payload, "fact_tags")
				results = append(results, hybridResult{
					Kind: "fact", Key: factKey, Value: value,
					Tags: tags, Match: "tag",
				})
				seen[factKey] = true
			}
		}
	}

	return results
}
