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

	// SynthesizeCited answers a query using context whose passages are
	// labelled with chunk IDs, instructing the model to attribute each
	// sentence with inline [cite: <chunk_id>] markers.
	SynthesizeCited(ctx context.Context, query, context string) (string, error)

	// Compare evaluates two text chunks from their sources and returns
	// a qualitative assessment of similarity or difference.
	Compare(ctx context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error)

	// Health checks connectivity by making a minimal API call.
	Health(ctx context.Context) error
}

// Completer sends a raw prompt to the LLM and returns the completion text
// with no templating. It is the minimal surface needed by retrieval-side
// query rewriting and listwise reranking, decoupling internal/retrieval from
// the full Client so those helpers stay pure and mockable.
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// Compile-time checks: *Client satisfies both facades.
var (
	_ Synthesizer = (*Client)(nil)
	_ Completer   = (*Client)(nil)
)
