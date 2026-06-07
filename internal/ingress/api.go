package ingress

import (
	"context"
	"fmt"
	"log/slog"
)

// ── APIIngestDriver ──────────────────────────────────────────────────────────

// IngestAPIFunc is the function signature for indexing content through the
// API driver. Implementations should resolve the vault and call idx.Ingest().
type IngestAPIFunc func(ctx context.Context, content, source, vault string, tags []string) error

// APIIngestDriver accepts content via the POST /v1/documents API and emits
// ingest events into the shared indexing pipeline.
//
// The HTTP handler calls Ingest() which invokes ingestFunc for the actual
// indexing and emits an IngestEvent on the Events() channel for downstream
// consumers (metrics, logging, future event-driven pipeline steps).
//
// Run(ctx) blocks until context cancellation — the driver maintains no polling
// goroutines since events are pushed synchronously from the HTTP handler.
type APIIngestDriver struct {
	name       string
	logger     *slog.Logger

	events      chan IngestEvent
	ingestFunc  IngestAPIFunc
}

// NewAPIIngestDriver creates an APIIngestDriver.
//
// The ingestFunc is called to index content into the correct vault.
func NewAPIIngestDriver(name string, logger *slog.Logger, ingestFunc IngestAPIFunc) *APIIngestDriver {
	return &APIIngestDriver{
		name:       name,
		logger:     logger.With("driver", name),
		events:     make(chan IngestEvent, 1000),
		ingestFunc: ingestFunc,
	}
}

// Name returns the driver identifier.
func (d *APIIngestDriver) Name() string { return d.name }

// Events returns the channel of ingest events for downstream consumers.
func (d *APIIngestDriver) Events() <-chan IngestEvent { return d.events }

// Ingest sends content through the indexing pipeline. It calls ingestFunc
// synchronously and emits an event on the Events() channel for observability.
func (d *APIIngestDriver) Ingest(ctx context.Context, content, source, vault string, tags []string) error {
	// Call the indexer directly (synchronous, returns any error)
	if err := d.ingestFunc(ctx, content, source, vault, tags); err != nil {
		return err
	}

	// Emit event for downstream consumers (fire-and-forget)
	// Tag keys are vault-namespaced to prevent collision when events from
	// different vaults are processed by the same downstream consumer.
	meta := map[string]string{"vault": vault}
	if len(tags) > 0 {
		for _, t := range tags {
			meta[fmt.Sprintf("%s:%s", vault, t)] = t
		}
	}
	select {
	case d.events <- IngestEvent{
		Action:  ActionAdd,
		Path:    source,
		Content: []byte(content),
		Meta:    meta,
	}:
	default:
		d.logger.Warn("ingest event dropped — channel full", "source", source)
	}

	return nil
}

// Run blocks until ctx is cancelled. The driver has no polling goroutines;
// events are pushed synchronously from the HTTP handler.
func (d *APIIngestDriver) Run(ctx context.Context) error {
	<-ctx.Done()
	d.logger.Info("API ingest driver shutting down")
	close(d.events)
	return nil
}


