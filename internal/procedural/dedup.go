package procedural

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// ── Interfaces ─────────────────────────────────────────────────────────────────

// QdrantClient is the subset of FactStore needed for dedup queries.
type QdrantClient interface {
	ScrollFiltered(ctx context.Context, collection string, filter *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error)
}

// ── Dedup ──────────────────────────────────────────────────────────────────────

// Dedup searches for existing procedure facts with similar names.
// Returns a DedupResult indicating whether to update an existing fact or create new.
func Dedup(ctx context.Context, qc QdrantClient, collection string, proc Procedure, threshold float64) (*DedupResult, error) {
	if threshold <= 0 {
		threshold = DefaultDedupThreshold
	}

	// Filter for procedure-type facts only
	filter := &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "fact_type",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: FactTypeProcedure,
							},
						},
					},
				},
			},
		},
	}

	// Scroll all procedure facts (reasonable limit — procedures are sparse)
	points, err := qc.ScrollFiltered(ctx, collection, filter, 1000, "")
	if err != nil {
		return nil, err
	}

	if len(points) == 0 {
		return &DedupResult{ShouldCreate: true}, nil
	}

	// Compare each existing procedure by name
	procName := strings.ToLower(strings.TrimSpace(proc.Name))
	var bestMatch *DedupResult

	for _, point := range points {
		val, ok := qdrantutil.GetPayloadString(point.GetPayload(), "procedure_name")
		if !ok || val == "" {
			val = extractProcedureNameFromValue(point.GetPayload())
		}
		if val == "" {
			continue
		}

		existingName := strings.ToLower(strings.TrimSpace(val))
		sim := nameSimilarity(procName, existingName)

		if sim >= threshold {
			if bestMatch == nil || sim > bestMatch.Similarity {
				key, _ := qdrantutil.GetPayloadString(point.GetPayload(), "fact_key")
				bestMatch = &DedupResult{
					ExistingKey:  key,
					ShouldUpdate: true,
					ShouldCreate: false,
					Similarity:   sim,
				}
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, nil
	}

	return &DedupResult{ShouldCreate: true}, nil
}

// ── Name Similarity ────────────────────────────────────────────────────────────

// nameSimilarity computes a similarity score between two procedure names.
// Uses word overlap (Dice-ish) and bigram similarity.
func nameSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}

	wordsA := strings.Fields(a)
	wordsB := strings.Fields(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0.0
	}

	// Word overlap (weight: 0.6)
	intersection := 0
	wordSet := make(map[string]bool)
	for _, w := range wordsA {
		wordSet[w] = true
	}
	for _, w := range wordsB {
		if wordSet[w] {
			intersection++
		}
	}
	union := len(wordsA) + len(wordsB) - intersection
	wordOverlap := 0.0
	if union > 0 {
		wordOverlap = float64(intersection) / float64(union)
	}

	// Bigram similarity (weight: 0.4)
	bigramScore := bigramSimilarity(a, b)

	// Combined
	score := wordOverlap*0.6 + bigramScore*0.4

	// Boost for same verb
	verbA := firstVerb(wordsA)
	verbB := firstVerb(wordsB)
	if verbA != "" && verbA == verbB {
		score = math.Min(1.0, score+0.15)
	}

	return math.Min(1.0, math.Max(0.0, score))
}

// bigramSimilarity computes Dice coefficient on character bigrams.
func bigramSimilarity(a, b string) float64 {
	if len(a) < 2 || len(b) < 2 {
		return 0.0
	}
	bigramsA := make(map[string]int)
	for i := 0; i < len(a)-1; i++ {
		bigramsA[a[i:i+2]]++
	}
	intersection := 0
	for i := 0; i < len(b)-1; i++ {
		bg := b[i : i+2]
		if bigramsA[bg] > 0 {
			bigramsA[bg]--
			intersection++
		}
	}
	totalA := len(a) - 1
	totalB := len(b) - 1
	return float64(2*intersection) / float64(totalA+totalB)
}

// firstVerb returns the first word that matches an action keyword.
func firstVerb(words []string) string {
	for _, w := range words {
		wl := strings.ToLower(w)
		for _, kw := range ActionKeywords {
			if wl == kw {
				return w
			}
		}
	}
	return ""
}

// extractProcedureNameFromValue tries to parse fact_value JSON for a name.
func extractProcedureNameFromValue(payload map[string]*pb.Value) string {
	val, ok := qdrantutil.GetPayloadString(payload, "fact_value")
	if !ok || val == "" {
		return ""
	}
	var p Procedure
	if err := json.Unmarshal([]byte(val), &p); err == nil {
		return p.Name
	}
	return ""
}
