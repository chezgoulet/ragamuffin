package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/chezgoulet/ragamuffin/internal/events"
)

var eventConnID int64

// handleEvents serves a Server-Sent Events (SSE) stream of vault CloudEvents.
// GET /events — streams events as they happen. Keeps the connection open.
//
// SSE format:
//
//	event: vault.file.changed
//	data: {"specversion":"1.0","type":"vault.file.changed",...}
//
// The subscriber channel has a buffer of 64 events. If a subscriber's buffer
// is full, older events are dropped (slow consumer protection).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.broker == nil {
		writeError(w, 404, "NOT_FOUND", "events not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "INTERNAL", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Subscribe
	id := atomic.AddInt64(&eventConnID, 1)
	ch := make(chan events.CloudEvent, 64)
	s.broker.Subscribe(ch)
	s.logger.Debug("events: SSE client connected", "id", id)

	defer func() {
		s.broker.Unsubscribe(ch)
		close(ch)
		s.logger.Debug("events: SSE client disconnected", "id", id)
	}()

	// Send initial connection event
	_, _ = fmt.Fprintf(w, "event: connected\ndata: {\"id\":%d}\n\n", id)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		}
	}
}
