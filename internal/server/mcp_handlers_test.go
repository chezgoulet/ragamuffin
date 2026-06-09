package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func testMCPLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newMCPTestServer creates a minimal Server for testing MCP adapters.
// Most do* methods will return errors because dependencies are nil,
// but the arg extraction and validation paths are exercised.
func newMCPTestServer(t *testing.T) *Server {
	t.Helper()
	srv := &Server{
		cfg:      minimalConfig(),
		facts:    &conversationMockStore{},  // satisfies qdrant.FactStore, all methods return nil/0
		logger:   testMCPLogger(t),
		indexers: indexer.NewManager(),
	}
	// Add a no-op indexer for "test-vault" so doStore doesn't
	// trigger provisionVault (which would try to connect to Qdrant).
	// nil qdrant + nil embedder is safe for Stats() and errors on Ingest().
	idx := indexer.New("/tmp/test-vault", "test-vault", nil, nil, srv.logger)
	if err := srv.indexers.Add("test-vault", idx, nil); err != nil {
		t.Fatalf("add test indexer: %v", err)
	}
	return srv
}

func minimalConfig() *config.Config {
	return &config.Config{
		FactsCollection:    "test_facts",
		FactsVectorSize:    4,
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

	// Verify each tool has required fields
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
			schema, ok := tool.InputSchema.(map[string]interface{})
			if !ok {
				t.Fatal("expected map input schema")
			}

			// Every tool with a "required" field should have non-empty required list
			if req, has := schema["required"]; has {
				if reqList, ok := req.([]interface{}); ok && len(reqList) > 0 {
					t.Logf("  required: %v", reqList)
				}
			}

			// Every tool should have "type":"object"
			if schema["type"] != "object" {
				t.Errorf("expected input schema type 'object', got %v", schema["type"])
			}

			// Every tool should have a "properties" map
			if _, has := schema["properties"]; !has {
				t.Errorf("missing properties in input schema")
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
		{"ragamuffin_recall", true, "query is required"},            // passes arg validation
		{"ragamuffin_ask", true, "query is required"},
		{"ragamuffin_store", true, "write access required"},         // needs auth claims
		{"ragamuffin_draft", true, "title is required"},             // passes to doDraft
		{"ragamuffin_facts", true, "operation is required"},
		{"ragamuffin_audit", false, ""},                             // optional args, may succeed or fail at do*
		{"ragamuffin_graph", true, `vault "default" not found`},     // needs real indexer
		{"ragamuffin_stats", false, ""},                             // calls doStats
		{"ragamuffin_session_create", true, "agent_id is required"},
		{"ragamuffin_session_get", true, "session_id is required"},
		{"ragamuffin_session_list", false, ""},                      // calls doListSessions
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
	// mcpRecall validates detail arg before calling doRecall
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
	// Make a request that would pass validation.
	// It will fail at doAsk because no LLM is configured, but we verify
	// that the mode defaults to "auto" before reaching doAsk.
	// We can't directly check the default, but we can confirm it doesn't
	// fail with "mode is required" or similar.
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_ask", map[string]interface{}{
		"query": "test question",
	})
	if err == nil {
		t.Fatal("expected error from doAsk (no LLM configured)")
	}
	// Should not be an arg validation error
	if err.Error() == "query is required" {
		t.Fatal("unexpected validation error after query was provided")
	}
}

// ── Adapter: mcpStore ─────────────────────────────────────────────────────────

func TestMCPStore_ArgsPassThrough(t *testing.T) {
	srv := newMCPTestServer(t)
	// mcpStore checks for write-auth claims before extracting args.
	// Without auth, the check short-circuits (claims == nil → skip).
	// Both content and source are provided, so arg extraction passes.
	// doStore then fails because no indexer is registered for the default vault.
	// We test the adapter directly to avoid provisionVault connecting to Qdrant.
	_, err := srv.mcpStore(context.Background(), map[string]interface{}{
		"content": "test content",
		"source":  "test source",
		"vault":   "test-vault",
	})
	if err == nil {
		t.Fatal("expected error — needs indexer for the vault")
	}
	// Should not be a missing-arg error
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
	// Will likely fail at doFactsList because no facts client configured,
	// but the point is that arg extraction succeeds with default limit.
	if err != nil {
		// Should not be "limit is required" — default is 100
		if err.Error() == "limit is required" {
			t.Error("limit should default to 100 without argument")
		}
		_ = result
	}
}

// ── Adapter: mcpAudit ─────────────────────────────────────────────────────────

func TestMCPAudit_DefaultValues(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_audit", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error — audit requires LLM and facts")
	}
	// Should fail at doAudit, not arg validation
	if err.Error() == "stale_days is required" || err.Error() == "checks is required" {
		t.Error("audit args should have defaults")
	}
}

// ── Adapter: mcpGraph ─────────────────────────────────────────────────────────

func TestMCPGraph_DefaultValues(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_graph", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error — needs real vault/indexer")
	}
	// Should fail because vault "default" is not found
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
		t.Fatal("expected error — needs vault path and git provider")
	}
	// Should not be "title is required" or similar validation errors
	if err.Error() == "title is required" || err.Error() == "target_path is required" {
		t.Error("args should pass validation with required fields")
	}
}

// ── Adapter: mcpSessionCreate ─────────────────────────────────────────────────

func TestMCPSessionCreate_MissingAgentID(t *testing.T) {
	srv := newMCPTestServer(t)
	_, err := srv.mcpDispatch(context.Background(), "ragamuffin_session_create", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing agent_id")
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
		name    string
		args    map[string]interface{}
		errMsg  string
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
	// Initialize the MCP handler as the server does
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
	json.NewDecoder(w.Body).Decode(&resp)
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
	// Should have all 13 tools from the server
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
