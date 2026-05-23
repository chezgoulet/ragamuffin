package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	internalQdrant "github.com/chezgoulet/ragamuffin/internal/qdrant"
	qdrant "github.com/qdrant/go-client/qdrant"
)

// ── Mock FactStore for review tests ───────────────────────────────────────────

type reviewMockStore struct {
	internalQdrant.FactStore
	mu sync.Mutex

	collection     string
	points         []*qdrant.RetrievedPoint // stored state
	upserted       []*qdrant.PointStruct     // what was last upserted
	scrollFilteredFn func(ctx context.Context, collection string, filter *qdrant.Filter, limit uint32, offset string) ([]*qdrant.RetrievedPoint, error)
}

func (m *reviewMockStore) Collection() string { return m.collection }

func (m *reviewMockStore) Upsert(_ context.Context, points []*qdrant.PointStruct) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Replace-insert: same ID overwrites
	for _, p := range points {
		found := false
		for i, existing := range m.points {
			if existing.GetId().GetUuid() == p.GetId().GetUuid() {
				// Replace stored point with upserted data for ScrollFiltered consistency
				pl := make(map[string]*qdrant.Value)
				for k, v := range p.GetPayload() {
					pl[k] = v
				}
				m.points[i] = &qdrant.RetrievedPoint{
					Id:      p.GetId(),
					Payload: pl,
				}
				found = true
				break
			}
		}
		if !found {
			pl := make(map[string]*qdrant.Value)
			for k, v := range p.GetPayload() {
				pl[k] = v
			}
			m.points = append(m.points, &qdrant.RetrievedPoint{
				Id:      p.GetId(),
				Payload: pl,
			})
		}
		m.upserted = append(m.upserted, p)
	}
	return nil
}

func (m *reviewMockStore) ScrollFiltered(ctx context.Context, collection string, filter *qdrant.Filter, limit uint32, offset string) ([]*qdrant.RetrievedPoint, error) {
	m.mu.Lock()
	fn := m.scrollFilteredFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, collection, filter, limit, offset)
	}
	// Default: filter points by status = needs_review
	m.mu.Lock()
	defer m.mu.Unlock()
	if offset != "" {
		return nil, nil // end of pagination
	}
	var result []*qdrant.RetrievedPoint
	for _, p := range m.points {
		if status, _ := getPayloadString(p.GetPayload(), "status"); status == "needs_review" {
			result = append(result, p)
		}
	}
	// Apply limit
	if int(limit) > 0 && len(result) > int(limit) {
		result = result[:limit]
	}
	return result, nil
}

func (m *reviewMockStore) CreatePayloadIndex(_ context.Context, _, _, _ string) error { return nil }

func (m *reviewMockStore) Health(_ context.Context) error { return nil }

// review helpers
func newReviewServer(store *reviewMockStore) *Server {
	cfg := &config.Config{
		VaultPath:        "/test/vault",
		FactsCollection:  "test_facts",
	}
	rl := ratelimit.New(false)
	idxm := indexer.NewManager()
	idxm.Add("default", indexer.New("/test/vault", nil, nil, nil), nil)
	logger := slog.New(slog.DiscardHandler)
	return New(cfg, store, store, nil, nil, idxm, nil, rl, nil, nil, nil, nil, logger)
}

func makeNeedsReviewPoint(id, key, value string, overrides map[string]any) *qdrant.RetrievedPoint {
	payload := map[string]*qdrant.Value{
		"fact_key":          nv(key),
		"fact_value":        nv(value),
		"status":            nv("needs_review"),
		"confidence":        nv(0.3),
		"conflict_resolved": nv(false),
		"created_at":        nv(time.Now().UTC().Format(time.RFC3339)),
		"updated_at":        nv(time.Now().UTC().Format(time.RFC3339)),
		"source":            nv(""),
		"source_type":       nv(""),
		"supersedes":        nv(""),
		"contradicts":       nvList([]string{}),
		"ttl_days":          nv(float64(0)),
		"expires_at":        nv(""),
		"expires_at_unix":   nv(float64(0)),
		"confirmation_count": nv(float64(0)),
		"last_confirmed_at": nv(""),
	}
	for k, v := range overrides {
		switch val := v.(type) {
		case string:
			payload[k] = nv(val)
		case float64:
			payload[k] = nv(val)
		case bool:
			payload[k] = nv(val)
		case []string:
			payload[k] = nvList(val)
		}
	}
	return &qdrant.RetrievedPoint{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{
				Uuid: id,
			},
		},
		Payload: payload,
	}
}

// nv converts a Go value to a qdrant Value for test payload construction.
func nv(v interface{}) *qdrant.Value {
	switch val := v.(type) {
	case string:
		return &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: val}}
	case float64:
		return &qdrant.Value{Kind: &qdrant.Value_DoubleValue{DoubleValue: val}}
	case bool:
		return &qdrant.Value{Kind: &qdrant.Value_BoolValue{BoolValue: val}}
	default:
		panic(fmt.Sprintf("nv: unsupported type %T", v))
	}
}

func nvList(items []string) *qdrant.Value {
	values := make([]*qdrant.Value, len(items))
	for i, s := range items {
		values[i] = nv(s)
	}
	return &qdrant.Value{
		Kind: &qdrant.Value_ListValue{
			ListValue: &qdrant.ListValue{Values: values},
		},
	}
}



// ── GET /v1/review ────────────────────────────────────────────────────────────

func TestReviewGet_Basic(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "key1", "value1", nil),
		makeNeedsReviewPoint("p2", "key2", "value2", nil),
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review", nil)
	w := httptest.NewRecorder()
	srv.handleReviewGet(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	entries := resp["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestReviewGet_EmptyResults(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = nil
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review", nil)
	w := httptest.NewRecorder()
	srv.handleReviewGet(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	total := resp["total"].(float64)
	if total != 0 {
		t.Errorf("expected 0 total, got %f", total)
	}
}

func TestReviewGet_ReasonFilter(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	// stale fact
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "stale-key", "stale-val", map[string]any{
			"expires_at": past,
			"ttl_days":   float64(90),
		}),
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review?reason=stale", nil)
	w := httptest.NewRecorder()
	srv.handleReviewGet(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("expected 1 stale entry, got %d", len(entries))
	}
	entry := entries[0].(map[string]any)
	reasons := entry["review_reasons"].([]any)
	if len(reasons) != 1 || reasons[0].(map[string]any)["type"] != "stale" {
		t.Errorf("expected stale reason, got %v", reasons)
	}
}

func TestReviewGet_TagFilter(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "tagged-key", "tagged-val", map[string]any{
			"fact_tags": []string{"important"},
		}),
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review?tag=important", nil)
	w := httptest.NewRecorder()
	srv.handleReviewGet(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]any)
	if len(entries) != 1 {
		t.Errorf("expected 1 tagged entry, got %d", len(entries))
	}
}

func TestReviewGet_SourceTypeFilter(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "src-key", "src-val", map[string]any{
			"source_type": "document",
		}),
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review?source_type=document", nil)
	w := httptest.NewRecorder()
	srv.handleReviewGet(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]any)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestReviewGet_MinConfidenceFilter(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "high", "high-val", map[string]any{
			"confidence": float64(0.9),
		}),
		makeNeedsReviewPoint("p2", "low", "low-val", map[string]any{
			"confidence": float64(0.1),
		}),
	}
	// Set a scrollFilteredFn that respects pagination offset and applies min_confidence filter
	store.scrollFilteredFn = func(ctx context.Context, collection string, filter *qdrant.Filter, limit uint32, offset string) ([]*qdrant.RetrievedPoint, error) {
		if offset != "" {
			return nil, nil // end of pagination
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		var result []*qdrant.RetrievedPoint
	outer:
		for _, p := range store.points {
			if status, _ := getPayloadString(p.GetPayload(), "status"); status != "needs_review" {
				continue
			}
			// Apply min_confidence filter if present
			if filter != nil && len(filter.Must) > 0 {
				for _, cond := range filter.Must {
					if fc := cond.GetField(); fc != nil && fc.Key == "confidence" {
						if rng := fc.GetRange(); rng != nil {
							if conf, _ := getPayloadFloat(p.GetPayload(), "confidence"); rng.Lt != nil && conf >= *rng.Lt {
								continue outer
							}
						}
					}
				}
			}
			result = append(result, p)
		}
		return result, nil
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review?min_confidence=0.5", nil)
	w := httptest.NewRecorder()
	srv.handleReviewGet(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]any)
	// Both are needs_review, min_confidence filter finds conf < 0.5, so only low should match
	// Wait, actually min_confidence filter uses Lt — it finds facts WHERE confidence < min_confidence
	// So with min_confidence=0.5, it finds facts with confidence < 0.5 → only "low" at 0.1
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (confidence < 0.5), got %d", len(entries))
	}
}

func TestReviewGet_LimitAndPagination(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	for i := 0; i < 5; i++ {
		store.points = append(store.points,
			makeNeedsReviewPoint(fmt.Sprintf("p%d", i), fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i), nil))
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review?limit=2", nil)
	w := httptest.NewRecorder()
	srv.handleReviewGet(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]any)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries (limited), got %d", len(entries))
	}
	if _, ok := resp["next_token"]; !ok {
		t.Error("expected next_token when results exceed limit")
	}
}

// ── POST /v1/review: confirm ──────────────────────────────────────────────────

func TestReviewPost_Confirm(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "confirm-key", "confirm-val", nil),
	}
	srv := newReviewServer(store)

	body := `{"action":"confirm"}`
	req := httptest.NewRequest("POST", "/v1/review?key=confirm-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the fact was updated
	store.mu.Lock()
	upserted := store.upserted
	store.mu.Unlock()

	if len(upserted) == 0 {
		t.Fatal("expected at least 1 upsert")
	}
	last := upserted[len(upserted)-1]
	if s, _ := getPayloadString(last.GetPayload(), "status"); s != "active" {
		t.Errorf("expected status 'active', got %q", s)
	}
}

func TestReviewPost_ConfirmWithConfidence(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "conf-key", "conf-val", nil),
	}
	srv := newReviewServer(store)

	body := `{"action":"confirm","confidence":0.95}`
	req := httptest.NewRequest("POST", "/v1/review?key=conf-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	store.mu.Lock()
	last := store.upserted[len(store.upserted)-1]
	store.mu.Unlock()

	c, _ := getPayloadFloat(last.GetPayload(), "confidence")
	if c != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", c)
	}
	// confirmation_count should be 1 (incremented from 0)
	cc, _ := getPayloadInt(last.GetPayload(), "confirmation_count")
	if cc != 1 {
		t.Errorf("expected confirmation_count 1, got %d", cc)
	}
}

// ── POST /v1/review: reject ───────────────────────────────────────────────────

func TestReviewPost_Reject(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "reject-key", "reject-val", nil),
	}
	srv := newReviewServer(store)

	body := `{"action":"reject"}`
	req := httptest.NewRequest("POST", "/v1/review?key=reject-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	store.mu.Lock()
	last := store.upserted[len(store.upserted)-1]
	store.mu.Unlock()

	if s, _ := getPayloadString(last.GetPayload(), "status"); s != "rejected" {
		t.Errorf("expected status 'rejected', got %q", s)
	}
}

// ── POST /v1/review: supersede ────────────────────────────────────────────────

func TestReviewPost_Supersede(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "old-key", "old-val", nil),
	}
	srv := newReviewServer(store)

	body := `{"action":"supersede","new_key":"new-key","new_value":"new-val"}`
	req := httptest.NewRequest("POST", "/v1/review?key=old-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	// supersede with new_value creates new fact then updates old one
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	store.mu.Lock()
	upserted := store.upserted
	store.mu.Unlock()

	if len(upserted) < 2 {
		t.Fatalf("expected at least 2 upserts (new fact + old update), got %d", len(upserted))
	}

	// Last upsert should be the old fact marked as superseded
	last := upserted[len(upserted)-1]
	if s, _ := getPayloadString(last.GetPayload(), "status"); s != "superseded" {
		t.Errorf("expected old fact status 'superseded', got %q", s)
	}

	// First upsert should be the new fact
	first := upserted[0]
	if k, _ := getPayloadString(first.GetPayload(), "fact_key"); k != "new-key" {
		t.Errorf("expected new fact key 'new-key', got %q", k)
	}
	if s, _ := getPayloadString(first.GetPayload(), "status"); s != "active" {
		t.Errorf("expected new fact status 'active', got %q", s)
	}
}

// ── POST /v1/review: reclassify ───────────────────────────────────────────────

func TestReviewPost_Reclassify(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "reclass-key", "reclass-val", nil),
	}
	srv := newReviewServer(store)

	body := `{"action":"reclassify","confidence":0.85,"ttl_days":30}`
	req := httptest.NewRequest("POST", "/v1/review?key=reclass-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	store.mu.Lock()
	last := store.upserted[len(store.upserted)-1]
	store.mu.Unlock()

	c, _ := getPayloadFloat(last.GetPayload(), "confidence")
	if c != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", c)
	}
	ttl, _ := getPayloadInt(last.GetPayload(), "ttl_days")
	if ttl != 30 {
		t.Errorf("expected ttl_days 30, got %d", ttl)
	}
	// Reclassify sets status to active
	if s, _ := getPayloadString(last.GetPayload(), "status"); s != "active" {
		t.Errorf("expected status 'active' after reclassify, got %q", s)
	}
}

// ── POST /v1/review: errors ───────────────────────────────────────────────────

func TestReviewPost_MissingKey(t *testing.T) {
	srv := newReviewServer(&reviewMockStore{collection: "test_facts"})
	body := `{"action":"confirm"}`
	req := httptest.NewRequest("POST", "/v1/review", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing key, got %d", w.Code)
	}
}

func TestReviewPost_MissingAction(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "act-key", "act-val", nil),
	}
	srv := newReviewServer(store)

	body := `{}`
	req := httptest.NewRequest("POST", "/v1/review?key=act-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing action, got %d", w.Code)
	}
}

func TestReviewPost_InvalidAction(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "bad-key", "bad-val", nil),
	}
	srv := newReviewServer(store)

	body := `{"action":"nonexistent"}`
	req := httptest.NewRequest("POST", "/v1/review?key=bad-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for invalid action, got %d", w.Code)
	}
}

func TestReviewPost_NotFound(t *testing.T) {
	srv := newReviewServer(&reviewMockStore{collection: "test_facts"})
	body := `{"action":"confirm"}`
	req := httptest.NewRequest("POST", "/v1/review?key=nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReviewPost(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404 for nonexistent fact, got %d", w.Code)
	}
}

// ── GET /v1/review/stats ──────────────────────────────────────────────────────

func TestReviewStats_Basic(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "k1", "v1", nil),
		makeNeedsReviewPoint("p2", "k2", "v2", nil),
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review/stats", nil)
	w := httptest.NewRecorder()
	srv.handleReviewStats(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var stats reviewStatsResponse
	json.NewDecoder(w.Body).Decode(&stats)

	if stats.TotalNeedsReview != 2 {
		t.Errorf("expected total 2, got %d", stats.TotalNeedsReview)
	}
}

func TestReviewStats_EmptyQueue(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review/stats", nil)
	w := httptest.NewRecorder()
	srv.handleReviewStats(w, req)

	var stats reviewStatsResponse
	json.NewDecoder(w.Body).Decode(&stats)

	if stats.TotalNeedsReview != 0 {
		t.Errorf("expected 0, got %d", stats.TotalNeedsReview)
	}
}

func TestReviewStats_ByReason(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "stale-k", "stale-v", map[string]any{
			"expires_at": past,
			"ttl_days":   float64(90),
		}),
		makeNeedsReviewPoint("p2", "conflict-k", "conflict-v", map[string]any{
			"contradicts": []string{"other-fact"},
		}),
	}
	srv := newReviewServer(store)

	req := httptest.NewRequest("GET", "/v1/review/stats", nil)
	w := httptest.NewRecorder()
	srv.handleReviewStats(w, req)

	var stats reviewStatsResponse
	json.NewDecoder(w.Body).Decode(&stats)

	if stats.ByReason["stale"] != 1 {
		t.Errorf("expected 1 stale, got %d", stats.ByReason["stale"])
	}
	if stats.ByReason["contradiction"] != 1 {
		t.Errorf("expected 1 contradiction, got %d", stats.ByReason["contradiction"])
	}
}

// ── PUT /v1/facts ─────────────────────────────────────────────────────────────

func TestFactsPut_UpdateFields(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "put-key", "original-val", nil),
	}
	srv := newReviewServer(store)

	body := `{"value":"updated-val","confidence":0.99}`
	req := httptest.NewRequest("PUT", "/v1/facts?key=put-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleFactsPut(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	store.mu.Lock()
	last := store.upserted[len(store.upserted)-1]
	store.mu.Unlock()

	v, _ := getPayloadString(last.GetPayload(), "fact_value")
	if v != "updated-val" {
		t.Errorf("expected fact_value 'updated-val', got %q", v)
	}
	c, _ := getPayloadFloat(last.GetPayload(), "confidence")
	if c != 0.99 {
		t.Errorf("expected confidence 0.99, got %f", c)
	}
}

func TestFactsPut_MissingKey(t *testing.T) {
	srv := newReviewServer(&reviewMockStore{collection: "test_facts"})
	body := `{"fact_value":"test"}`
	req := httptest.NewRequest("PUT", "/v1/facts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleFactsPut(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing key, got %d", w.Code)
	}
}

// ── PATCH /v1/facts ───────────────────────────────────────────────────────────

func TestFactsPatch_BulkUpdate(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	store.points = []*qdrant.RetrievedPoint{
		makeNeedsReviewPoint("p1", "bulk-1", "original-1", nil),
		makeNeedsReviewPoint("p2", "bulk-2", "original-2", nil),
	}
	srv := newReviewServer(store)

	body := `{"keys":["bulk-1","bulk-2"],"updates":{"value":"updated","confidence":0.8}}`
	req := httptest.NewRequest("PATCH", "/v1/facts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleFactsPatch(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	results := resp["results"].([]any)
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

// ── handleReview dispatch ─────────────────────────────────────────────────────

func TestHandleReview_InvalidMethod(t *testing.T) {
	srv := newReviewServer(&reviewMockStore{collection: "test_facts"})
	req := httptest.NewRequest("DELETE", "/v1/review", nil)
	w := httptest.NewRecorder()
	srv.handleReview(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── pointToReviewEntry ────────────────────────────────────────────────────────

func TestPointToReviewEntry_NilPoint(t *testing.T) {
	if r := pointToReviewEntry(nil, ""); r != nil {
		t.Error("expected nil for nil point")
	}
}

func TestPointToReviewEntry_NotNeedsReview(t *testing.T) {
	p := &qdrant.RetrievedPoint{Payload: map[string]*qdrant.Value{
		"fact_key":   nv("k"),
		"fact_value": nv("v"),
		"status":     nv("active"),
	}}
	if r := pointToReviewEntry(p, ""); r != nil {
		t.Error("expected nil for non-needs_review status")
	}
}

func TestPointToReviewEntry_MissingKeyValue(t *testing.T) {
	p := &qdrant.RetrievedPoint{Payload: map[string]*qdrant.Value{
		"status": nv("needs_review"),
	}}
	if r := pointToReviewEntry(p, ""); r != nil {
		t.Error("expected nil for missing key/value")
	}
}

// ── ───────────────────────────────────────────────────────────────────────────

// ── Auth mock ──────────────────────────────────────────────────────────────────

type mockReadOnlyAuth struct{}

func (a *mockReadOnlyAuth) Authenticate(r *http.Request) (*auth.Claims, error) {
	return &auth.Claims{Access: []string{"read"}}, nil
}

// ── Auth tests ─────────────────────────────────────────────────────────────────

func TestReviewPost_AuthForbidden(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	srv := newReviewServer(store)

	body := `{"key": "test-key", "action": "confirm"}`
	req := httptest.NewRequest("POST", "/v1/review", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	handler := auth.Middleware(&mockReadOnlyAuth{})(http.HandlerFunc(srv.handleReview))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 403 {
		t.Fatalf("expected 403 for read-only claims, got %d: %s", w.Code, w.Body.String())
	}
}

// ── Body limit tests ───────────────────────────────────────────────────────────

func TestReviewPost_BodyTooLarge(t *testing.T) {
	store := &reviewMockStore{collection: "test_facts"}
	srv := newReviewServer(store)

	// Build a body that exceeds the 512KB limit
	largeValue := strings.Repeat("x", 600*1024)
	body := fmt.Sprintf(`{"key": "test-key", "action": "confirm", "extra": "%s"}`, largeValue)

	req := httptest.NewRequest("POST", "/v1/review", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleReview(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400 for oversized body, got %d: %s", w.Code, w.Body.String())
	}
}
