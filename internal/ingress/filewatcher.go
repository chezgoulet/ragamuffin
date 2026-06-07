// Package ingress defines the IngressDriver interface and implementations
// for ingesting content into the Ragamuffin semantic memory pipeline.
package ingress

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
)

// ── FileWatcherDriver ────────────────────────────────────────────────────────

// FileWatcherDriver watches a filesystem vault directory and produces ingest
// events for file adds, modifications, and deletions.
//
// It owns the Qdrant client for the vault collection, the file watcher
// (inotify or poll), and the event fan-out that feeds both the indexer
// and the pruner. The indexer and pruner are shared infrastructure wired
// by the caller (main.go).
type FileWatcherDriver struct {
	name       string
	vaultPath  string
	collection string
	qc         qdrant.FactStore
	logger     *slog.Logger

	// Configuration
	watchMode     string
	watchInterval time.Duration
	chunkMaxToken int

	// Event channels — created in New, consumed before Run, populated in Run
	idxEvents    chan watcher.Event // → indexer ProcessEvents
	prunerEvents chan watcher.Event // → pruner ProcessEvents
	ingestEvents chan IngestEvent   // → IngressDriver.Events()
	doneCh       chan struct{}      // close to stop watcher
	rawEvents    chan watcher.Event // from the watcher → fan-out
}

// FileWatcherConfig holds the parameters for creating a FileWatcherDriver.
type FileWatcherConfig struct {
	Name            string
	VaultPath       string
	CollectionName  string
	QdrantURL       string
	ChunkVectorSize uint64
	ChunkMaxTokens  int
	WatcherMode     string
	WatchInterval   time.Duration
	Logger          *slog.Logger
}

// NewFileWatcherDriver creates a FileWatcherDriver, connecting to Qdrant and
// setting up event channels. Call Run() to start the watcher and fan-out.
func NewFileWatcherDriver(ctx context.Context, cfg FileWatcherConfig) (*FileWatcherDriver, error) {
	qc, err := qdrant.NewReconnecting(ctx, cfg.QdrantURL, cfg.CollectionName, cfg.ChunkVectorSize, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("qdrant connect for vault %q: %w", cfg.Name, err)
	}

	d := &FileWatcherDriver{
		name:          cfg.Name,
		vaultPath:     cfg.VaultPath,
		collection:    cfg.CollectionName,
		qc:            qc,
		logger:        cfg.Logger.With("vault", cfg.Name),
		watchMode:     cfg.WatcherMode,
		watchInterval: cfg.WatchInterval,
		chunkMaxToken: cfg.ChunkMaxTokens,

		// Buffered channels so producers never block on slow consumers
		rawEvents:    make(chan watcher.Event, 10000),
		idxEvents:    make(chan watcher.Event, 10000),
		prunerEvents: make(chan watcher.Event, 10000),
		ingestEvents: make(chan IngestEvent, 10000),
		doneCh:       make(chan struct{}),
	}

	return d, nil
}

// Name returns the driver identifier.
func (d *FileWatcherDriver) Name() string { return d.name }

// Events returns the channel of ingest events for the indexer pipeline.
// The channel exists from construction and is closed when Run returns.
func (d *FileWatcherDriver) Events() <-chan IngestEvent { return d.ingestEvents }

// WatcherEvents returns the raw watcher.Event channel for the indexer's
// ProcessEvents method (which expects watcher.Event).
func (d *FileWatcherDriver) WatcherEvents() <-chan watcher.Event { return d.idxEvents }

// PrunerEvents returns the pruner-bound watcher.Event channel for the pruner's
// ProcessEvents method.
func (d *FileWatcherDriver) PrunerEvents() <-chan watcher.Event { return d.prunerEvents }

// DoneCh returns the channel that is closed when the watcher stops.
func (d *FileWatcherDriver) DoneCh() <-chan struct{} { return d.doneCh }

// QdrantClient returns the Qdrant client for this vault.
func (d *FileWatcherDriver) QdrantClient() qdrant.FactStore { return d.qc }

// Close shuts down the Qdrant client. Safe to call multiple times.
func (d *FileWatcherDriver) Close() error { d.qc.Close(); return nil }

// Run starts the file watcher and event fan-out. It blocks until ctx is
// cancelled. The event channels (Events, WatcherEvents, PrunerEvents) are
// populated while Run is active and closed on return.
func (d *FileWatcherDriver) Run(ctx context.Context) error {
	w := watcher.New(d.vaultPath, d.watchInterval, d.logger, d.watchMode)
	d.logger.Info("watcher started", "interval", d.watchInterval)
	go w.Watch(d.rawEvents, d.doneCh)

	// Fan-out goroutine: rawEvents → idxEvents + ingestEvents + prunerEvents
	const fanoutSemCap = 1000
	idxSem := make(chan struct{}, fanoutSemCap)
	pruneSem := make(chan struct{}, fanoutSemCap)
	ingestSem := make(chan struct{}, fanoutSemCap)

	go func() {
		for {
			select {
			case e, ok := <-d.rawEvents:
				if !ok {
					close(d.idxEvents)
					close(d.ingestEvents)
					close(d.prunerEvents)
					return
				}

				// 1. Fan-out to indexer (raw watcher.Event, for ProcessEvents)
				fanoutEvent(e, d.idxEvents, idxSem, fanoutSemCap, d.logger, "indexer")
				// 2. Fan-out via IngressEvent (for the interface / other consumers)
				ie := IngestEvent{
					Action:  IngestAction(e.Action.String()),
					Path:    e.Path,
					AbsPath: e.AbsPath,
				}
				fanoutIngest(ie, d.ingestEvents, ingestSem, fanoutSemCap, d.logger, "ingest")
				// 3. Fan-out to pruner (raw watcher.Event)
				fanoutEvent(e, d.prunerEvents, pruneSem, fanoutSemCap, d.logger, "pruner")

			case <-d.doneCh:
				close(d.idxEvents)
				close(d.ingestEvents)
				close(d.prunerEvents)
				return
			}
		}
	}()

	// Block until context is cancelled
	<-ctx.Done()
	d.logger.Info("file watcher shutting down")
	close(d.doneCh)
	return nil
}

// fanoutEvent tries to send e to ch, or spawns a retry goroutine for up to 30s.
func fanoutEvent(e watcher.Event, ch chan<- watcher.Event, sem chan struct{}, cap int, logger *slog.Logger, label string) {
	select {
	case ch <- e:
	default:
		select {
		case sem <- struct{}{}:
			go func(ev watcher.Event) {
				defer func() { <-sem }()
				select {
				case ch <- ev:
				case <-time.After(30 * time.Second):
					logger.Warn(label+" event dropped after 30s back-pressure", "path", ev.Path, "action", ev.Action)
				}
			}(e)
		default:
			logger.Warn(label+" event dropped — fan-out semaphore full", "path", e.Path, "action", e.Action, "cap", cap)
		}
	}
}

// fanoutIngest tries to send ie to ch, or spawns a retry goroutine for up to 30s.
func fanoutIngest(ie IngestEvent, ch chan<- IngestEvent, sem chan struct{}, cap int, logger *slog.Logger, label string) {
	select {
	case ch <- ie:
	default:
		select {
		case sem <- struct{}{}:
			go func(ev IngestEvent) {
				defer func() { <-sem }()
				select {
				case ch <- ev:
				case <-time.After(30 * time.Second):
					logger.Warn(label+" event dropped after 30s back-pressure", "path", ev.Path, "action", ev.Action)
				}
			}(ie)
		default:
			logger.Warn(label+" event dropped — fan-out semaphore full", "path", ie.Path, "action", ie.Action, "cap", cap)
		}
	}
}
