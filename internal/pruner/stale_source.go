package pruner

import (
	"context"
	"fmt"

	pb "github.com/qdrant/go-client/qdrant"
)

// activeFactFilter returns a filter matching facts with status = "active".
// We post-filter for non-empty source in Go because Qdrant doesn't support
// a generic "field is not empty" condition.
func activeFactFilter() *pb.Filter {
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
		},
	}
}

// sourceChunkCheckFilter returns a filter that checks if any chunk exists for
// the given source file in the vault's chunk collection.
func sourceChunkCheckFilter(sourceFile string) *pb.Filter {
	return &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "source_file",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: sourceFile,
							},
						},
					},
				},
			},
		},
	}
}

// sourceStaleScan checks whether source files referenced by active facts still
// exist in the vault's chunk collection. Facts whose source file has been
// deleted are flagged as needs_review.
func (p *Pruner) sourceStaleScan(ctx context.Context) {
	if p.facts == nil {
		p.logger.Warn("sourceStaleScan: no facts client available")
		return
	}
	if p.vaultClient == nil {
		p.logger.Warn("sourceStaleScan: no vault client available — cannot check source files")
		return
	}

	// Scroll all active facts; post-filter for non-empty source in Go
	filter := activeFactFilter()
	points, err := p.scrollFilteredFacts(ctx, filter, 0)
	if err != nil {
		p.logger.Error("sourceStaleScan: query failed", "error", err)
		return
	}

	if len(points) == 0 {
		p.logger.Debug("sourceStaleScan: no active facts found")
		return
	}

	// Group facts by source to minimize vault checks
	type factRef struct {
		pointID string
		key     string
	}
	sourceFacts := make(map[string][]factRef)
	for _, pt := range points {
		s, ok := pt.Payload["source"]
		if !ok || s == nil || s.GetStringValue() == "" {
			continue
		}
		src := s.GetStringValue()
		pointID := pt.GetId().GetUuid()
		if pointID == "" {
			continue
		}
		key := ""
		if k := pt.Payload["fact_key"]; k != nil {
			key = k.GetStringValue()
		}
		sourceFacts[src] = append(sourceFacts[src], factRef{pointID: pointID, key: key})
	}

	if len(sourceFacts) == 0 {
		p.logger.Debug("sourceStaleScan: no facts with source field found")
		return
	}

	// For each unique source, check if the vault still has chunks for it
	vaultCollection := p.vaultClient.Collection()
	flagged := 0
	for src, refs := range sourceFacts {
		chunkFilter := sourceChunkCheckFilter(src)
		chunks, err := p.vaultClient.ScrollFiltered(ctx, vaultCollection, chunkFilter, 1, "")
		if err != nil {
			p.logger.Warn("sourceStaleScan: vault check failed",
				"source", src, "error", err)
			continue
		}

		if len(chunks) > 0 {
			// Source still exists in the chunk collection
			continue
		}

		// Source file deleted — flag all facts referencing it
		for _, ref := range refs {
			if err := p.updateFactStatus(ctx, ref.pointID, "needs_review"); err != nil {
				p.logger.Error("sourceStaleScan: failed to mark fact",
					"point_id", ref.pointID, "key", ref.key, "error", err)
				continue
			}
			if p.cfg.FlagCallback != nil && ref.key != "" {
				p.cfg.FlagCallback(ref.key, "source_deleted")
			}
			flagged++
		}
		p.logger.Info("sourceStaleScan: flagged facts for deleted source",
			"source", src, "count", len(refs))
	}

	p.logger.Info("sourceStaleScan complete",
		"checked_sources", len(sourceFacts), "flagged", flagged)
	if flagged > 0 {
		p.RecordFlagged(flagged)
	}
}

// Ensure compile-time check passes.
var _ = fmt.Sprintf("sourceStaleScan:%s", "registered")
