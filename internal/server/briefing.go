package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/qdrant/go-client/qdrant"
)

// briefingResponse is the structured landing page for a returning agent.
type briefingResponse struct {
	Version       string                  `json:"version"`
	Commit        string                  `json:"commit"`
	BuildDate     string                  `json:"build_date"`
	GoVersion     string                  `json:"go_version"`
	StartedAt     string                  `json:"started_at"`
	UptimeSeconds int                     `json:"uptime_seconds"`
	Vaults        []briefingVault         `json:"vaults"`
	ReviewQueue   *briefingReviewSummary  `json:"review_queue,omitempty"`
	InboxCount    int                     `json:"inbox_count,omitempty"`
	LastSession   *briefingSessionSummary `json:"last_session,omitempty"`
}

type briefingVault struct {
	Name         string  `json:"name"`
	Path         string  `json:"path"`
	IndexedFiles int     `json:"indexed_files"`
	TotalChunks  int     `json:"total_chunks"`
	LastIndexed  *string `json:"last_indexed"`
	Indexing     bool    `json:"indexing"`
}

type briefingReviewSummary struct {
	Total    int            `json:"total"`
	ByReason map[string]int `json:"by_reason,omitempty"`
}

type briefingSessionSummary struct {
	ID        string `json:"id"`
	Vault     string `json:"vault"`
	AgentID   string `json:"agent_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// handleBriefing returns a structured landing page for a returning agent.
// GET /v1/briefing?agent_id=...
// GET /vault/{name}/v1/briefing?agent_id=...
func (s *Server) handleBriefing(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")

	// ── Server identity ──
	resp := briefingResponse{
		Version:       Version,
		Commit:        Commit,
		BuildDate:     BuildDate,
		GoVersion:     GoVersion,
		StartedAt:     s.started.Format(time.RFC3339),
		UptimeSeconds: int(time.Since(s.started).Seconds()),
	}

	// ── Accessible vaults ──
	claims := auth.ClaimsFromContext(r.Context())
	var vaultPaths []string

	s.indexers.ForEach(func(name string, idx *indexer.Indexer) {
		if claims != nil && !claims.HasVaultAccess(name) {
			return
		}
		fileCount, chunkCount, lastIndexed, indexing, _, _ := idx.Stats()
		var lastIndexedStr *string
		if !lastIndexed.IsZero() {
			f := lastIndexed.Format(time.RFC3339)
			lastIndexedStr = &f
		}
		vc := s.cfg.Vaults[name]
		vp := ""
		if vc != nil {
			vp = vc.Path
		}
		resp.Vaults = append(resp.Vaults, briefingVault{
			Name: name, Path: vp,
			IndexedFiles: fileCount, TotalChunks: chunkCount,
			LastIndexed: lastIndexedStr, Indexing: indexing,
		})
		vaultPaths = append(vaultPaths, vp)
	})

	// ── Review queue count (single scroll page) ──
	filter := s.needsReviewFilter()
	if filter != nil {
		points, err := s.facts.ScrollFiltered(r.Context(), s.factsCollectionFor(r.Context()), filter, 10, "")
		if err == nil {
			resp.ReviewQueue = &briefingReviewSummary{Total: len(points)}
		}
	}

	// ── Inbox count across accessible vaults ──
	for _, vp := range vaultPaths {
		if vp == "" {
			continue
		}
		inboxPath := filepath.Join(vp, "_inbox")
		entries, err := os.ReadDir(inboxPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				resp.InboxCount++
			}
		}
	}

	// ── Last session for agent ──
	if s.logStore != nil && len(vaultPaths) > 0 {
		for _, vp := range vaultPaths {
			var vaultName string
			for name, vc := range s.cfg.Vaults {
				if vc.Path == vp {
					vaultName = name
					break
				}
			}
			if vaultName == "" {
				continue
			}
			sessions, err := s.logStore.ListSessions(r.Context(), vaultName, 3, 0)
			if err != nil || len(sessions) == 0 {
				continue
			}
			for _, sess := range sessions {
				if agentID != "" && sess.AgentID != agentID {
					continue
				}
				resp.LastSession = &briefingSessionSummary{
					ID: sess.ID, Vault: sess.Vault,
					AgentID: sess.AgentID, CreatedAt: sess.CreatedAt,
					UpdatedAt: sess.UpdatedAt,
				}
				break
			}
			if resp.LastSession != nil {
				break
			}
		}
	}

	writeJSON(w, 200, resp)
}

// needsReviewFilter returns a Qdrant filter for facts with status=needs_review.
func (s *Server) needsReviewFilter() *qdrant.Filter {
	return &qdrant.Filter{
		Must: []*qdrant.Condition{{
			ConditionOneOf: &qdrant.Condition_Field{
				Field: &qdrant.FieldCondition{
					Key: "status",
					Match: &qdrant.Match{
						MatchValue: &qdrant.Match_Keyword{Keyword: "needs_review"},
					},
				},
			},
		}},
	}
}
