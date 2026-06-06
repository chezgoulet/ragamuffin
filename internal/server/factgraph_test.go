package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
	pb "github.com/qdrant/go-client/qdrant"
)

// factGraphTestStore is a minimal FactStore stub for graph traversal tests.
type factGraphTestStore struct {
	points map[string]*pb.RetrievedPoint // key → point
}

func (s *factGraphTestStore) GetPoints(_ context.Context, _ string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error) {
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

func (s *factGraphTestStore) ScrollFiltered(_ context.Context, _ string, filter *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
	var result []*pb.RetrievedPoint
	for _, pt := range s.points {
		if factGraphMatchesFilter(pt, filter) {
			result = append(result, pt)
		}
	}
	return result, nil
}

func (s *factGraphTestStore) SetPayload(_ context.Context, _ string, _ []*pb.PointId, _ map[string]*pb.Value) error {
	return nil
}

func (s *factGraphTestStore) Upsert(_ context.Context, _ []*pb.PointStruct) error {
	return nil
}

func (s *factGraphTestStore) Close() error { return nil }

func (s *factGraphTestStore) Collection() string { return "test_facts" }

func (s *factGraphTestStore) Count(_ context.Context) (uint64, error) { return uint64(len(s.points)), nil }
func (s *factGraphTestStore) Scroll(_ context.Context, _ uint32, _ *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) { return nil, nil, nil }
func (s *factGraphTestStore) Search(_ context.Context, _ []float32, _ uint64, _ float32, _ string, _ *pb.Filter) ([]*pb.ScoredPoint, error) { return nil, nil }
func (s *factGraphTestStore) DeleteBySource(_ context.Context, _ string) error { return nil }
func (s *factGraphTestStore) DeleteFiltered(_ context.Context, _ string, _ *pb.Filter) error { return nil }
func (s *factGraphTestStore) CountFiles(_ context.Context) (int, error) { return 0, nil }
func (s *factGraphTestStore) CreatePayloadIndex(_ context.Context, _, _, _ string) error { return nil }
func (s *factGraphTestStore) Health(_ context.Context) error { return nil }

func factGraphMatchesFilter(pt *pb.RetrievedPoint, filter *pb.Filter) bool {
	if filter == nil || pt == nil || pt.Payload == nil {
		return filter == nil
	}
	for _, must := range filter.GetMust() {
		if !factGraphMatchesCondition(pt.Payload, must) {
			return false
		}
	}
	for _, mustNot := range filter.GetMustNot() {
		if factGraphMatchesCondition(pt.Payload, mustNot) {
			return false
		}
	}
	if should := filter.GetShould(); len(should) > 0 {
		found := false
		for _, cond := range should {
			if factGraphMatchesCondition(pt.Payload, cond) {
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

func factGraphMatchesCondition(payload map[string]*pb.Value, cond *pb.Condition) bool {
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
			// Empty keyword match — checking for existence of empty field
			if kw := m.GetKeyword(); kw == "" {
				return val.GetStringValue() == ""
			}
		}
		if r := f.GetRange(); r != nil {
			v := val.GetDoubleValue()
			if r.Gt != nil && !(v > *r.Gt) {
				return false
			}
			if r.Lt != nil && !(v < *r.Lt) {
				return false
			}
		}
		return true
	}
	if f := cond.GetFilter(); f != nil {
		return factGraphMatchesFilter(&pb.RetrievedPoint{Payload: payload}, f)
	}
	if isEmpty := cond.GetIsEmpty(); isEmpty != nil {
		_, ok := payload[isEmpty.Key]
		return !ok
	}
	return true
}

func testFactPayload(key, value, status string) map[string]*pb.Value {
	return map[string]*pb.Value{
		"fact_key":           {Kind: &pb.Value_StringValue{StringValue: key}},
		"fact_value":         {Kind: &pb.Value_StringValue{StringValue: value}},
		"status":             {Kind: &pb.Value_StringValue{StringValue: status}},
		"supersedes":         {Kind: &pb.Value_StringValue{StringValue: ""}},
		"refines":            {Kind: &pb.Value_StringValue{StringValue: ""}},
		"contradicts":        {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{}}},
		"supports":           {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{}}},
		"superseded_by":      {Kind: &pb.Value_DoubleValue{DoubleValue: 0}},
		"conflict_resolved":  {Kind: &pb.Value_BoolValue{BoolValue: true}},
		"confirmation_count": {Kind: &pb.Value_DoubleValue{DoubleValue: 1}},
	}
}

func newFactGraphServer(store *factGraphTestStore) *Server {
	return &Server{
		cfg:   &config.Config{FactsCollection: "test_facts"},
		facts: store,
	}
}

// graphRequest creates a GET request for the graph endpoint with the path value set
// so handleFactGraph's PathValue("key") works when called directly (not through mux).
func graphRequest(url string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	// Extract key from /v1/facts/{key}/graph pattern
	if key := extractGraphKey(url); key != "" {
		req.SetPathValue("key", key)
	}
	return req
}

func extractGraphKey(url string) string {
	// Pattern: /v1/facts/{key}/graph or /v1/facts/{key}/graph?params
	// Skip leading /, traverse segments
	parts := strings.Split(strings.TrimPrefix(url, "/"), "/")
	// segments: [v1, facts, {key}, graph]
	if len(parts) >= 4 && parts[0] == "v1" && parts[1] == "facts" && (parts[3] == "graph" || strings.HasPrefix(parts[3], "graph?") || strings.Contains(parts[3], "?")) {
		key := parts[2]
		if key == "" || key == "/" {
			return ""
		}
		return key
	}
	return ""
}

func TestFactGraph_BasicForwardEdges(t *testing.T) {
	store := &factGraphTestStore{
		points: map[string]*pb.RetrievedPoint{
			"fact-a": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-a"}},
				Payload: testFactPayload("fact-a", "Original decision", "active"),
			},
			"fact-b": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-b"}},
				Payload: testFactPayload("fact-b", "Updated decision", "active"),
			},
		},
	}
	store.points["fact-a"].Payload["supersedes"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: "fact-b"}}

	s := newFactGraphServer(store)

	w := httptest.NewRecorder()
	s.handleFactGraph(w, graphRequest("/v1/facts/fact-a/graph?depth=2"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !containsJSON(body, "fact-a") {
		t.Errorf("response missing root fact-a: %s", body)
	}
	if !containsJSON(body, "fact-b") {
		t.Errorf("response missing target fact-b: %s", body)
	}
	if !containsJSON(body, "supersedes") {
		t.Errorf("response missing supersedes edge: %s", body)
	}
}

func TestFactGraph_ReverseEdges(t *testing.T) {
	store := &factGraphTestStore{
		points: map[string]*pb.RetrievedPoint{
			"fact-a": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-a"}},
				Payload: testFactPayload("fact-a", "Original decision", "active"),
			},
			"fact-b": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-b"}},
				Payload: testFactPayload("fact-b", "Updated decision", "active"),
			},
		},
	}
	store.points["fact-a"].Payload["supersedes"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: "fact-b"}}

	s := newFactGraphServer(store)

	// Query from fact-b to discover reverse edges
	w := httptest.NewRecorder()
	s.handleFactGraph(w, graphRequest("/v1/facts/fact-b/graph?depth=2"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !containsJSON(body, "fact-a") {
		t.Errorf("response missing reverse edge source fact-a: %s", body)
	}
	if !containsJSON(body, "supersedes") {
		t.Errorf("response missing supersedes edge: %s", body)
	}
}

func TestFactGraph_NotFound(t *testing.T) {
	s := newFactGraphServer(&factGraphTestStore{points: map[string]*pb.RetrievedPoint{}})

	w := httptest.NewRecorder()
	s.handleFactGraph(w, graphRequest("/v1/facts/nonexistent/graph"))

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestFactGraph_MissingKey(t *testing.T) {
	s := newFactGraphServer(&factGraphTestStore{points: map[string]*pb.RetrievedPoint{}})

	w := httptest.NewRecorder()
	s.handleFactGraph(w, graphRequest("/v1/facts//graph"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestFactGraph_SupportsAndRefines(t *testing.T) {
	store := &factGraphTestStore{
		points: map[string]*pb.RetrievedPoint{
			"fact-a": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-a"}},
				Payload: testFactPayload("fact-a", "Root fact", "active"),
			},
			"fact-b": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-b"}},
				Payload: testFactPayload("fact-b", "Supporting evidence", "active"),
			},
			"fact-c": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-c"}},
				Payload: testFactPayload("fact-c", "Refinement", "active"),
			},
		},
	}
	store.points["fact-a"].Payload["supports"] = &pb.Value{
		Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{
			Values: []*pb.Value{{Kind: &pb.Value_StringValue{StringValue: "fact-b"}}},
		}},
	}
	store.points["fact-c"].Payload["refines"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: "fact-a"}}

	s := newFactGraphServer(store)

	w := httptest.NewRecorder()
	s.handleFactGraph(w, graphRequest("/v1/facts/fact-a/graph?depth=3"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !containsJSON(body, "fact-b") {
		t.Errorf("response missing supports target fact-b: %s", body)
	}
	if !containsJSON(body, "fact-c") {
		t.Errorf("response missing refines source fact-c: %s", body)
	}
	if !containsJSON(body, "supports") {
		t.Errorf("response missing supports edge: %s", body)
	}
	if !containsJSON(body, "refines") {
		t.Errorf("response missing refines edge: %s", body)
	}
}

func TestFactGraph_CycleSafe(t *testing.T) {
	store := &factGraphTestStore{
		points: map[string]*pb.RetrievedPoint{
			"fact-a": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-a"}},
				Payload: testFactPayload("fact-a", "Node A", "active"),
			},
			"fact-b": {
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "uuid-b"}},
				Payload: testFactPayload("fact-b", "Node B", "active"),
			},
		},
	}
	store.points["fact-a"].Payload["supersedes"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: "fact-b"}}
	store.points["fact-b"].Payload["contradicts"] = &pb.Value{
		Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{
			Values: []*pb.Value{{Kind: &pb.Value_StringValue{StringValue: "fact-a"}}},
		}},
	}

	s := newFactGraphServer(store)

	w := httptest.NewRecorder()
	s.handleFactGraph(w, graphRequest("/v1/facts/fact-a/graph?depth=5"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !containsJSON(body, "fact-a") || !containsJSON(body, "fact-b") {
		t.Errorf("response should contain both nodes: %s", body)
	}
	// Complete without panic/timeout = cycle safety verified
}

// helpers

func containsJSON(body, substr string) bool {
	return len(body) > 0 && substr != "" && searchSubstring(body, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
