package pruner

import (
	"context"
	"time"

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
// and marks them as needs_review.
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

	marked := 0
	for _, pt := range points {
		pointID := pt.GetId().GetUuid()
		if pointID == "" {
			continue
		}
		if err := p.updateFactStatus(ctx, pointID, "needs_review"); err != nil {
			p.logger.Error("staleScan: failed to mark fact", "point_id", pointID, "error", err)
			continue
		}
		marked++
	}

	p.logger.Info("staleScan complete", "found", len(points), "marked", marked)
	if marked > 0 {
		p.RecordFlagged(marked)
	}
}
