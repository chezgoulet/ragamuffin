// Package ingress defines the IngressDriver interface for ingesting content
// into the Ragamuffin semantic memory pipeline.
//
// An IngressDriver produces a stream of IngestEvents (add, modify, delete)
// from an external source — a filesystem watcher, an API endpoint, a webhook,
// a chat bot archive, etc. All drivers converge on the same downstream
// pipeline: chunk → embed → store → extract facts.
//
// The vault file watcher is one implementation. New sources implement
// the three-method contract and are registered in main.go.
package ingress

import "context"

// IngestAction describes the type of change for an ingest event.
type IngestAction string

const (
	ActionAdd    IngestAction = "add"
	ActionModify IngestAction = "modify"
	ActionDelete IngestAction = "delete"
)

// IngestEvent represents one unit of content to process.
type IngestEvent struct {
	// Action indicates whether the content was added, modified, or deleted.
	Action IngestAction

	// Path is the relative path or logical key within the source namespace
	// (e.g. "docs/architecture.md", "conversations/abc123.json").
	Path string

	// AbsPath is the absolute filesystem path, set only for file-based sources.
	AbsPath string

	// Content holds the raw bytes for inline/stream-based sources (API upload,
	// webhook payload, etc.). File-based sources may leave this nil.
	Content []byte

	// Meta carries arbitrary key-value metadata from the source (e.g. source
	// URL, mime type, author, labels).
	Meta map[string]string
}

// IngressDriver ingests content into the semantic memory pipeline.
// Each driver produces events that the indexer consumes.
//
// Lifecycle:
//  1. The caller calls Name() for identification / metrics.
//  2. The caller calls Events() to obtain the event channel.
//  3. The caller starts Run(ctx) in a goroutine. Run blocks until ctx
//     is cancelled or a fatal error occurs.
//  4. When Run returns, the Events() channel should be closed (the caller
//     is responsible for closing it if Run does not).
type IngressDriver interface {
	// Name returns a unique identifier for this driver instance
	// (e.g. "file-watcher", "api-ingest").
	Name() string

	// Events returns a channel of ingest events. The channel should
	// close when the driver shuts down (context cancelled or Run returns).
	Events() <-chan IngestEvent

	// Run starts the driver's event loop. It must block until ctx is
	// cancelled or a fatal error occurs. Returning is a shutdown signal.
	Run(ctx context.Context) error
}
