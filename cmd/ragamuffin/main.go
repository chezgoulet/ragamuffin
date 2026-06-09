package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/git"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/ingress"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/pruner"
	"github.com/chezgoulet/ragamuffin/internal/extraction"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	"github.com/chezgoulet/ragamuffin/internal/server"
	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
)

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

	// ── Connect to Qdrant facts collection (with reconnection loop) ──────────
	factsQc, err := qdrant.NewReconnecting(context.Background(), cfg.QdrantURL, cfg.FactsCollection, cfg.FactsVectorSize, logger)
	if err != nil {
		logger.Error("failed to connect to facts Qdrant after retries", "error", err)
		os.Exit(1)
	}
	defer factsQc.Close()
	logger.Info("qdrant facts collection ready", "collection", cfg.FactsCollection)

	// ── Initialize embedding client (shared, optional) ──────────────────────
	var ec embedding.Embedder
	if cfg.EmbeddingAPIKey != "" {
		ec = embedding.New(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingTimeout)
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
	// Single-tenant: store logs under the vault's .ragamuffin directory.
	// Multi-tenant: use a shared parent directory so no single vault owns the log DB.
	logPath := cfg.LogStorePath
	if logPath == "" {
		logPath = cfg.VaultPath + "/.ragamuffin/logs.db"
		if cfg.IsMultiTenant() {
			if len(cfg.Vaults) > 0 {
				for _, vc := range cfg.Vaults {
					logPath = filepath.Dir(vc.Path) + "/.ragamuffin/logs.db"
					break
				}
			} else if cfg.VaultsRoot != "" {
				logPath = cfg.VaultsRoot + "/.ragamuffin/logs.db"
			}
		}
	}
	// Create parent directory if needed
	if dir := filepath.Dir(logPath); dir != "." {
		os.MkdirAll(dir, 0755)
	}
	logStore, err := logstore.Open(logPath)
	if err != nil {
		logger.Error("failed to open log store", "error", err)
		os.Exit(1)
	}
	logStore.SetLogger(logger.With("component", "logstore"))
	defer logStore.Close()

	if err := logStore.IntegrityCheck(); err != nil {
		logger.Warn("logstore integrity check failed", "error", err)
	}

	logger.Info("log store ready", "path", logPath)

	// ── Initialize event emitter + SSE broker (optional) ─────────────────────
	eventBroker := events.NewBroker()
	emitterSource := cfg.VaultPath
	if emitterSource == "" {
		emitterSource = "ragamuffin"
	}
	emitter := events.NewEmitter(cfg.EventWebhookURL, emitterSource, logger, logStore, eventBroker, cfg.EventWebhookEvents)
	if cfg.EventWebhookURL != "" {
		logger.Info("event webhook configured", "url", cfg.EventWebhookURL)
	}

	// ── Build vault indexers via FileWatcherDriver ───────────────────────────
	idxManager := indexer.NewManager()
	ctx := context.Background()

	// Collections for shutdown tracking
	var idxCancelFuncs []context.CancelFunc
	var prunerEventChs []<-chan watcher.Event
	var driverCancelFuncs []context.CancelFunc
	var drivers []ingress.IngressDriver // all IngressDriver instances (for fan-in + lifecycle)
	var initialDoneChs []chan struct{}

	// Shared driver config
	chunkVectorSize := uint64(cfg.EmbeddingDims)
	if cfg.ChunkVectorSize > 0 {
		chunkVectorSize = cfg.ChunkVectorSize
	}
	watchInterval := 60 * time.Second
	if parsed, err := time.ParseDuration(cfg.WatchInterval); err == nil {
		watchInterval = parsed
	}

	if cfg.IsMultiTenant() {
		logger.Info("configuring vault indexers", "count", len(cfg.Vaults))

		for name, vc := range cfg.Vaults {
			collectionName := fmt.Sprintf("ragamuffin_%s", name)
			vlog := logger.With("vault", name)

			drv, err := ingress.NewFileWatcherDriver(ctx, ingress.FileWatcherConfig{
				Name:            name,
				VaultPath:       vc.Path,
				CollectionName:  collectionName,
				QdrantURL:       cfg.QdrantURL,
				ChunkVectorSize: chunkVectorSize,
				ChunkMaxTokens:  cfg.ChunkMaxTokens,
				WatcherMode:     cfg.WatcherMode,
				WatchInterval:   watchInterval,
				Logger:          vlog,
			})
			if err != nil {
				logger.Error("failed to create file watcher driver", "vault", name, "error", err)
				os.Exit(1)
			}
			defer drv.Close()

			// Create indexer (shared infrastructure, not driver-owned)
			idx := indexer.New(vc.Path, name, drv.QdrantClient(), ec, vlog)
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
			if err := idxManager.Add(name, idx, drv.QdrantClient()); err != nil {
				logger.Error("failed to register vault", "vault", name, "error", err)
				os.Exit(1)
			}

			// Wire pruner events
			prunerEventChs = append(prunerEventChs, drv.PrunerEvents())

			// Start indexer event processor
			idxCtx, idxCancel := context.WithCancel(context.Background())
			idxCancelFuncs = append(idxCancelFuncs, idxCancel)
			initialDone := make(chan struct{})
			initialDoneChs = append(initialDoneChs, initialDone)
			go idx.ProcessEvents(idxCtx, drv.WatcherEvents(), initialDone)

			// Start driver event loop (watcher + fan-out)
			drvCtx, drvCancel := context.WithCancel(context.Background())
			driverCancelFuncs = append(driverCancelFuncs, drvCancel)
			go drv.Run(drvCtx)
			drivers = append(drivers, drv)

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
				vec := embedding.New(vc.EmbeddingEndpoint, vc.EmbeddingApiKey, vc.EmbeddingModel, vc.EmbeddingTimeout)
				idxManager.SetEmbedder(name, vec)
				logger.Info("per-vault embedding client configured", "vault", name, "model", vc.EmbeddingModel)
			}

			logger.Info("vault indexer started", "vault", name, "path", vc.Path, "collection", collectionName)
		}

		// Wait for all vaults to complete initial indexing
		for _, ch := range initialDoneChs {
			<-ch
		}
		logger.Info("all vaults initial indexing complete")

	} else {
		// Single-tenant: one driver, one indexer
		drv, err := ingress.NewFileWatcherDriver(ctx, ingress.FileWatcherConfig{
			Name:            "default",
			VaultPath:       cfg.VaultPath,
			CollectionName:  cfg.QdrantCollection,
			QdrantURL:       cfg.QdrantURL,
			ChunkVectorSize: chunkVectorSize,
			ChunkMaxTokens:  cfg.ChunkMaxTokens,
			WatcherMode:     cfg.WatcherMode,
			WatchInterval:   watchInterval,
			Logger:          logger,
		})
		if err != nil {
			logger.Error("failed to create file watcher driver", "error", err)
			os.Exit(1)
		}
		defer drv.Close()

		// Create indexer
		idx := indexer.New(cfg.VaultPath, "default", drv.QdrantClient(), ec, logger)
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
		if err := idxManager.Add("default", idx, drv.QdrantClient()); err != nil {
			logger.Error("failed to register vault", "error", err)
			os.Exit(1)
		}

		// Wire pruner events
		prunerEventChs = append(prunerEventChs, drv.PrunerEvents())

		// Start indexer event processor
		idxCtx, idxCancel := context.WithCancel(context.Background())
		idxCancelFuncs = append(idxCancelFuncs, idxCancel)
		initialDone := make(chan struct{})
		initialDoneChs = append(initialDoneChs, initialDone)
		go idx.ProcessEvents(idxCtx, drv.WatcherEvents(), initialDone)

		// Start driver event loop (watcher + fan-out)
		drvCtx, drvCancel := context.WithCancel(context.Background())
		driverCancelFuncs = append(driverCancelFuncs, drvCancel)
		go drv.Run(drvCtx)
		drivers = append(drivers, drv)

		// Wait for initial indexing to complete
		<-initialDone
		logger.Info("initial indexing complete")

	}

	// ── Wire link index writer to all indexers ─────────────────────────────
	// Only if logStore is available (it always is at this point).
	if logStore != nil {
		idxManager.ForEach(func(name string, idx *indexer.Indexer) {
			idx.SetLinkWriter(logStore)
		})
		logger.Info("link index writer wired to all vaults")
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
	prunerCfg.SourceStaleScanInterval = cfg.PrunerSourceStaleInterval
	prunerCfg.ExpiredScanInterval = cfg.PrunerExpiredInterval
	prunerCfg.StaleDays = cfg.PrunerStaleDays
	prunerCfg.ConflictSampleSize = cfg.PrunerConflictSampleSize
	prunerCfg.ConflictThreshold = cfg.PrunerConflictThreshold
	prunerCfg.LowConfidenceThreshold = cfg.PrunerLowConfidenceThreshold
	prunerCfg.ImportanceThreshold = cfg.PrunerImportanceThreshold
	prunerCfg.ReembedScanInterval = cfg.PrunerReembedInterval
	prunerCfg.LogScanFn = func(scanName string, dur time.Duration, flagged int, errStr string) {
		body := fmt.Sprintf("scan=%s duration=%s facts_flagged=%d", scanName, dur, flagged)
		if errStr != "" {
			body += " error=" + errStr
		}
		logStore.Append(context.Background(), "pruner", "scan", body, []string{"pruner", scanName, "scan"}, time.Now())

		// Emit fact lifecycle event
		emitter.Emit(events.TypePrunerComplete, events.PrunerCompleteData{
			ScanName: scanName,
			Duration: dur.String(),
			Flagged:  flagged,
		})
	}
	prunerCfg.FlagCallback = func(factKey, reason string) {
		emitter.Emit(events.TypeFactFlagged, events.FactFlaggedData{
			Key:    factKey,
			Reason: reason,
		})
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

	// ── Logstore periodic pruning ────────────────────────────────────────────
	if cfg.LogstoreMaxRows > 0 {
		// Run prune immediately, then every hour
		go func() {
			ctxPrune, cancelPrune := context.WithCancel(context.Background())
			defer cancelPrune()

			// Initial prune at startup
			if deleted, err := logStore.Prune(ctxPrune, cfg.LogstoreMaxRows); err != nil {
				logger.Warn("logstore initial prune failed", "error", err)
			} else if deleted > 0 {
				logger.Info("logstore initial prune complete", "deleted", deleted)
			}

			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					deleted, err := logStore.Prune(ctxPrune, cfg.LogstoreMaxRows)
					if err != nil {
						logger.Warn("logstore prune failed", "error", err)
					} else if deleted > 0 {
						logger.Info("logstore prune complete", "deleted", deleted)
					}
				case <-ctxPrune.Done():
					return
				}
			}
		}()
	}

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
	if qc == nil {
		qc = factsQc // fallback to facts client when no vaults configured
	}

	extractionCfg := extraction.DefaultConfig()
	extractionCfg.Enabled = cfg.ExtractEnabled
	extractionCfg.Window = cfg.ExtractWindow
	extractionCfg.MaxConfidence = cfg.ExtractMaxConfidence
	extractionCfg.DedupThreshold = cfg.ExtractDedupThreshold
	extractionCfg.Concurrency = cfg.ExtractConcurrency
	extractionCfg.PerSessionCooldown = cfg.ExtractPerSessionCooldown
	ext := extraction.New(extractionCfg, lm, ec, factsQc, logger)
	// Wire the recent-turns lookup so the extractor can build context windows
	ext.RecentTurnsFn = func(ctx context.Context, sessionID string, n int) ([]extraction.TurnEntry, error) {
		_, turns, err := logStore.GetSession(ctx, sessionID, n)
		if err != nil {
			return nil, err
		}
		entries := make([]extraction.TurnEntry, len(turns))
		for i, t := range turns {
			entries[i] = extraction.TurnEntry{Content: t.Content, Role: t.Role}
		}
		return entries, nil
	}
	ext.SetEmitter(emitter)

	// ── API Ingest Driver ────────────────────────────────────────────────────
	apiDriver := ingress.NewAPIIngestDriver(
		"api-ingest",
		logger,
		func(ctx context.Context, content, source, vault string, tags []string) error {
			idx := idxManager.Get(vault)
			if idx == nil {
				return fmt.Errorf("vault %q not found and auto-provision not available via driver", vault)
			}
			return idx.Ingest(ctx, content, source, tags, nil)
		},
	)
	drvCtxAPI, drvCancelAPI := context.WithCancel(context.Background())
	driverCancelFuncs = append(driverCancelFuncs, drvCancelAPI)
	go apiDriver.Run(drvCtxAPI)
	drivers = append(drivers, apiDriver)

	// ── Driver Event Fan-In ────────────────────────────────────────────────────
	// Select across all IngressDriver Events() channels for observability.
	// In v0.9, this is primarily a monitoring/logging fan-in that consumes
	// events emitted by all drivers. Future iterations may feed these events
	// into a shared indexer pipeline.
	go func() {
		cases := make([]reflect.SelectCase, len(drivers))
		drvNames := make([]string, len(drivers))
		for i, drv := range drivers {
			cases[i] = reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(drv.Events()),
			}
			drvNames[i] = drv.Name()
		}
		for {
			chosen, recv, ok := reflect.Select(cases)
			if !ok {
				continue // channel was closed — driver is done
			}
			evt, ok := recv.Interface().(ingress.IngestEvent)
			if !ok {
				continue
			}
			logger.Debug("ingress event",
				"driver", drvNames[chosen],
				"action", evt.Action,
				"path", evt.Path,
			)
		}
	}()

	srv := server.New(cfg, qc, factsQc, ec, lm, idxManager, gp, rl, nil, logStore, p, emitter, eventBroker, logger, ext, apiDriver)

	// ── Snapshot restore detection ───────────────────────────────────────
	ctxCheck, cancelCheck := context.WithTimeout(context.Background(), 30*time.Second)
	restoreDetected, affected, err := srv.RestoreConsistencyCheck(ctxCheck, cfg.RestoreMismatchThreshold)
	cancelCheck()
	if err != nil {
		logger.Warn("restore consistency check failed", "error", err)
	} else if restoreDetected {
		logger.Warn("possible snapshot restore detected", "affected_vaults", affected)
		for _, v := range affected {
			logger.Info("re-indexing vault due to snapshot restore", "vault", v)
			idxManager.Reindex(v)
		}
	}

	authenticator := srv.BuildAuth()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Wrap with auth middleware (public paths like /health and /version bypass)
	authMw := auth.Middleware(authenticator)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Handler:           authMw(srv.Recovery(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // per-endpoint TimeoutHandler protects long uploads (#414)
		WriteTimeout:      0,                 // 0 = no timeout — streaming /v1/snapshot and SSE need unbounded writes
		IdleTimeout:       60 * time.Second,
	}

	// Unified signal handler — sequences: cancel server goroutines → cancel indexers → close watchers → shutdown HTTP server
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting down...")

		// 0. Cancel server background goroutines first (#420)
		srv.Shutdown()

		// 1. Flush SQLite pending writes
		if logStore != nil {
			if err := logStore.Flush(); err != nil {
				logger.Warn("logstore flush failed", "error", err)
			}
			logStore.Close()
		}

		// 2. Cancel all indexers (stopping in-flight indexing)
		for _, cancel := range idxCancelFuncs {
			cancel()
		}

		// 3. Stop all drivers (cancels watcher + fan-out goroutines)
		for _, cancel := range driverCancelFuncs {
			cancel()
		}

		// 4. Graceful HTTP server drain
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
