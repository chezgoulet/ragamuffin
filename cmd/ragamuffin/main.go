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
	"github.com/chezgoulet/ragamuffin/internal/git"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
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
	var ec *embedding.Client
	if cfg.EmbeddingAPIKey != "" {
		ec = embedding.New(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel)
		logger.Info("embedding client ready", "model", cfg.EmbeddingModel)
	} else {
		logger.Warn("EMBEDDING_API_KEY not set — indexing and /recall disabled")
	}

	// ── Initialize LLM client (optional) ─────────────────────────────────────
	var lm *llm.Client
	if cfg.HasLLM() {
		lm = llm.New(cfg.LLMProvider, cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMTimeout)
		logger.Info("LLM client ready", "model", cfg.LLMModel)
	} else {
		logger.Info("LLM not configured — /ask and semantic conflict audit disabled")
	}

	// ── Build vault indexers ─────────────────────────────────────────────────
	idxManager := indexer.NewManager()
	logPath := ""

	if cfg.IsMultiTenant() {
		// Multi-tenant: one indexer + Qdrant connection per vault
		logger.Info("configuring vault indexers", "count", len(cfg.Vaults))

		var readyChans []chan struct{}
		var idxCancelFuncs []context.CancelFunc

		for name, vc := range cfg.Vaults {
			collectionName := fmt.Sprintf("ragamuffin_%s", name)

			ctxQ, cancelQ := context.WithTimeout(context.Background(), 10*time.Second)
			qc, err := qdrant.New(ctxQ, cfg.QdrantURL, collectionName, 1536)
			cancelQ()
			if err != nil {
				logger.Error("failed to connect to Qdrant for vault", "vault", name, "error", err)
				os.Exit(1)
			}
			defer qc.Close()

			idx := indexer.New(vc.Path, qc, ec, logger.With("vault", name))
			idx.SetChunkMaxTokens(cfg.ChunkMaxTokens)

			if err := idxManager.Add(name, idx, qc); err != nil {
				logger.Error("failed to register vault indexer", "vault", name, "error", err)
				os.Exit(1)
			}

			// Start watcher for this vault
			interval, err := time.ParseDuration(cfg.WatchInterval)
			if err != nil {
				interval = 60 * time.Second
			}
			w := watcher.New(vc.Path, interval, logger.With("vault", name), cfg.WatcherMode)
			events := make(chan watcher.Event, 100)
			watcherDone := make(chan struct{})

			go w.Watch(events, watcherDone)

			// Start indexer for this vault
			idxCtx, idxCancel := context.WithCancel(context.Background())
			idxCancelFuncs = append(idxCancelFuncs, idxCancel)
			initialDone := make(chan struct{})
			readyChans = append(readyChans, initialDone)

			go idx.ProcessEvents(idxCtx, events, initialDone)
			logger.Info("vault indexer started", "vault", name, "path", vc.Path, "collection", collectionName)

			// Use first vault's log path
			if logPath == "" {
				logPath = vc.Path + "/.ragamuffin/logs.db"
			}
		}

		// Wait for all vaults to complete initial indexing
		for _, ch := range readyChans {
			<-ch
		}
		logger.Info("all vaults initial indexing complete")

		// Schedule cleanup on shutdown
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh
			for _, cancel := range idxCancelFuncs {
				cancel()
			}
		}()

	} else {
		// Single-tenant: one indexer, one Qdrant connection
		ctxQ, cancelQ := context.WithTimeout(context.Background(), 10*time.Second)
		qc, err := qdrant.New(ctxQ, cfg.QdrantURL, cfg.QdrantCollection, 1536)
		cancelQ()
		if err != nil {
			logger.Error("failed to connect to Qdrant", "error", err)
			os.Exit(1)
		}
		defer qc.Close()
		logger.Info("qdrant connected", "collection", cfg.QdrantCollection)

		idx := indexer.New(cfg.VaultPath, qc, ec, logger)
		idx.SetChunkMaxTokens(cfg.ChunkMaxTokens)
		logger.Info("chunk max tokens", "limit", cfg.ChunkMaxTokens)

		if err := idxManager.Add("default", idx, qc); err != nil {
			logger.Error("failed to register default indexer", "error", err)
			os.Exit(1)
		}

		// Start watcher
		interval, err := time.ParseDuration(cfg.WatchInterval)
		if err != nil {
			logger.Warn("invalid watch interval, using 60s", "value", cfg.WatchInterval)
			interval = 60 * time.Second
		}
		w := watcher.New(cfg.VaultPath, interval, logger, cfg.WatcherMode)
		events := make(chan watcher.Event, 100)
		watcherDone := make(chan struct{})

		go w.Watch(events, watcherDone)
		logger.Info("watcher started", "interval", interval)

		// Start indexer
		idxCtx, idxCancel := context.WithCancel(context.Background())
		defer idxCancel()
		initialDone := make(chan struct{})

		go idx.ProcessEvents(idxCtx, events, initialDone)
		<-initialDone // Wait for initial indexing to complete
		logger.Info("initial indexing complete")

		logPath = cfg.VaultPath + "/.ragamuffin/logs.db"

		// Single-tenant shutdown: close watcher + cancel indexer on signal
		// httpServer shutdown is handled below (after server creation)
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh
			logger.Info("shutting down watcher/indexer...")
			close(watcherDone)
			idxCancel()
		}()
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

	// ── Initialize log store ──────────────────────────────────────────────────
	logStore, err := logstore.Open(logPath)
	if err != nil {
		logger.Error("failed to open log store", "error", err)
		os.Exit(1)
	}
	defer logStore.Close()
	logger.Info("log store ready", "path", logPath)

	// ── Start HTTP server ────────────────────────────────────────────────────
	// Pass qc = first vault's Qdrant client for backward-compat /health checks
	var qc *qdrant.Client
	if cfg.IsMultiTenant() {
		// Use first vault's client for shared Qdrant health check
		for _, name := range idxManager.VaultNames() {
			qc = idxManager.GetClient(name)
			break
		}
	} else {
		qc = idxManager.GetClient("default")
	}

	srv := server.New(cfg, qc, factsQc, ec, lm, idxManager, gp, rl, nil, logStore, logger)

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

	// Graceful httpServer shutdown (works for single-tenant and multi-tenant)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		httpServer.Shutdown(shutdownCtx)
	}()

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
