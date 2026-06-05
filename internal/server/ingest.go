package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
)

// ── Request/Response types ────────────────────────────────────────────────────

type ingestRequest struct {
	Vault   string   `json:"vault"`
	Content string   `json:"content"`
	Source  string   `json:"source"`
	Tags    []string `json:"tags,omitempty"`
}

type ingestResponse struct {
	Status     string `json:"status"`
	Vault      string `json:"vault"`
	Source     string `json:"source"`
	ChunkCount int    `json:"chunk_count"`
}

// ── POST /v1/ingest ───────────────────────────────────────────────────────────

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024) // 10 MB limit

	var req ingestRequest
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
		// Auto-provision if not found
		idx = s.provisionVault(r.Context(), vaultName)
		if idx == nil {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("vault %q not found and could not be provisioned", vaultName))
			return
		}
	}

	// Perform the ingest
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if err := idx.Ingest(ctx, req.Content, req.Source, req.Tags); err != nil {
		s.log(r.Context()).Error("ingest failed", "vault", vaultName, "source", req.Source, "error", err)
		writeError(w, 502, "INGEST_FAILED", fmt.Sprintf("ingest failed: %s", err))
		return
	}

	// Get updated chunk count
	_, chunkCount, _, _, _, _ := idx.Stats()

	writeJSON(w, 200, ingestResponse{
		Status:     "ok",
		Vault:      vaultName,
		Source:     req.Source,
		ChunkCount: chunkCount,
	})
}

// ── Vault provisioning ────────────────────────────────────────────────────────

// provisionVault creates a new vault at runtime with a Qdrant collection
// and indexer. Returns nil on failure (errors are logged, not fatal).
func (s *Server) provisionVault(ctx context.Context, name string) *indexer.Indexer {
	if !config.ValidVaultName(name) {
		s.log(ctx).Warn("cannot provision vault: invalid name", "vault", name)
		return nil
	}

	// Determine vault path: sibling of existing vaults' parent directory
	var basePath string
	if s.cfg.IsMultiTenant() {
		// Use the first configured vault's parent as base
		for _, vc := range s.cfg.Vaults {
			basePath = filepath.Dir(vc.Path)
			break
		}
	} else if s.cfg.VaultPath != "" {
		basePath = filepath.Dir(s.cfg.VaultPath)
	}
	if basePath == "" {
		basePath = "."
	}
	vaultPath := filepath.Join(basePath, "agent-vaults", name)

	// Create directory
	if err := os.MkdirAll(vaultPath, 0755); err != nil {
		s.log(ctx).Error("failed to create vault directory", "vault", name, "path", vaultPath, "error", err)
		return nil
	}

	// Connect to Qdrant with vault-specific collection
	collectionName := fmt.Sprintf("ragamuffin_%s", name)
	qdrantCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	qc, err := qdrant.New(qdrantCtx, s.cfg.QdrantURL, collectionName, uint64(s.cfg.EmbeddingDims))
	if err != nil {
		s.log(ctx).Error("failed to connect to Qdrant for provisioned vault",
			"vault", name, "collection", collectionName, "error", err)
		os.RemoveAll(vaultPath)
		return nil
	}

	// Create indexer (share server-wide embedder, or the per-vault one if configured)
	ec := s.embeddingFor(ctx)
	idx := indexer.New(vaultPath, qc, ec, s.log(ctx).With("vault", name))
	idx.SetChunkMaxTokens(s.cfg.ChunkMaxTokens)

	// Register with manager
	if err := s.indexers.Add(name, idx, qc); err != nil {
		s.log(ctx).Error("failed to register provisioned vault indexer", "vault", name, "error", err)
		qc.Close()
		os.RemoveAll(vaultPath)
		return nil
	}

	// Store the vault config so vaultPathFromContext works
	s.mu.Lock()
	if s.cfg.Vaults == nil {
		s.cfg.Vaults = make(map[string]*config.VaultConfig)
	}
	s.cfg.Vaults[name] = &config.VaultConfig{Path: vaultPath}
	s.mu.Unlock()

	s.log(ctx).Info("vault provisioned", "vault", name, "path", vaultPath, "collection", collectionName)
	return idx
}


