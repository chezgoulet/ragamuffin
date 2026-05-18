package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
)

// ── handleIngest ──────────────────────────────────────────────────────────────

func TestHandleIngest_MethodNotAllowed(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest("GET", "/v1/ingest", nil)
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleIngest_InvalidJSON(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest("POST", "/v1/ingest",
		bytes.NewReader([]byte("{invalid")))
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleIngest_MissingContent(t *testing.T) {
	srv := &Server{}
	body, _ := json.Marshal(ingestRequest{Source: "test", Tags: []string{"tag1"}})
	req := httptest.NewRequest("POST", "/v1/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleIngest_MissingSource(t *testing.T) {
	srv := &Server{}
	body, _ := json.Marshal(ingestRequest{Content: "hello world", Tags: []string{"tag1"}})
	req := httptest.NewRequest("POST", "/v1/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleIngest_MultiTenantMissingVault(t *testing.T) {
	cfg := &config.Config{
		Vaults: map[string]*config.VaultConfig{
			"docs": {Path: "/tmp/docs"},
		},
	}
	srv := &Server{cfg: cfg, indexers: indexer.NewManager()}
	body, _ := json.Marshal(ingestRequest{Content: "hello", Source: "test"})
	req := httptest.NewRequest("POST", "/v1/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleIngest_SingleTenantDefaultsToDefault(t *testing.T) {
	// Single-tenant: no vault specified → try "default"
	cfg := &config.Config{
		VaultPath: "/tmp/vault",
	}
	idxManager := indexer.NewManager()
	srv := &Server{
		cfg:      cfg,
		indexers: idxManager,
	}

	// No indexer for "default" → provisioning attempt, expect failure
	body, _ := json.Marshal(ingestRequest{Content: "hello", Source: "test"})
	req := httptest.NewRequest("POST", "/v1/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 (provisioning failure), got %d", w.Code)
	}
}

func TestHandleIngest_ProvisionInvalidName(t *testing.T) {
	cfg := &config.Config{
		Vaults: map[string]*config.VaultConfig{},
	}
	srv := &Server{cfg: cfg, indexers: indexer.NewManager()}
	body, _ := json.Marshal(ingestRequest{
		Vault:   "INVALID_NAME!",
		Content: "hello",
		Source:  "test",
	})
	req := httptest.NewRequest("POST", "/v1/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ── provisionVault ────────────────────────────────────────────────────────────

func TestProvisionVault_InvalidName(t *testing.T) {
	srv := &Server{cfg: &config.Config{}, indexers: indexer.NewManager()}

	idx := srv.provisionVault(context.Background(), "")
	if idx != nil {
		t.Error("expected nil for empty name")
	}
	idx = srv.provisionVault(context.Background(), "-bad")
	if idx != nil {
		t.Error("expected nil for invalid name")
	}
	idx = srv.provisionVault(context.Background(), "name_with_underscores")
	if idx != nil {
		t.Error("expected nil for name with underscores")
	}
}

func TestProvisionVault_NoQdrant_ReturnsNil(t *testing.T) {
	cfg := &config.Config{
		QdrantURL: "http://localhost:19999", // nothing listening — provisioning fails gracefully
		VaultPath: "/tmp",
	}
	srv := &Server{cfg: cfg, indexers: indexer.NewManager()}

	idx := srv.provisionVault(context.Background(), "agent-dev")
	if idx != nil {
		t.Error("expected nil when Qdrant unreachable")
	}
}
