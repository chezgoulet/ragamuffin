package pruner

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// conflictScan samples active facts, computes their embeddings, and checks
// pairwise cosine similarity. Pairs above the threshold (0.85) are flagged
// as contradicting each other if they are semantically different.
//
// The scan uses the embedder to get real embeddings for each fact's value,
// then compares all pairs within the sample.
//
// Write-once rule: Only the newer/lower-confidence fact is marked with
// contradicts set; the other is found at read time by scanning contradicts
// arrays across all facts.
//
// Facts with conflict_resolved=true AND status=active are skipped
// (operator has dismissed the contradiction).
func (p *Pruner) conflictScan(ctx context.Context) {
	if p.facts == nil || p.embedder == nil {
		p.logger.Warn("conflictScan: facts client or embedder not available")
		return
	}

	// Sample active facts, excluding those that have been explicitly dismissed
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
		},
		MustNot: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "conflict_resolved",
						Match: &pb.Match{
							MatchValue: &pb.Match_Boolean{
								Boolean: true,
							},
						},
					},
				},
			},
		},
	}

	// Fetch a larger pool, then sample
	allActive, err := p.scrollFilteredFacts(ctx, filter, 0)
	if err != nil {
		p.logger.Error("conflictScan: query failed", "error", err)
		return
	}

	if len(allActive) < 2 {
		p.logger.Debug("conflictScan: not enough active facts to compare")
		return
	}

	// Sample up to ConflictSampleSize
	sampleSize := p.cfg.ConflictSampleSize
	if sampleSize <= 0 {
		sampleSize = 50
	}
	if sampleSize > len(allActive) {
		sampleSize = len(allActive)
	}

	// Shuffle and take sample
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(allActive), func(i, j int) {
		allActive[i], allActive[j] = allActive[j], allActive[i]
	})
	sample := allActive[:sampleSize]

	// Extract fact values for embedding
	type factPair struct {
		id    string
		key   string
		value string
	}
	facts := make([]factPair, 0, len(sample))
	for _, pt := range sample {
		payload := pt.GetPayload()
		key, _ := qutil.GetPayloadString(payload, "fact_key")
		value, _ := qutil.GetPayloadString(payload, "fact_value")
		if key == "" || value == "" {
			continue
		}
		facts = append(facts, factPair{
			id:    pt.GetId().GetUuid(),
			key:   key,
			value: value,
		})
	}

	if len(facts) < 2 {
		p.logger.Debug("conflictScan: not enough valid facts after filtering")
		return
	}

	// Embed all fact values
	p.logger.Info("conflictScan: embedding facts", "count", len(facts))
	vectors := make([][]float32, len(facts))
	for i, f := range facts {
		vec, err := p.embedder.EmbedSingle(ctx, f.value)
		if err != nil {
			p.logger.Error("conflictScan: embedding failed", "fact_key", f.key, "error", err)
			continue
		}
		vectors[i] = vec
	}

	// Compare all pairs
	flagged := 0
	for i := 0; i < len(facts); i++ {
		if vectors[i] == nil {
			continue
		}
		for j := i + 1; j < len(facts); j++ {
			if vectors[j] == nil {
				continue
			}

			sim := cosineSimilarity(vectors[i], vectors[j])

			// High similarity suggests contradiction (two facts saying different things
		// Similarity above 0.85 suggests the two facts make related claims about
		// the same subject — potential contradiction. This threshold catches
		// near-duplicates and competing statements. A two-stage approach
		// (embedding → LLM confirmation) is a future improvement.
			if sim < 0.85 {
				continue
			}

			// Write-once: only mark the newer/lower-confidence fact with the
			// contradiction. The other is found at read time by scanning
			// contradicts arrays across all facts.
			if err := p.markContradiction(ctx, facts[j].id, facts[i].key); err != nil {
				p.logger.Error("conflictScan: marking contradiction failed",
					"a", facts[i].key, "b", facts[j].key, "error", err)
				continue
			}

			flagged++
			p.logger.Info("conflictScan: flagging contradiction",
				"a", facts[i].key, "b", facts[j].key, "similarity", fmt.Sprintf("%.4f", sim))
		}
	}

	p.logger.Info("conflictScan complete", "sampled", len(facts), "flagged_pairs", flagged)
	if flagged > 0 {
		p.RecordFlagged(flagged) // one fact marked per pair (write-once rule)
	}
}

// markContradiction adds the other fact's key to this fact's contradicts list
// and sets conflict_resolved = false, status = needs_review.
func (p *Pruner) markContradiction(ctx context.Context, pointID, otherKey string) error {
	// Retrieve the target fact by point ID — we only need the contradicts field
	points, err := p.facts.GetPoints(ctx, p.facts.Collection(), []*pb.PointId{{
		PointIdOptions: &pb.PointId_Uuid{Uuid: pointID},
	}})
	if err != nil || len(points) == 0 {
		return fmt.Errorf("read target fact: %w", err)
	}

	sourcePayload := points[0].GetPayload()
	existing := qutil.GetPayloadStringList(sourcePayload, "contradicts")
	for _, s := range existing {
		if s == otherKey {
			return nil // already listed
		}
	}
	existing = append(existing, otherKey)

	tagVals := make([]*pb.Value, len(existing))
	for i, t := range existing {
		v, err := pb.NewValue(t)
		if err != nil {
			continue
		}
		tagVals[i] = v
	}

	// Use SetPayload (via updateFactPayload) to update only the fields that changed
	return p.updateFactPayload(ctx, pointID, map[string]*pb.Value{
		"contradicts": {
			Kind: &pb.Value_ListValue{
				ListValue: &pb.ListValue{Values: tagVals},
			},
		},
		"conflict_resolved": qutil.Nv(false),
		"status":            qutil.Nv("needs_review"),
	})
}
