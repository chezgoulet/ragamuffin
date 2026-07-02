package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embeddedstore"
	"github.com/chezgoulet/ragamuffin/internal/events"
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
		if !s.cfg.AutoProvisionVaults {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("vault %q not found and auto-provisioning is disabled", vaultName))
			return
		}
		// Auto-provision requires write access
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

	// Perform the ingest
	// Ingest timeout: use env var RAGAMUFFIN_INGEST_SERVER_TIMEOUT or default 120s
	ingestTimeout := 120 * time.Second
	if envStr := os.Getenv("RAGAMUFFIN_INGEST_SERVER_TIMEOUT"); envStr != "" {
		if d, err := time.ParseDuration(envStr); err == nil {
			ingestTimeout = d
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), ingestTimeout)
	defer cancel()

	if err := idx.Ingest(ctx, req.Content, req.Source, req.Tags, nil); err != nil {
		s.log(r.Context()).Error("ingest failed", "vault", vaultName, "source", req.Source, "error", err)
		writeError(w, 502, "INGEST_FAILED", fmt.Sprintf("ingest failed: %s", err))
		return
	}

	// Emit vault.file.changed event
	s.emitter.Emit(events.TypeFileChanged, events.FileChangedData{
		Path:   req.Source,
		Action: "created",
		Size:   int64(len(req.Content)),
	})

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

// newProvisionedStore opens a vector store for one collection of a vault
// being provisioned at runtime, honoring RAGAMUFFIN_VECTOR_STORE exactly like
// startup vault construction (cmd/ragamuffin newVaultDriver): "embedded" gets
// a per-collection SQLite file derived from EmbeddedDBPath; anything else
// connects to Qdrant.
func (s *Server) newProvisionedStore(ctx context.Context, collectionName string, vectorSize uint64) (qdrant.FactStore, error) {
	if s.cfg.VectorStore == "embedded" {
		path := s.cfg.EmbeddedDBPath
		if path != "" {
			ext := filepath.Ext(path)
			path = fmt.Sprintf("%s_%s%s", path[:len(path)-len(ext)], collectionName, ext)
		}
		return embeddedstore.Open(embeddedstore.Config{
			Path:       path,
			Collection: collectionName,
			VectorSize: vectorSize,
			Logger:     s.log(ctx),
		})
	}
	qdrantCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return qdrant.New(qdrantCtx, s.cfg.QdrantURL, collectionName, vectorSize)
}

// provisionVault creates a new vault at runtime with its vector-store
// collections and indexer. Returns nil on failure (errors are logged, not
// fatal).
func (s *Server) provisionVault(ctx context.Context, name string) *indexer.Indexer {
	if !config.ValidVaultName(name) {
		s.log(ctx).Warn("cannot provision vault: invalid name", "vault", name)
		return nil
	}

	// Determine vault path
	var vaultPath string
	if s.cfg.VaultsRoot != "" {
		// Use VaultsRoot directly (same convention as explicit vault paths at config.go:466)
		vaultPath = filepath.Join(s.cfg.VaultsRoot, name)
	} else {
		// Derive basePath from existing vaults: sibling of vaults' parent directory
		var basePath string
		if s.cfg.IsMultiTenant() {
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
		vaultPath = filepath.Join(basePath, "agent-vaults", name)
	}

	// Create directory
	if err := os.MkdirAll(vaultPath, 0755); err != nil {
		s.log(ctx).Error("failed to create vault directory", "vault", name, "path", vaultPath, "error", err)
		return nil
	}

	// Open the vault-specific chunk collection on the configured backend
	collectionName := fmt.Sprintf("ragamuffin_%s", name)
	chunkVectorSize := uint64(s.cfg.EmbeddingDims)
	if s.cfg.ChunkVectorSize > 0 {
		chunkVectorSize = s.cfg.ChunkVectorSize
	}
	qc, err := s.newProvisionedStore(ctx, collectionName, chunkVectorSize)
	if err != nil {
		s.log(ctx).Error("failed to open vector store for provisioned vault",
			"vault", name, "collection", collectionName, "error", err)
		os.RemoveAll(vaultPath)
		return nil
	}

	// Create vault-specific facts collection on the same backend
	factsCollectionName := fmt.Sprintf("ragamuffin_%s_facts", name)
	factsQc, err := s.newProvisionedStore(ctx, factsCollectionName, s.cfg.FactsVectorSize)
	if err != nil {
		s.log(ctx).Error("failed to create facts collection for provisioned vault",
			"vault", name, "collection", factsCollectionName, "error", err)
		qc.Close()
		os.RemoveAll(vaultPath)
		return nil
	}
	s.indexers.AddFactClient(name, factsQc)

	// Create indexer (share server-wide embedder, or the per-vault one if configured)
	ec := s.embeddingFor(ctx)
	idx := indexer.New(vaultPath, name, qc, ec, s.log(ctx).With("vault", name))
	idx.SetChunkMaxTokens(s.cfg.ChunkMaxTokens)

	// Register with manager
	if err := s.indexers.Add(name, idx, qc); err != nil {
		s.log(ctx).Error("failed to register provisioned vault indexer", "vault", name, "error", err)
		qc.Close()
		factsQc.Close()
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
