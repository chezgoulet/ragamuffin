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
	var lp logPayload
	if err := json.NewDecoder(r.Body).Decode(&lp); err != nil {
		writeError(w, 400, "INVALID_JSON", fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if lp.Agent == "" || lp.Type == "" || lp.Body == "" {
		writeError(w, 400, "INVALID_INPUT", "agent, type, and body are required")
		return
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
