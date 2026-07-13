package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
)

// newTestServer creates a Server with minimal backends for testing.
// testVaultDir is created once at package init so newTestServer callers
// (32 in this file) don't need a *testing.T parameter just for a temp dir.
var testVaultDir string

func init() {
	var err error
	testVaultDir, err = os.MkdirTemp("", "ragamuffin-test-vault-*")
	if err != nil {
		panic(fmt.Sprintf("create test vault dir: %v", err))
	}
}

func newTestServer() *Server {
	cfg := &config.Config{
		VaultPath: testVaultDir,
	}
	rl := ratelimit.New(false)
	idxm := indexer.NewManager()
	idxm.Add("default", indexer.New(testVaultDir, "default", nil, nil, nil), nil)
	return New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil, nil, nil, slog.Default(), nil, nil)
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
	idxm.Add("default", indexer.New("/test/vault", "default", nil, nil, nil), nil)
	srv := New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil, nil, nil, slog.Default(), nil, nil)
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
	idxm.Add("default", indexer.New("/test/vault", "default", nil, nil, nil), nil)
	srv := New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil, nil, nil, slog.Default(), nil, nil)
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

func TestHandleAudit_GET(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/audit", nil)
	w := httptest.NewRecorder()
	srv.handleAudit(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200 for GET audit, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	checks, _ := resp["checks_run"].([]interface{})
	if len(checks) != 4 {
		t.Errorf("expected 4 default checks for GET, got %d", len(checks))
	}
	// Verify UI-friendly aliases are present
	if _, ok := resp["staleness"]; !ok {
		t.Error("expected staleness alias in GET response")
	}
	if _, ok := resp["contradictions"]; !ok {
		t.Error("expected contradictions alias in GET response")
	}
}

// ── /v1/verify ─────────────────────────────────────────────────────────────────

func TestHandleVerify_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/verify", nil)
	w := httptest.NewRecorder()
	srv.handleVerify(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

func TestHandleVerify_InvalidJSON(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/v1/verify", body)
	w := httptest.NewRecorder()
	srv.handleVerify(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestHandleVerify_MissingFact(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/v1/verify", body)
	w := httptest.NewRecorder()
	srv.handleVerify(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for missing fact, got %d", w.Code)
	}
}

func TestHandleVerify_Defaults(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"fact":"test fact statement"}`)
	req := httptest.NewRequest("POST", "/v1/verify", body)
	w := httptest.NewRecorder()
	srv.handleVerify(w, req)

	// Without an embedding API, the handler returns 502 EMBEDDING_API_ERROR
	if w.Code != 502 {
		t.Errorf("expected 502 (embedding not configured), got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleVerify_TopKClamping(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"fact":"test","top_k":200}`)
	req := httptest.NewRequest("POST", "/v1/verify", body)
	w := httptest.NewRecorder()
	srv.handleVerify(w, req)

	if w.Code != 502 {
		t.Errorf("expected 502 (embedding not configured), got %d: %s", w.Code, w.Body.String())
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
	idxm.Add("docs", indexer.New("/tmp/docs", "docs", nil, nil, nil), nil)
	idxm.Add("code", indexer.New("/tmp/code", "code", nil, nil, nil), nil)
	srv := New(cfg, nil, nil, nil, nil, idxm, nil, rl, nil, nil, nil, nil, nil, slog.Default(), nil, nil)
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

// ── /v1/debt ─────────────────────────────────────────────────────────────────

func TestHandleDebt_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/debt", nil)
	w := httptest.NewRecorder()
	srv.handleDebt(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleDebt_Success(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/debt", nil)
	w := httptest.NewRecorder()
	srv.handleDebt(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["vault_count"]; !ok {
		t.Error("expected vault_count in response")
	}
}

// ── /v1/gaps ─────────────────────────────────────────────────────────────────

func TestHandleGaps_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/gaps", nil)
	w := httptest.NewRecorder()
	srv.handleGaps(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleGaps_Success(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/gaps", nil)
	w := httptest.NewRecorder()
	srv.handleGaps(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["poorly_covered"]; !ok {
		t.Error("expected poorly_covered in response")
	}
}

// ── /v1/agents/stats ─────────────────────────────────────────────────────────

func TestHandleAgentStats_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/agents/stats", nil)
	w := httptest.NewRecorder()
	srv.handleAgentStats(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleAgentStats_Success(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/agents/stats", nil)
	w := httptest.NewRecorder()
	srv.handleAgentStats(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["agents"]; !ok {
		t.Error("expected agents in response")
	}
}

// ── /v1/chunks (list) ────────────────────────────────────────────────────────

func TestHandleChunksList_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/chunks", nil)
	w := httptest.NewRecorder()
	srv.handleChunksList(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/embedding/project ─────────────────────────────────────────────────────

func TestHandleEmbedProject_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("PUT", "/v1/embedding/project", nil)
	w := httptest.NewRecorder()
	srv.handleEmbedProject(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleEmbedProject_Success(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/embedding/project", nil)
	w := httptest.NewRecorder()
	srv.handleEmbedProject(w, req)

	// Without a Qdrant client, the handler returns 404 (no backend)
	if w.Code != 404 {
		t.Errorf("expected 404 (no qdrant client), got %d", w.Code)
	}
}

// ── /v1/config ────────────────────────────────────────────────────────────────

func TestHandleConfig_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/config", nil)
	w := httptest.NewRecorder()
	srv.handleConfig(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleConfig_Success(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/config", nil)
	w := httptest.NewRecorder()
	srv.handleConfig(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["version"]; !ok {
		t.Error("expected version in response")
	}
}

// ── /v1/facts/{key}/provenance ───────────────────────────────────────────────

func TestHandleProvenance_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/facts/nonexistent/provenance", nil)
	w := httptest.NewRecorder()
	srv.handleProvenance(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/facts/{key}/history ──────────────────────────────────────────────────

func TestHandleFactHistory_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/facts/nonexistent/history", nil)
	w := httptest.NewRecorder()
	srv.handleFactHistory(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── DELETE /v1/vaults/{name} ─────────────────────────────────────────────────

func TestHandleVaultDelete_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/vaults/test", nil)
	w := httptest.NewRecorder()
	srv.handleVaultDelete(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── Export ───────────────────────────────────────────────────────────────────

func TestHandleExport_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/vaults/test/export", nil)
	w := httptest.NewRecorder()
	// Set path value to simulate route
	req.SetPathValue("name", "test")
	srv.handleExport(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── Import ───────────────────────────────────────────────────────────────────

func TestHandleImport_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/vaults/test/import", nil)
	w := httptest.NewRecorder()
	req.SetPathValue("name", "test")
	srv.handleImport(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/auth/check ────────────────────────────────────────────────────────────

func TestHandleAuthCheck_PUT(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("PUT", "/v1/auth/check", nil)
	w := httptest.NewRecorder()
	srv.handleAuthCheck(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405 for PUT, got %d", w.Code)
	}
}

func TestHandleAuthCheck_GET_Unauthenticated(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/auth/check", nil)
	w := httptest.NewRecorder()
	srv.handleAuthCheck(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["authenticated"] != false {
		t.Errorf("expected authenticated=false, got %v", resp["authenticated"])
	}
}

// ── /v1/briefing ──────────────────────────────────────────────────────────────

func TestHandleBriefing_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/briefing", nil)
	w := httptest.NewRecorder()
	srv.handleBriefing(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/hybrid ────────────────────────────────────────────────────────────────

func TestHandleHybrid_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/hybrid", nil)
	w := httptest.NewRecorder()
	srv.handleHybrid(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/pruner/ ────────────────────────────────────────────────────────────────

func TestHandlePrunerAutoTune_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/pruner/auto-tune", nil)
	w := httptest.NewRecorder()
	srv.handlePrunerAutoTune(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandlePrunerAutoTune_NoPruner(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/pruner/auto-tune", nil)
	w := httptest.NewRecorder()
	srv.handlePrunerAutoTune(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 (pruner not configured), got %d", w.Code)
	}
}

func TestHandlePrunerConfig_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/pruner/config", nil)
	w := httptest.NewRecorder()
	srv.handlePrunerConfig(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandlePrunerConfig_NoPruner(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/pruner/config", nil)
	w := httptest.NewRecorder()
	srv.handlePrunerConfig(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 (pruner not configured), got %d", w.Code)
	}
}

// ── /v1/batch/recall ──────────────────────────────────────────────────────────

func TestHandleBatchRecall_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/batch/recall", nil)
	w := httptest.NewRecorder()
	srv.handleBatchRecall(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleBatchRecall_InvalidJSON(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/v1/batch/recall", body)
	w := httptest.NewRecorder()
	srv.handleBatchRecall(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestHandleBatchRecall_EmptyQueries(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"queries":[]}`)
	req := httptest.NewRequest("POST", "/v1/batch/recall", body)
	w := httptest.NewRecorder()
	srv.handleBatchRecall(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for empty queries, got %d", w.Code)
	}
}

// ── /v1/chunks/{chunk_id} ─────────────────────────────────────────────────────

func TestHandleChunkGet_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/chunks/some-id", nil)
	w := httptest.NewRecorder()
	srv.handleChunkGet(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleChunkGet_EmptyID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/chunks/", nil)
	w := httptest.NewRecorder()
	srv.handleChunkGet(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for empty chunk_id, got %d", w.Code)
	}
}

func TestHandleChunkGet_InvalidUUID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/chunks/not-a-uuid", nil)
	req.SetPathValue("chunk_id", "not-a-uuid")
	w := httptest.NewRecorder()
	srv.handleChunkGet(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid UUID, got %d", w.Code)
	}
}

// ── /v1/chunks (DELETE via handleChunksList dispatch) ─────────────────────────

func TestHandleChunksList_PUT(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("PUT", "/v1/chunks", nil)
	w := httptest.NewRecorder()
	srv.handleChunksList(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405 for PUT, got %d", w.Code)
	}
}

// ── /reindex ──────────────────────────────────────────────────────────────────

func TestHandleReindex_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/reindex", nil)
	w := httptest.NewRecorder()
	srv.handleReindex(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/snapshot ──────────────────────────────────────────────────────────────

func TestHandleSnapshot_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/snapshot", nil)
	w := httptest.NewRecorder()
	srv.handleSnapshot(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/logs ──────────────────────────────────────────────────────────────────

func TestHandleLogs_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("PUT", "/v1/logs", nil)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405 for PUT, got %d", w.Code)
	}
}

func TestHandleLogsPost_MissingAgent(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"type":"test","body":"hello"}`)
	req := httptest.NewRequest("POST", "/v1/logs", body)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing agent, got %d", w.Code)
	}
}

func TestHandleLogsPost_MissingType(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"agent":"test","body":"hello"}`)
	req := httptest.NewRequest("POST", "/v1/logs", body)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing type, got %d", w.Code)
	}
}

func TestHandleLogsPost_InvalidJSON(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{bad json`)
	req := httptest.NewRequest("POST", "/v1/logs", body)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleLogsGet_InvalidSince(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/logs?since=not-a-date", nil)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid since, got %d", w.Code)
	}
}

func TestHandleLogsGet_InvalidUntil(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/logs?until=not-a-date", nil)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid until, got %d", w.Code)
	}
}

// ── /inbox ─────────────────────────────────────────────────────────────────────

func TestHandleInboxCreate_EmptyContent(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"content":"","source":"test"}`)
	req := httptest.NewRequest("POST", "/inbox", body)
	w := httptest.NewRecorder()
	srv.handleInbox(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for empty content, got %d", w.Code)
	}
}

// ── /v1/links ──────────────────────────────────────────────────────────────────

func TestHandleLinks_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/links", nil)
	w := httptest.NewRecorder()
	srv.handleLinks(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleLinks_MissingPath(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/links", nil)
	w := httptest.NewRecorder()
	srv.handleLinks(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing path, got %d", w.Code)
	}
}

func TestHandleBacklinks_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/links/backlinks", nil)
	w := httptest.NewRecorder()
	srv.handleBacklinks(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleLinkGraph_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/links/graph", nil)
	w := httptest.NewRecorder()
	srv.handleLinkGraph(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── /v1/extraction/stats ───────────────────────────────────────────────────────

func TestHandleExtractionStats_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/extraction/stats", nil)
	w := httptest.NewRecorder()
	srv.handleExtractionStats(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleExtractionStats_NoExtractor(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/extraction/stats", nil)
	w := httptest.NewRecorder()
	srv.handleExtractionStats(w, req)
	if w.Code != 503 {
		t.Errorf("expected 503 (no extractor), got %d", w.Code)
	}
}

// ── /v1/documents ──────────────────────────────────────────────────────────────

func TestHandleDocuments_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/documents", nil)
	w := httptest.NewRecorder()
	srv.handleDocuments(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleDocuments_MissingContent(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"source":"test"}`)
	req := httptest.NewRequest("POST", "/v1/documents", body)
	w := httptest.NewRecorder()
	srv.handleDocuments(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing content, got %d", w.Code)
	}
}

func TestHandleDocuments_MissingSource(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"content":"some content"}`)
	req := httptest.NewRequest("POST", "/v1/documents", body)
	w := httptest.NewRecorder()
	srv.handleDocuments(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing source, got %d", w.Code)
	}
}

// ── /events ───────────────────────────────────────────────────────────────────

func TestHandleEvents_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/events", nil)
	w := httptest.NewRecorder()
	srv.handleEvents(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404 (broker nil), got %d", w.Code)
	}
}

// ── /v1/sessions/batch ─────────────────────────────────────────────────────────

func TestHandleBatchSessions_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/sessions/batch", nil)
	w := httptest.NewRecorder()
	srv.handleBatchSessions(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleBatchSessions_EmptySessions(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"sessions":[]}`)
	req := httptest.NewRequest("POST", "/v1/sessions/batch", body)
	w := httptest.NewRecorder()
	srv.handleBatchSessions(w, req)

	// Without a logstore, the handler returns 503
	if w.Code != 503 {
		t.Errorf("expected 503 (logstore not configured), got %d", w.Code)
	}
}

// ── /v1/vaults/{name}/clear ───────────────────────────────────────────────────

func TestHandleVaultClear_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/vaults/docs/clear", nil)
	w := httptest.NewRecorder()
	srv.handleVaultClear(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleVaultClear_BadPath(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"confirm":true}`)
	req := httptest.NewRequest("POST", "/v1/vaults/noclear", body)
	w := httptest.NewRecorder()
	srv.handleVaultClear(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 (not /clear), got %d", w.Code)
	}
}

// ── /v1/refresh ──────────────────────────────────────────────────────────────

func TestHandleRefresh_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/refresh", nil)
	w := httptest.NewRecorder()
	srv.handleRefresh(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleRefresh_InvalidJSON(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/v1/refresh", body)
	w := httptest.NewRecorder()
	srv.handleRefresh(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

// ── /vaults (POST — create vault) ────────────────────────────────────────────

func TestHandleVaults_POST_InvalidJSON(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/vaults", body)
	w := httptest.NewRecorder()
	srv.handleVaults(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestHandleVaults_POST_MissingName(t *testing.T) {
	srv := newTestServer()
	body := bytes.NewBufferString(`{"path":"/tmp"}`)
	req := httptest.NewRequest("POST", "/vaults", body)
	w := httptest.NewRecorder()
	srv.handleVaults(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for missing name, got %d", w.Code)
	}
}

// ── /inbox (method dispatch) ─────────────────────────────────────────────────

func TestHandleInbox_PUT(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("PUT", "/inbox", nil)
	w := httptest.NewRecorder()
	srv.handleInbox(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405 for PUT, got %d", w.Code)
	}
}

// ── /v1/chunks (GET — list via handleChunksListGET) ──────────────────────────

func TestHandleChunksListGET_NoQdrant(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/chunks", nil)
	w := httptest.NewRecorder()
	srv.handleChunksListGET(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404 (no qdrant client), got %d", w.Code)
	}
}

// ── /v1/chunks (DELETE via handleChunksDelete) ───────────────────────────────

func TestHandleChunksDelete_NoQdrant(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("DELETE", "/v1/chunks?confirm=true", nil)
	w := httptest.NewRecorder()
	srv.handleChunksDelete(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404 (no qdrant client), got %d", w.Code)
	}
}

// ── /v1/digest (#824) ────────────────────────────────────────────────────────

func TestHandleDigest_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/digest", nil)
	w := httptest.NewRecorder()
	srv.handleDigest(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleDigest_Success(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/digest", nil)
	w := httptest.NewRecorder()
	srv.handleDigest(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["total_events"]; !ok {
		t.Error("expected total_events in response")
	}
	if _, ok := resp["period_hours"]; !ok {
		t.Error("expected period_hours in response")
	}
}

// ── /v1/contradictions (#823) ────────────────────────────────────────────────

func TestHandleContradictions_MethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/contradictions", nil)
	w := httptest.NewRecorder()
	srv.handleContradictions(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleContradictions_SingleVault(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/contradictions", nil)
	w := httptest.NewRecorder()
	srv.handleContradictions(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["contradictions"]; !ok {
		t.Error("expected contradictions in response")
	}
	if _, ok := resp["note"]; !ok {
		t.Error("expected note about single vault in response")
	}
}
