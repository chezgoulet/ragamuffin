// Package embedding provides an HTTP client for embedding model APIs.
//
// The Embedder interface enables mock-based testing of consumers
// (pruner, server, indexer) without a live embedding endpoint.
package embedding

import "context"

// Embedder turns text into vector embeddings.
type Embedder interface {
	// Embed converts a batch of texts into embedding vectors.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// EmbedSingle converts a single text into an embedding vector.
	EmbedSingle(ctx context.Context, text string) ([]float32, error)

	// Health checks connectivity by making a minimal API call.
	Health(ctx context.Context) error
}

// Compile-time check: *Client satisfies Embedder.
var _ Embedder = (*Client)(nil)
