package server

import (
	"context"
	"net/http"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// ── GET /v1/facts/freshness ──────────────────────────────────────────────
//
// Librarian health check (#795). Returns the timestamp of the most recently
// written fact so an external monitor can alert when the librarian (the cron
// that writes completed kanban-card knowledge into Ragamuffin) goes silent.
//
// The query fetches a single point ordered by updated_at_unix descending, so
// it is O(1) regardless of how many facts exist. Returns:
//
//	{
//	  "last_write_at":     "2026-07-09T00:00:00Z", // RFC3339 of newest fact, "" if none
//	  "last_write_unix":   1752019200,             // epoch seconds, 0 if none
//	  "age_seconds":       1234,                   // seconds since newest write
//	  "threshold_seconds": 86400,                  // configured freshness threshold
//	  "stale":             false,                  // true when age_seconds > threshold
//	  "fact_count":        42,                     // total facts in collection (best-effort)
//	  "collection":        "ragamuffin_facts"
//	}
//
// A 200 with stale=false means healthy. The external cron is silent on a
// healthy response and only alerts (e.g. via Telegram) when stale=true.

// factFreshnessResponse is the JSON body for GET /v1/facts/freshness.
type factFreshnessResponse struct {
	LastWriteAt      string `json:"last_write_at"`
	LastWriteUnix    int64  `json:"last_write_unix"`
	AgeSeconds       int64  `json:"age_seconds"`
	ThresholdSeconds int64  `json:"threshold_seconds"`
	Stale            bool   `json:"stale"`
	FactCount        int64  `json:"fact_count"`
	Collection       string `json:"collection"`
}

func (s *Server) handleFactsFreshness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	qc := s.factsQdrantFor(ctx)
	collection := s.factsCollectionFor(ctx)
	if qc == nil {
		writeError(w, 503, "FACTS_UNAVAILABLE", "facts store not configured")
		return
	}

	threshold := s.cfg.FactsFreshnessThreshold
	if threshold <= 0 {
		threshold = 24 * time.Hour
	}
	thresholdSeconds := int64(threshold.Seconds())

	// Most-recently-written fact via order_by updated_at_unix desc, limit 1.
	// The real fact store is always a *qdrant.Client; use its ordered scroll
	// for an O(1) query. Mocks that don't implement it take the fallback path.
	limitOne := uint32(1)
	req := &pb.ScrollPoints{
		CollectionName: collection,
		WithPayload:    pb.NewWithPayload(true),
		OrderBy: &pb.OrderBy{
			Key:       "updated_at_unix",
			Direction: pb.Direction_Desc.Enum(),
		},
		Limit: &limitOne,
	}

	var points []*pb.RetrievedPoint
	var err error
	if cc, ok := qc.(*qdrant.Client); ok {
		points, err = cc.ScrollOrdered(ctx, req)
		if err != nil {
			s.log(ctx).Error("facts freshness: ordered scroll failed", "error", err)
			writeError(w, 500, "FRESHNESS_QUERY_FAILED", "failed to query most recent fact")
			return
		}
	} else {
		// Fallback for non-Client FactStore implementations: scan a single
		// (unordered) point. Best-effort — production always uses *Client.
		var fb []*pb.RetrievedPoint
		fb, err = qc.ScrollFiltered(ctx, collection, nil, 1, "")
		if err != nil {
			s.log(ctx).Error("facts freshness: scroll failed", "error", err)
			writeError(w, 500, "FRESHNESS_QUERY_FAILED", "failed to query most recent fact")
			return
		}
		points = fb
	}

	resp := factFreshnessResponse{
		LastWriteAt:      "",
		LastWriteUnix:    0,
		ThresholdSeconds: thresholdSeconds,
		Stale:            false,
		Collection:       collection,
	}

	if len(points) > 0 {
		payload := points[0].GetPayload()
		// Prefer the explicit RFC3339 timestamp; fall back to the unix field.
		if ts, ok := qutil.GetPayloadString(payload, "updated_at"); ok && ts != "" {
			resp.LastWriteAt = ts
		}
		if u, ok := qutil.GetPayloadFloat(payload, "updated_at_unix"); ok && u > 0 {
			resp.LastWriteUnix = int64(u)
			if resp.LastWriteAt == "" {
				resp.LastWriteAt = time.Unix(int64(u), 0).UTC().Format(time.RFC3339)
			}
		}
	}

	if resp.LastWriteUnix == 0 {
		// No facts at all — treat as the most-stale possible (always stale).
		resp.AgeSeconds = -1 // sentinel: never written
		resp.Stale = true
	} else {
		resp.AgeSeconds = int64(time.Since(time.Unix(resp.LastWriteUnix, 0)).Seconds())
		resp.Stale = resp.AgeSeconds > thresholdSeconds
	}

	// Best-effort total count (non-fatal if unsupported).
	if n, err := qc.Count(ctx); err == nil {
		resp.FactCount = int64(n)
	}

	writeJSON(w, 200, resp)
}
