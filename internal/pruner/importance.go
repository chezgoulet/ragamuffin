package pruner

import (
	"math"
	"time"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/qdrant/go-client/qdrant"
)

// computeImportance calculates a 0.0-1.0 importance score for a fact based on:
//   - access_count: more accesses = higher importance
//   - recency (last_accessed_at or created_at): more recent = higher importance
//   - confirmation_count: more confirmations = higher importance
//   - confidence: explicit confidence value (0.0-1.0 or 1-10 scale)
//
// The score is a weighted combination:
//   - Access frequency: 30%
//   - Recency: 30%
//   - Confirmation count: 20%
//   - Confidence: 20%
func computeImportance(payload map[string]*qdrant.Value) float64 {
	now := time.Now().UTC()

	// Access count: 30% weight, capped at 100 accesses
	accessCount := qutil.GetPayloadIntValue(payload, "access_count")
	accessScore := math.Min(float64(accessCount)/100.0, 1.0)

	// Recency: 30% weight — decay from 1.0 at time of access to 0.0 after 365 days
	lastAccessedAt := qutil.GetPayloadStringValue(payload, "last_accessed_at")
	createdAt := qutil.GetPayloadStringValue(payload, "created_at")

	var recencyAnchor string
	if lastAccessedAt != "" {
		recencyAnchor = lastAccessedAt
	} else if createdAt != "" {
		recencyAnchor = createdAt
	}

	recencyScore := 0.0
	if recencyAnchor != "" {
		if t, err := time.Parse(time.RFC3339, recencyAnchor); err == nil {
			daysSinceAccess := now.Sub(t).Hours() / 24
			recencyScore = math.Max(1.0-daysSinceAccess/365.0, 0.0)
		}
	}

	// Confirmation count: 20% weight, capped at 10 confirmations
	confirmationCount := qutil.GetPayloadIntValue(payload, "confirmation_count")
	confirmationScore := math.Min(float64(confirmationCount)/10.0, 1.0)

	// Confidence: 20% weight. Could be 0.0-1.0 or 1-10 integer scale.
	confidence := qutil.GetPayloadFloatValue(payload, "confidence")
	confidenceScore := confidence
	if confidence > 1.0 {
		confidenceScore = confidence / 10.0
	}
	confidenceScore = math.Max(0.0, math.Min(confidenceScore, 1.0))

	// Weighted combination
	score := 0.3*accessScore + 0.3*recencyScore + 0.2*confirmationScore + 0.2*confidenceScore
	return math.Max(0.0, math.Min(score, 1.0))
}
