package pruner

import (
	"math"
	"time"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/qdrant/go-client/qdrant"
)

// Continuous accessibility decay (B1).
//
// Instead of a hard TTL cliff, a fact's *accessibility* fades continuously
// along an Ebbinghaus forgetting curve, R = exp(-t / S), where t is the time
// since the fact was last confirmed/accessed and S is the memory's stability.
// Stability grows with reinforcement — access count, confirmations, explicit
// confidence, and any configured TTL — so well-used facts fade slowly and
// one-off facts fade fast (the spacing effect; Bjork's new theory of disuse).
//
// effective_confidence = confidence * accessibility is what callers rank and
// threshold on. Decay is soft: it never deletes. For facts with ttl_days > 0,
// expires_at remains a hard floor enforced elsewhere; decay only shapes
// ranking before that floor.
//
// Refs: Ebbinghaus (1885); Bjork & Bjork (1992) new theory of disuse;
// Wei et al. arXiv:2601.18642 (FadeMem).

// DefaultHalfLifeDays is the accessibility half-life for a baseline fact with
// no reinforcement (access_count=0, confirmations=0, confidence~0.5, no TTL).
// After this many idle days such a fact's accessibility falls to ~0.5.
const DefaultHalfLifeDays = 30.0

// maxStabilityMultiplier caps how much reinforcement can stretch the half-life,
// so a heavily confirmed fact decays at most this many times slower than the
// baseline. Prevents "immortal" facts while still strongly rewarding use.
const maxStabilityMultiplier = 12.0

// ln2 converts a half-life into the exponential time constant S (S = HL/ln2).
var ln2 = math.Ln2

// stabilityMultiplier blends the reinforcement signals in a payload into a
// multiplier in [1, maxStabilityMultiplier] applied to the baseline half-life.
// It reuses the same signals as computeImportance so decay and importance stay
// consistent.
func stabilityMultiplier(payload map[string]*qdrant.Value) float64 {
	accessCount := qutil.GetPayloadIntValue(payload, "access_count")
	// Diminishing returns: log growth, ~+1 per order of magnitude of accesses.
	accessBoost := math.Log1p(float64(accessCount)) // 0 at 0, ~2.4 at 10, ~4.6 at 100

	confirmations := qutil.GetPayloadIntValue(payload, "confirmation_count")
	confirmBoost := math.Log1p(float64(confirmations)) * 1.5

	confidence := normalizeConfidence(qutil.GetPayloadFloatValue(payload, "confidence"))
	// Confidence in [0,1] contributes up to ~2x on its own.
	confidenceBoost := confidence * 2.0

	// A configured TTL signals intended durability; long TTLs add stability.
	ttlDays := qutil.GetPayloadIntValue(payload, "ttl_days")
	ttlBoost := 0.0
	if ttlDays > 0 {
		ttlBoost = math.Min(float64(ttlDays)/DefaultHalfLifeDays, 3.0)
	}

	mult := 1.0 + accessBoost + confirmBoost + confidenceBoost + ttlBoost
	if mult < 1.0 {
		mult = 1.0
	}
	return math.Min(mult, maxStabilityMultiplier)
}

// normalizeConfidence maps a raw confidence value (0-1 float or 1-10 integer
// scale) into [0,1]. Mirrors computeImportance's handling.
func normalizeConfidence(raw float64) float64 {
	c := raw
	if c > 1.0 {
		c = c / 10.0
	}
	return math.Max(0.0, math.Min(c, 1.0))
}

// decayAnchor picks the timestamp decay is measured from: the most recent of
// last_confirmed_at, last_accessed_at, or created_at. Returns the zero time if
// none parse (caller then treats accessibility as fully fresh).
func decayAnchor(payload map[string]*qdrant.Value) time.Time {
	var latest time.Time
	for _, key := range []string{"last_confirmed_at", "last_accessed_at", "created_at"} {
		s := qutil.GetPayloadStringValue(payload, key)
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// Accessibility returns the current retention R in (0,1] for a fact payload,
// evaluated at now. baseHalfLifeDays is the baseline half-life (use
// DefaultHalfLifeDays when unconfigured). A fact with no usable timestamp is
// treated as freshly created (accessibility 1.0).
func Accessibility(payload map[string]*qdrant.Value, now time.Time, baseHalfLifeDays float64) float64 {
	if baseHalfLifeDays <= 0 {
		baseHalfLifeDays = DefaultHalfLifeDays
	}
	anchor := decayAnchor(payload)
	if anchor.IsZero() {
		return 1.0
	}
	days := now.Sub(anchor).Hours() / 24.0
	if days <= 0 {
		return 1.0
	}
	halfLife := baseHalfLifeDays * stabilityMultiplier(payload)
	s := halfLife / ln2
	r := math.Exp(-days / s)
	// Clamp for numerical safety.
	return math.Max(0.0, math.Min(r, 1.0))
}

// EffectiveConfidence returns confidence * accessibility, the value callers
// rank and threshold on. Confidence is normalized to [0,1] first. A fact with
// no confidence recorded is treated as confidence 1.0 so decay alone drives the
// score (an unrated fact still fades with disuse).
func EffectiveConfidence(payload map[string]*qdrant.Value, now time.Time, baseHalfLifeDays float64) float64 {
	conf := 1.0
	if raw, ok := qutil.GetPayloadFloat(payload, "confidence"); ok {
		conf = normalizeConfidence(raw)
	}
	return conf * Accessibility(payload, now, baseHalfLifeDays)
}
