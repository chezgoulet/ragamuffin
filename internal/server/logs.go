package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/logstore"
)

// ── Log entry types ──────────────────────────────────────────────────────

// logPayload is the JSON body for POST /v1/logs.
type logPayload struct {
	Agent     string   `json:"agent"`
	Type      string   `json:"type"`
	Body      string   `json:"body"`
	Tags      []string `json:"tags,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"` // optional ISO8601
}

// logAppendResponse is returned by POST /v1/logs.
type logAppendResponse struct {
	ID      string `json:"id"`
	Written bool   `json:"written"`
}

// logListResponse is returned by GET /v1/logs.
type logListResponse struct {
	Entries   []logstore.LogEntry `json:"entries"`
	NextToken string              `json:"next_token,omitempty"`
}

// ── POST /v1/logs ────────────────────────────────────────────────────────

func (s *Server) handleLogsPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64 KB for logs
	var lp logPayload
	if err := json.NewDecoder(r.Body).Decode(&lp); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if lp.Agent == "" || lp.Type == "" || lp.Body == "" {
		writeError(w, 400, "INVALID_INPUT", "agent, type, and body are required")
		return
	}

	// Validate agent and type length (indexed SQLite columns)
	if len(lp.Agent) > 256 {
		writeError(w, 400, "AGENT_TOO_LONG", "agent must be <= 256 bytes")
		return
	}
	if len(lp.Type) > 256 {
		writeError(w, 400, "TYPE_TOO_LONG", "type must be <= 256 bytes")
		return
	}

	// Validate body size (separate from MaxBytesReader — the decoded body could be smaller)
	if len(lp.Body) > 64*1024 {
		writeError(w, 400, "BODY_TOO_LARGE", "body must be <= 64 KB")
		return
	}

	// Validate tag count and per-tag length
	if len(lp.Tags) > 50 {
		writeError(w, 400, "TOO_MANY_TAGS", "tags must be <= 50 entries")
		return
	}
	for _, t := range lp.Tags {
		if t == "" {
			writeError(w, 400, "EMPTY_TAG", "tags must not be empty")
			return
		}
		if len(t) > 256 {
			writeError(w, 400, "TAG_TOO_LONG", "each tag must be <= 256 bytes")
			return
		}
	}

	var ts time.Time
	if lp.Timestamp != "" {
		var err error
		ts, err = time.Parse(time.RFC3339Nano, lp.Timestamp)
		if err != nil {
			writeError(w, 400, "INVALID_TIMESTAMP", fmt.Sprintf("invalid timestamp: %v", err))
			return
		}
	}

	id, err := s.logStore.Append(r.Context(), lp.Agent, lp.Type, lp.Body, lp.Tags, ts)
	if err != nil {
		s.log(r.Context()).Error("log append failed", "error", err)
		writeError(w, 500, "APPEND_FAILED", "failed to append log entry")
		return
	}

	writeJSON(w, 201, logAppendResponse{ID: id, Written: true})
}

// ── GET /v1/logs ─────────────────────────────────────────────────────────

func (s *Server) handleLogsGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := 100
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	filter := logstore.Filter{
		Agent:  q.Get("agent"),
		Type:   q.Get("type"),
		Tag:    q.Get("tag"),
		Since:  q.Get("since"),
		Until:  q.Get("until"),
		Before: q.Get("before"),
		Limit:  limit,
	}

	// Validate ISO 8601 timestamps
	if filter.Since != "" {
		if _, err := time.Parse(time.RFC3339, filter.Since); err != nil {
			writeError(w, 400, "INVALID_SINCE", fmt.Sprintf("since must be ISO 8601 (RFC 3339): %v", err))
			return
		}
	}
	if filter.Until != "" {
		if _, err := time.Parse(time.RFC3339, filter.Until); err != nil {
			writeError(w, 400, "INVALID_UNTIL", fmt.Sprintf("until must be ISO 8601 (RFC 3339): %v", err))
			return
		}
	}

	entries, nextToken, err := s.logStore.List(r.Context(), filter)
	if err != nil {
		s.log(r.Context()).Error("log list failed", "error", err)
		writeError(w, 500, "QUERY_FAILED", "failed to query logs")
		return
	}

	writeJSON(w, 200, logListResponse{
		Entries:   entries,
		NextToken: nextToken,
	})
}

// ── Route dispatcher ─────────────────────────────────────────────────────

// handleLogs dispatches to POST/GET /v1/logs based on method.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleLogsPost(w, r)
	case http.MethodGet:
		s.handleLogsGet(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET or POST")
	}
}
