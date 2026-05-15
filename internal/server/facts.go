package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/qdrant/go-client/qdrant"
)

// ── Fact Data Model ──────────────────────────────────────────────────────

const factsCollection = "ragamuffin_facts"

// factPayload is the JSON body for upsert (POST /v1/facts).
type factPayload struct {
	Key   string   `json:"key"`
	Value string   `json:"value"`
	Tags  []string `json:"tags,omitempty"`
}

// factResponse is the JSON response for a single fact.
type factResponse struct {
	Key       string   `json:"key"`
	Value     string   `json:"value"`
	Tags      []string `json:"tags,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

// factKeyHash produces a deterministic 32-char hex ID from a fact key.
// Used as the Qdrant point ID so upserting the same key replaces the point.
func factKeyHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:16]) // 32 hex chars
}

// factFilter builds a Qdrant filter for exact fact_key match.
func factKeyFilter(key string) *qdrant.Filter {
	return &qdrant.Filter{
		Must: []*qdrant.Condition{
			{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "fact_key",
						Match: &qdrant.Match{
							MatchValue: &qdrant.Match_Keyword{
								Keyword: key,
							},
						},
					},
				},
			},
		},
	}
}

// ── POST /v1/facts ───────────────────────────────────────────────────────

func (s *Server) handleFactsPost(w http.ResponseWriter, r *http.Request) {
	var fp factPayload
	if err := json.NewDecoder(r.Body).Decode(&fp); err != nil {
		writeError(w, 400, "INVALID_JSON", fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if fp.Key == "" || fp.Value == "" {
		writeError(w, 400, "INVALID_INPUT", "key and value are required")
		return
	}
	if len(fp.Key) > 1024 {
		writeError(w, 400, "KEY_TOO_LONG", "key must be <= 1024 bytes")
		return
	}

	pointID := factKeyHash(fp.Key)
	now := time.Now().UTC().Format(time.RFC3339)

	payload := map[string]any{
		"fact_key":   fp.Key,
		"fact_value": fp.Value,
		"updated_at": now,
	}
	if len(fp.Tags) > 0 {
		payload["fact_tags"] = fp.Tags
	}

	point := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{
				Uuid: pointID,
			},
		},
		Payload: qdrant.NewValueMap(payload),
		Vectors: &qdrant.Vectors{
			VectorsOptions: &qdrant.Vectors_Vector{
				Vector: &qdrant.Vector{
					Data: []float32{0, 0, 0, 0},
				},
			},
		},
	}

	if err := s.facts.Upsert(r.Context(), []*qdrant.PointStruct{point}); err != nil {
		s.log(r.Context()).Error("facts upsert failed", "error", err)
		writeError(w, 500, "UPSERT_FAILED", "failed to store fact")
		return
	}

	writeJSON(w, 200, factResponse{
		Key:       fp.Key,
		Value:     fp.Value,
		Tags:      fp.Tags,
		UpdatedAt: now,
	})
}

// ── GET /v1/facts ────────────────────────────────────────────────────────

func (s *Server) handleFactsGet(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	prefix := r.URL.Query().Get("prefix")
	tag := r.URL.Query().Get("tag")

	// Exact key lookup
	if key != "" {
		points, err := s.facts.ScrollFiltered(r.Context(), factsCollection, factKeyFilter(key), 1)
		if err != nil {
			s.log(r.Context()).Error("facts scroll failed", "error", err)
			writeError(w, 500, "SCROLL_FAILED", "failed to query facts")
			return
		}
		if len(points) == 0 {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("fact not found: %s", key))
			return
		}
		resp := pointToFact(points[0])
		if resp == nil {
			writeError(w, 500, "INVALID_DATA", "corrupt fact data")
			return
		}
		writeJSON(w, 200, resp)
		return
	}

	// Build list filter from optional prefix and tag
	var conditions []*qdrant.Condition

	if prefix != "" {
		conditions = append(conditions, &qdrant.Condition{
			ConditionOneOf: &qdrant.Condition_Field{
				Field: &qdrant.FieldCondition{
					Key: "fact_key",
					Match: &qdrant.Match{
						MatchValue: &qdrant.Match_Text{
							Text: prefix,
						},
					},
				},
			},
		})
	}

	if tag != "" {
		conditions = append(conditions, &qdrant.Condition{
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
		})
	}

	var filter *qdrant.Filter
	if len(conditions) > 0 {
		filter = &qdrant.Filter{
			Must: conditions,
		}
	}

	// Scroll with reasonable limit
	points, err := s.facts.ScrollFiltered(r.Context(), factsCollection, filter, 1000)
	if err != nil {
		s.log(r.Context()).Error("facts scroll failed", "error", err)
		writeError(w, 500, "SCROLL_FAILED", "failed to query facts")
		return
	}

	resp := make([]factResponse, 0, len(points))
	for _, p := range points {
		if fr := pointToFact(p); fr != nil {
			resp = append(resp, *fr)
		}
	}
	writeJSON(w, 200, resp)
}

// ── DELETE /v1/facts ─────────────────────────────────────────────────────

func (s *Server) handleFactsDelete(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, 400, "MISSING_KEY", "key query parameter is required")
		return
	}

	if err := s.facts.DeleteFiltered(r.Context(), factsCollection, factKeyFilter(key)); err != nil {
		s.log(r.Context()).Error("facts delete failed", "error", err)
		writeError(w, 500, "DELETE_FAILED", "failed to delete fact")
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"deleted": true,
		"key":     key,
	})
}

// ── Route dispatcher ─────────────────────────────────────────────────────

// handleFacts dispatches to POST/GET/DELETE /v1/facts based on method.
func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleFactsPost(w, r)
	case http.MethodGet:
		s.handleFactsGet(w, r)
	case http.MethodDelete:
		s.handleFactsDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET, POST, or DELETE")
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────

// pointToFact converts a Qdrant RetrievedPoint to a factResponse.
func pointToFact(p *qdrant.RetrievedPoint) *factResponse {
	key, _ := getPayloadString(p.Payload, "fact_key")
	value, _ := getPayloadString(p.Payload, "fact_value")
	updatedAt, _ := getPayloadString(p.Payload, "updated_at")
	if key == "" || value == "" {
		return nil
	}

	tags := getPayloadStringList(p.Payload, "fact_tags")

	return &factResponse{
		Key:       key,
		Value:     value,
		Tags:      tags,
		UpdatedAt: updatedAt,
	}
}

// getPayloadString extracts a string value from a Qdrant payload map.
func getPayloadString(payload map[string]*qdrant.Value, key string) (string, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return "", false
	}
	return v.GetStringValue(), true
}

// getPayloadStringList extracts a list of strings from a Qdrant payload.
func getPayloadStringList(payload map[string]*qdrant.Value, key string) []string {
	v, ok := payload[key]
	if !ok || v == nil {
		return nil
	}

	// Single string
	if s := v.GetStringValue(); s != "" {
		return []string{s}
	}

	// List of values
	values := v.GetListValue()
	if values == nil {
		return nil
	}
	items := values.GetValues()
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s := item.GetStringValue(); s != "" {
			result = append(result, s)
		}
	}
	return result
}
