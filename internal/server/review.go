package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/events"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/qdrant/go-client/qdrant"
)


// ── Request/Response types ────────────────────────────────────────────────────

type reviewResponse struct {
	Key           string         `json:"key"`
	Value         string         `json:"value"`
	Tags          []string       `json:"tags,omitempty"`
	Source        string         `json:"source,omitempty"`
	SourceType    string         `json:"source_type,omitempty"`
	Confidence    *float64       `json:"confidence,omitempty"`
	Status        string         `json:"status"`
	ReviewReasons []reviewReason `json:"review_reasons,omitempty"`
	LastConfirmedAt string       `json:"last_confirmed_at,omitempty"`
	CreatedAt     string         `json:"created_at,omitempty"`
	UpdatedAt     string         `json:"updated_at"`
}

type reviewReason struct {
	Type         string   `json:"type"`
	Detail       string   `json:"detail"`
	ConflictKeys []string `json:"conflict_keys,omitempty"`
}

type reviewResolveRequest struct {
	Action          string   `json:"action"` // confirm | supersede | reject | reclassify
	Confidence      *float64 `json:"confidence,omitempty"`
	NewKey          string   `json:"new_key,omitempty"`
	NewValue        string   `json:"new_value,omitempty"`
	Note            string   `json:"note,omitempty"`
	ConflictResolved *bool   `json:"conflict_resolved,omitempty"`
	TTLDays         *int     `json:"ttl_days,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	Source          string   `json:"source,omitempty"`
	SourceType      string   `json:"source_type,omitempty"`
}

type reviewStatsResponse struct {
	TotalNeedsReview int                         `json:"total_needs_review"`
	ByReason         map[string]int              `json:"by_reason"`
	BySourceType     map[string]int              `json:"by_source_type"`
	OldestItem       string                      `json:"oldest_item,omitempty"`
	AvgPendingDays   float64                     `json:"avg_pending_days,omitempty"`
}

// ── GET /v1/review ────────────────────────────────────────────────────────────

func (s *Server) handleReviewGet(w http.ResponseWriter, r *http.Request) {
	reasonFilter := r.URL.Query().Get("reason")
	tag := r.URL.Query().Get("tag")
	sourceType := r.URL.Query().Get("source_type")
	minConfidenceStr := r.URL.Query().Get("min_confidence")

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}
	offset := r.URL.Query().Get("before")

	// Build filter: status = needs_review, plus optional tag/sourceType
	conditions := []*qdrant.Condition{
		{
			ConditionOneOf: &qdrant.Condition_Field{
				Field: &qdrant.FieldCondition{
					Key: "status",
					Match: &qdrant.Match{
						MatchValue: &qdrant.Match_Keyword{
							Keyword: "needs_review",
						},
					},
				},
			},
		},
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

	if sourceType != "" {
		conditions = append(conditions, &qdrant.Condition{
			ConditionOneOf: &qdrant.Condition_Field{
				Field: &qdrant.FieldCondition{
					Key: "source_type",
					Match: &qdrant.Match{
						MatchValue: &qdrant.Match_Keyword{
							Keyword: sourceType,
						},
					},
				},
			},
		})
	}

	if minConfidenceStr != "" {
		if mc, err := strconv.ParseFloat(minConfidenceStr, 64); err == nil {
			conditions = append(conditions, &qdrant.Condition{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "confidence",
						Range: &qdrant.Range{
							Lt: &mc,
						},
					},
				},
			})
		}
	}

	filter := &qdrant.Filter{Must: conditions}

	points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), filter, uint32(limit+1), offset)
	if err != nil {
		s.log(r.Context()).Error("review query failed", "error", err)
		writeError(w, 500, "QUERY_FAILED", "failed to query review queue")
		return
	}

	var nextToken string
	entries := make([]reviewResponse, 0, limit)
	for i, p := range points {
		if i >= limit {
			if id := p.GetId().GetUuid(); id != "" {
				nextToken = id
			}
			break
		}
		if r := pointToReviewEntry(p, reasonFilter); r != nil {
			entries = append(entries, *r)
		}
	}

	respBody := map[string]any{
		"entries": entries,
		"total":   len(entries),
	}
	if nextToken != "" {
		respBody["next_token"] = nextToken
	}
	writeJSON(w, 200, respBody)
}

// pointToReviewEntry converts a Qdrant point to a reviewResponse with computed
// review_reasons. Filters reasons by type if reasonFilter is non-empty.
func pointToReviewEntry(p *qdrant.RetrievedPoint, reasonFilter string) *reviewResponse {
	if p == nil {
		return nil
	}
	payload := p.GetPayload()

	key, _ := getPayloadString(payload, "fact_key")
	value, _ := getPayloadString(payload, "fact_value")
	if key == "" || value == "" {
		return nil
	}

	status, _ := getPayloadString(payload, "status")
	if status != "needs_review" {
		return nil
	}

	var confidence *float64
	if c, ok := getPayloadFloat(payload, "confidence"); ok {
		confidence = &c
	}
	r := &reviewResponse{
		Key:     key,
		Value:   value,
		Tags:    getPayloadStringList(payload, "fact_tags"),
		Source:  getPayloadStringValue(payload, "source"),
		SourceType: getPayloadStringValue(payload, "source_type"),
		Confidence: confidence,
		Status:  status,
		LastConfirmedAt: getPayloadStringValue(payload, "last_confirmed_at"),
		CreatedAt: getPayloadStringValue(payload, "created_at"),
		UpdatedAt: getPayloadStringValue(payload, "updated_at"),
	}

	// Compute review reasons dynamically from payload fields
	now := time.Now().UTC()
	reasons := []reviewReason{}

	// Stale check: expires_at is in the past
	expiresAt := getPayloadStringValue(payload, "expires_at")
	if expiresAt != "" {
		if expTime, err := time.Parse(time.RFC3339, expiresAt); err == nil && now.After(expTime) {
			ttlDays := getPayloadIntValue(payload, "ttl_days")
			reasons = append(reasons, reviewReason{
				Type:   "stale",
				Detail: fmt.Sprintf("Expired %s ago (TTL: %d days)", now.Sub(expTime).Truncate(time.Hour).String(), ttlDays),
			})
		}
	}

	// Contradiction check: non-empty contradicts list and not resolved
	contradicts := getPayloadStringList(payload, "contradicts")
	conflictResolved := getPayloadBoolValue(payload, "conflict_resolved")
	if len(contradicts) > 0 && !conflictResolved {
		reasons = append(reasons, reviewReason{
			Type:         "contradiction",
			Detail:       fmt.Sprintf("Conflicts with %d other facts", len(contradicts)),
			ConflictKeys: contradicts,
		})
	}

	// Low confidence check
	if r.Confidence != nil && *r.Confidence < 0.5 {
		reasons = append(reasons, reviewReason{
			Type:   "low_confidence",
			Detail: fmt.Sprintf("Confidence is %.2f (below threshold 0.5)", *r.Confidence),
		})
	}

	// Supersession check
	supersedes := getPayloadStringValue(payload, "supersedes")
	if supersedes != "" {
		reasons = append(reasons, reviewReason{
			Type:   "supersession",
			Detail: fmt.Sprintf("Supersedes fact: %s", supersedes),
		})
	}

	// Filter by reason type if requested
	if reasonFilter != "" && reasonFilter != "all" {
		filtered := []reviewReason{}
		for _, reason := range reasons {
			if reason.Type == reasonFilter {
				filtered = append(filtered, reason)
			}
		}
		r.ReviewReasons = filtered
	} else {
		r.ReviewReasons = reasons
	}

	return r
}

// ── POST /v1/review ───────────────────────────────────────────────────────────

func (s *Server) handleReviewPost(w http.ResponseWriter, r *http.Request) {
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, 400, "MISSING_KEY", "key query parameter is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	var req reviewResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid request body: %v", err))
		return
	}

	if req.Action == "" {
		writeError(w, 400, "MISSING_ACTION", "action is required (confirm|supersede|reject|reclassify)")
		return
	}

	// Read existing fact
	points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), factKeyFilter(key), 1, "")
	if err != nil {
		s.log(r.Context()).Error("review read failed", "error", err)
		writeError(w, 500, "READ_FAILED", "failed to read fact")
		return
	}
	if len(points) == 0 {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("fact not found: %s", key))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	payload := make(map[string]*qdrant.Value)
	for k, v := range points[0].GetPayload() {
		payload[k] = v
	}

	switch req.Action {
	case "confirm":
		payload["status"] = qutil.Nv("active")
		payload["last_confirmed_at"] = qutil.Nv(now)
		// Increment confirmation_count
		cc := getPayloadIntValue(payload, "confirmation_count") + 1
		payload["confirmation_count"] = qutil.Nv(float64(cc))
		if req.Confidence != nil {
			payload["confidence"] = qutil.Nv(*req.Confidence)
		}
		if req.ConflictResolved != nil {
			payload["conflict_resolved"] = qutil.Nv(*req.ConflictResolved)
		}

	case "supersede":
		payload["status"] = qutil.Nv("superseded")
		if req.NewKey != "" {
			payload["supersedes"] = qutil.Nv(req.NewKey)
		}
		if req.NewValue != "" {
			// Create a new fact via implicit POST
			newPoint, err := s.reviewSupersedeCreate(r, req.NewKey, req.NewValue, payload, now)
			if err != nil {
				s.log(r.Context()).Error("review supersede create failed", "error", err)
				writeError(w, 400, "SUPERSEDE_CREATE_FAILED", err.Error())
				return
			}
			// Then write updated status to the OLD fact
			payload["updated_at"] = qutil.Nv(now)
			oldPoint := &qdrant.PointStruct{
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
			if err := s.facts.Upsert(r.Context(), []*qdrant.PointStruct{oldPoint}); err != nil {
				s.log(r.Context()).Error("review supersede: failed to update old fact", "error", err)
				writeError(w, 500, "UPSERT_FAILED", "created new fact but failed to update old fact")
				return
			}
			// Emit fact lifecycle events for supersede with new value
			if s.emitter != nil {
				s.emitter.Emit(events.TypeFactCreated, events.FactCreatedData{
					Key:        req.NewKey,
					Value:      req.NewValue,
					Vault:      vaultFromContext(r.Context()),
					Confidence: defaultConfidence(nil),
				})
				s.emitter.Emit(events.TypeFactReviewed, events.FactReviewedData{
					Key:    key,
					Action: "supersede",
				})
			}
			writeJSON(w, 200, pointToFactResponse(newPoint.GetPayload(), req.NewKey))
			return
		}

	case "reject":
		payload["status"] = qutil.Nv("rejected")

	case "reclassify":
		// Set status to active (reclassification is a resolution action)
		payload["status"] = qutil.Nv("active")
		if req.Confidence != nil {
			payload["confidence"] = qutil.Nv(*req.Confidence)
		}
		if req.TTLDays != nil {
			ttl := *req.TTLDays
			payload["ttl_days"] = qutil.Nv(float64(ttl))
			if ttl > 0 {
				expiresAt := time.Now().UTC().AddDate(0, 0, ttl).Format(time.RFC3339)
				payload["expires_at"] = qutil.Nv(expiresAt)
				payload["expires_at_unix"] = qutil.Nv(float64(time.Now().UTC().AddDate(0, 0, ttl).Unix()))
			} else {
				payload["expires_at"] = qutil.Nv("")
				payload["expires_at_unix"] = qutil.Nv(float64(0))
			}
		}
		if req.Tags != nil {
			delete(payload, "fact_tags")
			if len(req.Tags) > 0 {
				setPayloadTags(payload, req.Tags)
			}
		}
		if req.Source != "" {
			payload["source"] = qutil.Nv(req.Source)
		}
		if req.SourceType != "" {
			payload["source_type"] = qutil.Nv(req.SourceType)
		}
		if req.ConflictResolved != nil {
			payload["conflict_resolved"] = qutil.Nv(*req.ConflictResolved)
		}

	default:
		writeError(w, 400, "INVALID_ACTION", "action must be confirm, supersede, reject, or reclassify")
		return
	}

	payload["updated_at"] = qutil.Nv(now)

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
		s.log(r.Context()).Error("review upsert failed", "error", err)
		writeError(w, 500, "UPSERT_FAILED", "failed to update fact")
		return
	}

	// Emit fact lifecycle event
	if s.emitter != nil {
		s.emitter.Emit(events.TypeFactReviewed, events.FactReviewedData{
			Key:    key,
			Action: req.Action,
		})
	}

	resp := pointToFactResponse(payload, key)
	writeJSON(w, 200, resp)
}

// handleReviewSupersedeCreate creates a new fact when supersede action includes new_value.
func (s *Server) reviewSupersedeCreate(r *http.Request, newKey, newValue string, oldPayload map[string]*qdrant.Value, now string) (*qdrant.PointStruct, error) {
	if newKey == "" || newValue == "" {
		return nil, fmt.Errorf("new_key and new_value are required for supersede with new_value")
	}
	if len(newKey) > 1024 {
		return nil, fmt.Errorf("key must be <= 1024 bytes")
	}
	if len(newValue) > 64*1024 {
		return nil, fmt.Errorf("value must be <= 64 KB")
	}

	// Inherit fields from old fact
	source, _ := getPayloadString(oldPayload, "source")
	sourceType, _ := getPayloadString(oldPayload, "source_type")
	tags := getPayloadStringList(oldPayload, "fact_tags")
	confidence := 1.0
	if raw, ok := getPayloadFloat(oldPayload, "confidence"); ok {
		confidence = raw
	}

	payload := qdrant.NewValueMap(map[string]any{
		"fact_key":           newKey,
		"fact_value":         newValue,
		"source":             source,
		"source_type":        sourceType,
		"confidence":         confidence,
		"status":             "active",
		"supersedes":         "",
		"conflict_resolved":  true,
		"confirmation_count": float64(1),
		"last_confirmed_at":  now,
		"created_at":         now,
		"updated_at":         now,
		"ttl_days":           float64(0),
		"expires_at":         "",
		"expires_at_unix":    float64(0),
	})
	payload["contradicts"] = &qdrant.Value{
		Kind: &qdrant.Value_ListValue{
			ListValue: &qdrant.ListValue{Values: []*qdrant.Value{}},
		},
	}
	if len(tags) > 0 {
		setPayloadTags(payload, tags)
	}

	point := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{
				Uuid: factKeyHash(newKey),
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
		return nil, fmt.Errorf("upsert new fact: %w", err)
	}
	return point, nil
}

// ── GET /v1/review/stats ──────────────────────────────────────────────────────

func (s *Server) handleReviewStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	// Query all needs_review facts
	filter := &qdrant.Filter{
		Must: []*qdrant.Condition{
			{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "status",
						Match: &qdrant.Match{
							MatchValue: &qdrant.Match_Keyword{
								Keyword: "needs_review",
							},
						},
					},
				},
			},
		},
	}

	const pageSize uint32 = 1000
	var scrollOffset string
	stats := reviewStatsResponse{
		ByReason:     make(map[string]int),
		BySourceType: make(map[string]int),
	}

	now := time.Now().UTC()
	var totalPendingDays float64
	var oldestTime time.Time
	var pointCount int

	for {
		points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), filter, pageSize, scrollOffset)
		if err != nil {
			s.log(r.Context()).Error("review stats query failed", "error", err)
			writeError(w, 500, "QUERY_FAILED", "failed to query review stats")
			return
		}
		if len(points) == 0 {
			break
		}

		for _, p := range points {
			payload := p.GetPayload()

			sourceType, _ := getPayloadString(payload, "source_type")
			if sourceType != "" {
				stats.BySourceType[sourceType]++
			} else {
				stats.BySourceType["unknown"]++
			}

			expiresAt, _ := getPayloadString(payload, "expires_at")
			if expiresAt != "" {
				if expTime, err := time.Parse(time.RFC3339, expiresAt); err == nil && now.After(expTime) {
					stats.ByReason["stale"]++
				}
			}

			contradicts := getPayloadStringList(payload, "contradicts")
			conflictResolved, _ := getPayloadBool(payload, "conflict_resolved")
			if len(contradicts) > 0 && !conflictResolved {
				stats.ByReason["contradiction"]++
			}

			supersedes, _ := getPayloadString(payload, "supersedes")
			if supersedes != "" {
				stats.ByReason["supersession"]++
			}

			confidence, _ := getPayloadFloat(payload, "confidence")
			if confidence < s.cfg.PrunerLowConfidenceThreshold {
				stats.ByReason["low_confidence"]++
			}

			createdAt, _ := getPayloadString(payload, "created_at")
			if createdAt != "" {
				if ct, err := time.Parse(time.RFC3339, createdAt); err == nil {
					if oldestTime.IsZero() || ct.Before(oldestTime) {
						oldestTime = ct
					}
					totalPendingDays += now.Sub(ct).Hours() / 24
				}
			}
		}

		pointCount += len(points)

		if id := points[len(points)-1].GetId().GetUuid(); id != "" {
			scrollOffset = id
		} else {
			break
		}
	}

	stats.TotalNeedsReview = pointCount
	if !oldestTime.IsZero() {
		stats.OldestItem = oldestTime.Format(time.RFC3339)
	}
	if pointCount > 0 {
		stats.AvgPendingDays = totalPendingDays / float64(pointCount)
	}

	writeJSON(w, 200, stats)
}

// ── Route dispatcher ──────────────────────────────────────────────────────────

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleReviewGet(w, r)
	case http.MethodPost:
		s.handleReviewPost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET or POST")
	}
}



