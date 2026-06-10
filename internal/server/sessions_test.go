package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func testSessionLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newSessionTestServer creates a minimal Server configured for session tests.
// Uses a real in-memory SQLite logstore and a minimal indexer manager.
func newSessionTestServer(t *testing.T, autoProvision bool) (*Server, *logstore.Store) {
	t.Helper()

	store, err := logstore.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory logstore: %v", err)
	}

	mgr := indexer.NewManager()
	// Register a minimal indexer for our test vault so the handler doesn't
	// attempt to auto-provision real Qdrant connections.
	mgr.Add("test_vault", &indexer.Indexer{}, nil)

	srv := &Server{
		logStore: store,
		indexers: mgr,
		cfg: &config.Config{
			AutoProvisionVaults: autoProvision,
			ProceduralEnabled:   false,
		},
		logger: testSessionLogger(t),
	}

	return srv, store
}

// ── POST /v1/sessions ─────────────────────────────────────────────────────────

func TestSessionCreate_Basic(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	body := createSessionRequest{
		AgentID: "test-agent",
		Vault:   "test_vault",
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if resp.AgentID != "test-agent" {
		t.Errorf("expected agent_id 'test-agent', got %q", resp.AgentID)
	}
	if resp.Vault != "test_vault" {
		t.Errorf("expected vault 'test_vault', got %q", resp.Vault)
	}
	if resp.TurnCount != 0 {
		t.Errorf("expected turn_count 0, got %d", resp.TurnCount)
	}
	if resp.CreatedAt == "" {
		t.Error("expected non-empty created_at")
	}
}

func TestSessionCreate_MissingAgentID(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	body := createSessionRequest{Vault: "test_vault"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSessionCreate_WithInitialContent(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	body := createSessionRequest{
		AgentID: "test-agent",
		Vault:   "test_vault",
		Content: "Hello, this is the first turn",
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// With initial content, turn count should be 1
	if resp.TurnCount != 1 {
		t.Errorf("expected turn_count 1 with initial content, got %d", resp.TurnCount)
	}
}

func TestSessionCreate_EmptyVaultDefaultsToAgentPath(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)
	// Override indexers to only have the auto-derived vault pre-registered
	vault := "agent::test-agent"
	mgr := indexer.NewManager()
	mgr.Add(vault, &indexer.Indexer{}, nil)
	srv.indexers = mgr

	body := createSessionRequest{
		AgentID: "test-agent",
		// No vault — defaults to agent::<agentID>
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessions(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the session was created with the derived vault name
	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Vault != vault {
		t.Errorf("expected vault %q, got %q", vault, resp.Vault)
	}

	// Verify via logstore as well
	sess, _, err := sto.GetSession(context.Background(), resp.ID, 1)
	if err != nil {
		t.Fatalf("failed to get session from store: %v", err)
	}
	if sess.Vault != vault {
		t.Errorf("expected stored vault %q, got %q", vault, sess.Vault)
	}
}

func TestSessionCreate_LogStoreUnavailable(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)
	sto.Close()        // close the store so it's unavailable
	srv.logStore = nil // nil triggers the 503 path

	body := createSessionRequest{
		AgentID: "test-agent",
		Vault:   "test_vault",
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessions(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// ── GET /v1/sessions ──────────────────────────────────────────────────────────

func TestSessionList(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)

	// Create two sessions
	for i := 0; i < 2; i++ {
		_, err := sto.CreateSession(context.Background(), fmt.Sprintf("sess-%d", i), "test_vault", "test-agent", "")
		if err != nil {
			t.Fatalf("failed to create session: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions?vault=test_vault", nil)
	w := httptest.NewRecorder()

	srv.handleSessions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp listSessionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Count != 2 {
		t.Errorf("expected 2 sessions, got %d", resp.Count)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("expected 2 session entries, got %d", len(resp.Sessions))
	}
}

func TestSessionList_LogStoreUnavailable(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)
	sto.Close()
	srv.logStore = nil // nil triggers the 503 path

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	w := httptest.NewRecorder()

	srv.handleSessions(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// ── GET /v1/sessions/{id} ─────────────────────────────────────────────────────

func TestSessionGet(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)

	// Create a session
	_, err := sto.CreateSession(context.Background(), "test-sess-1", "test_vault", "test-agent", "")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Append a turn
	_, err = sto.AppendTurn(context.Background(), "test-sess-1", "hello", "user")
	if err != nil {
		t.Fatalf("failed to append turn: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/test-sess-1", nil)
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != "test-sess-1" {
		t.Errorf("expected ID 'test-sess-1', got %q", resp.ID)
	}
	if resp.AgentID != "test-agent" {
		t.Errorf("expected agent_id 'test-agent', got %q", resp.AgentID)
	}
	if len(resp.Turns) != 1 {
		t.Errorf("expected 1 turn, got %d", len(resp.Turns))
	}
	if len(resp.Turns) > 0 && resp.Turns[0].Content != "hello" {
		t.Errorf("expected turn content 'hello', got %q", resp.Turns[0].Content)
	}
}

func TestSessionGet_NotFound(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/nonexistent", nil)
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSessionGet_EmptyID(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	// Path: /v1/sessions/ (empty ID)
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/", nil)
	// handleSessionByID strips the prefix and splits on "/"
	// We need to mock the path to trigger the empty ID check
	// Actually the router strips /v1/sessions/ before calling handleSessionByID
	// Let's just verify by calling with a modified path
	w := httptest.NewRecorder()
	srv.handleSessionByID(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ── POST /v1/sessions/{id}/turns ──────────────────────────────────────────────

func TestSessionAppendTurn(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)

	// Create a session first
	_, err := sto.CreateSession(context.Background(), "turn-test-1", "test_vault", "test-agent", "")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	body := appendTurnRequest{
		Content: "Hello from user",
		Role:    "user",
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/turn-test-1/turns", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp turnResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Content != "Hello from user" {
		t.Errorf("expected content 'Hello from user', got %q", resp.Content)
	}
	if resp.Role != "user" {
		t.Errorf("expected role 'user', got %q", resp.Role)
	}
	if resp.ID == 0 {
		t.Error("expected non-zero turn ID")
	}
}

func TestSessionAppendTurn_EmptyContent(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)

	_, err := sto.CreateSession(context.Background(), "turn-empty", "test_vault", "test-agent", "")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	body := appendTurnRequest{Content: "", Role: "user"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/turn-empty/turns", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSessionAppendTurn_InvalidRole(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)

	_, err := sto.CreateSession(context.Background(), "turn-badrole", "test_vault", "test-agent", "")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	body := appendTurnRequest{
		Content: "hello",
		Role:    "superadmin",
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/turn-badrole/turns", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSessionAppendTurn_SessionNotFound(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	body := appendTurnRequest{
		Content: "hello",
		Role:    "user",
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/nonexistent/turns", bytes.NewReader(b))
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ── DELETE /v1/sessions/{id} ──────────────────────────────────────────────────

func TestSessionDelete(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)

	_, err := sto.CreateSession(context.Background(), "del-sess-1", "test_vault", "test-agent", "")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/del-sess-1", nil)
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "deleted" {
		t.Errorf("expected status 'deleted', got %q", resp["status"])
	}

	// Verify session no longer exists
	_, _, err = sto.GetSession(context.Background(), "del-sess-1", 1)
	if err == nil {
		t.Error("expected error when fetching deleted session")
	}
}

func TestSessionDelete_NotFound(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/nonexistent", nil)
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ── POST /v1/sessions/{id}/finalize ───────────────────────────────────────────

func TestSessionFinalize(t *testing.T) {
	srv, sto := newSessionTestServer(t, false)

	_, err := sto.CreateSession(context.Background(), "final-sess-1", "test_vault", "test-agent", "")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/final-sess-1/finalize", nil)
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "finalized" {
		t.Errorf("expected status 'finalized', got %q", resp["status"])
	}

	// Verify via logstore that the session is finalized
	// (logstore doesn't expose a "finalized" field check directly,
	// but we trust it was called if no error was returned)
}

func TestSessionFinalize_NotFound(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/nonexistent/finalize", nil)
	w := httptest.NewRecorder()

	srv.handleSessionByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ── Method dispatch ───────────────────────────────────────────────────────────

func TestSessionByID_MissingID(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	w := httptest.NewRecorder()

	// Direct call to handleSessionByID with empty path
	srv.handleSessionByID(w, httptest.NewRequest(http.MethodGet, "/v1/sessions/", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty session ID, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSessionByID_MethodNotAllowed(t *testing.T) {
	srv, _ := newSessionTestServer(t, false)

	// PUT is not allowed on base session route
	req := httptest.NewRequest(http.MethodPut, "/v1/sessions/test-sess-1", nil)
	w := httptest.NewRecorder()
	srv.handleSessionByID(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", w.Code, w.Body.String())
	}
}
