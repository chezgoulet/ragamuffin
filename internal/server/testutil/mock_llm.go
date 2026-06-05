package testutil

import (
	"context"
	"sync/atomic"
)

// MockLLM is a function-pointer-based mock for llm.Client.
type MockLLM struct {
	SynthesizeFn func(ctx context.Context, query, context string) (string, error)
	CompareFn    func(ctx context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error)

	SynthesizeCallCount atomic.Int64
	CompareCallCount    atomic.Int64
}

func (m *MockLLM) Synthesize(ctx context.Context, query, context string) (string, error) {
	m.SynthesizeCallCount.Add(1)
	if m.SynthesizeFn != nil {
		return m.SynthesizeFn(ctx, query, context)
	}
	return "", nil
}

func (m *MockLLM) Compare(ctx context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error) {
	m.CompareCallCount.Add(1)
	if m.CompareFn != nil {
		return m.CompareFn(ctx, chunkA, chunkB, sourceA, sourceB)
	}
	return "", nil
}

func (m *MockLLM) Health(_ context.Context) error {
	return nil
}
