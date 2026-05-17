package pruner

import (
	"context"

	pb "github.com/qdrant/go-client/qdrant"
)

// lowConfidenceScan finds active facts with confidence below the threshold
// and marks them as needs_review. This is a one-time scan (not recurring)
// run at startup to catch facts created before confidence tracking existed.
func (p *Pruner) lowConfidenceScan(ctx context.Context) {
	if p.facts == nil {
		p.logger.Warn("lowConfidenceScan: no facts client available")
		return
	}

	threshold := p.cfg.LowConfidenceThreshold
	if threshold <= 0 {
		threshold = 0.5
	}

	lt := threshold - 0.001 // strict less-than to avoid edge of 0.5

	filter := &pb.Filter{
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
						Key: "confidence",
						Range: &pb.Range{
							Lt: &lt,
						},
					},
				},
			},
		},
	}

	points, err := p.scrollFilteredFacts(ctx, filter, 0)
	if err != nil {
		p.logger.Error("lowConfidenceScan: query failed", "error", err)
		return
	}

	if len(points) == 0 {
		p.logger.Debug("lowConfidenceScan: no low-confidence facts found")
		return
	}

	marked := 0
	for _, pt := range points {
		pointID := pt.GetId().GetUuid()
		if pointID == "" {
			continue
		}
		if err := p.updateFactStatus(ctx, pointID, "needs_review"); err != nil {
			p.logger.Error("lowConfidenceScan: failed to mark fact", "point_id", pointID, "error", err)
			continue
		}
		marked++
	}

	p.logger.Info("lowConfidenceScan complete", "found", len(points), "marked", marked)
	if marked > 0 {
		p.RecordFlagged(marked)
	}
}
