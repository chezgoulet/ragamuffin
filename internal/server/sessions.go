package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
)

// ── Request/Response types ─────────────────────────────────────────────────────

type createSessionRequest struct {
	AgentID     string `json:"agent_id"`
	Content     string `json:"content,omitempty"`
	Source      string `json:"source,omitempty"`
	Vault       string `json:"vault,omitempty"`
	AutoExtract *bool  `json:"auto_extract,omitempty"`
}

type appendTurnRequest struct {
	Content     string `json:"content"`
	Role        string `json:"role,omitempty"`
	AutoExtract *bool  `json:"auto_extract,omitempty"`
}

type sessionResponse struct {
	ID        string      `json:"id"`
	Vault     string      `json:"vault"`
	AgentID   string      `json:"agent_id"`
	Source    string      `json:"source,omitempty"`
	TurnCount int         `json:"turn_count"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
	Turns     []turnResp  `json:"turns,omitempty"`
}

type turnResp struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

type listSessionsResponse struct {
	Sessions []logstore.Session `json:"sessions"`
	Count    int                `json:"count"`
}

// ── Batch Ingest Request / Response ────────────────────────────────────────────

type batchSessionTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type batchSessionEntry struct {
	AgentID string             `json:"agent_id"`
	Turns   []batchSessionTurn `json:"turns"`
}

type batchSessionRequest struct {
	Vault    string               `json:"vault"`
	Sessions []batchSessionEntry `json:"sessions"`
}

type batchSessionResponse struct {
	Status      string `json:"status"`
	SessionCount int    `json:"session_count"`
	TurnCount   int    `json:"turn_count"`
	ChunkCount  int    `json:"chunk_count"`
}

// ── POST /v1/sessions/batch ────────────────────────────────────────────────────

func (s *Server) handleBatchSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}
	if s.logStore == nil {
		writeError(w, 503, "UNAVAILABLE", "session store not available")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50*1024*1024) // 50 MB limit for batch

	var req batchSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid JSON body: %v", err))
		return
	}

	if len(req.Sessions) == 0 {
		writeError(w, 400, "INVALID_REQUEST", "sessions array is required")
		return
	}

	// Resolve vault name
	vaultName := req.Vault
	if vaultName == "" {
		if s.cfg.IsMultiTenant() {
			writeError(w, 400, "INVALID_REQUEST", "vault is required in multi-tenant mode")
			return
		}
		vaultName = "default"
	}

	// Get or provision the indexer
	idx := s.indexers.Get(vaultName)
	if idx == nil {
		if !s.cfg.AutoProvisionVaults {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("vault %q not found and auto-provisioning is disabled", vaultName))
			return
		}
		if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
			writeError(w, 403, "FORBIDDEN", "write access required to provision vaults")
			return
		}
		idx = s.provisionVault(r.Context(), vaultName)
		if idx == nil {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("vault %q not found and could not be provisioned", vaultName))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	totalSessions := 0
	totalTurns := 0
	totalChunks := 0

	for _, entry := range req.Sessions {
		if entry.AgentID == "" {
			writeError(w, 400, "INVALID_REQUEST", "agent_id is required for each session")
			return
		}
		if len(entry.Turns) == 0 {
			continue // skip empty sessions
		}

		sessionID := uuid.New().String()

		// Resolve vault per session (default to the shared vault)
		sessVault := vaultName

		// Create the session
		_, err := s.logStore.CreateSession(ctx, sessionID, sessVault, entry.AgentID, "")
		if err != nil {
			s.logger.Error("batch session: create failed", "agent_id", entry.AgentID, "error", err)
			continue
		}

		// Append all turns
		var turnContents []string
		for _, turn := range entry.Turns {
			role := turn.Role
			if role == "" {
				role = "user"
			}
			if turn.Content == "" {
				continue
			}

			_, err := s.logStore.AppendTurn(ctx, sessionID, turn.Content, role)
			if err != nil {
				s.logger.Warn("batch session: turn append failed", "session_id", sessionID, "error", err)
				continue
			}
			totalTurns++
			turnContents = append(turnContents, role+": "+turn.Content)
		}

		if len(turnContents) > 0 {
			// Index the conversation as a single source (no LLM call — chunk + embed only)
			source := fmt.Sprintf("sessions/batch/%s/%s", entry.AgentID, sessionID)
			convContent := strings.Join(turnContents, "\n")
			if err := idx.Ingest(ctx, convContent, source, []string{"session", "batch"}); err != nil {
				s.logger.Warn("batch session: ingest failed", "session_id", sessionID, "error", err)
			}
		}

		totalSessions++
	}

	// Get updated chunk count
	_, chunkCount, _, _, _, _ := idx.Stats()
	totalChunks = chunkCount

	writeJSON(w, 200, batchSessionResponse{
		Status:       "ok",
		SessionCount: totalSessions,
		TurnCount:    totalTurns,
		ChunkCount:   totalChunks,
	})
}

// ── Router ─────────────────────────────────────────────────────────────────────

// handleSessions routes to create (POST) or list (GET).
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleSessionCreate(w, r)
	case http.MethodGet:
		s.handleSessionList(w, r)
	default:
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
	}
}

// handleSessionByID routes to get (GET), append turn (POST), or delete (DELETE).
func (s *Server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from path: /v1/sessions/{id} or /v1/sessions/{id}/turns
	path := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	parts := strings.SplitN(path, "/", 2)
	sessionID := parts[0]
	isTurn := len(parts) > 1 && parts[1] == "turns"

	if sessionID == "" {
		writeError(w, 400, "INVALID_REQUEST", "session ID is required")
		return
	}

	if isTurn {
		if r.Method != http.MethodPost {
			writeError(w, 405, "INVALID_REQUEST", "method not allowed; use POST for turns")
			return
		}
		s.handleTurnAppend(w, r, sessionID)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleSessionGet(w, r, sessionID)
	case http.MethodDelete:
		s.handleSessionDelete(w, r, sessionID)
	default:
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
	}
}

// ── Create Session ─────────────────────────────────────────────────────────────

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	if s.logStore == nil {
		writeError(w, 503, "UNAVAILABLE", "session store not available")
		return
	}

	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.AgentID == "" {
		writeError(w, 400, "INVALID_REQUEST", "agent_id is required")
		return
	}

	// Generate UUID v7 session ID
	sessionID := uuid.New().String()

	// Resolve vault
	vault := req.Vault
	if vault == "" {
		vault = fmt.Sprintf("agent::%s", req.AgentID)
	}

	sess, err := s.logStore.CreateSession(r.Context(), sessionID, vault, req.AgentID, req.Source)
	if err != nil {
		s.logger.Error("session create failed", "error", err)
		writeError(w, 500, "INTERNAL", "failed to create session")
		return
	}

	// Register auto_extract preference if set
	if req.AutoExtract != nil && s.extractor != nil {
		s.extractor.SetSessionAutoExtract(sessionID, *req.AutoExtract)
	}

	// Optionally append initial content as first turn
	if req.Content != "" {
		_, err := s.logStore.AppendTurn(r.Context(), sessionID, req.Content, "user")
		if err != nil {
			s.logger.Warn("session create: initial turn append failed", "error", err)
		} else {
			sess.TurnCount = 1
		}
	}

	writeJSON(w, 201, sessionResponse{
		ID:        sess.ID,
		Vault:     sess.Vault,
		AgentID:   sess.AgentID,
		Source:    sess.Source,
		TurnCount: sess.TurnCount,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
	})
}

// ── List Sessions ──────────────────────────────────────────────────────────────

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	if s.logStore == nil {
		writeError(w, 503, "UNAVAILABLE", "session store not available")
		return
	}

	agentID := r.URL.Query().Get("agent_id")
	vault := r.URL.Query().Get("vault")

	// If agent_id is specified but not vault, resolve vault from agent_id
	if agentID != "" && vault == "" {
		vault = fmt.Sprintf("agent::%s", agentID)
	}

	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 100
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	offset := 0
	if offsetStr != "" {
		if n, err := strconv.Atoi(offsetStr); err == nil && n >= 0 {
			offset = n
		}
	}

	sessions, err := s.logStore.ListSessions(r.Context(), vault, limit, offset)
	if err != nil {
		s.logger.Error("session list failed", "error", err)
		writeError(w, 500, "INTERNAL", "failed to list sessions")
		return
	}

	writeJSON(w, 200, listSessionsResponse{
		Sessions: sessions,
		Count:    len(sessions),
	})
}

// ── Get Session ────────────────────────────────────────────────────────────────

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.logStore == nil {
		writeError(w, 503, "UNAVAILABLE", "session store not available")
		return
	}

	turnLimitStr := r.URL.Query().Get("turns")
	turnLimit := 50 // default: last 50 turns
	if turnLimitStr != "" {
		if n, err := strconv.Atoi(turnLimitStr); err == nil && n >= 0 {
			turnLimit = n
		}
	}

	sess, turns, err := s.logStore.GetSession(r.Context(), sessionID, turnLimit)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("session %q not found", sessionID))
			return
		}
		s.logger.Error("session get failed", "error", err)
		writeError(w, 500, "INTERNAL", "failed to get session")
		return
	}

	turnsResp := make([]turnResp, len(turns))
	for i, t := range turns {
		turnsResp[i] = turnResp{
			ID:        t.ID,
			Content:   t.Content,
			Role:      t.Role,
			CreatedAt: t.CreatedAt,
		}
	}

	writeJSON(w, 200, sessionResponse{
		ID:        sess.ID,
		Vault:     sess.Vault,
		AgentID:   sess.AgentID,
		Source:    sess.Source,
		TurnCount: sess.TurnCount,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		Turns:     turnsResp,
	})
}

// ── Append Turn ────────────────────────────────────────────────────────────────

func (s *Server) handleTurnAppend(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.logStore == nil {
		writeError(w, 503, "UNAVAILABLE", "session store not available")
		return
	}

	var req appendTurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.Content == "" {
		writeError(w, 400, "INVALID_REQUEST", "content is required")
		return
	}

	role := req.Role
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "assistant" && role != "system" {
		writeError(w, 400, "INVALID_REQUEST", "role must be user, assistant, or system")
		return
	}

	// Temporarily allow 10 MB for session turns (conversation content can be large)
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

	turn, err := s.logStore.AppendTurn(r.Context(), sessionID, req.Content, role)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("session %q not found", sessionID))
			return
		}
		s.logger.Error("turn append failed", "error", err)
		writeError(w, 500, "INTERNAL", "failed to append turn")
		return
	}

	// Trigger automatic extraction if configured
	extract := false
	if req.AutoExtract != nil {
		extract = *req.AutoExtract
	} else if s.extractor != nil {
		extract = s.extractor.SessionAutoExtract(sessionID)
	}
	if extract && s.extractor != nil && s.extractor.Enabled() {
		go s.extractor.Extract(s.shutdownCtx, sessionID, req.Content, role)
	}

	writeJSON(w, 200, turnResp{
		ID:        turn.ID,
		Content:   turn.Content,
		Role:      turn.Role,
		CreatedAt: turn.CreatedAt,
	})
}

// ── Delete Session ─────────────────────────────────────────────────────────────

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.logStore == nil {
		writeError(w, 503, "UNAVAILABLE", "session store not available")
		return
	}

	if err := s.logStore.DeleteSession(r.Context(), sessionID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, 404, "NOT_FOUND", fmt.Sprintf("session %q not found", sessionID))
			return
		}
		s.logger.Error("session delete failed", "error", err)
		writeError(w, 500, "INTERNAL", "failed to delete session")
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"status": "deleted",
		"id":     sessionID,
	})
}

