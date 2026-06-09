package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// ── Mock fact store for facts handler tests ──────────────────────────────

// factsMockStore stores points in memory and supports all qdrant.FactStore methods.
// ScrollFiltered does basic filter matching (status, fact_key, fact_tags).
type factsMockStore struct {
	points  map[string]*pb.RetrievedPoint // key → point (key = fact_key, not UUID)
	coll    string                        // collection name
	upsertCalls int
	setPayloadCalls int
	deleteCalls int
}

func newFactsMockStore() *factsMockStore {
	return &factsMockStore{
		points: make(map[string]*pb.RetrievedPoint),
		coll:   "test_facts",
	}
}

func (s *factsMockStore) addPoint(key, value, status string, tags ...string) {
	payload := map[string]*pb.Value{
		"fact_key":           {Kind: &pb.Value_StringValue{StringValue: key}},
		"fact_value":         {Kind: &pb.Value_StringValue{StringValue: value}},
		"status":             {Kind: &pb.Value_StringValue{StringValue: status}},
		"source":             {Kind: &pb.Value_StringValue{StringValue: ""}},
		"source_type":        {Kind: &pb.Value_StringValue{StringValue: "manual"}},
		"confidence":         {Kind: &pb.Value_DoubleValue{DoubleValue: 1.0}},
		"version":            {Kind: &pb.Value_DoubleValue{DoubleValue: 0}},
		"supersedes":         {Kind: &pb.Value_StringValue{StringValue: ""}},
		"superseded_by":      {Kind: &pb.Value_DoubleValue{DoubleValue: 0}},
		"refines":            {Kind: &pb.Value_StringValue{StringValue: ""}},
		"conflict_resolved":  {Kind: &pb.Value_BoolValue{BoolValue: true}},
		"confirmation_count": {Kind: &pb.Value_DoubleValue{DoubleValue: 1}},
		"access_count":       {Kind: &pb.Value_DoubleValue{DoubleValue: 0}},
		"contradicts":        {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: []*pb.Value{}}}},
		"supports":           {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: []*pb.Value{}}}},
	}
	if len(tags) > 0 {
		tagVals := make([]*pb.Value, len(tags))
		for i, t := range tags {
			tagVals[i] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: t}}
		}
		payload["fact_tags"] = &pb.Value{
			Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: tagVals}},
		}
	}
	s.points[key] = &pb.RetrievedPoint{
		Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: factKeyHash(key)}},
		Payload: payload,
	}
}

// qdrant.FactStore implementation

func (s *factsMockStore) GetVectorSize(_ context.Context, _ string) (uint64, error) { return 4, nil }

func (s *factsMockStore) GetPoints(_ context.Context, _ string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error) {
	var result []*pb.RetrievedPoint
	for _, id := range ids {
		for _, pt := range s.points {
			if pt.GetId().GetUuid() == id.GetUuid() {
				result = append(result, pt)
			}
		}
	}
	return result, nil
}

func (s *factsMockStore) ScrollFiltered(_ context.Context, _ string, filter *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
	var result []*pb.RetrievedPoint
	for _, pt := range s.points {
		if matchFactsFilter(pt, filter) {
			result = append(result, pt)
		}
	}
	return result, nil
}

func (s *factsMockStore) SetPayload(_ context.Context, _ string, _ []*pb.PointId, _ map[string]*pb.Value) error {
	s.setPayloadCalls++
	return nil
}

func (s *factsMockStore) Upsert(_ context.Context, points []*pb.PointStruct) error {
	s.upsertCalls++
	for _, p := range points {
		if p.Payload != nil {
			if keyVal, ok := p.Payload["fact_key"]; ok && keyVal.GetStringValue() != "" {
				key := keyVal.GetStringValue()
				s.points[key] = &pb.RetrievedPoint{
					Id:      p.Id,
					Payload: p.Payload,
				}
			}
		}
	}
	return nil
}

func (s *factsMockStore) DeleteFiltered(_ context.Context, _ string, filter *pb.Filter) error {
	s.deleteCalls++
	keysToRemove := make([]string, 0)
	for key, pt := range s.points {
		if matchFactsFilter(pt, filter) {
			keysToRemove = append(keysToRemove, key)
		}
	}
	for _, k := range keysToRemove {
		delete(s.points, k)
	}
	return nil
}

func (s *factsMockStore) Close() error                       { return nil }
func (s *factsMockStore) Collection() string                 { return s.coll }
func (s *factsMockStore) Count(_ context.Context) (uint64, error) { return uint64(len(s.points)), nil }
func (s *factsMockStore) Scroll(_ context.Context, _ uint32, _ *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) {
	return nil, nil, nil
}
func (s *factsMockStore) Search(_ context.Context, _ []float32, _ uint64, _ float32, _ string, _ *pb.Filter) ([]*pb.ScoredPoint, error) {
	return nil, nil
}
func (s *factsMockStore) DeleteBySource(_ context.Context, _ string) error { return nil }
func (s *factsMockStore) CountFiles(_ context.Context) (int, error)       { return 0, nil }
func (s *factsMockStore) CreatePayloadIndex(_ context.Context, _, _, _ string) error { return nil }
func (s *factsMockStore) UpdateVectors(_ context.Context, _ string, _ []*pb.PointVectors) error { return nil }
func (s *factsMockStore) Health(_ context.Context) error { return nil }

// matchFactsFilter checks a RetrievedPoint against a Qdrant filter.
func matchFactsFilter(pt *pb.RetrievedPoint, filter *pb.Filter) bool {
	if filter == nil || pt == nil || pt.Payload == nil {
		return filter == nil
	}
	for _, must := range filter.GetMust() {
		if !matchFactsCondition(pt.Payload, must) {
			return false
		}
	}
	for _, mustNot := range filter.GetMustNot() {
		if matchFactsCondition(pt.Payload, mustNot) {
			return false
		}
	}
	if should := filter.GetShould(); len(should) > 0 {
		found := false
		for _, cond := range should {
			if matchFactsCondition(pt.Payload, cond) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchFactsCondition(payload map[string]*pb.Value, cond *pb.Condition) bool {
	if cond == nil {
		return true
	}
	if f := cond.GetField(); f != nil {
		val, ok := payload[f.Key]
		if !ok || val == nil {
			return false
		}
		if m := f.GetMatch(); m != nil {
			if kw := m.GetKeyword(); kw != "" {
				return val.GetStringValue() == kw
			}
		}
		return true
	}
	if f := cond.GetFilter(); f != nil {
		return matchFactsFilter(&pb.RetrievedPoint{Payload: payload}, f)
	}
	return true
}

// ── Test helper ──────────────────────────────────────────────────────────

func newFactsServer(store *factsMockStore) *Server {
	ctx := context.Background()
	return &Server{
		cfg:         &config.Config{FactsCollection: "test_facts", FactsVectorSize: 4, AutoProvisionVaults: false},
		facts:       store,
		shutdownCtx: ctx,
		logger:      slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func factsPostRequest(body interface{}) *http.Request {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/facts", &buf)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func factsGetRequest(path string) *http.Request {
	return httptest.NewRequest(http.MethodGet, path, nil)
}

func factsPutRequest(path string, body interface{}) *http.Request {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPut, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func factsDeleteRequest(path string) *http.Request {
	return httptest.NewRequest(http.MethodDelete, path, nil)
}

func factsPatchRequest(body interface{}) *http.Request {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPatch, "/v1/facts", &buf)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ── POST /v1/facts ──────────────────────────────────────────────────────

func TestFactsPost_CreateNewFact(t *testing.T) {
	store := newFactsMockStore()
	s := newFactsServer(store)

	body := map[string]interface{}{
		"key":   "test/version",
		"value": "test value",
	}
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp factResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Key != "test/version" {
		t.Errorf("expected key 'test/version', got %q", resp.Key)
	}
	if resp.Value != "test value" {
		t.Errorf("expected value 'test value', got %q", resp.Value)
	}
	if resp.Status != "active" {
		t.Errorf("expected status 'active', got %q", resp.Status)
	}
}

func TestFactsPost_MissingKey(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(map[string]interface{}{"value": "test"}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "key and value are required") {
		t.Errorf("expected key/value validation error, got: %s", w.Body.String())
	}
}

func TestFactsPost_MissingValue(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(map[string]interface{}{"key": "test"}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFactsPost_EmptyKey(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(map[string]interface{}{"key": "", "value": "test"}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFactsPost_WithTags(t *testing.T) {
	store := newFactsMockStore()
	s := newFactsServer(store)

	body := map[string]interface{}{
		"key":    "tagged-fact",
		"value":  "tagged value",
		"tags":   []string{"important", "v1.0"},
		"source": "test",
	}
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeFactResponse(t, w.Body.String())
	if len(resp.Tags) != 2 {
		t.Errorf("expected 2 tags, got %v", resp.Tags)
	}
}

func TestFactsPost_KeyTooLong(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	longKey := strings.Repeat("a", 1025)
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(map[string]interface{}{
		"key": longKey, "value": "test",
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for key > 1024 bytes, got %d", w.Code)
	}
}

func TestFactsPost_ValueTooLarge(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	largeVal := strings.Repeat("x", 64*1024+1)
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(map[string]interface{}{
		"key": "test", "value": largeVal,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for value > 64KB, got %d", w.Code)
	}
}

func TestFactsPost_InvalidJSON(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	req := httptest.NewRequest(http.MethodPost, "/v1/facts",
		bytes.NewReader([]byte(`{invalid json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleFactsPost(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestFactsPost_UpdateExistingFact(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("existing-fact", "original value", "active")
	s := newFactsServer(store)

	body := map[string]interface{}{
		"key":   "existing-fact",
		"value": "updated value",
	}
	w := httptest.NewRecorder()
	s.handleFactsPost(w, factsPostRequest(body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeFactResponse(t, w.Body.String())
	if resp.Value != "updated value" {
		t.Errorf("expected 'updated value', got %q", resp.Value)
	}
	// Verify the store was updated
	pt, ok := store.points["existing-fact"]
	if !ok {
		t.Fatal("fact should exist after upsert")
	}
	val, _ := pt.Payload["fact_value"]
	if val.GetStringValue() != "updated value" {
		t.Errorf("store value should be 'updated value', got %q", val.GetStringValue())
	}
}

// ── GET /v1/facts ───────────────────────────────────────────────────────

func TestFactsGet_ByKey(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("test-key", "test value", "active")
	s := newFactsServer(store)

	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts?key=test-key"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeFactResponse(t, w.Body.String())
	if resp.Key != "test-key" {
		t.Errorf("expected key 'test-key', got %q", resp.Key)
	}
	if resp.Value != "test value" {
		t.Errorf("expected value 'test value', got %q", resp.Value)
	}
}

func TestFactsGet_NotFound(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts?key=nonexistent"))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFactsGet_ListAll(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("fact-1", "value 1", "active")
	store.addPoint("fact-2", "value 2", "active")
	s := newFactsServer(store)

	// Step through handleFactsGet manually and check each step
	req := factsGetRequest("/v1/facts")

	// Step 1: read query params like handler does
	key := req.URL.Query().Get("key")
	prefix := req.URL.Query().Get("prefix")
	t.Logf("debug: key=%q prefix=%q", key, prefix)

	// Step 2: get collection
	coll := s.factsCollectionFor(req.Context())
	t.Logf("debug: collection=%q", coll)

	// Step 3: build filter
	var conditions []*pb.Condition
	var filter *pb.Filter
	if len(conditions) > 0 {
		filter = &pb.Filter{Must: conditions}
	}
	t.Logf("debug: filter=%v", filter)

	// Step 4: scroll
	limit := 100
	offset := req.URL.Query().Get("before")
	pts, err := s.facts.ScrollFiltered(req.Context(), coll, filter, uint32(limit+1), offset)
	if err != nil {
		t.Fatalf("scroll: %v", err)
	}
	t.Logf("debug: ScrollFiltered returned %d points", len(pts))

	// Step 5: iterate and build response
	var nextToken string
	resp := make([]factResponse, 0, limit)
	for _, p := range pts {
		k, _ := qutil.GetPayloadString(p.Payload, "fact_key")
		t.Logf("debug: loop key=%q prefix=%q", k, prefix)
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			t.Logf("debug:   skipping due to prefix")
			continue
		}
		if strings.HasPrefix(k, "_ragamuffin/") {
			t.Logf("debug:   skipping internal key")
			continue
		}
		if len(resp) >= limit {
			t.Logf("debug:   hit limit")
			if id := p.Id.GetUuid(); id != "" {
				nextToken = id
			}
			break
		}
		if fr := pointToFact(p); fr != nil {
			t.Logf("debug:   appending entry key=%q", k)
			resp = append(resp, *fr)
		} else {
			t.Logf("debug:   pointToFact returned nil!")
		}
	}
	t.Logf("debug: response entries length=%d", len(resp))

	w := httptest.NewRecorder()
	s.handleFactsGet(w, req)

	t.Logf("handler response: code=%d body=%s", w.Code, w.Body.String())

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var actualResp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&actualResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Logf("handler response map: %v", actualResp)
	entries, ok := actualResp["entries"].([]interface{})
	if !ok {
		t.Fatalf("expected entries array, got: %v (type: %T)", actualResp["entries"], actualResp["entries"])
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestFactsGet_FilterByStatus(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("active-fact", "active", "active")
	store.addPoint("archived-fact", "archived", "archived")
	s := newFactsServer(store)

	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts?status=archived"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]interface{})
	if len(entries) != 1 {
		t.Errorf("expected 1 archived fact, got %d", len(entries))
	}
	entry := entries[0].(map[string]interface{})
	if entry["key"] != "archived-fact" {
		t.Errorf("expected 'archived-fact', got %v", entry["key"])
	}
}

func TestFactsGet_FilterByTag(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("tagged-fact", "tagged", "active", "important")
	store.addPoint("plain-fact", "plain", "active")
	s := newFactsServer(store)

	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts?tag=important"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]interface{})
	if len(entries) != 1 {
		t.Errorf("expected 1 tagged fact, got %d", len(entries))
	}
}

func TestFactsGet_FilterByPrefix(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("api/v1/endpoint", "v1", "active")
	store.addPoint("api/v2/endpoint", "v2", "active")
	store.addPoint("other/fact", "other", "active")
	s := newFactsServer(store)

	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts?prefix=api/v1"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]interface{})
	if len(entries) != 1 {
		t.Errorf("expected 1 fact with prefix 'api/v1', got %d", len(entries))
	}
}

func TestFactsGet_Limit(t *testing.T) {
	store := newFactsMockStore()
	for i := 0; i < 10; i++ {
		key := "fact-" + string(rune('a'+i))
		store.addPoint(key, "value", "active")
	}
	s := newFactsServer(store)

	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts?limit=3"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]interface{})
	if len(entries) > 3 {
		t.Errorf("expected <= 3 entries with limit=3, got %d", len(entries))
	}
}

func TestFactsGet_InvalidTimeFilter(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts?time_filter=invalid_mode"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid time_filter, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFactsGet_SkipsInternalKeys(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("_ragamuffin/_migration/v0.6.1", "done", "active")
	store.addPoint("user/key", "user value", "active")
	s := newFactsServer(store)

	w := httptest.NewRecorder()
	s.handleFactsGet(w, factsGetRequest("/v1/facts"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	entries := resp["entries"].([]interface{})
	if len(entries) != 1 {
		t.Errorf("expected 1 non-internal entry, got %d", len(entries))
	}
}

// ── PUT /v1/facts ───────────────────────────────────────────────────────

func TestFactsPut_UpdateField(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("test-key", "original", "active")
	s := newFactsServer(store)

	body := map[string]interface{}{
		"value": "updated",
	}
	w := httptest.NewRecorder()
	s.handleFactsPut(w, factsPutRequest("/v1/facts?key=test-key", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeFactResponse(t, w.Body.String())
	if resp.Value != "updated" {
		t.Errorf("expected 'updated', got %q", resp.Value)
	}
}

func TestFactsPut_MissingKeyParam(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsPut(w, factsPutRequest("/v1/facts", map[string]interface{}{"value": "test"}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFactsPut_NotFound(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsPut(w, factsPutRequest("/v1/facts?key=nonexistent", map[string]interface{}{"value": "test"}))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFactsPut_UpdateStatus(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("status-test", "value", "active")
	s := newFactsServer(store)

	body := map[string]interface{}{
		"status": "archived",
	}
	w := httptest.NewRecorder()
	s.handleFactsPut(w, factsPutRequest("/v1/facts?key=status-test", body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := decodeFactResponse(t, w.Body.String())
	if resp.Status != "archived" {
		t.Errorf("expected status 'archived', got %q", resp.Status)
	}
}

func TestFactsPut_InvalidJSON(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("test-key", "value", "active")
	s := newFactsServer(store)

	req := httptest.NewRequest(http.MethodPut, "/v1/facts?key=test-key",
		bytes.NewReader([]byte(`{invalid`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleFactsPut(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── DELETE /v1/facts ────────────────────────────────────────────────────

func TestFactsDelete_RemovesFact(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("delete-me", "to delete", "active")
	s := newFactsServer(store)

	w := httptest.NewRecorder()
	s.handleFactsDelete(w, factsDeleteRequest("/v1/facts?key=delete-me"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["deleted"] != true {
		t.Errorf("expected deleted=true, got %v", resp["deleted"])
	}

	if _, exists := store.points["delete-me"]; exists {
		t.Error("fact should have been removed from store")
	}
}

func TestFactsDelete_MissingKey(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsDelete(w, factsDeleteRequest("/v1/facts"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ── PATCH /v1/facts ─────────────────────────────────────────────────────

func TestFactsPatch_BulkUpdateAll(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("fact-a", "a", "active")
	store.addPoint("fact-b", "b", "active")
	s := newFactsServer(store)

	body := factBulkUpdateRequest{
		Keys: []string{"fact-a", "fact-b"},
		Updates: factUpdateRequest{
			Status: strPtr("archived"),
		},
	}
	w := httptest.NewRecorder()
	s.handleFactsPatch(w, factsPatchRequest(body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	total := resp["total"].(float64)
	succeeded := resp["succeeded"].(float64)
	if total != 2 {
		t.Errorf("expected total 2, got %v", total)
	}
	if succeeded != 2 {
		t.Errorf("expected 2 succeeded, got %v", succeeded)
	}
}

func TestFactsPatch_EmptyKeys(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsPatch(w, factsPatchRequest(factBulkUpdateRequest{
		Keys:    []string{},
		Updates: factUpdateRequest{Status: strPtr("archived")},
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFactsPatch_TooManyKeys(t *testing.T) {
	keys := make([]string, 1001)
	for i := range keys {
		keys[i] = "key"
	}
	s := newFactsServer(newFactsMockStore())
	w := httptest.NewRecorder()
	s.handleFactsPatch(w, factsPatchRequest(factBulkUpdateRequest{
		Keys:    keys,
		Updates: factUpdateRequest{},
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for >1000 keys, got %d", w.Code)
	}
}

func TestFactsPatch_InvalidJSON(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	req := httptest.NewRequest(http.MethodPatch, "/v1/facts",
		bytes.NewReader([]byte(`{{invalid`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleFactsPatch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestFactsPatch_PartialSuccess(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("exists", "value", "active")
	s := newFactsServer(store)

	body := factBulkUpdateRequest{
		Keys: []string{"exists", "nonexistent"},
		Updates: factUpdateRequest{
			Status: strPtr("archived"),
		},
	}
	w := httptest.NewRecorder()
	s.handleFactsPatch(w, factsPatchRequest(body))

	// Should still be 200 since some succeeded
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	succeeded := resp["succeeded"].(float64)
	failed := resp["failed"].(float64)
	if succeeded != 1 {
		t.Errorf("expected 1 succeeded, got %v", succeeded)
	}
	if failed != 1 {
		t.Errorf("expected 1 failed, got %v", failed)
	}
}

// ── Dispatch: handleFacts ───────────────────────────────────────────────

func TestFactsDispatch_MethodRouting(t *testing.T) {
	s := newFactsServer(newFactsMockStore())

	// GET should route to handleFactsGet
	w := httptest.NewRecorder()
	s.handleFacts(w, factsGetRequest("/v1/facts"))
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for GET, got %d", w.Code)
	}

	// POST should route to handleFactsPost
	w = httptest.NewRecorder()
	s.handleFacts(w, factsPostRequest(map[string]interface{}{"key": "k", "value": "v"}))
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for POST, got %d: %s", w.Code, w.Body.String())
	}

	// PUT should route to handleFactsPut
	w = httptest.NewRecorder()
	s.handleFacts(w, factsPutRequest("/v1/facts", map[string]interface{}{}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for PUT missing key, got %d", w.Code)
	}

	// DELETE should route to handleFactsDelete
	w = httptest.NewRecorder()
	s.handleFacts(w, factsDeleteRequest("/v1/facts"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for DELETE missing key, got %d", w.Code)
	}

	// PATCH should route to handleFactsPatch
	w = httptest.NewRecorder()
	s.handleFactsPatch(w, factsPatchRequest(factBulkUpdateRequest{Keys: []string{}}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for PATCH empty keys, got %d", w.Code)
	}
}

func TestFactsDispatch_UnsupportedMethod(t *testing.T) {
	s := newFactsServer(newFactsMockStore())
	req := httptest.NewRequest(http.MethodHead, "/v1/facts", nil)
	w := httptest.NewRecorder()
	s.handleFacts(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────

func decodeFactResponse(t *testing.T, body string) *factResponse {
	t.Helper()
	var fr factResponse
	if err := json.Unmarshal([]byte(body), &fr); err != nil {
		t.Fatalf("decode fact response: %v (body: %s)", err, body)
	}
	return &fr
}

func strPtr(s string) *string { return &s }
