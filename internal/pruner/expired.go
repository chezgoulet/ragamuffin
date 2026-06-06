package pruner

import (
	"context"
	"time"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// expiredFilter builds the Qdrant filter for facts past their valid_until:
// status = "active" AND valid_until_unix > 0 AND valid_until_unix < now.
func expiredFilter(nowUnix float64) *pb.Filter {
	min := float64(0)
	// NotEq: 0 is not directly expressible in Qdrant Range proto — use Gt.
	// We check: valid_until_unix > 0 AND valid_until_unix < now.
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
						Key: "valid_until_unix",
						Range: &pb.Range{
							Gt: &min, // valid_until_unix > 0 (is set)
						},
					},
				},
			},
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "valid_until_unix",
						Range: &pb.Range{
							Lt: &nowUnix, // valid_until_unix < now (expired)
						},
					},
				},
			},
		},
	}
}

// expiredScan queries active facts whose valid_until is in the past
// and marks them as needs_review with reason "expired".
func (p *Pruner) expiredScan(ctx context.Context) {
	if p.facts == nil {
		p.logger.Warn("expiredScan: no facts client available")
		return
	}

	now := float64(time.Now().UTC().Unix())
	filter := expiredFilter(now)

	points, err := p.scrollFilteredFacts(ctx, filter, 0)
	if err != nil {
		p.logger.Error("expiredScan query failed", "error", err)
		return
	}

	if len(points) == 0 {
		p.logger.Debug("expiredScan: no expired facts found")
		return
	}

	marked := 0
	for _, pt := range points {
		pointID := pt.GetId().GetUuid()
		if pointID == "" {
			continue
		}

		key, _ := qutil.GetPayloadString(pt.GetPayload(), "fact_key")

		if err := p.updateFactStatus(ctx, pointID, "needs_review"); err != nil {
			p.logger.Error("expiredScan: failed to mark fact", "point_id", pointID, "error", err)
			continue
		}
		if p.cfg.FlagCallback != nil {
			p.cfg.FlagCallback(key, "expired")
		}
		marked++
	}

	p.logger.Info("expiredScan complete", "found", len(points), "marked", marked)
	if marked > 0 {
		p.RecordFlagged(marked)
	}
}
