package pruner

import (
	"math"
	"testing"
	"time"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/qdrant/go-client/qdrant"
)

func pv(m map[string]any) map[string]*qdrant.Value {
	out := make(map[string]*qdrant.Value, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case string:
			out[k] = qutil.Nv(t)
		case int:
			out[k] = qutil.Nv(float64(t))
		case float64:
			out[k] = qutil.Nv(t)
		}
	}
	return out
}

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestAccessibilityFreshFactIsFull(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := pv(map[string]any{"created_at": now.Format(time.RFC3339)})
	if got := Accessibility(p, now, DefaultHalfLifeDays); !approx(got, 1.0, 1e-9) {
		t.Fatalf("fresh fact accessibility = %v, want 1.0", got)
	}
}

func TestAccessibilityNoTimestampIsFull(t *testing.T) {
	if got := Accessibility(pv(nil), time.Now(), DefaultHalfLifeDays); got != 1.0 {
		t.Fatalf("no-timestamp accessibility = %v, want 1.0", got)
	}
}

func TestAccessibilityHalfLife(t *testing.T) {
	now := time.Now().UTC()
	// Baseline fact (no reinforcement): after DefaultHalfLifeDays, R ~ 0.5.
	created := now.Add(-time.Duration(DefaultHalfLifeDays*24) * time.Hour)
	p := pv(map[string]any{"created_at": created.Format(time.RFC3339)})
	got := Accessibility(p, now, DefaultHalfLifeDays)
	if !approx(got, 0.5, 0.02) {
		t.Fatalf("accessibility at one half-life = %v, want ~0.5", got)
	}
}

func TestAccessibilityMonotonicDecrease(t *testing.T) {
	now := time.Now().UTC()
	mk := func(days float64) float64 {
		p := pv(map[string]any{"created_at": now.Add(-time.Duration(days*24) * time.Hour).Format(time.RFC3339)})
		return Accessibility(p, now, DefaultHalfLifeDays)
	}
	prev := 1.0
	for _, d := range []float64{1, 7, 30, 90, 365} {
		got := mk(d)
		if got > prev {
			t.Fatalf("accessibility not monotonic: day %v gave %v > %v", d, got, prev)
		}
		if got <= 0 || got > 1 {
			t.Fatalf("accessibility out of range at day %v: %v", d, got)
		}
		prev = got
	}
}

func TestReinforcementSlowsDecay(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-60 * 24 * time.Hour).Format(time.RFC3339)

	bare := pv(map[string]any{"created_at": created})
	reinforced := pv(map[string]any{
		"created_at":         created,
		"access_count":       100,
		"confirmation_count": 10,
		"confidence":         0.9,
	})
	rb := Accessibility(bare, now, DefaultHalfLifeDays)
	rr := Accessibility(reinforced, now, DefaultHalfLifeDays)
	if rr <= rb {
		t.Fatalf("reinforced fact should decay slower: reinforced=%v bare=%v", rr, rb)
	}
}

func TestStabilityMultiplierCapped(t *testing.T) {
	p := pv(map[string]any{
		"access_count":       1000000,
		"confirmation_count": 1000000,
		"confidence":         1.0,
		"ttl_days":           100000,
	})
	if got := stabilityMultiplier(p); got > maxStabilityMultiplier+1e-9 {
		t.Fatalf("stability multiplier %v exceeds cap %v", got, maxStabilityMultiplier)
	}
}

func TestStabilityMultiplierFloor(t *testing.T) {
	if got := stabilityMultiplier(pv(nil)); got < 1.0 {
		t.Fatalf("stability multiplier floor violated: %v", got)
	}
}

func TestNormalizeConfidence(t *testing.T) {
	cases := map[float64]float64{0.0: 0.0, 0.5: 0.5, 1.0: 1.0, 7.0: 0.7, 10.0: 1.0, -1.0: 0.0, 20.0: 1.0}
	for in, want := range cases {
		if got := normalizeConfidence(in); !approx(got, want, 1e-9) {
			t.Errorf("normalizeConfidence(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestEffectiveConfidence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	created := now.Add(-time.Duration(DefaultHalfLifeDays*24) * time.Hour).Format(time.RFC3339)
	p := pv(map[string]any{"created_at": created, "confidence": 0.8})
	// effective = confidence(0.8) * accessibility. Confidence also reinforces
	// stability, so accessibility exceeds the bare 0.5 half-life value; the
	// product must lie between 0.8*0.5=0.4 and 0.8.
	got := EffectiveConfidence(p, now, DefaultHalfLifeDays)
	acc := Accessibility(p, now, DefaultHalfLifeDays)
	if !approx(got, 0.8*acc, 1e-9) {
		t.Fatalf("effective confidence = %v, want %v (0.8 * accessibility)", got, 0.8*acc)
	}
	if got <= 0.4 || got >= 0.8 {
		t.Fatalf("effective confidence = %v, expected in (0.4, 0.8)", got)
	}
}

func TestEffectiveConfidenceNoConfidenceDefaultsToOne(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := pv(map[string]any{"created_at": now.Format(time.RFC3339)})
	if got := EffectiveConfidence(p, now, DefaultHalfLifeDays); !approx(got, 1.0, 1e-9) {
		t.Fatalf("unrated fresh fact effective confidence = %v, want 1.0", got)
	}
}

func TestDecayAnchorPicksLatest(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-1 * 24 * time.Hour).Format(time.RFC3339)
	p := pv(map[string]any{"created_at": old, "last_accessed_at": recent})
	got := decayAnchor(p)
	if got.Sub(now).Hours() < -48 {
		t.Fatalf("anchor should be the recent timestamp, got %v", got)
	}
}

func TestLastConfirmedResetsDecay(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-200 * 24 * time.Hour).Format(time.RFC3339)
	confirmed := now.Add(-1 * 24 * time.Hour).Format(time.RFC3339)
	p := pv(map[string]any{"created_at": created, "last_confirmed_at": confirmed})
	if got := Accessibility(p, now, DefaultHalfLifeDays); got < 0.9 {
		t.Fatalf("recently confirmed fact should be near-fresh, got %v", got)
	}
}
