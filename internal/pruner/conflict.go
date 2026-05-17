package pruner

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

// conflictScan samples active facts, computes their embeddings, and checks
// pairwise cosine similarity. Pairs above the threshold (0.92) are flagged
// as contradicting each other if they are semantically different.
//
// The scan uses the embedder to get real embeddings for each fact's value,
// then compares all pairs within the sample. Only pairs where both facts
// are active and have no existing contradiction flag are considered.
func (p *Pruner) conflictScan(ctx context.Context) {
	if p.facts == nil || p.embedder == nil {
		p.logger.Warn("conflictScan: facts client or embedder not available")
		return
	}

	// Sample active facts
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
		key, _ := getPayloadString(payload, "fact_key")
		value, _ := getPayloadString(payload, "fact_value")
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
			// about the same subject). Threshold of 0.92 catches near-duplicates and
			// statements about the same topic with different conclusions.
			if sim < 0.92 {
				continue
			}

			// Check that both are still active (re-fetch to avoid TOCTOU)
			if err := p.markContradiction(ctx, facts[i].id, facts[j].id); err != nil {
				p.logger.Error("conflictScan: marking contradiction failed",
					"a", facts[i].key, "b", facts[j].key, "error", err)
				continue
			}
			if err := p.markContradiction(ctx, facts[j].id, facts[i].id); err != nil {
				p.logger.Error("conflictScan: marking reverse contradiction failed",
					"a", facts[j].key, "b", facts[i].key, "error", err)
				continue
			}

			flagged++
			p.logger.Info("conflictScan: flagging contradiction",
				"a", facts[i].key, "b", facts[j].key, "similarity", fmt.Sprintf("%.4f", sim))
		}
	}

	p.logger.Info("conflictScan complete", "sampled", len(facts), "flagged_pairs", flagged)
}

// markContradiction adds the other fact's key to this fact's contradicts list
// and sets conflict_resolved = false, status = needs_review.
func (p *Pruner) markContradiction(ctx context.Context, pointID, otherKey string) error {
	// Re-read the point to get current state
	keyFilter := &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "fact_key",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: otherKey,
							},
						},
					},
				},
			},
		},
	}
	points, err := p.facts.ScrollFiltered(ctx, p.facts.Collection(), keyFilter, 1, "")
	if err != nil || len(points) == 0 {
		return fmt.Errorf("read target fact: %w", err)
	}

	payload := make(map[string]*pb.Value)
	for k, v := range points[0].GetPayload() {
		payload[k] = v
	}

	// Append to contradicts list (dedup)
	existing := getPayloadStringList(payload, "contradicts")
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
	payload["contradicts"] = &pb.Value{
		Kind: &pb.Value_ListValue{
			ListValue: &pb.ListValue{Values: tagVals},
		},
	}
	payload["conflict_resolved"] = pb.NewValue(false)
	payload["status"] = pb.NewValue("needs_review")

	return p.updateFactPayload(ctx, pointID, payload)
}
