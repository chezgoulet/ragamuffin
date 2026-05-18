package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/git"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/pruner"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	"github.com/chezgoulet/ragamuffin/internal/server"
	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
)

// vaultSetup holds the components created for a single vault during startup.
type vaultSetup struct {
	Qc           qdrant.FactStore
	DoneCh       chan struct{}   // close to signal watcher shutdown
	PrunerEventCh chan watcher.Event // fed events for the pruner
	InitialDone  chan struct{}   // closed when initial indexing completes
}

// buildVault creates a Qdrant client, indexer, watcher (with fan-out), and
// starts the indexer event processor for one vault. Returns the assembled
// components or an error. The caller must defer cancelIdx() to stop the
// indexer goroutine, and use setup.DoneCh for watcher shutdown.
func buildVault(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	name, vaultPath, collectionName string,
	idxManager *indexer.Manager,
	ec embedding.Embedder,
	lm llm.Synthesizer,
	emitter *events.Emitter,
	idxCancelFuncs *[]context.CancelFunc,
	watcherDoneChs *[]chan struct{},
	prunerEventChs *[]chan watcher.Event,
) (*vaultSetup, error) {
	// ── Connect to Qdrant ──────────────────────────────────────────────
	ctxQ, cancelQ := context.WithTimeout(ctx, 10*time.Second)
	qc, err := qdrant.New(ctxQ, cfg.QdrantURL, collectionName, 1536)
	cancelQ()
	if err != nil {
		return nil, fmt.Errorf("qdrant connect for vault %q: %w", name, err)
	}

	// ── Create indexer ─────────────────────────────────────────────────
	l := logger.With("vault", name)
	idx := indexer.New(vaultPath, qc, ec, l)
	idx.SetChunkMaxTokens(cfg.ChunkMaxTokens)
	idx.OnFileEvent(func(action, path string) {
		switch action {
		case "deleted":
			emitter.Emit(events.TypeFileDeleted, events.FileDeletedData{Path: path})
		default:
			emitter.Emit(events.TypeFileChanged, events.FileChangedData{
				Path: path, Action: action,
			})
		}
	})

	if err := idxManager.Add(name, idx, qc); err != nil {
		qc.Close()
		return nil, fmt.Errorf("register vault %q: %w", name, err)
	}

	// ── Start watcher with fan-out ─────────────────────────────────────
	interval, err := time.ParseDuration(cfg.WatchInterval)
	if err != nil {
		interval = 60 * time.Second
	}
	w := watcher.New(vaultPath, interval, l, cfg.WatcherMode)

	rawEvents := make(chan watcher.Event, 100)
	idxEvents := make(chan watcher.Event, 100)
	prunevents := make(chan watcher.Event, 100)
	doneCh := make(chan struct{})
	*watcherDoneChs = append(*watcherDoneChs, doneCh)
	*prunerEventChs = append(*prunerEventChs, prunevents)

	go w.Watch(rawEvents, doneCh)
	go func() {
		for {
			select {
			case e, ok := <-rawEvents:
				if !ok {
					return
				}
				select {
				case idxEvents <- e:
				default:
					l.Warn("indexer event channel full, dropping event")
				}
				select {
				case prunevents <- e:
				default:
					l.Debug("pruner event channel full, dropping event")
				}
			case <-doneCh:
				return
			}
		}
	}()
	l.Info("watcher started", "interval", interval)

	// ── Start indexer event processor ──────────────────────────────────
	idxCtx, idxCancel := context.WithCancel(context.Background())
	*idxCancelFuncs = append(*idxCancelFuncs, idxCancel)
	initialDone := make(chan struct{})

	go idx.ProcessEvents(idxCtx, idxEvents, initialDone)

	return &vaultSetup{
		Qc:            qc,
		DoneCh:        doneCh,
		PrunerEventCh: prunevents,
		InitialDone:   initialDone,
	}, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel(),
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "error", err)
		os.Exit(1)
	}

	// Validate config — fail fast on misconfiguration
	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			logger.Error("config validation failed: " + e)
		}
		os.Exit(1)
	}

	if cfg.IsMultiTenant() {
		logger.Info("multi-tenant mode active", "vaults", len(cfg.Vaults))
	} else {
		logger.Info("single-tenant mode active", "vault", cfg.VaultPath)
	}

	logger.Info("starting ragamuffin", "qdrant", cfg.QdrantURL)

	// ── Connect to Qdrant facts collection ───────────────────────────────────
	ctxFacts, cancelFacts := context.WithTimeout(context.Background(), 10*time.Second)
	factsQc, err := qdrant.New(ctxFacts, cfg.QdrantURL, cfg.FactsCollection, cfg.FactsVectorSize)
	cancelFacts()
	if err != nil {
		logger.Error("failed to connect to facts Qdrant", "error", err)
		os.Exit(1)
	}
	defer factsQc.Close()
	logger.Info("qdrant facts collection ready", "collection", cfg.FactsCollection)

	// ── Initialize embedding client (shared, optional) ──────────────────────
	var ec embedding.Embedder
	if cfg.EmbeddingAPIKey != "" {
		ec = embedding.New(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel)
		logger.Info("embedding client ready", "model", cfg.EmbeddingModel)
	} else {
		logger.Warn("EMBEDDING_API_KEY not set — indexing and /recall disabled")
	}

	// ── Initialize LLM client (shared, optional) ─────────────────────────────
	var lm llm.Synthesizer
	if cfg.HasLLM() {
		lm = llm.New(cfg.LLMProvider, cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMTimeout)
		logger.Info("LLM client ready", "model", cfg.LLMModel)
	} else {
		logger.Info("LLM not configured — /ask and semantic conflict audit disabled")
	}

	// ── Initialize log store ──────────────────────────────────────────────────
	logPath := cfg.VaultPath + "/.ragamuffin/logs.db"
	if cfg.IsMultiTenant() {
		for _, vc := range cfg.Vaults {
			logPath = vc.Path + "/.ragamuffin/logs.db"
			break
		}
	}
	logStore, err := logstore.Open(logPath)
	if err != nil {
		logger.Error("failed to open log store", "error", err)
		os.Exit(1)
	}
	defer logStore.Close()
	logger.Info("log store ready", "path", logPath)

	// ── Initialize event emitter + SSE broker (optional) ─────────────────────
	eventBroker := events.NewBroker()
	emitter := events.NewEmitter(cfg.EventWebhookURL, cfg.VaultPath, logger, logStore, eventBroker)
	if cfg.EventWebhookURL != "" {
		logger.Info("event webhook configured", "url", cfg.EventWebhookURL)
	}

	// ── Build vault indexers ─────────────────────────────────────────────────
	idxManager := indexer.NewManager()
	ctx := context.Background()

	// Collections for shutdown tracking
	var idxCancelFuncs []context.CancelFunc
	var watcherDoneChs []chan struct{}
	var prunerEventChs []chan watcher.Event

	if cfg.IsMultiTenant() {
		logger.Info("configuring vault indexers", "count", len(cfg.Vaults))

		var readyChans []chan struct{}

		for name, vc := range cfg.Vaults {
			collectionName := fmt.Sprintf("ragamuffin_%s", name)
			vlog := logger.With("vault", name)

			setup, err := buildVault(ctx, vlog, cfg, name, vc.Path, collectionName,
				idxManager, ec, lm, emitter,
				&idxCancelFuncs, &watcherDoneChs, &prunerEventChs)
			if err != nil {
				logger.Error("failed to build vault", "vault", name, "error", err)
				os.Exit(1)
			}
			defer setup.Qc.Close()

			// Per-vault LLM client (optional override)
			if vc.HasLLM() {
				provider := vc.LLMProvider
				if provider == "" {
					provider = cfg.LLMProvider
				}
				vlm := llm.New(provider, vc.LLMEndpoint, vc.LLMApiKey, vc.LLMModel, vc.LLMTimeout)
				if vlm != nil {
					idxManager.SetLLM(name, vlm)
					logger.Info("per-vault LLM client configured", "vault", name, "model", vc.LLMModel)
				}
			}

			// Per-vault embedding client (optional override)
			if vc.HasEmbedding() {
				vec := embedding.New(vc.EmbeddingEndpoint, vc.EmbeddingApiKey, vc.EmbeddingModel)
				idxManager.SetEmbedder(name, vec)
				logger.Info("per-vault embedding client configured", "vault", name, "model", vc.EmbeddingModel)
			}

			logger.Info("vault indexer started", "vault", name, "path", vc.Path, "collection", collectionName)
			readyChans = append(readyChans, setup.InitialDone)
		}

		// Wait for all vaults to complete initial indexing
		for _, ch := range readyChans {
			<-ch
		}
		logger.Info("all vaults initial indexing complete")

	} else {
		// Single-tenant: one indexer, one Qdrant connection
		setup, err := buildVault(ctx, logger, cfg, "default", cfg.VaultPath, cfg.QdrantCollection,
			idxManager, ec, lm, emitter,
			&idxCancelFuncs, &watcherDoneChs, &prunerEventChs)
		if err != nil {
			logger.Error("failed to build vault", "error", err)
			os.Exit(1)
		}
		defer setup.Qc.Close()

		// Wait for initial indexing to complete
		<-setup.InitialDone
		logger.Info("initial indexing complete")

	}

	// ── Initialize git provider (optional) ───────────────────────────────────
	var gp git.Provider
	if cfg.HasGit() {
		gp = git.New(cfg.GitProvider, cfg.GitToken, cfg.GitBaseURL)
		logger.Info("git provider ready", "provider", cfg.GitProvider)
	} else {
		logger.Info("git provider not configured — /draft PR mode disabled")
	}

	// ── Initialize rate limiter ──────────────────────────────────────────────
	rl := ratelimit.New(cfg.RateLimitEnabled)
	logger.Info("rate limiter ready", "enabled", cfg.RateLimitEnabled,
		"recall_rpm", cfg.RateLimitRecall, "ask_rpm", cfg.RateLimitAsk,
		"draft_rpm", cfg.RateLimitDraft, "audit_rpm", cfg.RateLimitAudit)

	// ── Pruner (fact lifecycle management) ────────────────────────────────────
	prunerCfg := pruner.DefaultConfig()
	prunerCfg.Enabled = cfg.PrunerEnabled
	prunerCfg.StaleScanInterval = cfg.PrunerStaleInterval
	prunerCfg.ConflictScanInterval = cfg.PrunerConflictInterval
	prunerCfg.SupersedeScanInterval = cfg.PrunerSupersedeInterval
	prunerCfg.StaleDays = cfg.PrunerStaleDays
	prunerCfg.ConflictSampleSize = cfg.PrunerConflictSampleSize
	prunerCfg.LowConfidenceThreshold = cfg.PrunerLowConfidenceThreshold
	prunerCfg.LogScanFn = func(scanName string, dur time.Duration, flagged int, errStr string) {
		body := fmt.Sprintf("scan=%s duration=%s facts_flagged=%d", scanName, dur, flagged)
		if errStr != "" {
			body += " error=" + errStr
		}
		logStore.Append(context.Background(), "pruner", "scan", body, []string{"pruner", scanName, "scan"}, time.Now())
	}
	p := pruner.New(factsQc, nil, ec, lm, logger.With("component", "pruner"), prunerCfg)
	ctxPruner, cancelPruner := context.WithCancel(context.Background())
	defer cancelPruner()
	go p.Run(ctxPruner)
	logger.Info("pruner started", "enabled", prunerCfg.Enabled)

	// Start pruner event processors (watcher fan-out consumers)
	for _, ch := range prunerEventChs {
		go p.ProcessEvents(ctxPruner, ch)
	}
	logger.Info("pruner event processors started", "count", len(prunerEventChs))

	// ── Start HTTP server ────────────────────────────────────────────────────
	// Use first vault's Qdrant client for shared Qdrant health check
	var qc qdrant.FactStore
	if cfg.IsMultiTenant() {
		for _, name := range idxManager.VaultNames() {
			qc = idxManager.GetClient(name)
			break
		}
	} else {
		qc = idxManager.GetClient("default")
	}

	srv := server.New(cfg, qc, factsQc, ec, lm, idxManager, gp, rl, nil, logStore, p, eventBroker, logger)

	authenticator := srv.BuildAuth()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Wrap with auth middleware (public paths like /health and /version bypass)
	authMw := auth.Middleware(authenticator)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Handler:           authMw(srv.Recovery(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // 0 = no timeout — MaxBytesReader + per-handler context timeouts protect; 30s would kill slow /draft uploads
		WriteTimeout:      0, // 0 = no timeout — needed for streaming /v1/snapshot
		IdleTimeout:       60 * time.Second,
	}

	// Unified signal handler — sequences: cancel indexers → close watchers → shutdown HTTP server
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting down...")

		// 1. Cancel all indexers (stopping in-flight indexing)
		for _, cancel := range idxCancelFuncs {
			cancel()
		}

		// 2. Close all watcher event channels (no new events)
		for _, ch := range watcherDoneChs {
			close(ch)
		}

		// 3. Graceful HTTP server drain
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	// Emit startup event
	emitter.Emit(events.TypeServerStarted, events.ServerStartedData{
		Version:   server.Version,
		Commit:    server.Commit,
		GoVersion: server.GoVersion,
		Host:      cfg.Host,
		Port:      cfg.Port,
	})

	logger.Info("listening", "addr", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

func slogLevel() slog.Level {
	switch os.Getenv("RAGAMUFFIN_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
