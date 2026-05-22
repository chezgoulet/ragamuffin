package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chezgoulet/ragamuffin/internal/indexer"
)

// sessionRequest is the JSON body for POST /v1/sessions.
type sessionRequest struct {
	AgentID   string `json:"agent_id"`
	Content   string `json:"content"`
	Source    string `json:"source,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// handleSessions ingests agent session logs into the agent's vault.
// POST /v1/sessions — indexes content under the agent::<id> vault.
// GET  /v1/sessions?agent_id=X — recalls recent session content.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleSessionIngest(w, r)
	case http.MethodGet:
		s.handleSessionRecall(w, r)
	default:
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
	}
}

// handleSessionIngest is a placeholder for session persistence.
// Full implementation is deferred to v0.6 (see ROADMAP.md).
// The route is kept registered to preserve the API surface for forward
// compatibility. Clients should use POST /v1/ingest with vault="agent::<id>"
// instead for v0.5.
func (s *Server) handleSessionIngest(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if req.AgentID == "" || req.Content == "" {
		writeError(w, 400, "INVALID_REQUEST", "agent_id and content are required")
		return
	}

	vaultName := fmt.Sprintf("agent::%s", req.AgentID)

	s.logger.Debug("session ingest skipped (deferred to v0.6)",
		"agent_id", req.AgentID,
		"vault", vaultName,
		"content_len", len(req.Content),
		"source", req.Source,
	)

	writeJSON(w, 503, map[string]interface{}{
		"status":  "unavailable",
		"version": "v0.5",
		"message": "Session persistence is deferred to v0.6. Use POST /v1/ingest with vault=agent::<id> instead.",
	})
}

// handleSessionRecall is a placeholder for session recall.
// Full implementation is deferred to v0.6 (see ROADMAP.md).
// The route is kept registered to preserve the API surface.
func (s *Server) handleSessionRecall(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		writeError(w, 400, "INVALID_REQUEST", "agent_id is required")
		return
	}

	vaultName := fmt.Sprintf("agent::%s", agentID)

	writeJSON(w, 200, map[string]interface{}{
		"status":    "unavailable",
		"version":   "v0.5",
		"agent_id":  agentID,
		"vault":     vaultName,
		"sessions":  []interface{}{},
		"message":   "Session recall is deferred to v0.6. Use GET /v1/recall?query=...&vault=agent::<id> instead.",
	})
}

// indexerForName returns the indexer for a given vault name.
func (s *Server) indexerForName(name string) *indexer.Indexer {
	if s.indexers == nil {
		return nil
	}
	return s.indexers.Get(name)
}
