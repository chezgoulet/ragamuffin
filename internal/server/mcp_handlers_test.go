package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func testMCPLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newMCPTestServer creates a minimal Server for testing MCP adapters, with
// an in-memory logstore so session CRUD operations work end-to-end.
func newMCPTestServer(t *testing.T) *Server {
	t.Helper()

	sto, err := logstore.Open(":memory:")
	if err != nil {
		t.Fatalf("open in-memory logstore: %v", err)
	}
	t.Cleanup(func() { sto.Close() })

	srv := &Server{
		cfg:      minimalConfig(),
		facts:    &conversationMockStore{}, // satisfies qdrant.FactStore, all methods return nil/0
		logger:   testMCPLogger(t),
		logStore: sto,
		embedder: &mockEmbedder{},
		qdrant:   &conversationMockStore{},
		indexers: indexer.NewManager(),
	}
	// Add a no-op indexer for "test-vault" so doStore doesn't
	// trigger provisionVault (which would try to connect to Qdrant).
	idx := indexer.New("/tmp/test-vault", "test-vault", nil, nil, srv.logger)
	if err := srv.indexers.Add("test-vault", idx, nil); err != nil {
		t.Fatalf("add test indexer: %v", err)
	}
	return srv
}

func minimalConfig() *config.Config {
	return &config.Config{
		FactsCollection:     "test_facts",
		FactsVectorSize:     4,
		AutoProvisionVaults: false,
	}
}

// ── Tool definitions ──────────────────────────────────────────────────────────

func TestMCPTools_Definitions(t *testing.T) {
	srv := newMCPTestServer(t)
	tools := srv.mcpTools()

	expectedNames := []string{
		"ragamuffin_recall",
		"ragamuffin_ask",
		"ragamuffin_store",
		"ragamuffin_draft",
		"ragamuffin_facts",
		"ragamuffin_audit",
		"ragamuffin_graph",
		"ragamuffin_stats",
		"ragamuffin_session_create",
		"ragamuffin_session_get",
		"ragamuffin_session_list",
		"ragamuffin_get_chunk",
		"ragamuffin_turn_append",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d:\n%v", len(expectedNames), len(tools), toolNames(tools))
	}

	for i, name := range expectedNames {
		if i < len(tools) && tools[i].Name != name {
			t.Errorf("expected tool %d to be %q, got %q", i, name, tools[i].Name)
		}
	}

	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("found tool with empty name")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has nil input schema", tool.Name)
		}
	}
}

func TestMCPTools_RequiredFields(t *testing.T) {
	srv := newMCPTestServer(t)
	tools := srv.mcpTools()

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			schema := tool.InputSchema

			if schema["type"] != "object" {
				t.Errorf("expected input schema type 'object', got %v", schema["type"])
			}
			if _, has := schema["properties"]; !has {
				t.Errorf("missing properties in input schema for %q", tool.Name)
			}
		})
	}
}

func toolNames(tools []mcp.ToolDefinition) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

// ── Dispatch routing ──────────────────────────────────────────────────────────

func TestMCPDispatch_RoutesCorrectly(t *testing.T) {
	srv := newMCPTestServer(t)

	tests := []struct {
		toolName string
		wantErr  bool
		errMsg   string
	}{
		{"ragamuffin_recall", true, "query is required"},
		{"ragamuffin_ask", true, "query is required"},
		{"ragamuffin_store", true, "content is required"},
		{"ragamuffin_draft", true, "title is required"},
		{"ragamuffin_facts", true, "operation is required: list or upsert"},
		{"ragamuffin_audit", false, ""},
		{"ragamuffin_graph", true, `vault "default" not found`},
		{"ragamuffin_stats", false, ""},
		{"ragamuffin_session_create", true, "agent_id is required"},
		{"ragamuffin_session_get", true, "session_id is required"},
		{"ragamuffin_session_list", false, ""},
		{"ragamuffin_get_chunk", true, "chunk_id is required"},
		{"ragamuffin_turn_append", true, "session_id is required"},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			args := map[string]interface{}{}
			_, err := srv.mcpDispatch(context.Background(), tt.toolName, args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for tool %q, got nil", tt.toolName)
				} else if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error for tool %q: %v", tt.toolName, err)
			}
		})
	}
}

func TestMCPDispatch_UnknownTool(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "nonexistent_tool", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if err.Error() != "unknown tool: nonexistent_tool" {
		t.Errorf("expected 'unknown tool: nonexistent_tool', got %q", err.Error())
	}
}

// ── mcpVaultContext ───────────────────────────────────────────────────────────

func TestMCPVaultContext_WithVault(t *testing.T) {
	srv := newMCPTestServer(t)

	args := map[string]interface{}{
		"vault": "my-vault",
	}
	ctx := srv.mcpVaultContext(context.Background(), args)

	vault := vaultFromContext(ctx)
	if vault == "" {
		t.Error("expected non-empty vault from context")
	}
	if vault != "my-vault" {
		t.Errorf("expected vault 'my-vault', got %q", vault)
	}
}

func TestMCPVaultContext_EmptyVault(t *testing.T) {
	srv := newMCPTestServer(t)
	ctx := srv.mcpVaultContext(context.Background(), map[string]interface{}{})
	vault := vaultFromContext(ctx)
	if vault != "" {
		t.Errorf("expected empty vault, got %q", vault)
	}
}

// ── Security regression: Finding 3 — MCP cross-vault scope (#695) ───────────

// TestMCPDispatch_ScopeDenied checks that a scoped API key with access only
// to vault "allowed-vault" cannot access vault "test-vault" via MCP tool args.
func TestMCPDispatch_ScopeDenied(t *testing.T) {
	srv := newMCPTestServer(t)
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		Access: []string{"read"},
		Vaults: []string{"allowed-vault"},
	})

	// Try to mcpRecall against "test-vault" — should be denied by scope
	_, err := srv.mcpDispatch(ctx, "ragamuffin_recall", map[string]interface{}{
		"query": "test",
		"vault": "test-vault",
	})
	if err == nil {
		t.Fatal("expected scope error for unauthorized vault, got nil")
	}
	if !strings.Contains(err.Error(), "denied by key scope") {
		t.Errorf("expected 'denied by key scope' error, got %q", err.Error())
	}
}

// TestMCPDispatch_ScopeAllowed checks that a scoped key CAN access its
// authorized vault via MCP.
func TestMCPDispatch_ScopeAllowed(t *testing.T) {
	srv := newMCPTestServer(t)
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		Access: []string{"read"},
		Vaults: []string{"test-vault"},
	})

	// The test-vault exists (added in newMCPTestServer), so this should
	// proceed past the scope check to the actual handler (which will fail
	// on missing query field). We just want to confirm no scope denial.
	_, err := srv.mcpDispatch(ctx, "ragamuffin_recall", map[string]interface{}{
		"query": "test",
		"vault": "test-vault",
	})
	// Should NOT get scope-denial error. Actual handler may return different error.
	if err != nil && strings.Contains(err.Error(), "denied by key scope") {
		t.Fatalf("unexpected scope denial: %v", err)
	}
}

// TestMCPDispatch_UnscopedClaimsAlwaysAccess checks that claims without
// vault restrictions (Vaults==nil) can access any vault via MCP.
func TestMCPDispatch_UnscopedClaimsAlwaysAccess(t *testing.T) {
	srv := newMCPTestServer(t)
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		Access: []string{"read"},
		// Vaults is nil = unrestricted
	})

	_, err := srv.mcpDispatch(ctx, "ragamuffin_recall", map[string]interface{}{
		"query": "test",
		"vault": "test-vault",
	})
	if err != nil && strings.Contains(err.Error(), "denied by key scope") {
		t.Fatalf("unexpected scope denial for unrestricted claims: %v", err)
	}
}

// ── Adapter: mcpRecall ────────────────────────────────────────────────────────

func TestMCPRecall_MissingQuery(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_recall", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if err.Error() != "query is required" {
		t.Errorf("expected 'query is required', got %q", err.Error())
	}
}

func TestMCPRecall_InvalidDetail(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_recall", map[string]interface{}{
		"query":  "test",
		"detail": "l3",
	})
	if err == nil {
		t.Fatal("expected error for invalid detail")
	}
	if err.Error() != "detail must be one of: l0, l1, l2" {
		t.Errorf("expected detail validation error, got %q", err.Error())
	}
}

// ── Adapter: mcpAsk ───────────────────────────────────────────────────────────

func TestMCPAsk_MissingQuery(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_ask", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if err.Error() != "query is required" {
		t.Errorf("expected 'query is required', got %q", err.Error())
	}
}

func TestMCPAsk_DefaultMode(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_ask", map[string]interface{}{
		"query": "test question",
	})
	if err == nil {
		t.Fatal("expected error from doAsk (no LLM configured)")
	}
	if err.Error() == "query is required" {
		t.Fatal("unexpected validation error after query was provided")
	}
}

// ── Adapter: mcpStore ─────────────────────────────────────────────────────────

func TestMCPStore_ArgsPassThrough(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpStore(context.Background(), map[string]interface{}{
		"content": "test content",
		"source":  "test source",
		"vault":   "test-vault",
	})
	if err == nil {
		t.Fatal("expected error — needs embedder on indexer")
	}
	if err.Error() == "content is required" || err.Error() == "source is required" {
		t.Errorf("args should pass validation, got: %v", err)
	}
}

// ── Adapter: mcpFacts ─────────────────────────────────────────────────────────

func TestMCPFacts_MissingOperation(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_facts", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing operation")
	}
	if err.Error() != "operation is required: list or upsert" {
		t.Errorf("expected operation validation error, got %q", err.Error())
	}
}

func TestMCPFacts_InvalidOperation(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_facts", map[string]interface{}{
		"operation": "delete",
	})
	if err == nil {
		t.Fatal("expected error for invalid operation")
	}
	if err.Error() != `unknown operation: "delete" (expected list or upsert)` {
		t.Errorf("expected operation validation error, got %q", err.Error())
	}
}

func TestMCPFacts_List_DefaultLimit(t *testing.T) {
	srv := newMCPTestServer(t)
	result, err := srv.mcpDispatch(context.Background(), "ragamuffin_facts", map[string]interface{}{
		"operation": "list",
	})
	if err != nil {
		if err.Error() == "limit is required" {
			t.Error("limit should default to 100 without argument")
		}
		return
	}
	resp, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	facts, _ := resp["facts"].([]interface{})
	if len(facts) != 0 {
		t.Errorf("expected empty facts list, got %d facts", len(facts))
	}
}

// ── Adapter: mcpAudit ─────────────────────────────────────────────────────────

func TestMCPAudit_SucceedsWithDefaults(t *testing.T) {
	srv := newMCPTestServer(t)
	result, err := srv.mcpDispatch(context.Background(), "ragamuffin_audit", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	if _, has := resp["checks_run"]; !has {
		t.Error("expected checks_run in response")
	}
	if _, has := resp["stale_files"]; !has {
		t.Error("expected stale_files in response")
	}
}

// ── Adapter: mcpGraph ─────────────────────────────────────────────────────────

func TestMCPGraph_DefaultVault(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_graph", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error — needs real vault/indexer")
	}
	if err.Error() != `vault "default" not found` {
		t.Errorf("expected vault not found error, got %q", err.Error())
	}
}

// ── Adapter: mcpDraft ─────────────────────────────────────────────────────────

func TestMCPDraft_ArgsPassThrough(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_draft", map[string]interface{}{
		"title":       "Test Doc",
		"content":     "Hello world",
		"target_path": "test.md",
		"mode":        "direct",
	})
	if err == nil {
		t.Fatal("expected error — needs vault path")
	}
	if err.Error() == "title is required" || err.Error() == "target_path is required" {
		t.Error("args should pass validation with required fields")
	}
}

// ── Adapter: mcpSessionCreate ─────────────────────────────────────────────────

func TestMCPSessionCreate_MissingArgs(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_session_create", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing args")
	}
	if err.Error() != "agent_id is required" {
		t.Errorf("expected 'agent_id is required', got %q", err.Error())
	}
}

// ── Adapter: mcpSessionGet ────────────────────────────────────────────────────

func TestMCPSessionGet_MissingSessionID(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_session_get", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing session_id")
	}
	if err.Error() != "session_id is required" {
		t.Errorf("expected 'session_id is required', got %q", err.Error())
	}
}

// ── Adapter: mcpTurnAppend ────────────────────────────────────────────────────

func TestMCPTurnAppend_MissingRequired(t *testing.T) {
	srv := newMCPTestServer(t)

	tests := []struct {
		name   string
		args   map[string]interface{}
		errMsg string
	}{
		{
			"missing session_id",
			map[string]interface{}{"content": "hello"},
			"session_id is required",
		},
		{
			"missing content",
			map[string]interface{}{"session_id": "sess-1"},
			"content is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.mcpDispatch(context.Background(), "ragamuffin_turn_append", tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if err.Error() != tt.errMsg {
				t.Errorf("expected %q, got %q", tt.errMsg, err.Error())
			}
		})
	}
}

// ── Adapter: mcpGetChunk ──────────────────────────────────────────────────────

func TestMCPGetChunk_MissingChunkID(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_get_chunk", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing chunk_id")
	}
	if err.Error() != "chunk_id is required" {
		t.Errorf("expected 'chunk_id is required', got %q", err.Error())
	}
}

// ── HTTP integration ─────────────────────────────────────────────────────────

func TestMCPHandler_ServeHTTP_POST(t *testing.T) {
	srv := newMCPTestServer(t)
	srv.mcpHandler = mcp.New(srv.mcpTools(), srv.mcpDispatch, srv.logger, "1.0.0")

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.mcpHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp mcp.JSONRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocol v2024-11-05, got %v", result["protocolVersion"])
	}
}

func TestMCPHandler_ServeHTTP_MethodNotAllowed(t *testing.T) {
	srv := newMCPTestServer(t)
	srv.mcpHandler = mcp.New(srv.mcpTools(), srv.mcpDispatch, srv.logger, "1.0.0")

	req := httptest.NewRequest(http.MethodPut, "/mcp", nil)
	w := httptest.NewRecorder()
	srv.mcpHandler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestMCPHandler_ToolsList(t *testing.T) {
	srv := newMCPTestServer(t)
	srv.mcpHandler = mcp.New(srv.mcpTools(), srv.mcpDispatch, srv.logger, "1.0.0")

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.mcpHandler.ServeHTTP(w, req)

	var resp mcp.JSONRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	tools, ok := result["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		t.Fatal("expected non-empty tools list")
	}
	if len(tools) != 13 {
		t.Errorf("expected 13 tools, got %d", len(tools))
	}
}

// ── Tool list via mcp.Handler bridge ──────────────────────────────────────────

func TestMCPTools_JSONMarshal(t *testing.T) {
	srv := newMCPTestServer(t)
	tools := srv.mcpTools()

	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}

	var decoded []mcp.ToolDefinition
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}

	if len(decoded) != len(tools) {
		t.Errorf("expected %d tools, got %d after marshal/unmarshal", len(tools), len(decoded))
	}
}

func TestMCPTools_InputSchemaJSON(t *testing.T) {
	srv := newMCPTestServer(t)
	tools := srv.mcpTools()

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			data, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshal input schema for %q: %v", tool.Name, err)
			}

			var decoded map[string]interface{}
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal input schema for %q: %v", tool.Name, err)
			}

			if decoded["type"] != "object" {
				t.Errorf("expected schema type 'object', got %v", decoded["type"])
			}
			if _, ok := decoded["properties"]; !ok {
				t.Errorf("missing properties in schema for %q", tool.Name)
			}
		})
	}
}
