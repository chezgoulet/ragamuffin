package testutil

import "context"

// MockEmbedder is a function-pointer-based mock for embedding.Client.
type MockEmbedder struct {
	EmbedFn func(ctx context.Context, texts []string) ([][]float32, error)

	EmbedCallCount int
}

func (m *MockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.EmbedCallCount++
	if m.EmbedFn != nil {
		return m.EmbedFn(ctx, texts)
	}
	return [][]float32{}, nil
}
