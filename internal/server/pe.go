package server

import "math"

// PEThresholds holds the three decision boundaries for prediction-error
// classification. All thresholds must satisfy 0 ≤ reinforce < minor < major ≤ 1.
type PEThresholds struct {
	Reinforce float64 // below this → reinforcement (match)
	Minor     float64 // below this → minor-update
	Major     float64 // below this → major-update; above → new-learning
}

// PE constants for inference about whether we classify the pe.
const (
	PEReinforcement = "reinforcement"
	PEMinorUpdate   = "minor-update"
	PEMajorUpdate   = "major-update"
	PENewLearning   = "new-learning"
)

// computePE returns the prediction error between old and new fact values using
// normalized Levenshtein (edit) distance. Returns 0.0 for identical strings,
// 1.0 for completely different or when old is empty (new fact).
func computePE(oldValue, newValue string) float64 {
	if oldValue == "" || newValue == "" {
		return 1.0
	}
	if oldValue == newValue {
		return 0.0
	}
	d := levenshtein([]rune(oldValue), []rune(newValue))
	maxLen := math.Max(float64(len([]rune(oldValue))), float64(len([]rune(newValue))))
	if maxLen == 0 {
		return 0.0
	}
	return float64(d) / maxLen
}

// classifyPE maps a prediction error score to a human-readable label using the
// provided thresholds. Results are clamped to [0, 1]; thresholds must be
// monotonic (already validated in config.Validate).
func classifyPE(pe float64, t PEThresholds) string {
	if pe < 0 {
		pe = 0
	}
	if pe > 1 {
		pe = 1
	}
	switch {
	case pe < t.Reinforce:
		return PEReinforcement
	case pe < t.Minor:
		return PEMinorUpdate
	case pe < t.Major:
		return PEMajorUpdate
	default:
		return PENewLearning
	}
}

// levenshtein computes the edit distance between two rune slices using the
// Wagner–Fischer algorithm (O(n*m) time, O(min(n,m)) space).
func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// Ensure b is the shorter for the O(min(n,m)) space optimization.
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ca := range a {
		curr[0] = i + 1
		for j, cb := range b {
			cost := 0
			if ca != cb {
				cost = 1
			}
			curr[j+1] = min3(curr[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
