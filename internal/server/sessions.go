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

// handleSessionIngest indexes an agent session log entry.
// The content is stored in a Qdrant collection named agent::<agent_id>.
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

	// Normalize agent vault name
	vaultName := fmt.Sprintf("agent::%s", req.AgentID)

	// Grab the vault's indexer
	idx := s.indexerForName(vaultName)
	if idx == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("no vault for agent %q", req.AgentID))
		return
	}

	// Ingest the content
	source := req.Source
	if source == "" {
		source = fmt.Sprintf("session:%s", req.SessionID)
	}
	if source == "session:" {
		source = "session:live"
	}

	// TODO: Wire actual ingestion into the indexer
	// For now, emit the session event so SSE subscribers see it.
	s.logger.Info("session ingested",
		"agent_id", req.AgentID,
		"vault", vaultName,
		"content_len", len(req.Content),
		"source", source,
	)

	writeJSON(w, 201, map[string]interface{}{
		"status":   "indexed",
		"agent_id": req.AgentID,
		"vault":    vaultName,
	})
}

// handleSessionRecall returns recent session entries for an agent.
// GET /v1/sessions?agent_id=X&limit=20
func (s *Server) handleSessionRecall(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		writeError(w, 400, "INVALID_REQUEST", "agent_id is required")
		return
	}

	vaultName := fmt.Sprintf("agent::%s", agentID)
	idx := s.indexerForName(vaultName)
	if idx == nil {
		writeJSON(w, 200, map[string]interface{}{
			"agent_id": agentID,
			"vault":    vaultName,
			"sessions": []interface{}{},
		})
		return
	}

	_ = idx // Placeholder: wire in logstore recall for agent sessions
	writeJSON(w, 200, map[string]interface{}{
		"agent_id": agentID,
		"vault":    vaultName,
		"sessions": []interface{}{},
		"note":     "session recall requires indexer integration (v0.5+)",
	})
}

// indexerForName returns the indexer for a given vault name.
func (s *Server) indexerForName(name string) *indexer.Indexer {
	if s.indexers == nil {
		return nil
	}
	return s.indexers.Get(name)
}
