package server

import (
	"context"
	"net/http"
)

// handleLinks returns all outbound links from a file.
// GET /v1/links?path=<vault-path>
// GET /vault/{name}/v1/links?path=<vault-path>
func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, 400, "INVALID_REQUEST", "path query parameter is required")
		return
	}

	vault := vaultFromContext(r.Context())
	if vault == "" {
		vault = ""
	}

	links, err := s.logStore.GetOutboundLinks(r.Context(), path, vault)
	if err != nil {
		s.log(r.Context()).Error("links: outbound query failed", "path", path, "error", err)
		writeError(w, 500, "INTERNAL", "failed to query links")
		return
	}

	writeJSON(w, 200, map[string]any{
		"path":  path,
		"links": links,
	})
}

// handleBacklinks returns all inbound links to a file.
// GET /v1/links/backlinks?path=<vault-path>
// GET /vault/{name}/v1/links/backlinks?path=<vault-path>
func (s *Server) handleBacklinks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, 400, "INVALID_REQUEST", "path query parameter is required")
		return
	}

	vault := vaultFromContext(r.Context())
	if vault == "" {
		vault = ""
	}

	links, err := s.logStore.GetInboundLinks(r.Context(), path, vault)
	if err != nil {
		s.log(r.Context()).Error("links: inbound query failed", "path", path, "error", err)
		writeError(w, 500, "INTERNAL", "failed to query backlinks")
		return
	}

	writeJSON(w, 200, map[string]any{
		"path":     path,
		"backlinks": links,
	})
}

// handleLinkGraph returns a breadth-limited link graph.
// GET /v1/links/graph?path=<vault-path>&depth=2
// GET /vault/{name}/v1/links/graph?path=<vault-path>&depth=2
func (s *Server) handleLinkGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, 400, "INVALID_REQUEST", "path query parameter is required")
		return
	}

	depth := r.URL.Query().Get("depth")

	vault := vaultFromContext(r.Context())
	if vault == "" {
		vault = ""
	}

	graph, err := s.logStore.GetLinkGraph(r.Context(), path, depth, vault)
	if err != nil {
		s.log(r.Context()).Error("links: graph query failed", "path", path, "error", err)
		writeError(w, 500, "INTERNAL", "failed to query link graph")
		return
	}

	writeJSON(w, 200, graph)
}

// handleVaultLinks is a pass-through to handleLinks with vault context.
func (s *Server) handleVaultLinks(w http.ResponseWriter, r *http.Request) {
	s.handleLinks(w, r)
}

// handleVaultBacklinks is a pass-through to handleBacklinks with vault context.
func (s *Server) handleVaultBacklinks(w http.ResponseWriter, r *http.Request) {
	s.handleBacklinks(w, r)
}

// handleVaultLinkGraph is a pass-through to handleLinkGraph with vault context.
func (s *Server) handleVaultLinkGraph(w http.ResponseWriter, r *http.Request) {
	s.handleLinkGraph(w, r)
}

// enrichChunksWithLinks adds link information to recall results when
// ?enrich_links=true is queried. Returns enriched results as maps (so links
// can be injected as an extra field without modifying recallResult).
func (s *Server) enrichChunksWithLinks(ctx context.Context, results []recallResult, vault string) ([]map[string]any, error) {
	enriched := make([]map[string]any, len(results))
	for i, r := range results {
		m := map[string]any{
			"chunk_id":         r.ChunkID,
			"text":             r.Text,
			"first_paragraph":  r.FirstParagraph,
			"source_file":      r.SourceFile,
			"header":           r.Header,
			"chunk_index":      r.ChunkIndex,
			"score":            r.Score,
			"file_last_updated": r.FileLastUpdated,
		}

		if r.SourceFile != "" {
			links, err := s.logStore.GetOutboundLinks(ctx, r.SourceFile, vault)
			if err != nil {
				s.log(ctx).Warn("links: enrich failed", "source", r.SourceFile, "error", err)
			} else if len(links) > 0 {
				m["links"] = links
			}
		}

		enriched[i] = m
	}
	return enriched, nil
}

// handleEnrichLinks is used by /recall handlers.
// Parses ?enrich_links=true from the query string.
func enrichLinksEnabled(r *http.Request) bool {
	return r.URL.Query().Get("enrich_links") == "true"
}
