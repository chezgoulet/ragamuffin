package events

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"log/slog"
)

// Emitter sends CloudEvents to a configured webhook URL.
type Emitter struct {
	webhookURL string
	source     string
	client     *http.Client
	logger     *slog.Logger
	closed     bool
	mu         sync.Mutex
	queue      []CloudEvent
}

// NewEmitter creates an Emitter. If webhookURL is empty, Emit is a no-op.
func NewEmitter(webhookURL, source string, logger *slog.Logger) *Emitter {
	return &Emitter{
		webhookURL: webhookURL,
		source:     source,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// Emit sends a CloudEvent of the given type with data. Non-blocking —
// delivery is best-effort in a background goroutine.
func (e *Emitter) Emit(eventType string, data interface{}) {
	if e == nil || e.webhookURL == "" {
		return // no-op when not configured
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	evt := New(eventType, e.source, data)
	e.mu.Unlock()

	e.post(evt)
}

// EmitSync sends a CloudEvent and blocks until delivery succeeds or fails.
// Used for startup events where we want to log the result.
func (e *Emitter) EmitSync(ctx context.Context, eventType string, data interface{}) error {
	if e == nil || e.webhookURL == "" {
		return nil
	}

	evt := New(eventType, e.source, data)
	return e.postSync(ctx, evt)
}

// Close shuts down the emitter.
func (e *Emitter) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
}

func (e *Emitter) post(evt CloudEvent) {
	body, err := evt.MarshalJSON()
	if err != nil {
		e.logger.Warn("events: failed to marshal event", "type", evt.Type, "error", err)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.webhookURL, bytes.NewReader(body))
		if err != nil {
			e.logger.Warn("events: failed to create request", "type", evt.Type, "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/cloudevents+json")

		resp, err := e.client.Do(req)
		if err != nil {
			e.logger.Warn("events: delivery failed", "type", evt.Type, "error", err)
			return
		}
		resp.Body.Close()

		if resp.StatusCode >= 300 {
			e.logger.Warn("events: non-2xx response", "type", evt.Type, "status", resp.StatusCode)
		}
	}()
}

func (e *Emitter) postSync(ctx context.Context, evt CloudEvent) error {
	body, err := evt.MarshalJSON()
	if err != nil {
		return fmt.Errorf("events: marshal %s: %w", evt.Type, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("events: request %s: %w", evt.Type, err)
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("events: delivery %s: %w", evt.Type, err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("events: %s returned %d", evt.Type, resp.StatusCode)
	}
	return nil
}
