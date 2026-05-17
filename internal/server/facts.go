package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/qdrant/go-client/qdrant"
)

// ── Fact Data Model ──────────────────────────────────────────────────────

// Facts use a separate Qdrant collection (configurable via RAGAMUFFIN_FACTS_COLLECTION).
// Vector size defaults to 4 — a sentinel for payload-only storage (no embeddings).
// The cost of 4-dim vectors is negligible while satisfying Qdrant's vector requirement.
//
// v0.5 extends the payload with lifecycle fields:
//   - source, source_type, confidence, ttl_days — client-supplied, optional
//   - status, supersedes, contradicts, conflict_resolved — server-managed
//   - confirmation_count, last_confirmed_at, created_at, expires_at — server-managed

// factPayload is the JSON body for upsert (POST /v1/facts).
// Pointers distinguish zero-value from omitted for confidence/ttl_days.
type factPayload struct {
	Key        string   `json:"key"`
	Value      string   `json:"value"`
	Tags       []string `json:"tags,omitempty"`
	Source     string   `json:"source,omitempty"`
	SourceType string   `json:"source_type,omitempty"`

	Confidence *float64 `json:"confidence,omitempty"` // 0.0–1.0; default 1.0
	TTLDays    *int     `json:"ttl_days,omitempty"`    // days; 0 = never expire
}

// factResponse is the JSON response for a single fact (v0.5 lifecycle).
type factResponse struct {
	Key              string   `json:"key"`
	Value            string   `json:"value"`
	Tags             []string `json:"tags,omitempty"`
	Source           string   `json:"source,omitempty"`
	SourceType       string   `json:"source_type,omitempty"`
	Confidence       float64  `json:"confidence"`
	Status           string   `json:"status"`
	Supersedes       string   `json:"supersedes"`
	Contradicts      []string `json:"contradicts,omitempty"`
	ConflictResolved bool     `json:"conflict_resolved"`
	ConfirmationCount int     `json:"confirmation_count"`
	LastConfirmedAt  string   `json:"last_confirmed_at,omitempty"`
	CreatedAt        string   `json:"created_at,omitempty"`
	UpdatedAt        string   `json:"updated_at"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
}

// factUpdateRequest is the JSON body for PUT /v1/facts (partial update).
// Pointer fields distinguish "omitted" (nil) from "set to zero/empty" (non-nil).
type factUpdateRequest struct {
	Value            *string   `json:"value,omitempty"`
	Tags             *[]string `json:"tags,omitempty"`
	Source           *string   `json:"source,omitempty"`
	SourceType       *string   `json:"source_type,omitempty"`
	Status           *string   `json:"status,omitempty"`
	Supersedes       *string   `json:"supersedes,omitempty"`
	Confidence       *float64  `json:"confidence,omitempty"`
	ConflictResolved *bool     `json:"conflict_resolved,omitempty"`
	ConfirmationCount *int     `json:"confirmation_count,omitempty"`
	LastConfirmedAt  *string   `json:"last_confirmed_at,omitempty"`
	TTLDays          *int      `json:"ttl_days,omitempty"`
}

// factBulkUpdateRequest is the JSON body for PATCH /v1/facts (bulk update).
type factBulkUpdateRequest struct {
	Keys    []string          `json:"keys"`
	Updates factUpdateRequest `json:"updates"`
}

// factBulkResult is one entry in the PATCH response.
type factBulkResult struct {
	Key   string `json:"key"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ── Helpers ──────────────────────────────────────────────────────────────

// factKeyHash produces a deterministic 32-char hex ID from a fact key.
// Used as the Qdrant point ID so upserting the same key replaces the point.
func factKeyHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:16]) // 32 hex chars
}

// factKeyFilter builds a Qdrant filter for exact fact_key match.
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

// factExists checks whether a fact with the given key exists.
func (s *Server) factExists(ctx context.Context, key string) (bool, error) {
	points, err := s.facts.ScrollFiltered(ctx, s.cfg.FactsCollection, factKeyFilter(key), 1, "")
	if err != nil {
		return false, err
	}
	return len(points) > 0, nil
}

// computeExpiresAt returns an ISO8601 timestamp for (now + ttl_days), or "" if 0.
func computeExpiresAt(ttlDays int) string {
	if ttlDays <= 0 {
		return ""
	}
	return time.Now().UTC().AddDate(0, 0, ttlDays).Format(time.RFC3339)
}

// defaultConfidence returns 1.0 if nil/0, otherwise clamps to [0,1].
func defaultConfidence(c *float64) float64 {
	if c == nil || *c <= 0 {
		return 1.0
	}
	if *c > 1.0 {
		return 1.0
	}
	return *c
}

// writableFields returns the set of field names that clients may update via PUT/PATCH.
var writableFields = map[string]bool{
	"value": true, "tags": true, "source": true, "source_type": true,
	"status": true, "supersedes": true, "confidence": true,
	"conflict_resolved": true, "confirmation_count": true,
	"last_confirmed_at": true, "ttl_days": true,
}

// ── POST /v1/facts ───────────────────────────────────────────────────────

func (s *Server) handleFactsPost(w http.ResponseWriter, r *http.Request) {
	// Require write access
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 256*1024) // 256 KB for facts
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
	if len(fp.Value) > 64*1024 {
		writeError(w, 400, "VALUE_TOO_LARGE", "value must be <= 64 KB")
		return
	}

	pointID := factKeyHash(fp.Key)
	now := time.Now().UTC().Format(time.RFC3339)

	// Check if fact already exists → preserve created_at
	var createdAt string
	exists, err := s.factExists(r.Context(), fp.Key)
	if err != nil {
		s.log(r.Context()).Error("fact existence check failed", "error", err)
		writeError(w, 500, "INTERNAL", "failed to check fact existence")
		return
	}
	if exists {
		// Read existing point to preserve created_at
		points, err := s.facts.ScrollFiltered(r.Context(), s.cfg.FactsCollection, factKeyFilter(fp.Key), 1, "")
		if err != nil {
			s.log(r.Context()).Error("fact read for created_at failed", "error", err)
		} else if len(points) > 0 {
			createdAt, _ = getPayloadString(points[0].GetPayload(), "created_at")
			// Carry forward confirmation_count and last_confirmed_at
			// Only updated explicitly via PUT — preserve existing value on upsert
		}
	}
	if createdAt == "" {
		createdAt = now
	}

	// Compute fields
	confidence := defaultConfidence(fp.Confidence)
	expiresAt := computeExpiresAt(intValue(fp.TTLDays))
	var expiresAtUnix float64
	if ttl := intValue(fp.TTLDays); ttl > 0 {
		expiresAtUnix = float64(time.Now().UTC().AddDate(0, 0, ttl).Unix())
	}

	payload := qdrant.NewValueMap(map[string]any{
		"fact_key":          fp.Key,
		"fact_value":        fp.Value,
		"source":            fp.Source,
		"source_type":       fp.SourceType,
		"confidence":        confidence,
		"status":            "active",
		"supersedes":        "",
		"conflict_resolved": true,
		"confirmation_count": 1,
		"last_confirmed_at": now,
		"created_at":        createdAt,
		"updated_at":        now,
		"ttl_days":          intValue(fp.TTLDays),
		"expires_at":        expiresAt,
		"expires_at_unix":   expiresAtUnix,
	})
	// Contradicts: empty list (server-managed)
	payload["contradicts"] = &qdrant.Value{
		Kind: &qdrant.Value_ListValue{
			ListValue: &qdrant.ListValue{Values: []*qdrant.Value{}},
		},
	}

	if len(fp.Tags) > 0 {
		setPayloadTags(payload, fp.Tags)
	}
	if fp.Source != "" {
		payload["source"] = qdrant.NewValue(fp.Source)
	}
	if fp.SourceType != "" {
		payload["source_type"] = qdrant.NewValue(fp.SourceType)
	}

	point := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{
				Uuid: pointID,
			},
		},
		Payload: payload,
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

	resp := pointToFactResponse(point.Payload, fp.Key)
	writeJSON(w, 200, resp)
}

// ── GET /v1/facts ────────────────────────────────────────────────────────

func (s *Server) handleFactsGet(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	prefix := r.URL.Query().Get("prefix")
	tag := r.URL.Query().Get("tag")
	status := r.URL.Query().Get("status")
	conflictResolvedStr := r.URL.Query().Get("conflict_resolved")

	// Exact key lookup
	if key != "" {
		points, err := s.facts.ScrollFiltered(r.Context(), s.cfg.FactsCollection, factKeyFilter(key), 1, "")
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

	// Build list filter from optional search text, tag, status, and conflict_resolved
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

	if status != "" {
		conditions = append(conditions, &qdrant.Condition{
			ConditionOneOf: &qdrant.Condition_Field{
				Field: &qdrant.FieldCondition{
					Key: "status",
					Match: &qdrant.Match{
						MatchValue: &qdrant.Match_Keyword{
							Keyword: status,
						},
					},
				},
			},
		})
	}

	if conflictResolvedStr != "" && status != "" {
		cr, err := strconv.ParseBool(conflictResolvedStr)
		if err == nil {
			conditions = append(conditions, &qdrant.Condition{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "conflict_resolved",
						Match: &qdrant.Match{
							MatchValue: &qdrant.Match_Bool{
								Bool: cr,
							},
						},
					},
				},
			})
		}
	}

	var filter *qdrant.Filter
	if len(conditions) > 0 {
		filter = &qdrant.Filter{
			Must: conditions,
		}
	}

	// Pagination params
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 1000 {
			limit = v
		}
	}
	offset := r.URL.Query().Get("before")

	// Fetch limit+1 to detect if there's a next page
	points, err := s.facts.ScrollFiltered(r.Context(), s.cfg.FactsCollection, filter, uint32(limit+1), offset)
	if err != nil {
		s.log(r.Context()).Error("facts scroll failed", "error", err)
		writeError(w, 500, "SCROLL_FAILED", "failed to query facts")
		return
	}

	var nextToken string
	resp := make([]factResponse, 0, limit)
	for i, p := range points {
		if i >= limit {
			if id := p.Id.GetUuid(); id != "" {
				nextToken = id
			}
			break
		}
		if fr := pointToFact(p); fr != nil {
			resp = append(resp, *fr)
		}
	}

	respBody := map[string]interface{}{
		"entries": resp,
	}
	if nextToken != "" {
		respBody["next_token"] = nextToken
	}
	writeJSON(w, 200, respBody)
}

// ── PUT /v1/facts ────────────────────────────────────────────────────────

func (s *Server) handleFactsPut(w http.ResponseWriter, r *http.Request) {
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, 400, "MISSING_KEY", "key query parameter is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	var req factUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_JSON", fmt.Sprintf("invalid request body: %v", err))
		return
	}

	// Read existing point
	points, err := s.facts.ScrollFiltered(r.Context(), s.cfg.FactsCollection, factKeyFilter(key), 1, "")
	if err != nil {
		s.log(r.Context()).Error("facts scroll for update failed", "error", err)
		writeError(w, 500, "SCROLL_FAILED", "failed to read fact")
		return
	}
	if len(points) == 0 {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("fact not found: %s", key))
		return
	}

	payload := make(map[string]*qdrant.Value)
	for k, v := range points[0].GetPayload() {
		payload[k] = v
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// Apply partial updates
	applyFieldUpdate(payload, "fact_value", req.Value)
	applyFieldUpdate(payload, "source", req.Source)
	applyFieldUpdate(payload, "source_type", req.SourceType)
	applyFieldUpdate(payload, "status", req.Status)
	applyFieldUpdate(payload, "supersedes", req.Supersedes)

	if req.Confidence != nil {
		payload["confidence"] = qdrant.NewValue(*req.Confidence)
	}
	if req.ConflictResolved != nil {
		payload["conflict_resolved"] = qdrant.NewValue(*req.ConflictResolved)
	}
	if req.ConfirmationCount != nil {
		payload["confirmation_count"] = qdrant.NewValue(float64(*req.ConfirmationCount))
	}
	if req.LastConfirmedAt != nil {
		payload["last_confirmed_at"] = qdrant.NewValue(*req.LastConfirmedAt)
	}
	if req.TTLDays != nil {
		ttl := *req.TTLDays
		payload["ttl_days"] = qdrant.NewValue(float64(ttl))
		if expiresAt := computeExpiresAt(ttl); expiresAt != "" {
			payload["expires_at"] = qdrant.NewValue(expiresAt)
			payload["expires_at_unix"] = qdrant.NewValue(float64(time.Now().UTC().AddDate(0, 0, ttl).Unix()))
		} else {
			payload["expires_at"] = qdrant.NewValue("")
			payload["expires_at_unix"] = qdrant.NewValue(float64(0))
		}
	}
	if req.Tags != nil {
		// Clear and re-set
		delete(payload, "fact_tags")
		if len(*req.Tags) > 0 {
			setPayloadTags(payload, *req.Tags)
		}
	}

	payload["updated_at"] = qdrant.NewValue(now)

	point := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{
				Uuid: factKeyHash(key),
			},
		},
		Payload: payload,
		Vectors: &qdrant.Vectors{
			VectorsOptions: &qdrant.Vectors_Vector{
				Vector: &qdrant.Vector{
					Data: []float32{0, 0, 0, 0},
				},
			},
		},
	}

	if err := s.facts.Upsert(r.Context(), []*qdrant.PointStruct{point}); err != nil {
		s.log(r.Context()).Error("facts put failed", "error", err)
		writeError(w, 500, "UPSERT_FAILED", "failed to update fact")
		return
	}

	resp := pointToFactResponse(payload, key)
	writeJSON(w, 200, resp)
}

// ── PATCH /v1/facts ──────────────────────────────────────────────────────

func (s *Server) handleFactsPatch(w http.ResponseWriter, r *http.Request) {
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	var req factBulkUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_JSON", fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if len(req.Keys) == 0 {
		writeError(w, 400, "MISSING_KEYS", "keys array is required")
		return
	}

	results := make([]factBulkResult, 0, len(req.Keys))
	succeeded := 0
	failed := 0

	for _, key := range req.Keys {
		// Perform a single-key update via read-modify-write
		points, err := s.facts.ScrollFiltered(r.Context(), s.cfg.FactsCollection, factKeyFilter(key), 1, "")
		if err != nil {
			results = append(results, factBulkResult{Key: key, OK: false, Error: "INTERNAL"})
			failed++
			continue
		}
		if len(points) == 0 {
			results = append(results, factBulkResult{Key: key, OK: false, Error: "NOT_FOUND"})
			failed++
			continue
		}

		payload := make(map[string]*qdrant.Value)
		for k, v := range points[0].GetPayload() {
			payload[k] = v
		}
		now := time.Now().UTC().Format(time.RFC3339)

		// Apply same updates to each key
		applyFieldUpdate(payload, "fact_value", req.Updates.Value)
		applyFieldUpdate(payload, "source", req.Updates.Source)
		applyFieldUpdate(payload, "source_type", req.Updates.SourceType)
		applyFieldUpdate(payload, "status", req.Updates.Status)
		applyFieldUpdate(payload, "supersedes", req.Updates.Supersedes)

		if req.Updates.Confidence != nil {
			payload["confidence"] = qdrant.NewValue(*req.Updates.Confidence)
		}
		if req.Updates.ConflictResolved != nil {
			payload["conflict_resolved"] = qdrant.NewValue(*req.Updates.ConflictResolved)
		}
		if req.Updates.ConfirmationCount != nil {
			payload["confirmation_count"] = qdrant.NewValue(float64(*req.Updates.ConfirmationCount))
		}
		if req.Updates.LastConfirmedAt != nil {
			payload["last_confirmed_at"] = qdrant.NewValue(*req.Updates.LastConfirmedAt)
		}
		if req.Updates.TTLDays != nil {
			ttl := *req.Updates.TTLDays
			payload["ttl_days"] = qdrant.NewValue(float64(ttl))
			if expiresAt := computeExpiresAt(ttl); expiresAt != "" {
				payload["expires_at"] = qdrant.NewValue(expiresAt)
			} else {
				payload["expires_at"] = qdrant.NewValue("")
			}
		}
		if req.Updates.Tags != nil {
			delete(payload, "fact_tags")
			if len(*req.Updates.Tags) > 0 {
				setPayloadTags(payload, *req.Updates.Tags)
			}
		}

		payload["updated_at"] = qdrant.NewValue(now)

		point := &qdrant.PointStruct{
			Id: &qdrant.PointId{
				PointIdOptions: &qdrant.PointId_Uuid{
					Uuid: factKeyHash(key),
				},
			},
			Payload: payload,
			Vectors: &qdrant.Vectors{
				VectorsOptions: &qdrant.Vectors_Vector{
					Vector: &qdrant.Vector{
						Data: []float32{0, 0, 0, 0},
					},
				},
			},
		}

		if err := s.facts.Upsert(r.Context(), []*qdrant.PointStruct{point}); err != nil {
			results = append(results, factBulkResult{Key: key, OK: false, Error: "INTERNAL"})
			failed++
			continue
		}

		results = append(results, factBulkResult{Key: key, OK: true})
		succeeded++
	}

	writeJSON(w, 200, map[string]interface{}{
		"results":   results,
		"total":     len(req.Keys),
		"succeeded": succeeded,
		"failed":    failed,
	})
}

// ── DELETE /v1/facts ─────────────────────────────────────────────────────

func (s *Server) handleFactsDelete(w http.ResponseWriter, r *http.Request) {
	// Require write access
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, 400, "MISSING_KEY", "key query parameter is required")
		return
	}

	if err := s.facts.DeleteFiltered(r.Context(), s.cfg.FactsCollection, factKeyFilter(key)); err != nil {
		s.log(r.Context()).Error("facts delete failed", "error", err)
		writeError(w, 500, "DELETE_FAILED", "failed to delete fact")
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"deleted": true,
		"key":     key,
	})
}

// migrateFacts reads all existing facts and fills in default values for
// v0.5 lifecycle fields that didn't exist before. This is a one-time
// migration run at server startup. Errors are logged but non-fatal.
func (s *Server) migrateFacts() {
	if s.facts == nil {
		return
	}
	ctx := context.Background()
	collection := s.cfg.FactsCollection

	var offset string
	const pageSize uint32 = 100
	migrated := 0

	for {
		points, err := s.facts.ScrollFiltered(ctx, collection, nil, pageSize, offset)
		if err != nil {
			s.logger.Error("facts migration scroll failed", "error", err)
			return
		}
		if len(points) == 0 {
			break
		}

		for _, p := range points {
			payload := p.GetPayload()
			if payload == nil {
				continue
			}

			needsUpdate := false
			now := time.Now().UTC().Format(time.RFC3339)

			// status: default "active"
			if _, ok := payload["status"]; !ok {
				payload["status"] = qdrant.NewValue("active")
				needsUpdate = true
			}
			// confidence: default 1.0
			if _, ok := payload["confidence"]; !ok {
				payload["confidence"] = qdrant.NewValue(1.0)
				needsUpdate = true
			}
			// source_type: default "manual"
			if _, ok := payload["source_type"]; !ok {
				payload["source_type"] = qdrant.NewValue("manual")
				needsUpdate = true
			}
			// conflict_resolved: default true
			if _, ok := payload["conflict_resolved"]; !ok {
				payload["conflict_resolved"] = qdrant.NewValue(true)
				needsUpdate = true
			}
			// confirmation_count: default 1
			if _, ok := payload["confirmation_count"]; !ok {
				payload["confirmation_count"] = qdrant.NewValue(float64(1))
				needsUpdate = true
			}
			// created_at: default to updated_at or now
			if _, ok := payload["created_at"]; !ok {
				if ua, ok := payload["updated_at"]; ok && ua.GetStringValue() != "" {
					payload["created_at"] = qdrant.NewValue(ua.GetStringValue())
				} else {
					payload["created_at"] = qdrant.NewValue(now)
				}
				needsUpdate = true
			}
			// last_confirmed_at: default to updated_at or now
			if _, ok := payload["last_confirmed_at"]; !ok {
				if ua, ok := payload["updated_at"]; ok && ua.GetStringValue() != "" {
					payload["last_confirmed_at"] = qdrant.NewValue(ua.GetStringValue())
				} else {
					payload["last_confirmed_at"] = qdrant.NewValue(now)
				}
				needsUpdate = true
			}
			// supersedes: default ""
			if _, ok := payload["supersedes"]; !ok {
				payload["supersedes"] = qdrant.NewValue("")
				needsUpdate = true
			}
			// contradicts: default empty list
			if _, ok := payload["contradicts"]; !ok {
				payload["contradicts"] = &qdrant.Value{
					Kind: &qdrant.Value_ListValue{
						ListValue: &qdrant.ListValue{Values: []*qdrant.Value{}},
					},
				}
				needsUpdate = true
			}
			// source: default ""
			if _, ok := payload["source"]; !ok {
				payload["source"] = qdrant.NewValue("")
				needsUpdate = true
			}
			// ttl_days: default 0
			if _, ok := payload["ttl_days"]; !ok {
				payload["ttl_days"] = qdrant.NewValue(float64(0))
				needsUpdate = true
			}
			// expires_at: default ""
			if _, ok := payload["expires_at"]; !ok {
				payload["expires_at"] = qdrant.NewValue("")
				needsUpdate = true
			}
			// expires_at_unix: default 0
			if _, ok := payload["expires_at_unix"]; !ok {
				payload["expires_at_unix"] = qdrant.NewValue(float64(0))
				needsUpdate = true
			}

			if !needsUpdate {
				continue
			}

			point := &qdrant.PointStruct{
				Id: p.Id,
				Payload: payload,
				Vectors: &qdrant.Vectors{
					VectorsOptions: &qdrant.Vectors_Vector{
						Vector: &qdrant.Vector{
							Data: []float32{0, 0, 0, 0},
						},
					},
				},
			}

			if err := s.facts.Upsert(ctx, []*qdrant.PointStruct{point}); err != nil {
				s.logger.Error("facts migration upsert failed", "error", err)
				continue
			}
			migrated++
		}

		// Set offset for next page
		if id := points[len(points)-1].GetId().GetUuid(); id != "" {
			offset = id
		} else {
			break
		}
	}

	if migrated > 0 {
		s.logger.Info("facts migration complete", "migrated", migrated)
	}
}

// ── Route dispatcher ─────────────────────────────────────────────────────

// handleFacts dispatches to POST/GET/PUT/PATCH/DELETE /v1/facts based on method.
func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleFactsPost(w, r)
	case http.MethodGet:
		s.handleFactsGet(w, r)
	case http.MethodPut:
		s.handleFactsPut(w, r)
	case http.MethodPatch:
		s.handleFactsPatch(w, r)
	case http.MethodDelete:
		s.handleFactsDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, PUT, PATCH, DELETE")
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET, POST, PUT, PATCH, or DELETE")
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────

// pointToFact converts a Qdrant RetrievedPoint to a factResponse with all lifecycle fields.
func pointToFact(p *qdrant.RetrievedPoint) *factResponse {
	if p == nil {
		return nil
	}
	return pointToFactResponse(p.GetPayload(), "")
}

// pointToFactResponse builds a factResponse from a payload map and (optionally) a key.
// If key is provided, it's used; otherwise reads from payload.
func pointToFactResponse(payload map[string]*qdrant.Value, keyOverride string) *factResponse {
	fr := &factResponse{}

	if keyOverride != "" {
		fr.Key = keyOverride
	} else {
		fr.Key, _ = getPayloadString(payload, "fact_key")
	}

	fr.Value, _ = getPayloadString(payload, "fact_value")
	fr.Tags = getPayloadStringList(payload, "fact_tags")
	fr.Source, _ = getPayloadString(payload, "source")
	fr.SourceType, _ = getPayloadString(payload, "source_type")
	fr.Confidence, _ = getPayloadFloat(payload, "confidence")
	fr.Status, _ = getPayloadString(payload, "status")
	fr.Supersedes, _ = getPayloadString(payload, "supersedes")
	fr.Contradicts = getPayloadStringList(payload, "contradicts")
	fr.ConflictResolved, _ = getPayloadBool(payload, "conflict_resolved")
	fr.ConfirmationCount, _ = getPayloadInt(payload, "confirmation_count")
	fr.LastConfirmedAt, _ = getPayloadString(payload, "last_confirmed_at")
	fr.CreatedAt, _ = getPayloadString(payload, "created_at")
	fr.UpdatedAt, _ = getPayloadString(payload, "updated_at")
	fr.ExpiresAt, _ = getPayloadString(payload, "expires_at")

	if fr.Status == "" {
		fr.Status = "active"
	}
	if fr.Confidence == 0 {
		fr.Confidence = 1.0
	}

	return fr
}

// applyFieldUpdate sets payload[key] = qdrant.NewValue(*val) when val is non-nil.
func applyFieldUpdate(payload map[string]*qdrant.Value, key string, val *string) {
	if val != nil {
		payload[key] = qdrant.NewValue(*val)
	}
}

// setPayloadTags writes fact_tags as a Qdrant list value into the payload map.
func setPayloadTags(payload map[string]*qdrant.Value, tags []string) {
	tagVals := make([]*qdrant.Value, len(tags))
	for i, t := range tags {
		v, err := qdrant.NewValue(t)
		if err != nil {
			// Should never happen for strings, but skip if it does
			continue
		}
		tagVals[i] = v
	}
	payload["fact_tags"] = &qdrant.Value{
		Kind: &qdrant.Value_ListValue{
			ListValue: &qdrant.ListValue{Values: tagVals},
		},
	}
}

// intValue safely dereferences a *int, returning 0 for nil.
func intValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// ── Payload extraction helpers ───────────────────────────────────────────

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

// getPayloadFloat extracts a float64 from a Qdrant payload.
func getPayloadFloat(payload map[string]*qdrant.Value, key string) (float64, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return 0, false
	}
	return v.GetDoubleValue(), true
}

// getPayloadBool extracts a bool from a Qdrant payload.
func getPayloadBool(payload map[string]*qdrant.Value, key string) (bool, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return false, false
	}
	return v.GetBoolValue(), true
}

// getPayloadInt extracts an integer from a Qdrant payload (stored as double).
func getPayloadInt(payload map[string]*qdrant.Value, key string) (int, bool) {
	f, ok := getPayloadFloat(payload, key)
	if !ok {
		return 0, false
	}
	return int(f), true
}
