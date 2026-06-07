package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chezgoulet/ragamuffin/internal/auth"
)

// ── Request/Response types ────────────────────────────────────────────────────

type documentsRequest struct {
	Vault       string   `json:"vault"`
	Content     string   `json:"content"`
	Source      string   `json:"source"`
	Tags        []string `json:"tags,omitempty"`
	AutoExtract *bool    `json:"auto_extract,omitempty"`
}

type documentsResponse struct {
	Status string `json:"status"`
	Vault  string `json:"vault"`
	Source string `json:"source"`
}

// ── POST /v1/documents ───────────────────────────────────────────────────────

func (s *Server) handleDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024) // 10 MB limit

	var req documentsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid JSON body: %s", err))
		return
	}

	// Validate required fields
	if req.Content == "" {
		writeError(w, 400, "INVALID_REQUEST", "content is required")
		return
	}
	if req.Source == "" {
		writeError(w, 400, "INVALID_REQUEST", "source is required")
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

	// Route through the API driver (if available) for proper event pipeline
	if s.apiDriver != nil {
		if err := s.apiDriver.Ingest(r.Context(), req.Content, req.Source, vaultName, req.Tags); err != nil {
			s.log(r.Context()).Error("document ingest failed", "vault", vaultName, "source", req.Source, "error", err)
			writeError(w, 502, "INGEST_FAILED", fmt.Sprintf("ingest failed: %s", err))
			return
		}
	} else {
		// Fallback: direct ingest without driver
		if err := idx.Ingest(r.Context(), req.Content, req.Source, req.Tags, nil); err != nil {
			s.log(r.Context()).Error("document ingest failed", "vault", vaultName, "source", req.Source, "error", err)
			writeError(w, 502, "INGEST_FAILED", fmt.Sprintf("ingest failed: %s", err))
			return
		}
	}

	// Trigger fact extraction if auto_extract is requested (#529)
	if req.AutoExtract != nil && *req.AutoExtract && s.extractor != nil && s.extractor.Enabled() {
		go s.extractor.Extract(r.Context(), req.Source, req.Content, "system")
	}

	writeJSON(w, 200, documentsResponse{
		Status: "ok",
		Vault:  vaultName,
		Source: req.Source,
	})
}
