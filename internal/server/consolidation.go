package server

import (
	"context"
	"net/http"

	"github.com/chezgoulet/ragamuffin/internal/consolidation"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
)

// logstoreSessionSource adapts *logstore.Store to consolidation.SessionSource.
type logstoreSessionSource struct {
	store *logstore.Store
}

// NewLogstoreSessionSource wraps a logstore for use by the consolidator.
func NewLogstoreSessionSource(store *logstore.Store) consolidation.SessionSource {
	return &logstoreSessionSource{store: store}
}

func (a *logstoreSessionSource) RecentSessions(ctx context.Context, vault string, limit int) ([]consolidation.Session, error) {
	sessions, err := a.store.ListSessions(ctx, vault, limit, 0)
	if err != nil {
		return nil, err
	}
	out := make([]consolidation.Session, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, consolidation.Session{
			ID:        s.ID,
			Vault:     s.Vault,
			TurnCount: s.TurnCount,
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
		})
	}
	return out, nil
}

func (a *logstoreSessionSource) Transcript(ctx context.Context, sessionID string, n int) ([]consolidation.Turn, error) {
	_, turns, err := a.store.GetSession(ctx, sessionID, n)
	if err != nil {
		return nil, err
	}
	out := make([]consolidation.Turn, 0, len(turns))
	for _, t := range turns {
		out = append(out, consolidation.Turn{Role: t.Role, Content: t.Content})
	}
	return out, nil
}

// SetConsolidator attaches the consolidation worker so /v1/consolidation/status
// can report on it.
func (s *Server) SetConsolidator(c *consolidation.Consolidator) {
	s.consolidator = c
}

// handleConsolidationStatus reports consolidation worker stats.
// GET /v1/consolidation/status
func (s *Server) handleConsolidationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}
	if s.consolidator == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, 200, s.consolidator.Snapshot())
}
