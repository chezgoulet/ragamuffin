package testutil

import "context"

// MockLLM is a function-pointer-based mock for llm.Client.
type MockLLM struct {
	SynthesizeFn func(ctx context.Context, query, context string) (string, error)
	CompareFn    func(ctx context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error)

	SynthesizeCallCount int
	CompareCallCount    int
}

func (m *MockLLM) Synthesize(ctx context.Context, query, context string) (string, error) {
	m.SynthesizeCallCount++
	if m.SynthesizeFn != nil {
		return m.SynthesizeFn(ctx, query, context)
	}
	return "", nil
}

func (m *MockLLM) Compare(ctx context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error) {
	m.CompareCallCount++
	if m.CompareFn != nil {
		return m.CompareFn(ctx, chunkA, chunkB, sourceA, sourceB)
	}
	return "", nil
}
