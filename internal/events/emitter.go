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

// LogStorer is the subset of logstore.Store needed by Emitter.
type LogStorer interface {
	Append(ctx context.Context, agent, eventType, body string, tags []string, timestamp time.Time) (string, error)
}

// Emitter sends CloudEvents to a configured webhook URL and persists
// them to a logstore if configured. Also broadcasts to SSE subscribers.
type Emitter struct {
	webhookURL string
	source     string
	client     *http.Client
	logger     *slog.Logger
	logStore   LogStorer      // optional — persists events
	broker     *Broker        // optional — SSE fan-out
	closed     bool
	mu         sync.Mutex
	queue      []CloudEvent
}

// NewEmitter creates an Emitter. If webhookURL is empty, Emit is a no-op.
func NewEmitter(webhookURL, source string, logger *slog.Logger, logStore LogStorer, broker *Broker) *Emitter {
	return &Emitter{
		webhookURL: webhookURL,
		source:     source,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger:   logger,
		logStore: logStore,
		broker:   broker,
	}
}

// Emit sends a CloudEvent of the given type with data. Non-blocking —
// delivery is best-effort in a background goroutine.
// If a logStore is configured, the event is also persisted to SQLite.
// If a broker is configured, the event is broadcast to SSE subscribers.
func (e *Emitter) Emit(eventType string, data any) {
	if e == nil {
		return
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	evt := New(eventType, e.source, data)
	e.mu.Unlock()

	// Persist to logstore if configured
	if e.logStore != nil {
		body, _ := evt.MarshalJSON()
		_, err := e.logStore.Append(context.Background(), "system", eventType, string(body), nil, time.Now())
		if err != nil {
			e.logger.Warn("events: logstore append failed", "type", eventType, "error", err)
		}
	}

	// Broadcast to SSE subscribers
	if e.broker != nil {
		e.broker.Publish(evt)
	}

	// Send to webhook if configured
	if e.webhookURL == "" {
		return
	}

	e.post(evt)
}

// EmitSync sends a CloudEvent and blocks until delivery succeeds or fails.
// Also persists to logstore and broadcasts to SSE subscribers.
func (e *Emitter) EmitSync(ctx context.Context, eventType string, data any) error {
	if e == nil {
		return nil
	}

	evt := New(eventType, e.source, data)

	// Persist to logstore
	if e.logStore != nil {
		body, _ := evt.MarshalJSON()
		_, err := e.logStore.Append(ctx, "system", eventType, string(body), nil, time.Now())
		if err != nil {
			e.logger.Warn("events: logstore append failed", "type", eventType, "error", err)
		}
	}

	// Broadcast to SSE subscribers
	if e.broker != nil {
		e.broker.Publish(evt)
	}

	if e.webhookURL == "" {
		return nil
	}

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
