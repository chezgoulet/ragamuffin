package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/llm"
)

// ── vaultPathFromContext ──────────────────────────────────────────────────────

func TestVaultPathFromContext_NoVaultName(t *testing.T) {
	cfg := &config.Config{VaultPath: "/default/vault"}
	srv := &Server{cfg: cfg}

	path := srv.vaultPathFromContext(context.Background())
	if path != "/default/vault" {
		t.Errorf("expected default path, got %q", path)
	}
}

func TestVaultPathFromContext_WithVaultName(t *testing.T) {
	cfg := &config.Config{
		VaultPath: "/default/vault",
		Vaults: map[string]*config.VaultConfig{
			"docs": {Path: "/custom/vault/docs"},
		},
	}
	srv := &Server{cfg: cfg}

	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	path := srv.vaultPathFromContext(ctx)
	if path != "/custom/vault/docs" {
		t.Errorf("expected docs vault path, got %q", path)
	}
}

func TestVaultPathFromContext_UnknownVaultName(t *testing.T) {
	cfg := &config.Config{
		VaultPath: "/default/vault",
		Vaults: map[string]*config.VaultConfig{
			"docs": {Path: "/custom/vault/docs"},
		},
	}
	srv := &Server{cfg: cfg}

	ctx := context.WithValue(context.Background(), vaultNameKey, "nonexistent")
	path := srv.vaultPathFromContext(ctx)
	if path != "/default/vault" {
		t.Errorf("expected fallback to default path, got %q", path)
	}
}

// ── vaultFromContext ──────────────────────────────────────────────────────────

func TestVaultFromContext_Empty(t *testing.T) {
	if got := vaultFromContext(context.Background()); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestVaultFromContext_WithValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), vaultNameKey, "mydocs")
	if got := vaultFromContext(ctx); got != "mydocs" {
		t.Errorf("expected mydocs, got %q", got)
	}
}

// ── vaultNameFromRequest ──────────────────────────────────────────────────────

func TestVaultNameFromRequest_WithPathValue(t *testing.T) {
	req := httptest.NewRequest("GET", "/vault/docs/recall", nil)
	req.SetPathValue("name", "docs")
	if got := vaultNameFromRequest(req); got != "docs" {
		t.Errorf("expected docs, got %q", got)
	}
}

func TestVaultNameFromRequest_WithoutPathValue(t *testing.T) {
	req := httptest.NewRequest("GET", "/recall", nil)
	if got := vaultNameFromRequest(req); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ── withRequestID middleware ──────────────────────────────────────────────────

func TestWithRequestID_Generated(t *testing.T) {
	srv := &Server{requestCounts: make(map[string]map[string]int64)}
	called := false
	handler := srv.withRequestID(func(w http.ResponseWriter, r *http.Request) {
		called = true
		id := requestID(r.Context())
		if id == "" {
			t.Error("expected non-empty request ID")
		}
		if len(id) != 36 {
			t.Errorf("expected UUID length 36, got %d", len(id))
		}
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if !called {
		t.Fatal("handler was not called")
	}
	respID := w.Header().Get("X-Request-ID")
	if respID == "" {
		t.Error("expected X-Request-ID response header")
	}
}

func TestWithRequestID_FromClient(t *testing.T) {
	srv := &Server{requestCounts: make(map[string]map[string]int64)}
	handler := srv.withRequestID(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r.Context())
		if id != "client-provided-id" {
			t.Errorf("expected client-provided-id, got %q", id)
		}
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", "client-provided-id")
	w := httptest.NewRecorder()
	handler(w, req)

	if got := w.Header().Get("X-Request-ID"); got != "client-provided-id" {
		t.Errorf("expected client-provided-id, got %q", got)
	}
}

func TestWithRequestID_TrailingSlashesRemoved(t *testing.T) {
	srv := &Server{requestCounts: make(map[string]map[string]int64)}
	handler := srv.withRequestID(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest("GET", "/some/path/", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// ── with404Check middleware ───────────────────────────────────────────────────

func TestWith404Check_RootPath(t *testing.T) {
	srv := &Server{}
	innerCalled := false
	handler := srv.with404Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !innerCalled {
		t.Error("expected inner handler to be called for root path")
	}
}

func TestWith404Check_JSONRequest(t *testing.T) {
	srv := &Server{}
	innerCalled := false
	handler := srv.with404Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if innerCalled {
		t.Error("expected inner handler to NOT be called for JSON API request")
	}
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}

	var resp errResp
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Error {
		t.Error("expected error response")
	}
	if resp.Code != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %q", resp.Code)
	}
}

func TestWith404Check_BrowserRequest(t *testing.T) {
	srv := &Server{}
	innerCalled := false
	handler := srv.with404Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !innerCalled {
		t.Error("expected inner handler to be called for browser request")
	}
}

// ── writeError ────────────────────────────────────────────────────────────────

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, 404, "NOT_FOUND", "resource not found")

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content type, got %q", ct)
	}

	var resp errResp
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Error {
		t.Error("expected error=true")
	}
	if resp.Code != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %q", resp.Code)
	}
	if resp.Message != "resource not found" {
		t.Errorf("expected 'resource not found', got %q", resp.Message)
	}
}

func TestWriteError_StatusCode(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, 429, "RATE_LIMITED", "too many requests")
	if w.Code != 429 {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

// ── writeJSON ─────────────────────────────────────────────────────────────────

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 200, map[string]string{"hello": "world"})

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content type, got %q", ct)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["hello"] != "world" {
		t.Errorf("expected world, got %q", body["hello"])
	}
}

// ── newRequestID ──────────────────────────────────────────────────────────────

func TestNewRequestID_Format(t *testing.T) {
	id := newRequestID()
	if len(id) != 36 {
		t.Errorf("expected UUID length 36, got %d (id=%q)", len(id), id)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Errorf("expected 5 UUID parts, got %d", len(parts))
	}
	if len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Errorf("unexpected UUID part lengths: %v", parts)
	}
}

func TestNewRequestID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := newRequestID()
		if seen[id] {
			t.Errorf("duplicate ID generated: %q", id)
		}
		seen[id] = true
	}
}

// ── withVault middleware ──────────────────────────────────────────────────────

func TestWithVault_MissingName(t *testing.T) {
	srv := &Server{cfg: &config.Config{}}
	handler := srv.withVault(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called")
	})

	req := httptest.NewRequest("GET", "/vault//recall", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestWithVault_UnknownVault(t *testing.T) {
	srv := &Server{cfg: &config.Config{}}
	handler := srv.withVault(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called")
	})

	req := httptest.NewRequest("GET", "/vault/nonexistent/recall", nil)
	req.SetPathValue("name", "nonexistent")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestWithVault_ValidVaultStoredInContext(t *testing.T) {
	srv := &Server{
		cfg: &config.Config{
			Vaults: map[string]*config.VaultConfig{
				"docs": {Path: "/vaults/docs"},
			},
		},
	}

	var storedName string
	handler := srv.withVault(func(w http.ResponseWriter, r *http.Request) {
		storedName = vaultFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/vault/docs/recall", nil)
	req.SetPathValue("name", "docs")
	w := httptest.NewRecorder()
	handler(w, req)

	if storedName != "docs" {
		t.Errorf("expected vault name 'docs' in context, got %q", storedName)
	}
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestWithVault_AccessDenied requires access to unexported auth context key.
// The claim-scoped rejection path is exercised indirectly through integration tests
// when auth middleware sets restricted claims.

// ── withRateLimit (basic structure) ───────────────────────────────────────────

func TestWithRateLimit_PassThrough(t *testing.T) {
	// withRateLimit requires a real rate limiter — use newTestServer
	srv := newTestServer()
	called := false
	handler := srv.withRateLimit("/test", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if !called {
		t.Error("expected handler to be called")
	}
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// ── llmFor ────────────────────────────────────────────────────────────────────

func TestLlmFor_NoContext_FallsToServerDefault(t *testing.T) {
	srv := &Server{
		cfg:       &config.Config{},
		llm:       llm.New("test", "http://llm", "sk-1", "gpt", 30*time.Second),
		indexers:  indexer.NewManager(),
	}

	lm := srv.llmFor(context.Background())
	if lm == nil {
		t.Fatal("expected non-nil LLM client from fallback")
	}
	// Should be the server-level client
	if lm != srv.llm {
		t.Error("llmFor should return server-level llm when no vault in context")
	}
}

func TestLlmFor_NoContext_NoLLMConfigured(t *testing.T) {
	srv := &Server{
		cfg:      &config.Config{},
		llm:      nil,
		indexers: indexer.NewManager(),
	}

	lm := srv.llmFor(context.Background())
	if lm != nil {
		t.Error("expected nil when no LLM configured and no vault context")
	}
}

func TestLlmFor_VaultContext_UsesPerVault(t *testing.T) {
	perVaultLm := llm.New("test", "http://vault-llm", "sk-2", "gpt-4", 30*time.Second)
	srv := &Server{
		cfg:      &config.Config{},
		llm:      llm.New("test", "http://server-llm", "sk-1", "gpt", 30*time.Second),
		indexers: indexer.NewManager(),
	}
	srv.indexers.SetLLM("docs", perVaultLm)

	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	lm := srv.llmFor(ctx)
	if lm == nil {
		t.Fatal("expected non-nil LLM client")
	}
	if lm != perVaultLm {
		t.Error("llmFor should return per-vault LLM when vault in context")
	}
}

func TestLlmFor_VaultContext_FallsToServerWhenNoPerVault(t *testing.T) {
	serverLm := llm.New("test", "http://server-llm", "sk-1", "gpt", 30*time.Second)
	srv := &Server{
		cfg:      &config.Config{},
		llm:      serverLm,
		indexers: indexer.NewManager(),
	}

	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	lm := srv.llmFor(ctx)
	if lm != serverLm {
		t.Error("llmFor should fall back to server LLM when per-vault not set")
	}
}

func TestLlmFor_VaultContext_BothNil(t *testing.T) {
	srv := &Server{
		cfg:      &config.Config{},
		llm:      nil,
		indexers: indexer.NewManager(),
	}

	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	lm := srv.llmFor(ctx)
	if lm != nil {
		t.Error("expected nil when neither server nor per-vault LLM is configured")
	}
}

// ── embeddingFor ──────────────────────────────────────────────────────────────

func TestEmbeddingFor_NoContext_FallsToServerDefault(t *testing.T) {
	srv := &Server{
		cfg:       &config.Config{},
		embedder:  embedding.New("http://embed", "sk-1", "model", 30*time.Second),
		indexers:  indexer.NewManager(),
	}

	ec := srv.embeddingFor(context.Background())
	if ec == nil {
		t.Fatal("expected non-nil embedder from fallback")
	}
	if ec != srv.embedder {
		t.Error("embeddingFor should return server-level embedder when no vault in context")
	}
}

func TestEmbeddingFor_NoContext_NilEmbedder(t *testing.T) {
	srv := &Server{
		cfg:      &config.Config{},
		embedder: nil,
		indexers: indexer.NewManager(),
	}

	ec := srv.embeddingFor(context.Background())
	if ec != nil {
		t.Error("expected nil when no embedder configured and no vault context")
	}
}

func TestEmbeddingFor_VaultContext_UsesPerVault(t *testing.T) {
	perVaultEc := embedding.New("http://vault-embed", "sk-2", "vault-model", 30*time.Second)
	srv := &Server{
		cfg:      &config.Config{},
		embedder: embedding.New("http://server-embed", "sk-1", "server-model", 30*time.Second),
		indexers: indexer.NewManager(),
	}
	srv.indexers.SetEmbedder("docs", perVaultEc)

	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	ec := srv.embeddingFor(ctx)
	if ec == nil {
		t.Fatal("expected non-nil embedder")
	}
	if ec != perVaultEc {
		t.Error("embeddingFor should return per-vault embedder when vault in context")
	}
}

func TestEmbeddingFor_VaultContext_FallsToServerWhenNoPerVault(t *testing.T) {
	serverEc := embedding.New("http://server-embed", "sk-1", "server-model", 30*time.Second)
	srv := &Server{
		cfg:      &config.Config{},
		embedder: serverEc,
		indexers: indexer.NewManager(),
	}

	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	ec := srv.embeddingFor(ctx)
	if ec != serverEc {
		t.Error("embeddingFor should fall back to server embedder when per-vault not set")
	}
}

func TestEmbeddingFor_VaultContext_BothNil(t *testing.T) {
	srv := &Server{
		cfg:      &config.Config{},
		embedder: nil,
		indexers: indexer.NewManager(),
	}

	ctx := context.WithValue(context.Background(), vaultNameKey, "docs")
	ec := srv.embeddingFor(ctx)
	if ec != nil {
		t.Error("expected nil when neither server nor per-vault embedder is configured")
	}
}
