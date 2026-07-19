package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/graph"
)

// graphVault resolves the vault for a temporal-graph request, defaulting to
// "default" for bare (single-tenant) endpoints.
func (s *Server) graphVault(ctx context.Context) string {
	if v := vaultFromContext(ctx); v != "" {
		return v
	}
	return "default"
}

// handleGraphIngest builds/extends the temporal graph for a vault from a text
// body. POST /v1/graph/ingest  { "text": "..." }
func (s *Server) handleGraphIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}
	if s.graph == nil {
		writeError(w, 503, "UNAVAILABLE", "temporal graph is not enabled")
		return
	}
	if s.graphExtractor == nil {
		writeError(w, 503, "UNAVAILABLE", "temporal graph ingest requires an LLM")
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if req.Text == "" {
		writeError(w, 400, "INVALID_REQUEST", "text is required")
		return
	}

	vault := s.graphVault(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	entities, edges, err := s.graphExtractor.IngestText(ctx, vault, req.Text)
	if err != nil {
		s.log(r.Context()).Error("graph ingest failed", "vault", vault, "error", err)
		writeError(w, 502, "UPSTREAM_ERROR", "graph extraction failed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"vault":    vault,
		"entities": entities,
		"edges":    edges,
	})
}

// handleGraphEntity returns an entity and its temporal edges.
// GET /v1/graph/entity/{id}?as_of=<RFC3339>
func (s *Server) handleGraphEntity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	if s.graph == nil {
		writeError(w, 503, "UNAVAILABLE", "temporal graph is not enabled")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, 400, "INVALID_REQUEST", "entity id is required")
		return
	}
	asOf, ok := parseAsOf(r.URL.Query().Get("as_of"))
	if !ok {
		writeError(w, 400, "INVALID_REQUEST", "as_of must be RFC3339")
		return
	}

	view, err := s.graph.EntityAsOf(r.Context(), id, asOf)
	if err != nil {
		s.log(r.Context()).Error("graph entity query failed", "id", id, "error", err)
		writeError(w, 500, "INTERNAL_ERROR", "graph query failed")
		return
	}
	if view == nil {
		writeError(w, 404, "NOT_FOUND", "entity not found")
		return
	}
	writeJSON(w, 200, view)
}

// handleGraphEdges returns temporal edges filtered by type/entity and as_of.
// GET /v1/graph/edges?type=...&entity=...&as_of=...&limit=...
func (s *Server) handleGraphEdges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	if s.graph == nil {
		writeError(w, 503, "UNAVAILABLE", "temporal graph is not enabled")
		return
	}
	asOf, ok := parseAsOf(r.URL.Query().Get("as_of"))
	if !ok {
		writeError(w, 400, "INVALID_REQUEST", "as_of must be RFC3339")
		return
	}

	q := graph.EdgeQuery{
		Vault:    s.graphVault(r.Context()),
		Type:     r.URL.Query().Get("type"),
		EntityID: r.URL.Query().Get("entity"),
		AsOf:     asOf,
	}
	edges, err := s.graph.Edges(r.Context(), q)
	if err != nil {
		s.log(r.Context()).Error("graph edges query failed", "error", err)
		writeError(w, 500, "INTERNAL_ERROR", "graph query failed")
		return
	}
	if edges == nil {
		edges = []graph.Edge{}
	}
	writeJSON(w, 200, map[string]any{
		"vault": q.Vault,
		"as_of": asOf,
		"count": len(edges),
		"edges": edges,
	})
}

// handleGraphStats returns entity/edge counts for the vault.
// GET /v1/graph/stats
func (s *Server) handleGraphStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	if s.graph == nil {
		writeError(w, 503, "UNAVAILABLE", "temporal graph is not enabled")
		return
	}
	vault := s.graphVault(r.Context())
	entities, edges, invalidated, err := s.graph.Stats(r.Context(), vault)
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "graph stats failed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"vault":             vault,
		"entities":          entities,
		"edges":             edges,
		"invalidated_edges": invalidated,
	})
}

// parseAsOf validates an optional as_of query param. Returns (normalized, true)
// for empty or valid RFC3339 input, and ("", false) for malformed input.
func parseAsOf(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
}
