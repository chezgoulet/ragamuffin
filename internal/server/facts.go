package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/pruner"
	store "github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/qdrant/go-client/qdrant"
)

// ── Fact Data Model ──────────────────────────────────────────────────────

// Facts use a separate Qdrant collection (configurable via RAGAMUFFIN_FACTS_COLLECTION).
// Vector dimension is set by RAGAMUFFIN_FACTS_VECTOR_SIZE (default: EMBEDDING_DIMS = 1536).
// Facts are embedded on upsert for semantic search via the ?query= parameter.
// Prior to v1.0, facts used zero vectors — existing zero-vector facts are
// re-embedded at startup by the pruner's reembed scan.
//
// v0.5 extends the payload with lifecycle fields:
//   - source, source_type, confidence, ttl_days — client-supplied, optional
//   - status, supersedes, contradicts, conflict_resolved — server-managed
//   - confirmation_count, last_confirmed_at, created_at, expires_at — server-managed

// factPayload is the JSON body for upsert (POST /v1/facts).
// Pointers distinguish zero-value from omitted for confidence/ttl_days/version.
type factPayload struct {
	Key        string   `json:"key"`
	Value      string   `json:"value"`
	Tags       []string `json:"tags,omitempty"`
	Source     string   `json:"source,omitempty"`
	SourceType string   `json:"source_type,omitempty"`

	Confidence    *float64 `json:"confidence,omitempty"`     // 0.0–1.0; default 1.0
	TTLDays       *int     `json:"ttl_days,omitempty"`       // days; 0 = never expire
	Version       *int     `json:"version,omitempty"`        // >0 = versioned; 0/omitted = unversioned
	RelatedChunks []string `json:"related_chunks,omitempty"` // server-populated; ignored on upsert
	ValidFrom     *string  `json:"valid_from,omitempty"`     // RFC 3339; default = created_at
	ValidUntil    *string  `json:"valid_until,omitempty"`    // RFC 3339; null = no expiry
}

// factResponse is the JSON response for a single fact (v0.8 temporal reasoning).
type factResponse struct {
	Key                  string           `json:"key"`
	Value                string           `json:"value"`
	Tags                 []string         `json:"tags,omitempty"`
	Source               string           `json:"source,omitempty"`
	SourceType           string           `json:"source_type,omitempty"`
	Confidence           *float64         `json:"confidence,omitempty"`
	Status               string           `json:"status"`
	Version              int              `json:"version,omitempty"`
	Supersedes           string           `json:"supersedes"`
	SupersededBy         int              `json:"superseded_by,omitempty"`
	Contradicts          []string         `json:"contradicts,omitempty"`
	Refines              string           `json:"refines"`
	Supports             []string         `json:"supports,omitempty"`
	ConflictResolved     bool             `json:"conflict_resolved"`
	ConfirmationCount    int              `json:"confirmation_count"`
	LastConfirmedAt      string           `json:"last_confirmed_at,omitempty"`
	CreatedAt            string           `json:"created_at,omitempty"`
	UpdatedAt            string           `json:"updated_at"`
	ExpiresAt            string           `json:"expires_at,omitempty"`
	RelatedChunks        []string         `json:"related_chunks,omitempty"`
	ValidFrom            string           `json:"valid_from,omitempty"`  // RFC 3339; default = created_at
	ValidUntil           string           `json:"valid_until,omitempty"` // RFC 3339; null = no expiry
	Provenance           *provenanceEntry `json:"provenance,omitempty"`
	Score                float64          `json:"score,omitempty"`
	ReadCount            int              `json:"read_count,omitempty"`
	LastReadAt           string           `json:"last_read_at,omitempty"`
	Accessibility        *float64         `json:"accessibility,omitempty"`         // B1: soft decay score in (0,1]
	EffectiveConfidence  *float64         `json:"effective_confidence,omitempty"`  // B1: confidence × accessibility
	LastRecalledAt       string           `json:"last_recalled_at,omitempty"`      // B5: last recall (recall path)
	ReconsolidatedAt     string           `json:"reconsolidated_at,omitempty"`     // B5: last reconsolidation event
	ReconsolidationChain []string         `json:"reconsolidation_chain,omitempty"` // B5: immutable rewrite chain
}

// provenanceEntry is a resolved provenance link from a fact to its source.
type provenanceEntry struct {
	Target   string `json:"target"`
	Type     string `json:"type"`
	Resolved bool   `json:"resolved"`
}

// factUpdateRequest is the JSON body for PUT /v1/facts (partial update).
// Pointer fields distinguish "omitted" (nil) from "set to zero/empty" (non-nil).
type factUpdateRequest struct {
	Value             *string   `json:"value,omitempty"`
	Tags              *[]string `json:"tags,omitempty"`
	Source            *string   `json:"source,omitempty"`
	SourceType        *string   `json:"source_type,omitempty"`
	Status            *string   `json:"status,omitempty"`
	Supersedes        *string   `json:"supersedes,omitempty"`
	Refines           *string   `json:"refines,omitempty"`
	Supports          *[]string `json:"supports,omitempty"`
	Confidence        *float64  `json:"confidence,omitempty"`
	ConflictResolved  *bool     `json:"conflict_resolved,omitempty"`
	ConfirmationCount *int      `json:"confirmation_count,omitempty"`
	LastConfirmedAt   *string   `json:"last_confirmed_at,omitempty"`
	TTLDays           *int      `json:"ttl_days,omitempty"`
	ValidFrom         *string   `json:"valid_from,omitempty"`  // RFC 3339; set to "" to clear
	ValidUntil        *string   `json:"valid_until,omitempty"` // RFC 3339; set to "" to clear
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
	points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), factKeyFilter(key), 1, "")
	if err != nil {
		return false, err
	}
	return len(points) > 0, nil
}

// zeroFactVector returns a Vectors wrapper with a zero vector sized to match the
// configured facts vector dimension. Used as fallback when the embedder is
// unavailable (degraded mode).
func (s *Server) zeroFactVector() *qdrant.Vectors {
	return &qdrant.Vectors{
		VectorsOptions: &qdrant.Vectors_Vector{
			Vector: &qdrant.Vector{
				Data: make([]float32, s.cfg.FactsVectorSize),
			},
		},
	}
}

// embedFactVector embeds a fact value and returns a Vectors wrapper.
// Falls back to zeroFactVector if the embedder is unavailable or embedding fails.
func (s *Server) embedFactVector(ctx context.Context, value string) *qdrant.Vectors {
	if s.embedder != nil && value != "" {
		vec, err := s.embedder.EmbedSingle(ctx, value)
		if err == nil {
			return &qdrant.Vectors{
				VectorsOptions: &qdrant.Vectors_Vector{
					Vector: &qdrant.Vector{
						Data: vec,
					},
				},
			}
		}
		s.log(ctx).Warn("fact embed failed, using zero vector", "error", err)
	}
	return s.zeroFactVector()
}

// computeExpiresAt returns an ISO8601 timestamp for (now + ttl_days), or "" if 0.
func computeExpiresAt(ttlDays int) string {
	if ttlDays <= 0 {
		return ""
	}
	return time.Now().UTC().AddDate(0, 0, ttlDays).Format(time.RFC3339)
}

// defaultConfidence returns 1.0 if nil (omitted), otherwise clamps to [0,1].
// 0.0 is a valid confidence value — only nil means "not set" (#416).
func defaultConfidence(c *float64) float64 {
	if c == nil {
		return 1.0
	}
	if *c < 0 {
		return 0.0
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

	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024) // 1 MB for facts
	var fp factPayload
	if err := json.NewDecoder(r.Body).Decode(&fp); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid request body: %v", err))
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

	// Check if fact already exists → preserve server-managed lifecycle fields
	var createdAt, status, supersedes, refines, lastConfirmedAt, lastAccessedAt string
	var confirmationCount, supersededBy, accessCount float64
	var conflictResolved bool
	var existingContradicts, existingSupports []string

	exists, err := s.factExists(r.Context(), fp.Key)
	if err != nil {
		s.log(r.Context()).Error("fact existence check failed", "error", err)
		writeError(w, 500, "INTERNAL", "failed to check fact existence")
		return
	}
	if exists {
		points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), factKeyFilter(fp.Key), 1, "")
		if err != nil {
			s.log(r.Context()).Error("fact read for existing fields failed", "error", err)
		} else if len(points) > 0 {
			p := points[0].GetPayload()
			createdAt, _ = qutil.GetPayloadString(p, "created_at")
			status, _ = qutil.GetPayloadString(p, "status")
			supersedes, _ = qutil.GetPayloadString(p, "supersedes")
			refines, _ = qutil.GetPayloadString(p, "refines")
			lastConfirmedAt, _ = qutil.GetPayloadString(p, "last_confirmed_at")
			lastAccessedAt, _ = qutil.GetPayloadString(p, "last_accessed_at")
			confirmationCount, _ = qutil.GetPayloadFloat(p, "confirmation_count")
			supersededBy, _ = qutil.GetPayloadFloat(p, "superseded_by")
			accessCount, _ = qutil.GetPayloadFloat(p, "access_count")
			conflictResolved = qutil.GetPayloadBoolValue(p, "conflict_resolved")
			existingContradicts = qutil.GetPayloadStringList(p, "contradicts")
			existingSupports = qutil.GetPayloadStringList(p, "supports")
		}
	}
	if createdAt == "" {
		createdAt = now
	}
	if status == "" {
		status = "active"
	}
	if lastConfirmedAt == "" {
		lastConfirmedAt = now
	}
	if confirmationCount <= 0 {
		confirmationCount = 1
	}

	// Compute fields
	confidence := defaultConfidence(fp.Confidence)
	expiresAt := computeExpiresAt(intValue(fp.TTLDays))
	var expiresAtUnix float64
	if ttl := intValue(fp.TTLDays); ttl > 0 {
		expiresAtUnix = float64(time.Now().UTC().AddDate(0, 0, ttl).Unix())
	}

	// Determine version: explicit or parse from key pattern
	version := 0
	if fp.Version != nil {
		version = *fp.Version
	}
	if version <= 0 {
		// Try to infer version from key pattern like /vN/
		version = parseVersionFromKey(fp.Key)
	}

	validFrom := createdAt
	if fp.ValidFrom != nil && *fp.ValidFrom != "" {
		validFrom = *fp.ValidFrom
	}
	validUntil := ""
	if fp.ValidUntil != nil {
		validUntil = *fp.ValidUntil
	}

	// Compute unix timestamps for Qdrant range filtering
	var validFromUnix, validUntilUnix float64
	if t, err := time.Parse(time.RFC3339, validFrom); err == nil {
		validFromUnix = float64(t.Unix())
	}
	if validUntil != "" {
		if t, err := time.Parse(time.RFC3339, validUntil); err == nil {
			validUntilUnix = float64(t.Unix())
		}
	}

	payload := qdrant.NewValueMap(map[string]any{
		"fact_key":           fp.Key,
		"key_prefix":         versionKeyPrefix(fp.Key),
		"fact_value":         fp.Value,
		"source":             fp.Source,
		"source_type":        fp.SourceType,
		"confidence":         confidence,
		"version":            version,
		"status":             status,
		"supersedes":         supersedes,
		"superseded_by":      supersededBy,
		"refines":            refines,
		"conflict_resolved":  conflictResolved,
		"confirmation_count": confirmationCount,
		"last_confirmed_at":  lastConfirmedAt,
		"access_count":       accessCount,
		"last_accessed_at":   lastAccessedAt,
		"created_at":         createdAt,
		"updated_at":         now,
		"ttl_days":           intValue(fp.TTLDays),
		"expires_at":         expiresAt,
		"expires_at_unix":    expiresAtUnix,
		"valid_from":         validFrom,
		"valid_from_unix":    validFromUnix,
		"valid_until":        validUntil,
		"valid_until_unix":   validUntilUnix,
	})
	// Contradicts: carried forward on re-upsert, empty list on create
	payload["contradicts"] = qutil.NvList(existingContradicts)
	// Supports: carried forward on re-upsert, empty list on create
	payload["supports"] = qutil.NvList(existingSupports)

	if len(fp.Tags) > 0 {
		setPayloadTags(payload, fp.Tags)
	}
	if fp.Source != "" {
		payload["source"] = qutil.Nv(fp.Source)
	}
	if fp.SourceType != "" {
		payload["source_type"] = qutil.Nv(fp.SourceType)
	}

	point := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{
				Uuid: pointID,
			},
		},
		Payload: payload,
		Vectors: s.embedFactVector(r.Context(), fp.Value),
	}

	qc := s.factsQdrantFor(r.Context())
	if err := qc.Upsert(r.Context(), []*qdrant.PointStruct{point}); err != nil {
		s.log(r.Context()).Error("facts upsert failed", "error", err)
		writeError(w, 500, "UPSERT_FAILED", "failed to store fact")
		return
	}

	// Auto-supersede: if version > 0, find any active facts with lower versions
	// that share the same key prefix and mark them as superseded.
	if version > 0 {
		go s.supersedeOlderVersions(s.shutdownCtx, fp.Key, version)
	}

	// Background fact-to-chunk bridge: link this fact to related chunks
	vaultName := vaultFromContext(r.Context())
	if s.embedder != nil && fp.Value != "" && !s.QdrantReconnecting() && s.facts != nil {
		go s.linkFactToChunks(fp.Key, fp.Value, vaultName)
	}

	// Background provenance link: if source is set, register a resolvable link
	if fp.Source != "" && s.logStore != nil {
		go s.registerProvenanceLink(s.shutdownCtx, fp.Key, fp.Source, vaultName)
	}

	// Emit fact lifecycle event
	if s.emitter != nil {
		s.emitter.Emit(events.TypeFactCreated, events.FactCreatedData{
			Key:        fp.Key,
			Value:      fp.Value,
			Source:     fp.Source,
			Vault:      vaultFromContext(r.Context()),
			Confidence: confidence,
		})
	}

	resp := pointToFactResponse(point.Payload, fp.Key, s.cfg.DecayEnabled, s.cfg.DecayHalfLifeDays)
	writeJSON(w, 200, resp)
}

// ── GET /v1/facts ────────────────────────────────────────────────────────

func (s *Server) handleFactsGet(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	prefix := r.URL.Query().Get("prefix")
	keyContains := r.URL.Query().Get("key_contains")
	tag := r.URL.Query().Get("tag")
	status := r.URL.Query().Get("status")
	conflictResolvedStr := r.URL.Query().Get("conflict_resolved")
	timeFilterMode := r.URL.Query().Get("time_filter")

	// Exact key lookup
	if key != "" {
		points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), factKeyFilter(key), 1, "")
		if err != nil {
			s.log(r.Context()).Error("facts scroll failed", "error", err)
			writeError(w, 500, "SCROLL_FAILED", "failed to query facts")
			return
		}
		if len(points) == 0 {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("fact not found: %s", key))
			return
		}
		resp := pointToFact(points[0], s.cfg.DecayEnabled, s.cfg.DecayHalfLifeDays)
		if resp == nil {
			writeError(w, 500, "INVALID_DATA", "corrupt fact data")
			return
		}
		// Track access for importance scoring
		go s.incrementFactAccess(s.shutdownCtx, key)

		// Resolve provenance from link index
		if s.logStore != nil {
			provenance := s.resolveFactProvenance(r.Context(), key, vaultFromContext(r.Context()))
			resp.Provenance = provenance
		}

		writeJSON(w, 200, resp)
		return
	}

	// ── Semantic vector search ──────────────────────────────────────────
	query := r.URL.Query().Get("query")
	if query != "" {
		queryLimit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 1000 {
				queryLimit = v
			}
		}

		// Embed the query
		eb := s.embeddingFor(r.Context())
		if eb == nil {
			writeError(w, 500, "EMBEDDER_UNAVAILABLE", "embedder not configured")
			return
		}
		vector, err := eb.EmbedSingle(r.Context(), query)
		if err != nil {
			writeError(w, 500, "EMBED_FAILED", fmt.Sprintf("failed to embed query: %v", err))
			return
		}

		// Build filter from optional scalar params
		var conditions []*qdrant.Condition
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
		if conflictResolvedStr != "" {
			cr, err := strconv.ParseBool(conflictResolvedStr)
			if err == nil {
				conditions = append(conditions, &qdrant.Condition{
					ConditionOneOf: &qdrant.Condition_Field{
						Field: &qdrant.FieldCondition{
							Key: "conflict_resolved",
							Match: &qdrant.Match{
								MatchValue: &qdrant.Match_Boolean{
									Boolean: cr,
								},
							},
						},
					},
				})
			}
		}
		tf, err := timeFilter(timeFilterMode)
		if err != nil {
			writeError(w, 400, "INVALID_TIME_FILTER", err.Error())
			return
		}
		if tf != nil {
			conditions = append(conditions, tf)
		}

		var filter *qdrant.Filter
		if len(conditions) > 0 {
			filter = &qdrant.Filter{Must: conditions}
		}

		// Search facts collection
		qc := s.factsQdrantFor(r.Context())
		points, err := qc.Search(r.Context(), vector, uint64(queryLimit), 0.0, "", filter)
		if err != nil {
			writeError(w, 500, "SEARCH_FAILED", fmt.Sprintf("fact search failed: %v", err))
			return
		}

		resp := make([]factResponse, 0, len(points))
		for _, p := range points {
			key, _ := qutil.GetPayloadString(p.GetPayload(), "fact_key")
			fr := pointToFactResponse(p.GetPayload(), key, s.cfg.DecayEnabled, s.cfg.DecayHalfLifeDays)
			if fr != nil {
				fr.Score = float64(p.GetScore())
				resp = append(resp, *fr)
			}
		}

		// Reconsolidation-on-recall / accessibility decay (B1/B5): semantic
		// fact search is a recall path, so stamp the returned facts.
		stampFactsRecalled(s, r.Context(), points)

		writeJSON(w, 200, map[string]any{"entries": resp})
		return
	}

	// Build list filter from optional tag, status, and conflict_resolved.
	// All filters combine independently (AND). conflict_resolved does NOT require
	// status to be present — when used alone it filters all facts regardless of
	// status (#395).
	// Note: prefix filtering is applied in Go below (Qdrant has no native string
	// prefix match for payload fields, and Match_Text performs tokenized full-text
	// search which produces false positives).
	var conditions []*qdrant.Condition

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

	if conflictResolvedStr != "" {
		cr, err := strconv.ParseBool(conflictResolvedStr)
		if err == nil {
			conditions = append(conditions, &qdrant.Condition{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "conflict_resolved",
						Match: &qdrant.Match{
							MatchValue: &qdrant.Match_Boolean{
								Boolean: cr,
							},
						},
					},
				},
			})
		}
	}

	// Apply temporal filter
	tf, err := timeFilter(timeFilterMode)
	if err != nil {
		writeError(w, 400, "INVALID_TIME_FILTER", err.Error())
		return
	}
	if tf != nil {
		conditions = append(conditions, tf)
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
	points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), filter, uint32(limit+1), offset)
	if err != nil {
		s.log(r.Context()).Error("facts scroll failed", "error", err)
		writeError(w, 500, "SCROLL_FAILED", "failed to query facts")
		return
	}

	// Prefix filtering: Qdrant has no native string prefix match for payload
	// fields, so we filter in Go. The number of scroll results may exceed the
	// requested limit; we iterate until we fill the limit or run out.
	var nextToken string
	resp := make([]factResponse, 0, limit)
	for _, p := range points {
		key, _ := qutil.GetPayloadString(p.Payload, "fact_key")
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		if keyContains != "" && !strings.Contains(key, keyContains) {
			continue
		}
		// Skip internal keys (_ragamuffin/ prefix) in list results (#437).
		// Explicit key lookups via ?key= are unaffected.
		if strings.HasPrefix(key, "_ragamuffin/") {
			continue
		}
		if len(resp) >= limit {
			if id := p.Id.GetUuid(); id != "" {
				nextToken = id
			}
			break
		}
		if fr := pointToFact(p, s.cfg.DecayEnabled, s.cfg.DecayHalfLifeDays); fr != nil {
			resp = append(resp, *fr)
		}
	}

	respBody := map[string]any{
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
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid request body: %v", err))
		return
	}

	// Read existing point
	points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), factKeyFilter(key), 1, "")
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
	applyFieldUpdate(payload, "refines", req.Refines)
	applyFieldUpdate(payload, "valid_from", req.ValidFrom)
	applyFieldUpdate(payload, "valid_until", req.ValidUntil)
	// Propagate unix timestamp for range filtering
	if req.ValidFrom != nil {
		if t, err := time.Parse(time.RFC3339, *req.ValidFrom); err == nil {
			payload["valid_from_unix"] = qutil.Nv(float64(t.Unix()))
		} else {
			payload["valid_from_unix"] = qutil.Nv(float64(0))
		}
	}
	if req.ValidUntil != nil {
		if *req.ValidUntil != "" {
			if t, err := time.Parse(time.RFC3339, *req.ValidUntil); err == nil {
				payload["valid_until_unix"] = qutil.Nv(float64(t.Unix()))
			} else {
				payload["valid_until_unix"] = qutil.Nv(float64(0))
			}
		} else {
			payload["valid_until_unix"] = qutil.Nv(float64(0))
		}
	}
	if req.Supports != nil {
		sv := make([]*qdrant.Value, len(*req.Supports))
		for i, s := range *req.Supports {
			sv[i] = qutil.Nv(s)
		}
		payload["supports"] = &qdrant.Value{
			Kind: &qdrant.Value_ListValue{
				ListValue: &qdrant.ListValue{Values: sv},
			},
		}
	}

	if req.Confidence != nil {
		payload["confidence"] = qutil.Nv(*req.Confidence)
	}
	if req.ConflictResolved != nil {
		payload["conflict_resolved"] = qutil.Nv(*req.ConflictResolved)
	}
	if req.ConfirmationCount != nil {
		payload["confirmation_count"] = qutil.Nv(float64(*req.ConfirmationCount))
	}
	if req.LastConfirmedAt != nil {
		payload["last_confirmed_at"] = qutil.Nv(*req.LastConfirmedAt)
	}
	if req.TTLDays != nil {
		ttl := *req.TTLDays
		payload["ttl_days"] = qutil.Nv(float64(ttl))
		if expiresAt := computeExpiresAt(ttl); expiresAt != "" {
			payload["expires_at"] = qutil.Nv(expiresAt)
			payload["expires_at_unix"] = qutil.Nv(float64(time.Now().UTC().AddDate(0, 0, ttl).Unix()))
		} else {
			payload["expires_at"] = qutil.Nv("")
			payload["expires_at_unix"] = qutil.Nv(float64(0))
		}
	}
	if req.Tags != nil {
		// Clear and re-set
		delete(payload, "fact_tags")
		if len(*req.Tags) > 0 {
			setPayloadTags(payload, *req.Tags)
		}
	}

	payload["updated_at"] = qutil.Nv(now)

	// Reconsolidation-on-recall (B5): an update within the recall window is
	// recorded as a reconsolidation of the just-recalled memory. Read against
	// the original payload copied above (last_recalled_at is untouched here).
	s.applyReconsolidationV(payload, time.Now().UTC())

	// Read the final fact value from the updated payload for embedding
	finalValue := ""
	if v := payload["fact_value"]; v != nil {
		finalValue = v.GetStringValue()
	}

	point := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{
				Uuid: factKeyHash(key),
			},
		},
		Payload: payload,
		Vectors: s.embedFactVector(r.Context(), finalValue),
	}

	qc := s.factsQdrantFor(r.Context())
	if err := qc.Upsert(r.Context(), []*qdrant.PointStruct{point}); err != nil {
		s.log(r.Context()).Error("facts put failed", "error", err)
		writeError(w, 500, "UPSERT_FAILED", "failed to update fact")
		return
	}

	// If source was updated, refresh the provenance link
	if req.Source != nil && s.logStore != nil {
		vaultName := vaultFromContext(r.Context())
		go s.registerProvenanceLink(s.shutdownCtx, key, *req.Source, vaultName)
	}

	resp := pointToFactResponse(payload, key, s.cfg.DecayEnabled, s.cfg.DecayHalfLifeDays)
	writeJSON(w, 200, resp)
}

// ── PATCH /v1/facts ──────────────────────────────────────────────────────

// buildPatchUpdates constructs a SetPayload-ready updates map from a factUpdateRequest.
func buildPatchUpdates(updates factUpdateRequest) map[string]*qdrant.Value {
	now := time.Now().UTC().Format(time.RFC3339)
	m := make(map[string]*qdrant.Value)

	if updates.Value != nil {
		m["fact_value"] = qutil.Nv(*updates.Value)
	}
	if updates.Source != nil {
		m["source"] = qutil.Nv(*updates.Source)
	}
	if updates.SourceType != nil {
		m["source_type"] = qutil.Nv(*updates.SourceType)
	}
	if updates.Status != nil {
		m["status"] = qutil.Nv(*updates.Status)
	}
	if updates.Supersedes != nil {
		m["supersedes"] = qutil.Nv(*updates.Supersedes)
	}
	if updates.Refines != nil {
		m["refines"] = qutil.Nv(*updates.Refines)
	}
	if updates.Supports != nil {
		sv := make([]*qdrant.Value, len(*updates.Supports))
		for i, s := range *updates.Supports {
			sv[i] = qutil.Nv(s)
		}
		m["supports"] = &qdrant.Value{
			Kind: &qdrant.Value_ListValue{
				ListValue: &qdrant.ListValue{Values: sv},
			},
		}
	}
	if updates.Confidence != nil {
		m["confidence"] = qutil.Nv(*updates.Confidence)
	}
	if updates.ConflictResolved != nil {
		m["conflict_resolved"] = qutil.Nv(*updates.ConflictResolved)
	}
	if updates.ConfirmationCount != nil {
		m["confirmation_count"] = qutil.Nv(float64(*updates.ConfirmationCount))
	}
	if updates.LastConfirmedAt != nil {
		m["last_confirmed_at"] = qutil.Nv(*updates.LastConfirmedAt)
	}
	if updates.ValidFrom != nil {
		m["valid_from"] = qutil.Nv(*updates.ValidFrom)
	}
	if updates.ValidUntil != nil {
		m["valid_until"] = qutil.Nv(*updates.ValidUntil)
	}
	if updates.TTLDays != nil {
		ttl := *updates.TTLDays
		m["ttl_days"] = qutil.Nv(float64(ttl))
		if expiresAt := computeExpiresAt(ttl); expiresAt != "" {
			m["expires_at"] = qutil.Nv(expiresAt)
			m["expires_at_unix"] = qutil.Nv(float64(time.Now().UTC().AddDate(0, 0, ttl).Unix()))
		} else {
			m["expires_at"] = qutil.Nv("")
			m["expires_at_unix"] = qutil.Nv(float64(0))
		}
	}

	// Always set updated_at
	m["updated_at"] = qutil.Nv(now)
	return m
}

func (s *Server) handleFactsPatch(w http.ResponseWriter, r *http.Request) {
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	var req factBulkUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if len(req.Keys) == 0 {
		writeError(w, 400, "MISSING_KEYS", "keys array is required")
		return
	}
	if len(req.Keys) > 1000 {
		writeError(w, 400, "TOO_MANY_KEYS", "batch update limited to 1000 keys per request")
		return
	}

	// Build SetPayload updates once — no per-key payload construction (#408).
	// This avoids read-modify-write races by using Qdrant's SetPayload API
	// which atomically updates only the specified fields.
	updates := buildPatchUpdates(req.Updates)
	collection := s.factsCollectionFor(r.Context())

	results := make([]factBulkResult, 0, len(req.Keys))
	succeeded := 0
	failed := 0

	for _, key := range req.Keys {
		pointID := factKeyHash(key)

		// Check existence first (separate from the atomic SetPayload).
		// The NOT_FOUND check is racy, but the actual field update via
		// SetPayload is atomic per-point.
		points, err := s.facts.ScrollFiltered(r.Context(), collection, factKeyFilter(key), 1, "")
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

		qc := s.factsQdrantFor(r.Context())

		// Atomic field-level update — no read-modify-write race (#408)
		if err := qc.SetPayload(r.Context(), collection, []*qdrant.PointId{{
			PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointID},
		}}, updates); err != nil {
			results = append(results, factBulkResult{Key: key, OK: false, Error: "INTERNAL"})
			failed++
			continue
		}

		// Reconsolidation-on-recall (B5): if this fact was recalled within the
		// window, record the update as a reconsolidation event. Computed
		// per-key from the fact's own last_recalled_at read above.
		reconFields := map[string]any{}
		if s.applyReconsolidation(points[0].GetPayload(), reconFields, time.Now().UTC()) {
			if err := qc.SetPayload(r.Context(), collection, []*qdrant.PointId{{
				PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointID},
			}}, qdrant.NewValueMap(reconFields)); err != nil {
				s.log(r.Context()).Warn("reconsolidation stamp failed", "key", key, "error", err)
			}
		}

		// Handle tags separately: SetPayload can't "delete" a key. We need
		// to overwrite fact_tags. If nil was passed, don't touch tags.
		if req.Updates.Tags != nil {
			tagUpdates := make(map[string]*qdrant.Value)
			tagUpdates["updated_at"] = qutil.Nv(time.Now().UTC().Format(time.RFC3339))
			if len(*req.Updates.Tags) > 0 {
				tagValues := make([]*qdrant.Value, len(*req.Updates.Tags))
				for i, t := range *req.Updates.Tags {
					tagValues[i] = qutil.Nv(t)
				}
				tagUpdates["fact_tags"] = &qdrant.Value{
					Kind: &qdrant.Value_ListValue{
						ListValue: &qdrant.ListValue{Values: tagValues},
					},
				}
			} else {
				// Tags explicitly set to empty — store empty list
				tagUpdates["fact_tags"] = &qdrant.Value{
					Kind: &qdrant.Value_ListValue{
						ListValue: &qdrant.ListValue{Values: []*qdrant.Value{}},
					},
				}
			}
			if err := qc.SetPayload(r.Context(), collection, []*qdrant.PointId{{
				PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointID},
			}}, tagUpdates); err != nil {
				results = append(results, factBulkResult{Key: key, OK: false, Error: "INTERNAL"})
				failed++
				continue
			}
		}

		results = append(results, factBulkResult{Key: key, OK: true})
		succeeded++
	}

	status := 200
	if failed == len(req.Keys) {
		status = 500 // all updates failed (#419)
	}

	writeJSON(w, status, map[string]any{
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

	if err := s.factsQdrantFor(r.Context()).DeleteFiltered(r.Context(), s.factsCollectionFor(r.Context()), factKeyFilter(key)); err != nil {
		s.log(r.Context()).Error("facts delete failed", "error", err)
		writeError(w, 500, "DELETE_FAILED", "failed to delete fact")
		return
	}

	writeJSON(w, 200, map[string]any{
		"deleted": true,
		"key":     key,
	})
}

// ── Version-based supersedure ─────────────────────────────────────────────

// parseVersionFromKey detects a version segment in a fact key and returns
// the integer version value. Returns 0 if no version pattern is found.
// Recognized patterns: /vN/, /vN at end, vN/ at start.
// When multiple version segments exist (e.g. api/v2/models/v3/config), the
// LAST segment wins — nested API versions are more specific.
func parseVersionFromKey(key string) int {
	parts := strings.Split(key, "/")
	// Iterate in reverse so the last version segment wins
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if len(part) > 1 && part[0] == 'v' && isAllDigits(part[1:]) {
			var v int
			for _, c := range part[1:] {
				v = v*10 + int(c-'0')
			}
			if v >= 1 {
				return v
			}
		}
	}
	return 0
}

// isAllDigits returns true if s is non-empty and every character is a decimal digit.
func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// versionKeyPrefix returns the key prefix (everything before the version
// segment). Returns empty string if no version pattern is found.
func versionKeyPrefix(key string) string {
	parts := strings.Split(key, "/")
	// Iterate in reverse so the last version segment wins, matching
	// the pruner's parseVersionedKey (nested API versions are more specific).
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if len(part) > 1 && part[0] == 'v' {
			var v int
			for _, c := range part[1:] {
				if c < '0' || c > '9' {
					v = 0
					break
				}
				v = v*10 + int(c-'0')
			}
			if v >= 1 {
				parts = parts[:i]
				return strings.Join(parts, "/")
			}
		}
	}
	return ""
}

// supersedeOlderVersions queries the facts collection for active facts with
// the same prefix and a lower version, marking them as superseded.
// Runs asynchronously; errors are logged only.
func (s *Server) supersedeOlderVersions(ctx context.Context, key string, currentVersion int) {
	prefix := versionKeyPrefix(key)
	if prefix == "" {
		return
	}

	// Filter by status=active AND key_prefix=prefix, plus backward compat.
	// New facts store key_prefix in the payload (#409); for old facts without
	// the field, we include records where key_prefix is null and filter in Go.
	activeFilter := &qdrant.Filter{
		Must: []*qdrant.Condition{
			{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "status",
						Match: &qdrant.Match{
							MatchValue: &qdrant.Match_Keyword{
								Keyword: "active",
							},
						},
					},
				},
			},
		},
		Should: []*qdrant.Condition{
			{
				ConditionOneOf: &qdrant.Condition_IsNull{
					IsNull: &qdrant.IsNullCondition{Key: "key_prefix"},
				},
			},
			{
				ConditionOneOf: &qdrant.Condition_Field{
					Field: &qdrant.FieldCondition{
						Key: "key_prefix",
						Match: &qdrant.Match{
							MatchValue: &qdrant.Match_Keyword{Keyword: prefix},
						},
					},
				},
			},
		},
	}

	points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), activeFilter, 0, "")
	if err != nil {
		s.logger.Warn("supersedeOlderVersions: query failed", "error", err)
		return
	}

	marked := 0
	for _, pt := range points {
		payload := pt.GetPayload()
		factKey, _ := qutil.GetPayloadString(payload, "fact_key")
		if factKey == key {
			continue // skip self
		}

		// Prefer stored key_prefix; fall back to Go parse for old facts
		candidatePrefix, _ := qutil.GetPayloadString(payload, "key_prefix")
		if candidatePrefix == "" {
			candidatePrefix = versionKeyPrefix(factKey)
		}
		if candidatePrefix == "" || candidatePrefix != prefix {
			continue
		}

		// Mark as superseded
		pointID := pt.GetId().GetUuid()
		if pointID == "" {
			continue
		}

		now := time.Now().UTC()
		fields := map[string]any{
			"status":        "superseded",
			"superseded_by": currentVersion,
			"updated_at":    now.Format(time.RFC3339),
		}
		// Reconsolidation-on-recall (B5): if this older version was recalled
		// within the window, mark the supersede as a reconsolidation event.
		s.applyReconsolidation(payload, fields, now)

		if err := s.facts.SetPayload(ctx, s.factsCollectionFor(ctx), []*qdrant.PointId{{
			PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointID},
		}}, qdrant.NewValueMap(fields)); err != nil {
			s.logger.Warn("supersedeOlderVersions: failed to mark", "key", factKey, "error", err)
			continue
		}
		marked++
	}

	if marked > 0 {
		s.logger.Info("auto-superseded older versions", "prefix", prefix, "current_version", currentVersion, "marked", marked)
	}
}

// Migration sentinel key — stored as a fact_key when migration completes (#412).
// The key prefix `_ragamuffin/` is reserved for internal use.
const migrationSentinelKey = "_ragamuffin/_migration/v0.6.1"

// migrateFacts reads all existing facts and fills in default values for
// v0.5 lifecycle fields that didn't exist before. This is a one-time
// migration run at server startup. Errors are logged but non-fatal.
func (s *Server) migrateFacts() {
	if s.facts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	collection := s.cfg.FactsCollection

	// Check migration sentinel — skip if already done (#412)
	sentinelPoints, err := s.facts.ScrollFiltered(ctx, collection, factKeyFilter(migrationSentinelKey), 1, "")
	if err == nil && len(sentinelPoints) > 0 {
		s.logger.Info("facts migration already complete, skipping")
		return
	}

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
				payload["status"] = qutil.Nv("active")
				needsUpdate = true
			}
			// confidence: default 1.0
			if _, ok := payload["confidence"]; !ok {
				payload["confidence"] = qutil.Nv(1.0)
				needsUpdate = true
			}
			// source_type: default "manual"
			if _, ok := payload["source_type"]; !ok {
				payload["source_type"] = qutil.Nv("manual")
				needsUpdate = true
			}
			// conflict_resolved: default true
			if _, ok := payload["conflict_resolved"]; !ok {
				payload["conflict_resolved"] = qutil.Nv(true)
				needsUpdate = true
			}
			// confirmation_count: default 1
			if _, ok := payload["confirmation_count"]; !ok {
				payload["confirmation_count"] = qutil.Nv(float64(1))
				needsUpdate = true
			}
			// created_at: default to updated_at or now
			if _, ok := payload["created_at"]; !ok {
				if ua, ok := payload["updated_at"]; ok && ua.GetStringValue() != "" {
					payload["created_at"] = qutil.Nv(ua.GetStringValue())
				} else {
					payload["created_at"] = qutil.Nv(now)
				}
				needsUpdate = true
			}
			// last_confirmed_at: default to updated_at or now
			if _, ok := payload["last_confirmed_at"]; !ok {
				if ua, ok := payload["updated_at"]; ok && ua.GetStringValue() != "" {
					payload["last_confirmed_at"] = qutil.Nv(ua.GetStringValue())
				} else {
					payload["last_confirmed_at"] = qutil.Nv(now)
				}
				needsUpdate = true
			}
			// supersedes: default ""
			if _, ok := payload["supersedes"]; !ok {
				payload["supersedes"] = qutil.Nv("")
				needsUpdate = true
			}
			// key_prefix: populate from fact_key (#409)
			if _, ok := payload["key_prefix"]; !ok {
				fk, _ := qutil.GetPayloadString(payload, "fact_key")
				payload["key_prefix"] = qutil.Nv(versionKeyPrefix(fk))
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
				payload["source"] = qutil.Nv("")
				needsUpdate = true
			}
			// ttl_days: default 0
			if _, ok := payload["ttl_days"]; !ok {
				payload["ttl_days"] = qutil.Nv(float64(0))
				needsUpdate = true
			}
			// expires_at: default ""
			if _, ok := payload["expires_at"]; !ok {
				payload["expires_at"] = qutil.Nv("")
				needsUpdate = true
			}
			// expires_at_unix: default 0
			if _, ok := payload["expires_at_unix"]; !ok {
				payload["expires_at_unix"] = qutil.Nv(float64(0))
				needsUpdate = true
			}
			// version: populate from key pattern or default 0
			if _, ok := payload["version"]; !ok {
				factKey, _ := qutil.GetPayloadString(payload, "fact_key")
				v := parseVersionFromKey(factKey)
				payload["version"] = qutil.Nv(float64(v))
				needsUpdate = true
			}
			// superseded_by: default 0
			if _, ok := payload["superseded_by"]; !ok {
				payload["superseded_by"] = qutil.Nv(float64(0))
				needsUpdate = true
			}
			// refines: default ""
			if _, ok := payload["refines"]; !ok {
				payload["refines"] = qutil.Nv("")
				needsUpdate = true
			}
			// supports: default empty list
			if _, ok := payload["supports"]; !ok {
				payload["supports"] = &qdrant.Value{
					Kind: &qdrant.Value_ListValue{
						ListValue: &qdrant.ListValue{Values: []*qdrant.Value{}},
					},
				}
				needsUpdate = true
			}

			if !needsUpdate {
				continue
			}

			// Preserve existing vector if available; fall back to zero vector
			// so migration doesn't destroy embeddings for the embedded store.
			vec := store.GetPointVector(p)
			if vec == nil || len(vec) == 0 {
				vec = make([]float32, s.cfg.FactsVectorSize)
			}
			point := &qdrant.PointStruct{
				Id:      p.Id,
				Payload: payload,
				Vectors: &qdrant.Vectors{
					VectorsOptions: &qdrant.Vectors_Vector{
						Vector: &qdrant.Vector{Data: vec},
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

	// Write migration sentinel so we don't re-scan on every startup (#412)
	sentinelPoint := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{Uuid: factKeyHash(migrationSentinelKey)},
		},
		Payload: qdrant.NewValueMap(map[string]any{
			"fact_key":   migrationSentinelKey,
			"status":     "active",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}),
		Vectors: s.zeroFactVector(),
	}
	if err := s.facts.Upsert(ctx, []*qdrant.PointStruct{sentinelPoint}); err != nil {
		s.logger.Warn("facts migration: failed to write sentinel", "error", err)
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
func pointToFact(p *qdrant.RetrievedPoint, decayEnabled bool, halfLifeDays float64) *factResponse {
	if p == nil {
		return nil
	}
	return pointToFactResponse(p.GetPayload(), "", decayEnabled, halfLifeDays)
}

// pointToFactResponse builds a factResponse from a payload map and (optionally) a key.
// If key is provided, it's used; otherwise reads from payload.
func pointToFactResponse(payload map[string]*qdrant.Value, keyOverride string, decayEnabled bool, halfLifeDays float64) *factResponse {
	fr := &factResponse{}

	if keyOverride != "" {
		fr.Key = keyOverride
	} else {
		fr.Key, _ = qutil.GetPayloadString(payload, "fact_key")
	}

	fr.Value, _ = qutil.GetPayloadString(payload, "fact_value")
	fr.Tags = qutil.GetPayloadStringList(payload, "fact_tags")
	fr.Source, _ = qutil.GetPayloadString(payload, "source")
	fr.SourceType, _ = qutil.GetPayloadString(payload, "source_type")
	if c, ok := qutil.GetPayloadFloat(payload, "confidence"); ok {
		fr.Confidence = &c
	}
	fr.Status, _ = qutil.GetPayloadString(payload, "status")
	fr.Version, _ = qutil.GetPayloadInt(payload, "version")
	fr.Supersedes, _ = qutil.GetPayloadString(payload, "supersedes")
	fr.SupersededBy, _ = qutil.GetPayloadInt(payload, "superseded_by")
	fr.Contradicts = qutil.GetPayloadStringList(payload, "contradicts")
	fr.Refines, _ = qutil.GetPayloadString(payload, "refines")
	fr.Supports = qutil.GetPayloadStringList(payload, "supports")
	fr.ConflictResolved, _ = qutil.GetPayloadBool(payload, "conflict_resolved")
	fr.ConfirmationCount, _ = qutil.GetPayloadInt(payload, "confirmation_count")
	fr.LastConfirmedAt, _ = qutil.GetPayloadString(payload, "last_confirmed_at")
	fr.CreatedAt, _ = qutil.GetPayloadString(payload, "created_at")
	fr.UpdatedAt, _ = qutil.GetPayloadString(payload, "updated_at")
	fr.ExpiresAt, _ = qutil.GetPayloadString(payload, "expires_at")
	fr.RelatedChunks = qutil.GetPayloadStringList(payload, "related_chunks")
	fr.ValidFrom, _ = qutil.GetPayloadString(payload, "valid_from")
	fr.ValidUntil, _ = qutil.GetPayloadString(payload, "valid_until")
	fr.ReadCount, _ = qutil.GetPayloadInt(payload, "access_count")
	fr.LastReadAt, _ = qutil.GetPayloadString(payload, "last_accessed_at")
	// Accessibility / effective_confidence (B1) are computed live from the
	// payload's last_accessed_at rather than read from a stored field. The
	// stored value was stamped at access time (when last_accessed_at == now),
	// so it always reported ~1.0 and never reflected real decay. Computing on
	// read gives a true decayed value, matching what review stats already do.
	if decayEnabled {
		now := time.Now().UTC()
		acc := pruner.Accessibility(payload, now, halfLifeDays)
		eff := pruner.EffectiveConfidence(payload, now, halfLifeDays)
		fr.Accessibility = &acc
		fr.EffectiveConfidence = &eff
	}
	fr.LastRecalledAt, _ = qutil.GetPayloadString(payload, "last_recalled_at")
	fr.ReconsolidatedAt, _ = qutil.GetPayloadString(payload, "reconsolidated_at")
	fr.ReconsolidationChain = qutil.GetPayloadStringList(payload, "reconsolidation_chain")

	if fr.Status == "" {
		fr.Status = "active"
	}

	return fr
}

// applyFieldUpdate sets payload[key] = qutil.Nv(*val) when val is non-nil.
// registerProvenanceLink registers a resolvable provenance link from a fact to
// its source in the link index. Runs as a background goroutine.
func (s *Server) registerProvenanceLink(ctx context.Context, factKey, source, vaultName string) {
	if s.logStore == nil {
		return
	}
	err := s.logStore.WriteLinks(ctx, vaultName, []logstore.LinkRecord{{
		SourcePath: "fact:" + factKey,
		TargetPath: source,
		LinkType:   "provenance",
		Context:    "fact: " + factKey,
	}})
	if err != nil {
		s.logger.Warn("provenance: write link failed", "fact", factKey, "error", err)
	}
}

// resolveFactProvenance looks up the provenance link for a fact from the link index.
// Returns nil if no provenance link exists.
func (s *Server) resolveFactProvenance(ctx context.Context, factKey, vaultName string) *provenanceEntry {
	links, err := s.logStore.GetOutboundLinks(ctx, "fact:"+factKey, vaultName)
	if err != nil {
		s.logger.Warn("provenance: lookup failed", "fact", factKey, "error", err)
		return nil
	}
	for _, l := range links {
		if l.Type == "provenance" {
			return &provenanceEntry{
				Target:   l.Target,
				Type:     l.Type,
				Resolved: l.Target != "",
			}
		}
	}
	return nil
}

// linkFactToChunks embeds the fact value, searches the vault's chunk collection
// for semantically similar chunks (score > 0.7), and stores the top chunk IDs
// in the fact's related_chunks payload field. Runs as a background goroutine.
func (s *Server) linkFactToChunks(key, value, vaultName string) {
	ctx, cancel := context.WithTimeout(s.shutdownCtx, 30*time.Second)
	defer cancel()

	// Embed the fact value
	vector, err := s.embedder.EmbedSingle(ctx, value)
	if err != nil {
		s.logger.Warn("fact bridge: embed failed", "key", key, "error", err)
		return
	}

	// Resolve vault-scoped chunk client
	var qc store.FactStore
	if vaultName != "" {
		qc = s.indexers.GetClient(vaultName)
	}
	if qc == nil {
		qc = s.qdrant
	}
	if qc == nil {
		s.logger.Warn("fact bridge: no Qdrant client available", "key", key)
		return
	}

	// Search chunk collection with score threshold
	results, err := qc.Search(ctx, vector, 10, 0.7, "", nil)
	if err != nil {
		s.logger.Warn("fact bridge: search failed", "key", key, "error", err)
		return
	}

	// Collect chunk IDs (max 20)
	chunkIDs := make([]string, 0, len(results))
	for _, r := range results {
		if len(chunkIDs) >= 20 {
			break
		}
		if r.GetId() != nil {
			chunkIDs = append(chunkIDs, r.GetId().GetUuid())
		}
	}
	if len(chunkIDs) == 0 {
		return
	}

	// Set related_chunks on the fact point via SetPayload
	collection := s.cfg.FactsCollection
	if vaultName != "" {
		collection = s.cfg.FactsCollectionFor(vaultName)
	}

	// Build list value from chunk IDs
	chunkValues := make([]*qdrant.Value, len(chunkIDs))
	for i, id := range chunkIDs {
		chunkValues[i] = qutil.Nv(id)
	}

	pointID := factKeyHash(key)
	err = s.facts.SetPayload(ctx, collection, []*qdrant.PointId{{
		PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointID},
	}}, map[string]*qdrant.Value{
		"related_chunks": {
			Kind: &qdrant.Value_ListValue{
				ListValue: &qdrant.ListValue{Values: chunkValues},
			},
		},
	})
	if err != nil {
		s.logger.Warn("fact bridge: set payload failed", "key", key, "error", err)
		return
	}

	s.logger.Debug("fact bridge: linked chunks", "key", key, "chunks", len(chunkIDs))
}

func applyFieldUpdate(payload map[string]*qdrant.Value, key string, val *string) {
	if val != nil {
		payload[key] = qutil.Nv(*val)
	}
}

// setPayloadTags writes fact_tags as a Qdrant list value into the payload map.
func setPayloadTags(payload map[string]*qdrant.Value, tags []string) {
	tagVals := make([]*qdrant.Value, len(tags))
	for i, t := range tags {
		tagVals[i] = qutil.Nv(t)
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

// timeFilter builds a Qdrant Must condition for temporal filtering.
// Modes:
//   - "active" or "": valid_from <= now < valid_until (or no bounds = always active)
//   - "active_at:2006-01-02T15:04:05Z": effective at a specific point in time
//   - "active_at:2006-01-02": also accepted, midnight UTC
//   - "all": no filter (returns nil, nil)
//
// Returns an error for malformed active_at values.
func timeFilter(mode string) (*qdrant.Condition, error) {
	if mode == "all" {
		return nil, nil
	}

	now := time.Now().UTC()
	target := now

	if mode != "" {
		if strings.HasPrefix(mode, "active_at:") {
			ts := strings.TrimPrefix(mode, "active_at:")
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				target = t
			} else if t, err := time.Parse("2006-01-02", ts); err == nil {
				target = t
			} else {
				return nil, fmt.Errorf("invalid timestamp in active_at: %q (expected RFC 3339 or YYYY-MM-DD)", ts)
			}
		} else {
			return nil, fmt.Errorf("unknown time filter mode: %q", mode)
		}
	}

	targetUnix := float64(target.Unix())

	// Must: valid_from_unix <= target AND (valid_until_unix == 0 OR target < valid_until_unix)
	return &qdrant.Condition{
		ConditionOneOf: &qdrant.Condition_Filter{
			Filter: &qdrant.Filter{
				Must: []*qdrant.Condition{
					// valid_from_unix <= target (or valid_from_unix == 0 for unset)
					{
						ConditionOneOf: &qdrant.Condition_Filter{
							Filter: &qdrant.Filter{
								Should: []*qdrant.Condition{
									{
										ConditionOneOf: &qdrant.Condition_Field{
											Field: &qdrant.FieldCondition{
												Key: "valid_from_unix",
												Range: &qdrant.Range{
													Lte: &targetUnix,
												},
											},
										},
									},
									{
										ConditionOneOf: &qdrant.Condition_Field{
											Field: &qdrant.FieldCondition{
												Key: "valid_from_unix",
												Range: &qdrant.Range{
													Gte: float64Ptr(0),
													Lte: float64Ptr(0),
												},
											},
										},
									},
								},
							},
						},
					},
					// (valid_until_unix == 0 OR target < valid_until_unix)
					{
						ConditionOneOf: &qdrant.Condition_Filter{
							Filter: &qdrant.Filter{
								Should: []*qdrant.Condition{
									{
										ConditionOneOf: &qdrant.Condition_Field{
											Field: &qdrant.FieldCondition{
												Key: "valid_until_unix",
												Range: &qdrant.Range{
													Gte: float64Ptr(0),
													Lte: float64Ptr(0),
												},
											},
										},
									},
									{
										ConditionOneOf: &qdrant.Condition_Field{
											Field: &qdrant.FieldCondition{
												Key: "valid_until_unix",
												Range: &qdrant.Range{
													Gt: &targetUnix,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}, nil
}

// float64Ptr returns a pointer to the given float64 value.
func float64Ptr(v float64) *float64 {
	return &v
}

// ── Payload extraction helpers ───────────────────────────────────────────

func (s *Server) incrementFactAccess(ctx context.Context, factKey string) {
	points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), factKeyFilter(factKey), 1, "")
	if err != nil || len(points) == 0 {
		s.log(ctx).Debug("incrementFactAccess: fact not found", "key", factKey, "error", err)
		return
	}
	s.stampFactPointRecalled(ctx, points[0])
}

// stampFactsRecalled records recall access (access_count, last_accessed_at,
// last_recalled_at, decay fields) for a batch of facts that were just returned
// by a recall path (semantic fact search, /ask fact context). It is the shared
// entry point so that reconsolidation-on-recall (B5) and accessibility decay
// (B1) are stamped consistently regardless of which endpoint recalled the fact.
//
// It is a cheap no-op when neither reconsolidation nor decay is enabled and the
// per-point debounce would skip the write anyway. Each point is stamped
// independently; a failure on one does not abort the rest.
func stampFactsRecalled[T factPoint](s *Server, ctx context.Context, points []T) {
	for _, pt := range points {
		s.stampFactPointRecalled(ctx, pt)
	}
}

// factPoint is the minimal surface shared by qdrant.RetrievedPoint (scroll) and
// qdrant.ScoredPoint (search) so the recall-stamping helpers accept results
// from either API.
type factPoint interface {
	GetId() *qdrant.PointId
	GetPayload() map[string]*qdrant.Value
}

// stampFactPointRecalled applies the access/recall/decay payload update to a
// single already-fetched fact point. Callers that only have a fact key should
// use incrementFactAccess; recall paths that already hold the points should use
// stampFactsRecalled.
func (s *Server) stampFactPointRecalled(ctx context.Context, pt factPoint) {
	if pt == nil {
		return
	}
	pointID := pt.GetId()
	if pointID == nil || pointID.GetUuid() == "" {
		return
	}
	factKey, _ := qutil.GetPayloadString(pt.GetPayload(), "fact_key")

	// Read current access_count or default 0
	currentCount := qutil.GetPayloadIntValue(pt.GetPayload(), "access_count")
	lastAccessStr, _ := qutil.GetPayloadString(pt.GetPayload(), "last_accessed_at")
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	// Debounce: skip write if read within the last 60 seconds, unless it's
	// every 5th read (to keep the count roughly accurate).
	newCount := currentCount + 1
	if lastAccessStr != "" {
		lastAccess, err := time.Parse(time.RFC3339, lastAccessStr)
		if err == nil && now.Sub(lastAccess) < 60*time.Second && newCount%5 != 0 {
			// Too soon since last write — skip the Qdrant write but still
			// count in-the-money for the caller's perspective.
			// After 5 reads the write goes through regardless.
			return
		}
	}

	setPayload := map[string]*qdrant.Value{
		"access_count":     qutil.Nv(float64(newCount)),
		"last_accessed_at": qutil.Nv(nowStr),
	}

	// Reconsolidation-on-recall (B5): stamp the recall timestamp so that a
	// subsequent update within the reconsolidation window is treated as a
	// rewrite of the freshly-recalled memory rather than a cold edit
	// (Nader et al. 2000). This is independent of the debounced last_accessed_at
	// above and is only written when reconsolidation is enabled.
	if s.cfg.ReconsolidationEnabled {
		setPayload["last_recalled_at"] = qutil.Nv(nowStr)
	}

	// Note: accessibility / effective_confidence are intentionally NOT stamped
	// here. Stamping at access time (when last_accessed_at == now) always yields
	// ~1.0 and never reflects real decay. These fields are computed live in
	// pointToFactResponse from the stored last_accessed_at instead (B1).

	if err := s.facts.SetPayload(ctx, s.factsCollectionFor(ctx), []*qdrant.PointId{pointID}, setPayload); err != nil {
		s.log(ctx).Debug("incrementFactAccess: set payload failed", "key", factKey, "error", err)
	}
}

// applyReconsolidation records a reconsolidation event when a mutation lands
// within the reconsolidation window of the fact's last recall (Nader et al.
// 2000). It reads last_recalled_at from the current payload and, when the
// window is satisfied, writes reconsolidated_at into setPayload and appends the
// recall timestamp to the immutable reconsolidation_chain. It is a no-op when
// reconsolidation is disabled or the fact was not recently recalled.
//
// setPayload must be the map of fields about to be written for the fact; the
// updated chain is written back into it. The raw event log is never touched.
func (s *Server) applyReconsolidation(current map[string]*qdrant.Value, setPayload map[string]any, now time.Time) bool {
	nowStr, chain, ok := s.reconsolidationChain(current, now)
	if !ok {
		return false
	}
	setPayload["reconsolidated_at"] = nowStr
	setPayload["reconsolidation_chain"] = chain
	return true
}

// applyReconsolidationV is the qdrant.Value variant of applyReconsolidation for
// callers (e.g. the review flow) that mutate a map[string]*qdrant.Value in
// place. It reads the prior state and writes the updated chain into the same map.
func (s *Server) applyReconsolidationV(payload map[string]*qdrant.Value, now time.Time) bool {
	nowStr, chain, ok := s.reconsolidationChain(payload, now)
	if !ok {
		return false
	}
	payload["reconsolidated_at"] = qutil.Nv(nowStr)
	payload["reconsolidation_chain"] = qutil.NvList(chain)
	return true
}

// reconsolidationChain decides whether a mutation at time now counts as a
// reconsolidation of a recently-recalled fact and, if so, returns the new
// timestamp and the appended reconsolidation_chain. Nader et al. 2000.
func (s *Server) reconsolidationChain(current map[string]*qdrant.Value, now time.Time) (string, []string, bool) {
	if !s.cfg.ReconsolidationEnabled {
		return "", nil, false
	}
	lastRecalledStr, ok := qutil.GetPayloadString(current, "last_recalled_at")
	if !ok || lastRecalledStr == "" {
		return "", nil, false
	}
	lastRecalled, err := time.Parse(time.RFC3339, lastRecalledStr)
	if err != nil {
		return "", nil, false
	}
	if now.Sub(lastRecalled) > s.cfg.ReconsolidationWindow || now.Before(lastRecalled) {
		return "", nil, false
	}
	nowStr := now.Format(time.RFC3339)
	chain := append(qutil.GetPayloadStringList(current, "reconsolidation_chain"), nowStr)
	return nowStr, chain, true
}

// ── /v1/facts/{key}/provenance — Fact Lineage (#803) ──────────────────────────

type provenanceResponse struct {
	Key           string   `json:"key"`
	Value         string   `json:"value"`
	Source        string   `json:"source"`
	SourceType    string   `json:"source_type"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	RelatedChunks []string `json:"related_chunks,omitempty"`
}

func (s *Server) handleProvenance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeError(w, 400, "INVALID_REQUEST", "fact key is required")
		return
	}

	ctx := r.Context()
	pointID := &qdrant.PointId{
		PointIdOptions: &qdrant.PointId_Uuid{Uuid: factKeyHash(key)},
	}

	points, err := s.facts.GetPoints(ctx, s.factsCollectionFor(ctx), []*qdrant.PointId{pointID})
	if err != nil {
		writeError(w, 502, "QUERY_FAILED", fmt.Sprintf("failed to query fact: %v", err))
		return
	}
	if len(points) == 0 {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("fact %q not found", key))
		return
	}

	payload := points[0].GetPayload()
	if payload == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("fact %q has no data", key))
		return
	}

	var relatedChunks []string
	if rc, ok := payload["related_chunks"]; ok {
		for _, v := range rc.GetListValue().GetValues() {
			relatedChunks = append(relatedChunks, v.GetStringValue())
		}
	}

	writeJSON(w, 200, provenanceResponse{
		Key:           payload["fact_key"].GetStringValue(),
		Value:         payload["fact_value"].GetStringValue(),
		Source:        payload["source"].GetStringValue(),
		SourceType:    payload["source_type"].GetStringValue(),
		CreatedAt:     payload["created_at"].GetStringValue(),
		UpdatedAt:     payload["updated_at"].GetStringValue(),
		RelatedChunks: relatedChunks,
	})
}

// ── /v1/facts/{key}/history — Fact Evolution Timeline (#805) ──────────────────

type historyEntry struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
	Detail    string `json:"detail,omitempty"`
}

func (s *Server) handleFactHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeError(w, 400, "INVALID_REQUEST", "fact key is required")
		return
	}

	ctx := r.Context()
	history := []historyEntry{}

	// Get current fact state to include created/updated in timeline
	pointID := &qdrant.PointId{
		PointIdOptions: &qdrant.PointId_Uuid{Uuid: factKeyHash(key)},
	}
	points, err := s.facts.GetPoints(ctx, s.factsCollectionFor(ctx), []*qdrant.PointId{pointID})
	if err == nil && len(points) > 0 {
		payload := points[0].GetPayload()
		if payload != nil {
			if created := payload["created_at"].GetStringValue(); created != "" {
				history = append(history, historyEntry{Event: "created", Timestamp: created})
			}
			if updated := payload["updated_at"].GetStringValue(); updated != "" {
				history = append(history, historyEntry{Event: "updated", Timestamp: updated})
			}
		}
	}

	// Query review_resolution logstore entries for this fact key
	if s.logStore != nil {
		err := s.logStore.QueryResolutions(ctx, key, func(action, createdAt string) {
			history = append(history, historyEntry{
				Event:     action,
				Timestamp: createdAt,
			})
		})
		if err != nil {
			s.log(ctx).Debug("history: resolution query failed", "key", key, "error", err)
		}
	}

	if len(history) == 0 {
		writeJSON(w, 200, []historyEntry{})
		return
	}

	writeJSON(w, 200, history)
}
