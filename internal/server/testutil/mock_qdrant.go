package testutil

import (
	"context"

	qdrant "github.com/qdrant/go-client/qdrant"
)

// MockQdrant is a function-pointer-based mock for qdrant.Client methods
// that the server handlers use. Each function field defaults to returning
// empty/nil results when not set. CallCount fields track invocations.
type MockQdrant struct {
	ScrollFn func(ctx context.Context, limit uint32, offset *qdrant.PointId) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error)
	UpsertFn func(ctx context.Context, points []*qdrant.PointStruct) error
	SearchFn func(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string) ([]*qdrant.ScoredPoint, error)
	DeleteFn func(ctx context.Context, sourceFile string) error
	CountFn  func(ctx context.Context) (uint64, error)
	HealthFn func(ctx context.Context) error

	ScrollCallCount int
	UpsertCallCount int
	SearchCallCount int
	DeleteCallCount int
	CountCallCount  int
	HealthCallCount int
}

func (m *MockQdrant) Scroll(ctx context.Context, limit uint32, offset *qdrant.PointId) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error) {
	m.ScrollCallCount++
	if m.ScrollFn != nil {
		return m.ScrollFn(ctx, limit, offset)
	}
	return []*qdrant.RetrievedPoint{}, nil, nil
}

func (m *MockQdrant) Upsert(ctx context.Context, points []*qdrant.PointStruct) error {
	m.UpsertCallCount++
	if m.UpsertFn != nil {
		return m.UpsertFn(ctx, points)
	}
	return nil
}

func (m *MockQdrant) Search(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string) ([]*qdrant.ScoredPoint, error) {
	m.SearchCallCount++
	if m.SearchFn != nil {
		return m.SearchFn(ctx, vector, limit, scoreThreshold, sourceFilter)
	}
	return []*qdrant.ScoredPoint{}, nil
}

func (m *MockQdrant) Delete(ctx context.Context, sourceFile string) error {
	m.DeleteCallCount++
	if m.DeleteFn != nil {
		return m.DeleteFn(ctx, sourceFile)
	}
	return nil
}

func (m *MockQdrant) Count(ctx context.Context) (uint64, error) {
	m.CountCallCount++
	if m.CountFn != nil {
		return m.CountFn(ctx)
	}
	return 0, nil
}

func (m *MockQdrant) Health(ctx context.Context) error {
	m.HealthCallCount++
	if m.HealthFn != nil {
		return m.HealthFn(ctx)
	}
	return nil
}
