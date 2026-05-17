package testutil

import (
	"context"
	"sync/atomic"
)

// MockEmbedder is a function-pointer-based mock for embedding.Client.
type MockEmbedder struct {
	EmbedFn       func(ctx context.Context, texts []string) ([][]float32, error)
	EmbedSingleFn func(ctx context.Context, text string) ([]float32, error)
	HealthFn      func(ctx context.Context) error

	EmbedCallCount       atomic.Int64
	EmbedSingleCallCount atomic.Int64
	HealthCallCount      atomic.Int64
}

func (m *MockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.EmbedCallCount.Add(1)
	if m.EmbedFn != nil {
		return m.EmbedFn(ctx, texts)
	}
	return [][]float32{}, nil
}

func (m *MockEmbedder) EmbedSingle(ctx context.Context, text string) ([]float32, error) {
	m.EmbedSingleCallCount.Add(1)
	if m.EmbedSingleFn != nil {
		return m.EmbedSingleFn(ctx, text)
	}
	return []float32{}, nil
}

func (m *MockEmbedder) Health(ctx context.Context) error {
	m.HealthCallCount.Add(1)
	if m.HealthFn != nil {
		return m.HealthFn(ctx)
	}
	return nil
}
