package server

import (
	"net/http"
)

// ── GET /v1/extraction/stats ────────────────────────────────────────────────

func (s *Server) handleExtractionStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	if s.extractor == nil {
		writeError(w, 503, "UNAVAILABLE", "extraction pipeline not configured")
		return
	}

	stats := s.extractor.Stats()
	writeJSON(w, 200, stats)
}
