package pruner

import (
	"context"
	"time"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// staleFilter builds the Qdrant filter for stale facts:
// status = "active" AND ttl_days > 0 AND expires_at_unix < now.
func staleFilter(nowUnix float64) *pb.Filter {
	minTTL := float64(0)
	return &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "status",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: "active",
							},
						},
					},
				},
			},
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "ttl_days",
						Range: &pb.Range{
							Gt: &minTTL, // ttl_days > 0
						},
					},
				},
			},
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "expires_at_unix",
						Range: &pb.Range{
							Lt: &nowUnix, // expires_at_unix < now
						},
					},
				},
			},
		},
	}
}

// staleScan queries active facts whose expires_at_unix is in the past
// and marks them as needs_review. Facts referenced by graph edges
// (supersedes, refines, contradicts, supports) are skipped — their
// relevance is tied to the referencing fact's lifecycle.
func (p *Pruner) staleScan(ctx context.Context) {
	if p.facts == nil {
		p.logger.Warn("staleScan: no facts client available")
		return
	}

	now := float64(time.Now().UTC().Unix())
	filter := staleFilter(now)

	points, err := p.scrollFilteredFacts(ctx, filter, 0)
	if err != nil {
		p.logger.Error("staleScan query failed", "error", err)
		return
	}

	if len(points) == 0 {
		p.logger.Debug("staleScan: no stale facts found")
		return
	}

	// Collect keys referenced by active graph edges so we don't prune them.
	referencedKeys, err := p.collectReferencedKeys(ctx)
	if err != nil {
		p.logger.Warn("staleScan: failed to collect referenced keys, proceeding without edge check", "error", err)
		referencedKeys = nil
	}

	threshold := p.cfg.ImportanceThreshold
	skipped := 0
	marked := 0
	skippedEdges := 0
	for _, pt := range points {
		pointID := pt.GetId().GetUuid()
		if pointID == "" {
			continue
		}

		// Skip facts referenced by active graph edges first
		key, _ := qutil.GetPayloadString(pt.GetPayload(), "fact_key")
		if key != "" && referencedKeys[key] {
			skippedEdges++
			continue
		}

		// If importance threshold is set, skip facts above the threshold
		if threshold > 0 {
			importance := computeImportance(pt.GetPayload())
			if importance >= threshold {
				skipped++
				p.logger.Debug("staleScan: skipping high-importance fact",
					"point_id", pointID, "importance", importance, "threshold", threshold)
				continue
			}
		}

		if err := p.updateFactStatus(ctx, pointID, "needs_review"); err != nil {
			p.logger.Error("staleScan: failed to mark fact", "point_id", pointID, "error", err)
			continue
		}
		if p.cfg.FlagCallback != nil {
			p.cfg.FlagCallback(key, "stale")
		}
		marked++
	}

	if skipped > 0 {
		p.logger.Info("staleScan importance filter", "skipped", skipped, "threshold", threshold)
	}

	p.logger.Info("staleScan complete", "found", len(points), "skipped_due_to_edges", skippedEdges, "marked", marked)
	if marked > 0 {
		p.RecordFlagged(marked)
	}
}

// collectReferencedKeys scrolls all active facts and returns the set of
// fact keys referenced by edge fields (supersedes, refines, contradicts,
// supports). Used by staleScan to avoid pruning facts that are part of
// the active graph.
func (p *Pruner) collectReferencedKeys(ctx context.Context) (map[string]bool, error) {
	// Scroll all active facts — we'll check edge fields in Go code
	// rather than building complex nested Qdrant filters for list non-emptiness.
	activeFilter := &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "status",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: "active",
							},
						},
					},
				},
			},
		},
	}

	points, err := p.scrollFilteredFacts(ctx, activeFilter, 0)
	if err != nil {
		return nil, err
	}

	keys := make(map[string]bool, len(points)/2)
	for _, pt := range points {
		payload := pt.GetPayload()
		if payload == nil {
			continue
		}

		if s, _ := qutil.GetPayloadString(payload, "supersedes"); s != "" {
			keys[s] = true
		}
		if s, _ := qutil.GetPayloadString(payload, "refines"); s != "" {
			keys[s] = true
		}
		for _, t := range qutil.GetPayloadStringList(payload, "contradicts") {
			if t != "" {
				keys[t] = true
			}
		}
		for _, t := range qutil.GetPayloadStringList(payload, "supports") {
			if t != "" {
				keys[t] = true
			}
		}
	}

	return keys, nil
}
