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
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	"github.com/chezgoulet/ragamuffin/internal/server"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel(),
	}))
	slog.SetDefault(logger)

	cfg := config.Load()

	// Validate config — fail fast on misconfiguration
	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			logger.Error("config validation failed: " + e)
		}
		os.Exit(1)
	}

	logger.Info("starting ragamuffin", "vault", cfg.VaultPath, "qdrant", cfg.QdrantURL)

	// ── Connect to Qdrant ────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	qc, err := qdrant.New(ctx, cfg.QdrantURL, cfg.QdrantCollection, 1536) // 1536 = text-embedding-3-small dims
	cancel()
	if err != nil {
		logger.Error("failed to connect to Qdrant", "error", err)
		os.Exit(1)
	}
	defer qc.Close()
	logger.Info("qdrant connected", "collection", cfg.QdrantCollection)

	// ── Initialize embedding client (optional) ──────────────────────────────
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
		lm = llm.New(cfg.LLMProvider, cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)
		logger.Info("LLM client ready", "model", cfg.LLMModel)
	} else {
		logger.Info("LLM not configured — /ask and semantic conflict audit disabled")
	}

	// ── Initialize indexer ───────────────────────────────────────────────────
	idx := indexer.New(cfg.VaultPath, qc, ec, logger)
	idx.SetChunkMaxTokens(cfg.ChunkMaxTokens)
	logger.Info("chunk max tokens", "limit", cfg.ChunkMaxTokens)

	// ── Start watcher ────────────────────────────────────────────────────────
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

	// ── Start indexer ─────────────────────────────────────────────────────────
	idxCtx, idxCancel := context.WithCancel(context.Background())
	defer idxCancel()
	initialDone := make(chan struct{})

	go idx.ProcessEvents(idxCtx, events, initialDone)
	<-initialDone // Wait for initial indexing to complete
	logger.Info("initial indexing complete")

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

	// ── Start HTTP server ────────────────────────────────────────────────────
	srv := server.New(cfg, qc, ec, lm, idx, gp, rl, logger)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down...")
		close(watcherDone)
		idxCancel()
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
