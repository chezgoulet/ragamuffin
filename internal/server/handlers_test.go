package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
)

// newTestServer creates a Server with minimal backends for testing.
func newTestServer() *Server {
	cfg := &config.Config{
		VaultPath: "/test/vault",
	}
	rl := ratelimit.New(false)
	idxm := indexer.NewManager()
	idxm.Add("default", indexer.New("/test/vault", nil, nil, nil), nil)
	return New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil)
}

func TestHandleHealth_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleStats_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/stats", nil)
	w := httptest.NewRecorder()
	srv.handleStats(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleVersion(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()
	srv.handleVersion(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["version"] != "unknown" {
		t.Errorf("version = %q, want unknown", body["version"])
	}
}

func TestHandleVersion_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/version", nil)
	w := httptest.NewRecorder()
	srv.handleVersion(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleMetrics(t *testing.T) {
	t.Skip("needs Qdrant backend for health check")
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Error("expected Content-Type header")
	}
}

func TestHandleMetrics_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /recall ────────────────────────────────────────────────────────────────────

func TestHandleRecall_MissingQuery(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"top_k": 10}`)
	req := httptest.NewRequest("POST", "/recall", body)
	w := httptest.NewRecorder()
	srv.handleRecall(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing query, got %d", w.Code)
	}
}

func TestHandleRecall_InvalidJSON(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/recall", body)
	w := httptest.NewRecorder()
	srv.handleRecall(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestHandleRecall_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/recall", nil)
	w := httptest.NewRecorder()
	srv.handleRecall(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleRecall_DefaultsTopK(t *testing.T) {
	t.Skip("needs embedding backend for full request path")
	srv := newTestServer()
	body := bytes.NewBufferString(`{"query": "test", "top_k": 0}`)
	req := httptest.NewRequest("POST", "/recall", body)
	w := httptest.NewRecorder()

	// This will fail at the embedding step (nil embedder), but the
	// validation should pass — defaults should be applied.
	req.Header.Set("Content-Type", "application/json")
	srv.handleRecall(w, req)

	// Either 502 (backend failure) or 500 (nil pointer). Not 400.
	if w.Code == 400 {
		var resp errResp
		json.NewDecoder(w.Body).Decode(&resp)
		t.Errorf("validation should pass with defaults, got 400: %s", resp.Message)
	}
}

func TestHandleRecall_TopKCeiling(t *testing.T) {
	t.Skip("needs embedding backend for full request path")
	srv := newTestServer()
	body := bytes.NewBufferString(`{"query": "test", "top_k": 200}`)
	req := httptest.NewRequest("POST", "/recall", body)
	w := httptest.NewRecorder()
	srv.handleRecall(w, req)

	// Should not be a validation error — top_k gets capped server-side
	if w.Code == 400 {
		var resp errResp
		json.NewDecoder(w.Body).Decode(&resp)
		t.Errorf("top_k should be capped not rejected, got 400: %s", resp.Message)
	}
}

// ── /ask ───────────────────────────────────────────────────────────────────────

func TestHandleAsk_NoLLMConfigured(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"query": "what is this?"}`)
	req := httptest.NewRequest("POST", "/ask", body)
	w := httptest.NewRecorder()
	srv.handleAsk(w, req)

	if w.Code != 503 {
		t.Errorf("expected 503 for no LLM, got %d", w.Code)
	}
}

func TestHandleAsk_MissingQuery(t *testing.T) {
	cfg := &config.Config{LLMProvider: "test", LLMAPIKey: "test"}
	rl := ratelimit.New(false)
	idxm := indexer.NewManager()
	idxm.Add("default", indexer.New("/test/vault", nil, nil, nil), nil)
	srv := New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil)
	body := bytes.NewBufferString(`{"top_k": 8}`)
	req := httptest.NewRequest("POST", "/ask", body)
	w := httptest.NewRecorder()
	srv.handleAsk(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing query, got %d", w.Code)
	}
}

func TestHandleAsk_InvalidJSON(t *testing.T) {
	cfg := &config.Config{LLMProvider: "test", LLMAPIKey: "test"}
	rl := ratelimit.New(false)
	idxm := indexer.NewManager()
	idxm.Add("default", indexer.New("/test/vault", nil, nil, nil), nil)
	srv := New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil)
	body := bytes.NewBufferString(`bad`)
	req := httptest.NewRequest("POST", "/ask", body)
	w := httptest.NewRecorder()
	srv.handleAsk(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestHandleAsk_Defaults(t *testing.T) {
	t.Skip("needs embedding + LLM backends for full request path")
	srv := newTestServer()
	body := bytes.NewBufferString(`{"query": "test"}`)
	req := httptest.NewRequest("POST", "/ask", body)
	w := httptest.NewRecorder()
	srv.handleAsk(w, req)

	// Will fail at embedding step (nil embedder), but mode and top_k
	// should default correctly — not a validation error.
	if w.Code == 400 {
		var resp errResp
		json.NewDecoder(w.Body).Decode(&resp)
		t.Errorf("defaults should be applied, got 400: %s", resp.Message)
	}
}

// ── /draft ─────────────────────────────────────────────────────────────────────

func TestHandleDraft_MissingTitle(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"content": "test", "target_path": "test.md"}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing title, got %d", w.Code)
	}
}

func TestHandleDraft_MissingPath(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"title": "test", "content": "test"}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing target_path, got %d", w.Code)
	}
}

func TestHandleDraft_PathTraversal(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"title":"test","content":"test","target_path":"../../../etc/passwd"}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for path traversal, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleDraft_PRModeNoGit(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"title":"test","content":"test","target_path":"test.md","mode":"pr"}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	if w.Code != 503 {
		t.Errorf("expected 503 for PR mode without git config, got %d", w.Code)
	}
}

func TestHandleDraft_DefaultMode(t *testing.T) {
	t.Skip("needs filesystem backend for direct write")
	srv := newTestServer()
	body := bytes.NewBufferString(`{"title":"test","content":"test","target_path":"test.md"}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	// Will fail at filesystem write (nonexistent vault), but mode
	// should default to "direct" — not a validation error.
	if w.Code == 400 {
		var resp errResp
		json.NewDecoder(w.Body).Decode(&resp)
		t.Errorf("default mode should be direct, got 400: %s", resp.Message)
	}
}

func TestHandleDraft_DeleteRequiresExplicitFlag(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"title":"test","content":"","target_path":"test.md"}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for empty content without delete=true, got %d: %s", w.Code, w.Body.String())
	}

	var resp errResp
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Message != "content required unless delete=true" {
		t.Errorf("expected content-required message, got: %s", resp.Message)
	}
}

func TestHandleDraft_ExplicitDelete(t *testing.T) {
	t.Skip("needs filesystem backend for direct write")
	srv := newTestServer()
	body := bytes.NewBufferString(`{"title":"test","content":"","target_path":"test.md","delete":true}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	if w.Code >= 400 && w.Code < 500 {
		var resp errResp
		json.NewDecoder(w.Body).Decode(&resp)
		t.Errorf("delete=true with empty content should not be 400, got %d: %s", w.Code, resp.Message)
	}
}

func TestHandleDraft_PRNoContentRequired(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"title":"test","target_path":"test.md","mode":"pr"}`)
	req := httptest.NewRequest("POST", "/draft", body)
	w := httptest.NewRecorder()
	srv.handleDraft(w, req)

	// Should fail at git-not-configured, not at missing content
	if w.Code == 400 {
		var resp errResp
		json.NewDecoder(w.Body).Decode(&resp)
		t.Errorf("content should not be required in PR mode, got 400: %s", resp.Message)
	}
	if w.Code != 503 {
		t.Errorf("expected 503 for PR mode without git (content should be optional), got %d", w.Code)
	}
}

// ── /audit ─────────────────────────────────────────────────────────────────────

func TestHandleAudit_InvalidJSON(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/audit", body)
	w := httptest.NewRecorder()
	srv.handleAudit(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestHandleAudit_Defaults(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/audit", body)
	w := httptest.NewRecorder()
	srv.handleAudit(w, req)

	// Default checks and sample_size should be applied.
	// Will succeed with empty results (no files to audit).
	if w.Code != 200 {
		t.Errorf("expected 200 with defaults, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	checks, _ := resp["checks_run"].([]interface{})
	if len(checks) != 4 {
		t.Errorf("expected 4 default checks, got %d: %v", len(checks), checks)
	}
}

func TestHandleAudit_SpecificChecks(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"checks":["stale","gap"]}`)
	req := httptest.NewRequest("POST", "/audit", body)
	w := httptest.NewRecorder()
	srv.handleAudit(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	checks, _ := resp["checks_run"].([]interface{})
	if len(checks) != 2 {
		t.Errorf("expected 2 checks, got %d: %v", len(checks), checks)
	}
}

func TestHandleAudit_SampleSizeCap(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"sample_size": 500}`)
	req := httptest.NewRequest("POST", "/audit", body)
	w := httptest.NewRecorder()
	srv.handleAudit(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200 with capped sample_size, got %d", w.Code)
	}
}

func TestHandleVaults_MultiTenant(t *testing.T) {
	cfg := &config.Config{
		Vaults: map[string]*config.VaultConfig{
			"docs": {Path: "/tmp/docs"},
			"code": {Path: "/tmp/code"},
		},
	}
	rl := ratelimit.New(false)
	idxm := indexer.NewManager()
	idxm.Add("docs", indexer.New("/tmp/docs", nil, nil, nil), nil)
	idxm.Add("code", indexer.New("/tmp/code", nil, nil, nil), nil)
	srv := New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil)
	req := httptest.NewRequest("GET", "/vaults", nil)
	w := httptest.NewRecorder()
	srv.handleVaults(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	vaults, ok := resp["vaults"].([]interface{})
	if !ok {
		t.Fatal("expected vaults array")
	}
	if len(vaults) != 2 {
		t.Errorf("expected 2 vaults, got %d", len(vaults))
	}
}

func TestHandleVaults_SingleTenant(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/vaults", nil)
	w := httptest.NewRecorder()
	srv.handleVaults(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	vaults, ok := resp["vaults"].([]interface{})
	if !ok {
		t.Fatal("expected vaults array")
	}
	if len(vaults) != 1 {
		t.Errorf("expected 1 vault in single-tenant, got %d", len(vaults))
	}
}

func TestHandleGraph_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/graph", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

type graphTestResponse struct {
	Nodes []interface{} `json:"nodes"`
	Edges []interface{} `json:"edges"`
}

func TestHandleGraph_FullGraph(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Nodes == nil {
		t.Error("expected non-nil nodes")
	}
	if resp.Edges == nil {
		t.Error("expected non-nil edges")
	}
}

func TestHandleGraph_EntityDepth0(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph?entity=test&depth=0", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Nodes) != 1 {
		t.Errorf("expected 1 node for depth=0, got %d", len(resp.Nodes))
	}
	if resp.Nodes[0].(map[string]interface{})["id"] != "entity:test" {
		t.Errorf("expected entity:test, got %v", resp.Nodes[0])
	}
}

func TestHandleGraph_LimitParam(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph?limit=5", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Nodes) > 5 {
		t.Errorf("expected <=5 nodes with limit=5, got %d", len(resp.Nodes))
	}
}

func TestHandleGraph_InvalidDepthClamped(t *testing.T) {
	srv := newTestServer()
	// depth=5 should be clamped to 3
	req := httptest.NewRequest("GET", "/graph?entity=test&depth=5", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleGraph_InvalidLimitClamped(t *testing.T) {
	srv := newTestServer()
	// limit=999 should be clamped to 200
	req := httptest.NewRequest("GET", "/graph?limit=999", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestVaultRouting_ExtractsVaultName(t *testing.T) {
	req := httptest.NewRequest("GET", "/vault/docs/recall?query=test", nil)
	req.SetPathValue("name", "docs")
	vault := vaultNameFromRequest(req)
	if vault != "docs" {
		t.Errorf("expected docs, got %q", vault)
	}
}

func TestVaultRouting_NoVault(t *testing.T) {
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	vault := vaultNameFromRequest(req)
	if vault != "" {
		t.Errorf("expected empty, got %q", vault)
	}
}

func TestVaultRouting_ContextRoundTrip(t *testing.T) {
	// vaultFromContext is used by handlers after withVault middleware sets it
	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	if got := vaultFromContext(ctx); got != "docs" {
		t.Errorf("expected docs, got %q", got)
	}
}
