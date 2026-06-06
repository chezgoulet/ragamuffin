package server

import (
	"net/http"
	"strconv"

	"github.com/chezgoulet/ragamuffin/internal/auth"
)

// handlePrunerAutoTune returns threshold recommendations based on
// review resolution history. Supports ?dry_run=true (default) to preview
// without applying. When dry_run=false, adjustments are applied to the
// server's in-memory pruner config.
//
// GET /v1/pruner/auto-tune?dry_run=true
func (s *Server) handlePrunerAutoTune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	// Require admin access
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("admin") {
		writeError(w, 403, "FORBIDDEN", "admin access required")
		return
	}

	dryRun := true
	if d := r.URL.Query().Get("dry_run"); d != "" {
		parsed, err := strconv.ParseBool(d)
		if err == nil {
			dryRun = parsed
		}
	}

	if s.pruner == nil || !s.pruner.Enabled() {
		writeError(w, 503, "PRUNER_DISABLED", "pruner is not enabled")
		return
	}

	if s.logStore == nil {
		writeError(w, 503, "NO_LOGSTORE", "logstore not available")
		return
	}

	recs, err := s.logStore.ThresholdRecommendations(r.Context(), dryRun)
	if err != nil {
		s.log(r.Context()).Error("auto-tune query failed", "error", err)
		writeError(w, 500, "QUERY_FAILED", "failed to query resolution history")
		return
	}

	// Per-recommendation response type with applied status
	type recEntry struct {
		ReasonType     string  `json:"reason_type"`
		Current        float64 `json:"current"`
		Recommended    float64 `json:"recommended"`
		AcceptRate     float64 `json:"accept_rate"`
		SampleSize     int     `json:"sample_size"`
		Rationale      string  `json:"rationale"`
		Applied        bool    `json:"applied"`
		Note           string  `json:"note,omitempty"`
	}

	type autoTuneResponse struct {
		DryRun         bool       `json:"dry_run"`
		Recommendations []recEntry `json:"recommendations"`
		SampleCount    int        `json:"sample_count"`
	}

	// Build per-recommendation entries
	entries := make([]recEntry, 0, len(recs))
	for _, rec := range recs {
		e := recEntry{
			ReasonType:  rec.ReasonType,
			Current:     rec.Current,
			Recommended: rec.Recommended,
			AcceptRate:  rec.AcceptRate,
			SampleSize:  rec.SampleSize,
			Rationale:   rec.Rationale,
		}

		// Apply recommendations when dry_run=false
		if !dryRun && s.pruner != nil && rec.Recommended != rec.Current && rec.Recommended != 0 {
			switch rec.ReasonType {
			case "low_confidence":
				s.pruner.SetLowConfidenceThreshold(rec.Recommended)
				e.Applied = true
				s.log(r.Context()).Info("auto-tune: applied low_confidence threshold",
					"from", rec.Current, "to", rec.Recommended)
			case "conflict":
				s.pruner.SetConflictThreshold(rec.Recommended)
				e.Applied = true
				s.log(r.Context()).Info("auto-tune: applied conflict threshold",
					"from", rec.Current, "to", rec.Recommended)
			default:
				e.Note = "threshold changes for this reason type must be applied manually"
			}
		}

		entries = append(entries, e)
	}

	resp := autoTuneResponse{
		DryRun:         dryRun,
		Recommendations: entries,
		SampleCount:    len(entries),
	}

	writeJSON(w, 200, resp)
}

// handlePrunerConfig exposes current pruner configuration.
// GET /v1/pruner/config
func (s *Server) handlePrunerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	if s.pruner == nil {
		writeError(w, 503, "PRUNER_DISABLED", "pruner is not enabled")
		return
	}

	cfg := s.pruner.Config()

	type configResponse struct {
		Enabled                bool    `json:"enabled"`
		StaleDays              int     `json:"stale_days"`
		ConflictSampleSize     int     `json:"conflict_sample_size"`
		ConflictThreshold      float64 `json:"conflict_threshold"`
		LowConfidenceThreshold float64 `json:"low_confidence_threshold"`
		ImportanceThreshold    float64 `json:"importance_threshold"`
	}
	writeJSON(w, 200, configResponse{
		Enabled:                cfg.Enabled,
		StaleDays:              cfg.StaleDays,
		ConflictSampleSize:     cfg.ConflictSampleSize,
		ConflictThreshold:      cfg.ConflictThreshold,
		LowConfidenceThreshold: cfg.LowConfidenceThreshold,
		ImportanceThreshold:    cfg.ImportanceThreshold,
	})
}
