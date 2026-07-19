package server

import (
	"context"
	"net/http"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/graph"
)

// handleGraphCommunityDetect (re)computes community structure for the vault and,
// when an LLM is available and community summaries are enabled, generates
// natural-language summaries. POST /v1/graph/community/detect
func (s *Server) handleGraphCommunityDetect(w http.ResponseWriter, r *http.Request) {
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
	if !s.cfg.GraphCommunityEnabled {
		writeError(w, 503, "UNAVAILABLE", "community detection is not enabled")
		return
	}

	vault := s.graphVault(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	comms, err := s.graph.DetectCommunities(ctx, vault)
	if err != nil {
		s.log(r.Context()).Error("community detect failed", "vault", vault, "error", err)
		writeError(w, 500, "INTERNAL_ERROR", "community detection failed")
		return
	}

	summarized := 0
	if s.graphCommunitySummarizer != nil {
		if n, serr := s.graphCommunitySummarizer.SummarizeVault(ctx, vault); serr != nil {
			s.log(r.Context()).Warn("community summarize failed", "vault", vault, "error", serr)
		} else {
			summarized = n
		}
	}

	if s.emitter != nil {
		s.emitter.Emit(events.TypeGraphCommunityDetected, events.GraphCommunityDetectedData{
			Vault: vault, Communities: len(comms), Summarized: summarized,
		})
	}

	writeJSON(w, 200, map[string]any{
		"vault":       vault,
		"communities": len(comms),
		"summarized":  summarized,
	})
}

// handleGraphCommunity returns a single community by id.
// GET /v1/graph/community/{id}
func (s *Server) handleGraphCommunity(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 400, "INVALID_REQUEST", "community id is required")
		return
	}
	c, err := s.graph.GetCommunity(r.Context(), id)
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "community query failed")
		return
	}
	if c == nil {
		writeError(w, 404, "NOT_FOUND", "community not found")
		return
	}
	writeJSON(w, 200, c)
}

// handleGraphCommunities lists all communities in the vault, largest first.
// GET /v1/graph/communities
func (s *Server) handleGraphCommunities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	if s.graph == nil {
		writeError(w, 503, "UNAVAILABLE", "temporal graph is not enabled")
		return
	}
	vault := s.graphVault(r.Context())
	comms, err := s.graph.Communities(r.Context(), vault)
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "community list failed")
		return
	}
	if comms == nil {
		comms = []graph.Community{}
	}
	writeJSON(w, 200, map[string]any{
		"vault":       vault,
		"count":       len(comms),
		"communities": comms,
	})
}
