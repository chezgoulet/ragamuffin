package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// Config configures the error reporter.
type Config struct {
	// Level is the minimum log level to write (default: info).
	Level slog.Level

	// TelegramBotToken sends error-level events to Telegram.
	TelegramBotToken string
	// TelegramChatID is the chat or channel to receive alerts.
	TelegramChatID string
	// SentryDSN sends error-level events to Sentry-compatible endpoints.
	SentryDSN string
}

// Reporter wraps slog with a multi-handler that fans out to stderr
// and optionally to external services (#814).
type Reporter struct {
	handler slog.Handler
	cfg     Config
	client  *http.Client
	mu      sync.Mutex
}

// New creates a Reporter that writes to stderr (always) and forwards
// error-level events to configured external services.
func New(cfg Config) *Reporter {
	if cfg.Level == 0 {
		cfg.Level = slog.LevelInfo
	}
	return &Reporter{
		handler: slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: cfg.Level,
		}),
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Handler returns the slog.Handler for use with slog.New.
func (r *Reporter) Handler() slog.Handler {
	return &multiHandler{
		stderr: r.handler,
		report: r.reportError,
	}
}

// reportError sends the error to configured external services.
func (r *Reporter) reportError(msg string, attrs map[string]any) {
	if r.cfg.TelegramBotToken != "" && r.cfg.TelegramChatID != "" {
		r.sendTelegram(msg)
	}
	if r.cfg.SentryDSN != "" {
		r.sendSentry(msg, attrs)
	}
}

func (r *Reporter) sendTelegram(msg string) {
	payload, _ := json.Marshal(map[string]string{
		"chat_id": r.cfg.TelegramChatID,
		"text":    fmt.Sprintf("🚨 *Ragamuffin Error*\n```\n%s\n```", msg),
		"parse_mode": "markdown",
	})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", r.cfg.TelegramBotToken)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	r.client.Do(req)
}

func (r *Reporter) sendSentry(msg string, attrs map[string]any) {
	payload, _ := json.Marshal(map[string]any{
		"message":  msg,
		"level":    "error",
		"tags":     attrs,
		"platform": "other",
	})
	url := fmt.Sprintf("%s/api/42/store/", r.cfg.SentryDSN)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	r.client.Do(req)
}

// multiHandler fans out to stderr and the error reporter.
type multiHandler struct {
	stderr slog.Handler
	report func(msg string, attrs map[string]any)
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.stderr.Enabled(ctx, level)
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	// Always write to stderr
	if err := h.stderr.Handle(ctx, r); err != nil {
		return err
	}
	// Forward error-level to external service
	if r.Level >= slog.LevelError {
		attrs := make(map[string]any)
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		h.report(r.Message, attrs)
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &multiHandler{
		stderr: h.stderr.WithAttrs(attrs),
		report: h.report,
	}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	return &multiHandler{
		stderr: h.stderr.WithGroup(name),
		report: h.report,
	}
}
