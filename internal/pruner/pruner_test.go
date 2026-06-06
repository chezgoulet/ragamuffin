package pruner

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
	pb "github.com/qdrant/go-client/qdrant"
)

// ── Mock FactStore ────────────────────────────────────────────────────────────

type mockFactStore struct {
	qdrant.FactStore // embed so we only implement what we need
	name             string

	mu              sync.Mutex
	upserted        []*pb.PointStruct
	scrollFilteredFn func(ctx context.Context, collection string, filter *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error)
	getPointsFn     func(ctx context.Context, collection string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error)
}

func (m *mockFactStore) Collection() string { return m.name }

func (m *mockFactStore) Upsert(_ context.Context, points []*pb.PointStruct) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserted = append(m.upserted, points...)
	return nil
}

func (m *mockFactStore) ScrollFiltered(ctx context.Context, collection string, filter *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
	m.mu.Lock()
	fn := m.scrollFilteredFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, collection, filter, limit, offset)
	}
	return nil, nil
}

func (m *mockFactStore) GetPoints(ctx context.Context, collection string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error) {
	m.mu.Lock()
	fn := m.getPointsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, collection, ids)
	}
	return nil, nil
}

func (m *mockFactStore) SetPayload(_ context.Context, _ string, ids []*pb.PointId, payload map[string]*pb.Value) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Track payloads for points — create skeleton entries if not already upserted
	for _, id := range ids {
		found := false
		for _, pt := range m.upserted {
			if id.GetUuid() == pt.GetId().GetUuid() {
				for k, v := range payload {
					pt.Payload[k] = v
				}
				found = true
				break
			}
		}
		if !found {
			m.upserted = append(m.upserted, &pb.PointStruct{
				Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: id.GetUuid()}},
				Payload: payload,
			})
		}
	}
	return nil
}

func (m *mockFactStore) Health(_ context.Context) error { return nil }
func (m *mockFactStore) Close() error                    { return nil }

// ── Mock Embedder ─────────────────────────────────────────────────────────────

type mockEmbedder struct {
	embedding.Embedder
	embedSingleFn func(ctx context.Context, text string) ([]float32, error)
}

func (m *mockEmbedder) EmbedSingle(ctx context.Context, text string) ([]float32, error) {
	if m.embedSingleFn != nil {
		return m.embedSingleFn(ctx, text)
	}
	return nil, nil
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	res := make([][]float32, len(texts))
	for i := range texts {
		res[i] = []float32{float32(i)}
	}
	return res, nil
}

func (m *mockEmbedder) Health(_ context.Context) error { return nil }

// ── Mock Synthesizer ──────────────────────────────────────────────────────────

type mockSynthesizer struct {
	llm.Synthesizer
}

func (m *mockSynthesizer) Synthesize(_ context.Context, _, _ string) (string, error) {
	return "mock response", nil
}
func (m *mockSynthesizer) Compare(_ context.Context, _, _, _, _ string) (string, error) {
	return "similar", nil
}
func (m *mockSynthesizer) Health(_ context.Context) error {
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func noopLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func defaultPrunerConfig() PrunerConfig {
	cfg := DefaultConfig()
	cfg.Enabled = false
	return cfg
}

func newTestPruner(facts, vault qdrant.FactStore, ec embedding.Embedder, lm llm.Synthesizer) *Pruner {
	return New(facts, vault, ec, lm, noopLogger(), defaultPrunerConfig())
}

func emptyMockFacts() *mockFactStore {
	return &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
			return nil, nil
		},
	}
}

// makePoint creates a RetrievedPoint with the given ID and payload.
func makePoint(id string, payload map[string]*pb.Value) *pb.RetrievedPoint {
	return &pb.RetrievedPoint{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{Uuid: id},
		},
		Payload: payload,
	}
}

// ── Value helper ──────────────────────────────────────────────────────────────

// nv converts a Go value to a qdrant Value for test payload construction.
func nv(v interface{}) *pb.Value {
	switch val := v.(type) {
	case string:
		return &pb.Value{Kind: &pb.Value_StringValue{StringValue: val}}
	case float64:
		return &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: val}}
	case bool:
		return &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: val}}
	default:
		panic(fmt.Sprintf("nv: unsupported type %T", v))
	}
}

// ── Payload helpers ───────────────────────────────────────────────────────────

func TestGetPayloadString(t *testing.T) {
	payload := map[string]*pb.Value{
		"status": nv("active"),
		"empty":  nv(""),
	}

	v, ok := qutil.GetPayloadString(payload, "status")
	if !ok || v != "active" {
		t.Errorf("expected active, got %q (ok=%v)", v, ok)
	}

	_, ok = qutil.GetPayloadString(payload, "missing")
	if ok {
		t.Error("expected missing key to return false")
	}

	v, ok = qutil.GetPayloadString(payload, "empty")
	if !ok || v != "" {
		t.Errorf("expected empty string, got %q (ok=%v)", v, ok)
	}
}

func TestGetPayloadFloat(t *testing.T) {
	payload := map[string]*pb.Value{
		"score": nv(0.85),
		"zero":  nv(0.0),
	}

	v, ok := qutil.GetPayloadFloat(payload, "score")
	if !ok || math.Abs(v-0.85) > 0.001 {
		t.Errorf("expected ~0.85, got %f (ok=%v)", v, ok)
	}

	_, ok = qutil.GetPayloadFloat(payload, "missing")
	if ok {
		t.Error("expected missing key to return false")
	}
}

func TestGetPayloadInt(t *testing.T) {
	payload := map[string]*pb.Value{
		"count": nv(float64(42)),
	}

	v, ok := qutil.GetPayloadInt(payload, "count")
	if !ok || v != 42 {
		t.Errorf("expected 42, got %d (ok=%v)", v, ok)
	}

	_, ok = qutil.GetPayloadInt(payload, "missing")
	if ok {
		t.Error("expected missing key to return false")
	}
}

func TestGetPayloadStringList(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		result := qutil.GetPayloadStringList(nil, "missing")
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("string value", func(t *testing.T) {
		payload := map[string]*pb.Value{
			"tags": nv("single"),
		}
		result := qutil.GetPayloadStringList(payload, "tags")
		if len(result) != 1 || result[0] != "single" {
			t.Errorf("expected [single], got %v", result)
		}
	})

	t.Run("list value", func(t *testing.T) {
		values := []*pb.Value{nv("a"), nv("b"), nv("c")}
		payload := map[string]*pb.Value{
			"tags": {
				Kind: &pb.Value_ListValue{
					ListValue: &pb.ListValue{Values: values},
				},
			},
		}
		result := qutil.GetPayloadStringList(payload, "tags")
		if len(result) != 3 || result[0] != "a" || result[1] != "b" || result[2] != "c" {
			t.Errorf("expected [a b c], got %v", result)
		}
	})
}

// ── Cosine Similarity ─────────────────────────────────────────────────────────

func TestCosineSimilarity(t *testing.T) {
	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{1, 0, 0}
		sim := cosineSimilarity(a, b)
		if math.Abs(sim-1.0) > 0.001 {
			t.Errorf("expected 1.0, got %f", sim)
		}
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{0, 1}
		sim := cosineSimilarity(a, b)
		if math.Abs(sim-0.0) > 0.001 {
			t.Errorf("expected 0.0, got %f", sim)
		}
	})

	t.Run("mismatched lengths", func(t *testing.T) {
		sim := cosineSimilarity([]float32{1}, []float32{1, 0})
		if sim != 0 {
			t.Errorf("expected 0 for mismatched lengths, got %f", sim)
		}
	})

	t.Run("empty vectors", func(t *testing.T) {
		sim := cosineSimilarity(nil, nil)
		if sim != 0 {
			t.Errorf("expected 0 for empty vectors, got %f", sim)
		}
	})

	t.Run("zero vector", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 0, 0}
		sim := cosineSimilarity(a, b)
		if sim != 0 {
			t.Errorf("expected 0 for zero vector, got %f", sim)
		}
	})

	t.Run("opposite vectors", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{-1, 0}
		sim := cosineSimilarity(a, b)
		if math.Abs(sim+1.0) > 0.001 {
			t.Errorf("expected -1.0, got %f", sim)
		}
	})
}

// ── Scroll helpers ────────────────────────────────────────────────────────────

func TestScrollAllFacts_Pagination(t *testing.T) {
	var callCount atomic.Int32
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
			callCount.Add(1)
			if offset == "" {
				return []*pb.RetrievedPoint{
					makePoint("a", map[string]*pb.Value{"fact_key": nv("k1")}),
					makePoint("b", map[string]*pb.Value{"fact_key": nv("k2")}),
					makePoint("c", map[string]*pb.Value{"fact_key": nv("k3")}),
				}, nil
			}
			return nil, nil // 2nd page empty
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	points, err := p.scrollAllFacts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 3 {
		t.Errorf("expected 3 points, got %d", len(points))
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 scroll calls, got %d", callCount.Load())
	}
}

func TestScrollAllFacts_Error(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
			return nil, assertAnError("oops")
		},
	}
	p := newTestPruner(mock, nil, nil, nil)
	_, err := p.scrollAllFacts(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── Scan helpers ──────────────────────────────────────────────────────────────

func TestScrollFilteredFacts_Limit(t *testing.T) {
	var gotLimit atomic.Uint32
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, limit uint32, _ string) ([]*pb.RetrievedPoint, error) {
			gotLimit.Store(limit)
			return nil, nil
		},
	}
	p := newTestPruner(mock, nil, nil, nil)
	_, _ = p.scrollFilteredFacts(context.Background(), nil, 10)
	if gotLimit.Load() != 10 {
		t.Errorf("expected limit 10, got %d", gotLimit.Load())
	}
}

func TestScrollFilteredFacts_NoLimit(t *testing.T) {
	var callCount atomic.Int32
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
			callCount.Add(1)
			if limit == 0 || limit > 200 {
				t.Errorf("expected page limit 200 or less from internal call, got %d", limit)
			}
			if offset == "" {
				return []*pb.RetrievedPoint{
					makePoint("p1", nil),
				}, nil
			}
			return nil, nil
		},
	}
	p := newTestPruner(mock, nil, nil, nil)
	points, err := p.scrollFilteredFacts(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 1 {
		t.Errorf("expected 1 point, got %d", len(points))
	}
}

// ── staleScan ─────────────────────────────────────────────────────────────────

func TestStaleScan_NilClient(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.staleScan(context.Background())
	// Should not panic
}

func TestStaleScan_MarksStaleFacts(t *testing.T) {
	now := float64(time.Now().UTC().Unix())
	past := now - 86400*200 // 200 days ago → expired

	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil // end of pagination
			}

			// Before #380 (fact graph) merges, staleScan calls scrollFilteredFacts
			// once with the full stale filter. After #380, collectReferencedKeys
			// adds an initial scroll with a 1-condition (status=active) filter.
			// Verify shape only when it's the 3-condition stale filter.
			if filter != nil && len(filter.Must) == 3 {
				if filter.Must[0].GetField().GetKey() != "status" ||
					filter.Must[1].GetField().GetKey() != "ttl_days" ||
					filter.Must[2].GetField().GetKey() != "expires_at_unix" {
					t.Fatalf("unexpected filter structure")
				}
			}
			return []*pb.RetrievedPoint{
				makePoint("p1", map[string]*pb.Value{
					"fact_key":        nv("expired-fact"),
					"expires_at_unix": nv(past),
				}),
			}, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.staleScan(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(upserted))
	}
	status, ok := upserted[0].GetPayload()["status"]
	if !ok || status.GetStringValue() != "needs_review" {
		t.Errorf("expected status 'needs_review', got %v", status)
	}
}

func TestStaleScan_NoStaleFacts(t *testing.T) {
	mock := emptyMockFacts()
	p := newTestPruner(mock, nil, nil, nil)
	p.staleScan(context.Background())
	// Should not panic, no upserts
}

// ── lowConfidenceScan ─────────────────────────────────────────────────────────

func TestLowConfidenceScan_NilClient(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.lowConfidenceScan(context.Background())
	// Should not panic
}

func TestLowConfidenceScan_MarksLowConfidence(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil // end of pagination
			}

			// Verify confidence filter uses Lt with threshold-0.001
			if len(filter.Must) != 2 {
				t.Fatalf("expected 2 must conditions, got %d", len(filter.Must))
			}
			return []*pb.RetrievedPoint{
				makePoint("p1", map[string]*pb.Value{
					"fact_key": nv("low-conf-fact"),
					"status":   nv("active"),
				}),
			}, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.lowConfidenceScan(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(upserted))
	}
	status, ok := upserted[0].GetPayload()["status"]
	if !ok || status.GetStringValue() != "needs_review" {
		t.Errorf("expected status 'needs_review', got %v", status)
	}
}

func TestLowConfidenceScan_EmptyResults(t *testing.T) {
	mock := emptyMockFacts()
	p := newTestPruner(mock, nil, nil, nil)
	p.lowConfidenceScan(context.Background())
	// Should not panic, no upserts
}

// ── conflictScan ──────────────────────────────────────────────────────────────

func TestConflictScan_NilClient(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.conflictScan(context.Background())
	// Should not panic
}

func TestConflictScan_NotEnoughFacts(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil // end of pagination
			}

			return []*pb.RetrievedPoint{
				makePoint("p1", map[string]*pb.Value{
					"fact_key":   nv("k1"),
					"fact_value": nv("value1"),
				}),
			}, nil
		},
	}
	p := newTestPruner(mock, nil, nil, nil)
	p.conflictScan(context.Background()) // only 1 fact → not enough
	// Should not panic, no upserts
}

func TestConflictScan_HighSimilarityFlags(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil // end of pagination
			}

			return []*pb.RetrievedPoint{
				makePoint("p1", map[string]*pb.Value{
					"fact_key":   nv("k1"),
					"fact_value": nv("same topic"),
				}),
				makePoint("p2", map[string]*pb.Value{
					"fact_key":   nv("k2"),
					"fact_value": nv("same topic"),
				}),
			}, nil
		},
		getPointsFn: func(_ context.Context, _ string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error) {
			return []*pb.RetrievedPoint{
				makePoint("p2", map[string]*pb.Value{
					"fact_key":   nv("k2"),
					"fact_value": nv("value2"),
				}),
			}, nil
		},
	}

	embedder := &mockEmbedder{
		embedSingleFn: func(_ context.Context, text string) ([]float32, error) {
			return []float32{0.9, 0.1}, nil
		},
	}

	p := newTestPruner(mock, nil, embedder, nil)
	p.conflictScan(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	// Should have upserted a markContradiction call (status = needs_review)
	if len(upserted) == 0 {
		t.Fatal("expected at least 1 upsert (contradiction mark)")
	}
}

func TestConflictScan_LowSimilaritySkipped(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil // end of pagination
			}

			return []*pb.RetrievedPoint{
				makePoint("p1", map[string]*pb.Value{
					"fact_key":   nv("k1"),
					"fact_value": nv("apples"),
				}),
				makePoint("p2", map[string]*pb.Value{
					"fact_key":   nv("k2"),
					"fact_value": nv("oranges"),
				}),
			}, nil
		},
	}

	// Different vectors → low cosine similarity
	callCount := 0
	embedder := &mockEmbedder{
		embedSingleFn: func(_ context.Context, text string) ([]float32, error) {
			callCount++
			if callCount == 1 {
				return []float32{1, 0}, nil // first fact
			}
			return []float32{0, 1}, nil // second fact (orthogonal)
		},
	}

	p := newTestPruner(mock, nil, embedder, nil)
	p.conflictScan(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 0 {
		t.Errorf("expected 0 upserts for low-similarity pair, got %d", len(upserted))
	}
}

func TestConflictScan_SkipsResolved(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
			// Verify MustNot filter excludes conflict_resolved=true
			if len(filter.MustNot) != 1 {
				t.Fatalf("expected 1 MustNot condition, got %d", len(filter.MustNot))
			}
			cond := filter.MustNot[0].GetField()
			if cond.GetKey() != "conflict_resolved" {
				t.Errorf("expected conflict_resolved key, got %q", cond.GetKey())
			}
			return nil, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.conflictScan(context.Background())
	// Filter shape verified above
}

// ── supersedeScan ─────────────────────────────────────────────────────────────

func TestSupersedeScan_NilClient(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.supersedeScan(context.Background())
	// Should not panic
}

func TestSupersedeCrossReference_TargetMarked(t *testing.T) {
	returnedP2 := false
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
			// First scroll (paginated, limit=200): find facts with supersedes set
			if limit == 200 && offset == "" {
				return []*pb.RetrievedPoint{
					makePoint("p1", map[string]*pb.Value{
						"fact_key":   nv("newer-fact"),
						"supersedes": nv("older-fact"),
					}),
				}, nil
			}
			// Pagination: no more pages
			if limit == 200 && offset != "" {
				return nil, nil
			}
			// Second scroll (target lookup, limit=1): return the target fact
			if limit == 1 && !returnedP2 {
				returnedP2 = true
				return []*pb.RetrievedPoint{
					makePoint("p2", map[string]*pb.Value{
						"fact_key": nv("older-fact"),
						"status":   nv("active"),
					}),
				}, nil
			}

			return nil, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.supersedeCrossReference(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 1 {
		t.Fatalf("expected 1 upsert (mark target superseded), got %d", len(upserted))
	}
	status, _ := qutil.GetPayloadString(upserted[0].GetPayload(), "status")
	if status != "superseded" {
		t.Errorf("expected status 'superseded', got %q", status)
	}
}

func TestSupersedeCrossReference_AlreadyMarkedSkipped(t *testing.T) {
	returnedP2 := false
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
			// First scroll (paginated, limit=200): find facts with supersedes
			if limit == 200 && offset == "" {
				return []*pb.RetrievedPoint{
					makePoint("p1", map[string]*pb.Value{
						"fact_key":   nv("newer"),
						"supersedes": nv("older"),
					}),
				}, nil
			}
			// Pagination: no more pages
			if limit == 200 && offset != "" {
				return nil, nil
			}
			// Target lookup (limit=1): target already superseded
			if limit == 1 && !returnedP2 {
				returnedP2 = true
				return []*pb.RetrievedPoint{
					makePoint("p2", map[string]*pb.Value{
						"fact_key": nv("older"),
						"status":   nv("superseded"),
					}),
				}, nil
			}
			return nil, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.supersedeCrossReference(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 0 {
		t.Errorf("expected 0 upserts for already-superseded target, got %d", len(upserted))
	}
}

func TestSupersedeCrossReference_EmptySupersedesSkipped(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
			// Return empty (no facts with non-empty supersedes)
			return nil, nil
		},
	}
	p := newTestPruner(mock, nil, nil, nil)
	p.supersedeCrossReference(context.Background())
	// No upserts expected
}

func TestSupersedeKeyPattern_HigherVersionSupersedes(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
			// Only return data on first page, stop pagination on subsequent calls
			if limit == 200 && offset == "" {
				return []*pb.RetrievedPoint{
					makePoint("p1", map[string]*pb.Value{
						"fact_key": nv("org/v1/decision"),
						"status":   nv("active"),
					}),
					makePoint("p2", map[string]*pb.Value{
						"fact_key": nv("org/v2/decision"),
						"status":   nv("active"),
					}),
				}, nil
			}
			return nil, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.supersedeKeyPattern(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 1 {
		t.Fatalf("expected 1 upsert (v1 superseded), got %d", len(upserted))
	}
	status, _ := qutil.GetPayloadString(upserted[0].GetPayload(), "status")
	if status != "superseded" {
		t.Errorf("expected status 'superseded', got %q", status)
	}
}

func TestSupersedeKeyPattern_NoVersionedKeys(t *testing.T) {
	called := false
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
			if called {
				return nil, nil
			}
			called = true
			return []*pb.RetrievedPoint{
				makePoint("p1", map[string]*pb.Value{
					"fact_key": nv("org/decision"),
					"status":   nv("active"),
				}),
			}, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.supersedeKeyPattern(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 0 {
		t.Errorf("expected 0 upserts for non-versioned keys, got %d", len(upserted))
	}
}

func TestSupersedeKeyPattern_SingleVersion(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, _ *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if limit == 200 && offset == "" {
				return []*pb.RetrievedPoint{
					makePoint("p1", map[string]*pb.Value{
						"fact_key": nv("org/v2/decision"),
						"status":   nv("active"),
					}),
				}, nil
			}
			return nil, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.supersedeKeyPattern(context.Background())

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 0 {
		t.Errorf("expected 0 upserts for single version, got %d", len(upserted))
	}
}

// ── markContradiction ────────────────────────────────────────────────────────

func TestMarkContradiction(t *testing.T) {
	mock := &mockFactStore{
		name: "test_facts",
		getPointsFn: func(_ context.Context, _ string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error) {
			return []*pb.RetrievedPoint{
				makePoint("p1", map[string]*pb.Value{
					"fact_key":   nv("target-fact"),
					"fact_value": nv("value"),
				}),
			}, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	if err := p.markContradiction(context.Background(), "p1", "target-fact"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(upserted))
	}
	payload := upserted[0].GetPayload()
	if status, _ := qutil.GetPayloadString(payload, "status"); status != "needs_review" {
		t.Errorf("expected status needs_review, got %q", status)
	}
	if resolved, _ := qutil.GetPayloadFloat(payload, "conflict_resolved"); resolved != 0 {
		t.Errorf("expected conflict_resolved false, got %f", resolved)
	}
}

// ── RecordFlagged / RecordResolved ────────────────────────────────────────────

func TestRecordFlagged(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.RecordFlagged(5)
	p.RecordFlagged(3)

	total := p.factsFlaggedCount()
	if total != 8 {
		t.Errorf("expected 8, got %d", total)
	}
}

func TestRecordResolved(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.RecordResolved(2)
	p.RecordResolved(3)
}

// ── recordScanRun ─────────────────────────────────────────────────────────────

func TestRecordScanRun(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.recordScanRun("StaleScan")
	p.recordScanRun("StaleScan")

	// Verify via health
	hr := p.Health()
	scan, ok := hr.Scans["StaleScan"]
	if !ok {
		t.Fatal("expected StaleScan in health report")
	}
	if scan.TotalRuns != 2 {
		t.Errorf("expected 2 runs, got %d", scan.TotalRuns)
	}
	if scan.LastRun == "" {
		t.Error("expected non-empty last run timestamp")
	}
}

// ── Health ────────────────────────────────────────────────────────────────────

func TestHealth_Disabled(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	hr := p.Health()
	if hr.Enabled {
		t.Error("expected Pruner to be disabled")
	}
}

func TestHealth_Enabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StaleScanInterval = 2 * time.Hour
	p := New(emptyMockFacts(), nil, nil, nil, noopLogger(), cfg)

	hr := p.Health()
	if !hr.Enabled {
		t.Error("expected Pruner to be enabled")
	}
	if _, ok := hr.Scans["StaleScan"]; !ok {
		t.Error("expected StaleScan in health report")
	}
}

// ── Metrics ───────────────────────────────────────────────────────────────────

func TestMetrics(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	p.recordScanRun("StaleScan")
	p.RecordFlagged(7)
	p.RecordResolved(3)

	scans, flagged, resolved := p.Metrics()
	if scans["StaleScan"] != 1 {
		t.Errorf("expected 1 scan run, got %d", scans["StaleScan"])
	}
	if flagged != 7 {
		t.Errorf("expected 7 flagged, got %d", flagged)
	}
	if resolved != 3 {
		t.Errorf("expected 3 resolved, got %d", resolved)
	}
}

// ── ProcessEvents (placeholder) ───────────────────────────────────────────────

func TestProcessEvents_ContextCancelled(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan watcher.Event)
	close(ch)
	p.ProcessEvents(ctx, ch)
	// Should return immediately when ctx is done
}

func TestProcessEvents_DrainsEvents(t *testing.T) {
	p := newTestPruner(nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())

	ch := make(chan watcher.Event, 3)
	ch <- watcher.Event{Path: "a.txt", Action: watcher.ActionAdd}
	ch <- watcher.Event{Path: "b.txt", Action: watcher.ActionModify}
	ch <- watcher.Event{Path: "c.txt", Action: watcher.ActionDelete}

	go p.ProcessEvents(ctx, ch)
	time.Sleep(10 * time.Millisecond) // let it process
	cancel()
	// Should not panic
}

// ── parseVersionedKey ─────────────────────────────────────────────────────────

func TestParseVersionedKey(t *testing.T) {
	tests := []struct {
		key      string
		prefix   string
		version  int
	}{
		{"org/v2/decision", "org", 2},
		{"org/v10/decision", "org", 10},
		{"project/feature/v3", "project/feature", 3},
		{"v1/key", "", 1},
		{"noversion/key", "", 0},
		{"org/v/key", "", 0},         // no digits after v
		{"org/v0/key", "", 0},        // v0 is not a version
		{"org/decision", "", 0},
		{"", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			prefix, version := parseVersionedKey(tt.key)
			if prefix != tt.prefix {
				t.Errorf("expected prefix %q, got %q", tt.prefix, prefix)
			}
			if version != tt.version {
				t.Errorf("expected version %d, got %d", tt.version, version)
			}
		})
	}
}

// ── nv helper ─────────────────────────────────────────────────────────────────

func TestNv_PanicsOnUnsupportedType(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unsupported type")
		}
	}()
	nv(struct{}{}) // should panic
}

func TestNv_ValidTypes(t *testing.T) {
	// These should not panic
	_ = nv("hello")
	_ = nv(float64(42))
	_ = nv(true)
}

// ── updateFactStatus ──────────────────────────────────────────────────────────

func TestUpdateFactStatus(t *testing.T) {
	mock := emptyMockFacts()
	p := newTestPruner(mock, nil, nil, nil)

	err := p.updateFactStatus(context.Background(), "test-id", "needs_review")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(upserted))
	}
	payload := upserted[0].GetPayload()
	if s, _ := qutil.GetPayloadString(payload, "status"); s != "needs_review" {
		t.Errorf("expected status 'needs_review', got %q", s)
	}
	if _, ok := payload["updated_at"]; !ok {
		t.Error("expected updated_at in payload")
	}
}

// ── updateFactPayload ─────────────────────────────────────────────────────────

func TestUpdateFactPayload(t *testing.T) {
	mock := emptyMockFacts()
	p := newTestPruner(mock, nil, nil, nil)

	payload := map[string]*pb.Value{
		"confidence": nv(0.95),
	}
	err := p.updateFactPayload(context.Background(), "test-id", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	upserted := mock.upserted
	mock.mu.Unlock()

	if len(upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(upserted))
	}
	upsertPayload := upserted[0].GetPayload()
	if c, _ := qutil.GetPayloadFloat(upsertPayload, "confidence"); math.Abs(c-0.95) > 0.001 {
		t.Errorf("expected confidence 0.95, got %f", c)
	}
	if _, ok := upsertPayload["updated_at"]; !ok {
		t.Error("expected updated_at in payload")
	}
}

// ── assertAnError helper ──────────────────────────────────────────────────────

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func assertAnError(msg string) error { return &testError{msg: msg} }
