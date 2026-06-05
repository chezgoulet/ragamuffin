package testutil

import (
	"context"
	"sync/atomic"

	qdrant "github.com/qdrant/go-client/qdrant"
)

// MockQdrant is a function-pointer-based mock for qdrant.Client methods.
//
// Note on structural incompatibility: These mocks are standalone structs, not
// implementations of an interface. The production code (Server, Pruner) uses
// concrete *qdrant.Client pointers, so MockQdrant cannot be directly substituted.
// Long-term fix: extract interfaces from qdrant.Client, embedding.Client, and
// llm.Client. Short-term: use these mocks in tests that accept the function
// pointers (or embed *qdrant.Client per the robot-chezgoulet suggestion).
//
// Each callable method has a corresponding callback field and a CallCount.
// CallCounts are atomic.Int64 for safe use with t.Parallel().
type MockQdrant struct {
	ScrollFn          func(ctx context.Context, limit uint32, offset *qdrant.PointId) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error)
	ScrollFilteredFn  func(ctx context.Context, collection string, filter *qdrant.Filter, limit uint32, offset string) ([]*qdrant.RetrievedPoint, error)
	UpsertFn          func(ctx context.Context, points []*qdrant.PointStruct) error
	SetPayloadFn      func(ctx context.Context, collection string, points []*qdrant.PointId, payload map[string]*qdrant.Value) error
	SearchFn          func(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string) ([]*qdrant.ScoredPoint, error)
	DeleteBySourceFn  func(ctx context.Context, sourceFile string) error
	CountFn           func(ctx context.Context) (uint64, error)
	CountFilesFn      func(ctx context.Context) (int, error)
	HealthFn          func(ctx context.Context) error
	CreatePayloadIndexFn func(ctx context.Context, collection, field, fieldType string) error
	CollectionFn      func() string
	CloseFn           func() error

	ScrollCallCount          atomic.Int64
	ScrollFilteredCallCount  atomic.Int64
	UpsertCallCount          atomic.Int64
	SetPayloadCallCount      atomic.Int64
	SearchCallCount          atomic.Int64
	DeleteBySourceCallCount  atomic.Int64
	CountCallCount           atomic.Int64
	CountFilesCallCount      atomic.Int64
	HealthCallCount          atomic.Int64
	CreatePayloadIndexCallCount atomic.Int64
	CollectionCallCount      atomic.Int64
	CloseCallCount           atomic.Int64
}

func (m *MockQdrant) Scroll(ctx context.Context, limit uint32, offset *qdrant.PointId) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error) {
	m.ScrollCallCount.Add(1)
	if m.ScrollFn != nil {
		return m.ScrollFn(ctx, limit, offset)
	}
	return []*qdrant.RetrievedPoint{}, nil, nil
}

func (m *MockQdrant) ScrollFiltered(ctx context.Context, collection string, filter *qdrant.Filter, limit uint32, offset string) ([]*qdrant.RetrievedPoint, error) {
	m.ScrollFilteredCallCount.Add(1)
	if m.ScrollFilteredFn != nil {
		return m.ScrollFilteredFn(ctx, collection, filter, limit, offset)
	}
	return []*qdrant.RetrievedPoint{}, nil
}

func (m *MockQdrant) Upsert(ctx context.Context, points []*qdrant.PointStruct) error {
	m.UpsertCallCount.Add(1)
	if m.UpsertFn != nil {
		return m.UpsertFn(ctx, points)
	}
	return nil
}

func (m *MockQdrant) SetPayload(ctx context.Context, collection string, points []*qdrant.PointId, payload map[string]*qdrant.Value) error {
	m.SetPayloadCallCount.Add(1)
	if m.SetPayloadFn != nil {
		return m.SetPayloadFn(ctx, collection, points, payload)
	}
	return nil
}

func (m *MockQdrant) Search(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string, filter *qdrant.Filter) ([]*qdrant.ScoredPoint, error) {
	m.SearchCallCount.Add(1)
	if m.SearchFn != nil {
		return m.SearchFn(ctx, vector, limit, scoreThreshold, sourceFilter, filter)
	}
	return []*qdrant.ScoredPoint{}, nil
}

func (m *MockQdrant) DeleteBySource(ctx context.Context, sourceFile string) error {
	m.DeleteBySourceCallCount.Add(1)
	if m.DeleteBySourceFn != nil {
		return m.DeleteBySourceFn(ctx, sourceFile)
	}
	return nil
}

func (m *MockQdrant) Count(ctx context.Context) (uint64, error) {
	m.CountCallCount.Add(1)
	if m.CountFn != nil {
		return m.CountFn(ctx)
	}
	return 0, nil
}

func (m *MockQdrant) CountFiles(ctx context.Context) (int, error) {
	m.CountFilesCallCount.Add(1)
	if m.CountFilesFn != nil {
		return m.CountFilesFn(ctx)
	}
	return 0, nil
}

func (m *MockQdrant) Health(ctx context.Context) error {
	m.HealthCallCount.Add(1)
	if m.HealthFn != nil {
		return m.HealthFn(ctx)
	}
	return nil
}

func (m *MockQdrant) CreatePayloadIndex(ctx context.Context, collection, field, fieldType string) error {
	m.CreatePayloadIndexCallCount.Add(1)
	if m.CreatePayloadIndexFn != nil {
		return m.CreatePayloadIndexFn(ctx, collection, field, fieldType)
	}
	return nil
}

func (m *MockQdrant) Collection() string {
	m.CollectionCallCount.Add(1)
	if m.CollectionFn != nil {
		return m.CollectionFn()
	}
	return "test-collection"
}

func (m *MockQdrant) Close() error {
	m.CloseCallCount.Add(1)
	if m.CloseFn != nil {
		return m.CloseFn()
	}
	return nil
}
