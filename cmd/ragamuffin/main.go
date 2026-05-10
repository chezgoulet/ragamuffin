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
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/server"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel(),
	}))
	slog.SetDefault(logger)

	cfg := config.Load()
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

	// ── Initialize embedding client ──────────────────────────────────────────
	ec := embedding.New(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel)
	logger.Info("embedding client ready", "model", cfg.EmbeddingModel)

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

	// ── Start watcher ────────────────────────────────────────────────────────
	interval, err := time.ParseDuration(cfg.WatchInterval)
	if err != nil {
		logger.Warn("invalid watch interval, using 60s", "value", cfg.WatchInterval)
		interval = 60 * time.Second
	}
	w := watcher.New(cfg.VaultPath, interval, logger)
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

	// ── Start HTTP server ────────────────────────────────────────────────────
	srv := server.New(cfg, qc, ec, lm, idx, logger)
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
