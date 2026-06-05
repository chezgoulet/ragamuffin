package pruner

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

func TestStaleScan_SkipsFactsWithIncomingEdges(t *testing.T) {
	now := float64(time.Now().UTC().Unix())
	past := now - 86400*200 // 200 days ago → expired

	var mu sync.Mutex
	callCount := 0
	factP1ID := "p1-uuid"
	factP2ID := "p2-uuid"
	factP3ID := "p3-uuid"

	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil
			}

			mu.Lock()
			callCount++
			count := callCount
			mu.Unlock()

			if count == 1 {
				// First call: stale scan — return 3 expired facts
				return []*pb.RetrievedPoint{
					makePoint(factP1ID, map[string]*pb.Value{
						"fact_key":        nv("expired-fact-isolated"),
						"expires_at_unix": nv(past),
						"status":          nv("active"),
					}),
					makePoint(factP2ID, map[string]*pb.Value{
						"fact_key":        nv("expired-fact-with-edge"),
						"expires_at_unix": nv(past),
						"status":          nv("active"),
					}),
					makePoint(factP3ID, map[string]*pb.Value{
						"fact_key":        nv("another-isolated-fact"),
						"expires_at_unix": nv(past),
						"status":          nv("active"),
					}),
				}, nil
			}

			// Second call: collectReferencedKeys — return facts that reference expired-fact-with-edge
			return []*pb.RetrievedPoint{
				makePoint("p99", map[string]*pb.Value{
					"fact_key":   nv("referencing-fact"),
					"supersedes": nv("expired-fact-with-edge"),
					"status":     nv("active"),
					"refines":    nv(""),
				}),
			}, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.staleScan(context.Background())

	mock.mu.Lock()
	setPayloadCount := len(mock.upserted)
	var setPayloadKeys []string
	for _, pt := range mock.upserted {
		if pt != nil && pt.Payload != nil {
			if s, ok := pt.Payload["status"]; ok {
				setPayloadKeys = append(setPayloadKeys, s.GetStringValue())
			}
		}
	}
	mock.mu.Unlock()

	// Only 2 facts should be marked (p1 and p3 — isolated ones).
	// p2 (expired-fact-with-edge) should be skipped because it has incoming edges.
	if setPayloadCount < 2 || setPayloadCount > 2 {
		t.Fatalf("expected 2 SetPayload calls (skipping the edge-referenced fact), got %d. Marked statuses: %v", setPayloadCount, setPayloadKeys)
	}

	// Verify the isolated facts were marked
	if len(setPayloadKeys) != 2 {
		t.Fatalf("expected 2 marked statuses, got %d: %v", len(setPayloadKeys), setPayloadKeys)
	}
	allNeedsReview := true
	for _, status := range setPayloadKeys {
		if status != "needs_review" {
			allNeedsReview = false
		}
	}
	if !allNeedsReview {
		t.Errorf("all marked facts should have status 'needs_review', got %v", setPayloadKeys)
	}
}

func TestStaleScan_ReferencedKeys_SupportsList(t *testing.T) {
	now := float64(time.Now().UTC().Unix())
	past := now - 86400*200

	var mu sync.Mutex
	callCount := 0

	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil
			}

			mu.Lock()
			callCount++
			count := callCount
			mu.Unlock()

			if count == 1 {
				return []*pb.RetrievedPoint{
					makePoint("p1", map[string]*pb.Value{
						"fact_key":        nv("target-fact"),
						"expires_at_unix": nv(past),
						"status":          nv("active"),
					}),
				}, nil
			}

			// collectReferencedKeys — fact that "supports" target-fact via list
			return []*pb.RetrievedPoint{
				makePoint("p99", map[string]*pb.Value{
					"fact_key": nv("supporter"),
					"supports": nvList([]string{"target-fact", "other-fact"}),
					"status":   nv("active"),
				}),
			}, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.staleScan(context.Background())

	mock.mu.Lock()
	marked := len(mock.upserted)
	mock.mu.Unlock()

	if marked != 0 {
		t.Errorf("target-fact has incoming supports edge, should not be marked. Got %d marked", marked)
	}
}

func TestStaleScan_ContradictsList(t *testing.T) {
	now := float64(time.Now().UTC().Unix())
	past := now - 86400*200

	var mu sync.Mutex
	callCount := 0

	mock := &mockFactStore{
		name: "test_facts",
		scrollFilteredFn: func(_ context.Context, _ string, filter *pb.Filter, _ uint32, offset string) ([]*pb.RetrievedPoint, error) {
			if offset != "" {
				return nil, nil
			}

			mu.Lock()
			callCount++
			count := callCount
			mu.Unlock()

			if count == 1 {
				return []*pb.RetrievedPoint{
					makePoint("p1", map[string]*pb.Value{
						"fact_key":        nv("contradicted-fact"),
						"expires_at_unix": nv(past),
						"status":          nv("active"),
					}),
				}, nil
			}

			return []*pb.RetrievedPoint{
				makePoint("p99", map[string]*pb.Value{
					"fact_key":    nv("contradictor"),
					"contradicts": nvList([]string{"contradicted-fact"}),
					"status":      nv("active"),
				}),
			}, nil
		},
	}

	p := newTestPruner(mock, nil, nil, nil)
	p.staleScan(context.Background())

	mock.mu.Lock()
	marked := len(mock.upserted)
	mock.mu.Unlock()

	if marked != 0 {
		t.Errorf("contradicted-fact has incoming contradicts edge, should not be marked. Got %d marked", marked)
	}
}

// nvList creates a Qdrant list value from string slice
func nvList(items []string) *pb.Value {
	values := make([]*pb.Value, len(items))
	for i, s := range items {
		values[i] = nv(s)
	}
	return &pb.Value{
		Kind: &pb.Value_ListValue{
			ListValue: &pb.ListValue{Values: values},
		},
	}
}
