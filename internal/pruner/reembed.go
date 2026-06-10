package pruner

import (
	"context"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	pb "github.com/qdrant/go-client/qdrant"
)

// reembedScan scans all facts for points with zero-valued vectors and
// re-embeds them using the configured embedder. This makes zero-vector
// facts (created when embedding failed transiently) a temporary degraded
// state rather than a permanent data quality issue.
//
// The fallback to zero vectors exists because we'd rather store a fact
// with no vector than drop data entirely on a transient embedding failure.
// This scan fixes those facts once embedding is available again.
func (p *Pruner) reembedScan(ctx context.Context) {
	if p.facts == nil || p.embedder == nil {
		p.logger.Warn("reembedScan: facts client or embedder not available")
		return
	}

	p.logger.Info("reembedScan: scanning for facts with zero vectors")

	facts, err := p.scrollAllFacts(ctx)
	if err != nil {
		p.logger.Error("reembedScan: failed to scroll facts", "error", err)
		return
	}

	var reembedded, skipped, failed int
	for _, pt := range facts {
		vec := qdrant.GetPointVector(pt)
		if vec == nil || !qdrant.IsZeroVector(vec) {
			continue // only re-embed zero-vector points
		}

		// Get the fact value from the point's payload
		payload := pt.GetPayload()
		val := payload["fact_value"]
		if val == nil {
			skipped++
			continue
		}
		value := val.GetStringValue()
		if value == "" {
			skipped++
			continue
		}

		// Get the fact key for logging
		key := ""
		if k := payload["fact_key"]; k != nil {
			key = k.GetStringValue()
		}

		pointID := pt.GetId().GetUuid()

		newVec, err := p.embedder.EmbedSingle(ctx, value)
		if err != nil {
			p.logger.Error("reembedScan: permanent embedding failure, cannot fix",
				"key", key, "point_id", pointID, "error", err)
			failed++
			continue
		}

		// Update the point's vector in Qdrant
		pv := &pb.PointVectors{
			Id: &pb.PointId{
				PointIdOptions: &pb.PointId_Uuid{
					Uuid: pointID,
				},
			},
			Vectors: &pb.Vectors{
				VectorsOptions: &pb.Vectors_Vector{
					Vector: &pb.Vector{
						Data: newVec,
					},
				},
			},
		}

		if err := p.facts.UpdateVectors(ctx, p.facts.Collection(), []*pb.PointVectors{pv}); err != nil {
			p.logger.Error("reembedScan: failed to update vector",
				"key", key, "point_id", pointID, "error", err)
			failed++
			continue
		}

		reembedded++
		p.logger.Debug("reembedScan: re-embedded fact",
			"key", key, "point_id", pointID, "vector_size", len(newVec))
	}

	if reembedded > 0 || failed > 0 {
		p.logger.Info("reembedScan complete",
			"reembedded", reembedded,
			"failed", failed,
			"skipped", skipped,
			"total_scanned", len(facts))
	} else {
		p.logger.Debug("reembedScan: no zero-vector facts found", "total_scanned", len(facts))
	}
}


