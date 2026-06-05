// Package llm provides an HTTP client for large-language-model APIs.
//
// The Synthesizer interface enables mock-based testing of consumers
// (pruner, server) without a live LLM endpoint.
package llm

import "context"

// Synthesizer generates text responses from natural-language queries.
type Synthesizer interface {
	// Synthesize answers a query using the given context text.
	Synthesize(ctx context.Context, query, context string) (string, error)

	// Compare evaluates two text chunks from their sources and returns
	// a qualitative assessment of similarity or difference.
	Compare(ctx context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error)

	// Health checks connectivity by making a minimal API call.
	Health(ctx context.Context) error
}

// Compile-time check: *Client satisfies Synthesizer.
var _ Synthesizer = (*Client)(nil)
