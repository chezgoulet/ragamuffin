package server

import (
	"net/http"
	"strconv"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
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

	type autoTuneResponse struct {
		DryRun         bool                            `json:"dry_run"`
		Recommendations []logstore.ThresholdRecommendation `json:"recommendations"`
		SampleCount    int                             `json:"sample_count"`
		Applied        bool                            `json:"applied"`
	}

	resp := autoTuneResponse{
		DryRun:         dryRun,
		Recommendations: recs,
		SampleCount:    len(recs),
		Applied:        false,
	}

	// Apply recommendations when dry_run=false
	if !dryRun && s.pruner != nil {
		for _, rec := range recs {
			if rec.Recommended == rec.Current || rec.Recommended == 0 {
				continue
			}
			switch rec.ReasonType {
			case "conflict":
				// Conflict threshold is embedded in the scan logic; log intent
				s.log(r.Context()).Info("auto-tune: conflict threshold recommendation",
					"current", rec.Current, "recommended", rec.Recommended)
			case "low_confidence":
				s.pruner.SetLowConfidenceThreshold(rec.Recommended)
				resp.Applied = true
				s.log(r.Context()).Info("auto-tune: applied low_confidence threshold",
					"from", rec.Current, "to", rec.Recommended)
			}
		}
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
		LowConfidenceThreshold float64 `json:"low_confidence_threshold"`
		ImportanceThreshold    float64 `json:"importance_threshold"`
	}
	writeJSON(w, 200, configResponse{
		Enabled:                cfg.Enabled,
		StaleDays:              cfg.StaleDays,
		ConflictSampleSize:     cfg.ConflictSampleSize,
		LowConfidenceThreshold: cfg.LowConfidenceThreshold,
		ImportanceThreshold:    cfg.ImportanceThreshold,
	})
}
