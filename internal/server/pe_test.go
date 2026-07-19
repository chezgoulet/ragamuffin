package server

import (
	"testing"
)

func TestComputePEIdentical(t *testing.T) {
	if pe := computePE("hello", "hello"); pe != 0.0 {
		t.Errorf("identical strings: got %f, want 0.0", pe)
	}
}

func TestComputePECompletelyDifferent(t *testing.T) {
	pe := computePE("abc", "xyz")
	if pe < 0.8 {
		t.Errorf("completely different: got %f, want >= 0.8", pe)
	}
}

func TestComputePESimilar(t *testing.T) {
	pe := computePE("the value is 42", "the value is 43")
	if pe <= 0 || pe >= 0.5 {
		t.Errorf("similar strings: got %f, want between 0 and 0.5", pe)
	}
}

func TestComputePEEmptyOld(t *testing.T) {
	if pe := computePE("", "new value"); pe != 1.0 {
		t.Errorf("empty old: got %f, want 1.0", pe)
	}
}

func TestComputePEEmptyNew(t *testing.T) {
	if pe := computePE("old value", ""); pe != 1.0 {
		t.Errorf("empty new: got %f, want 1.0", pe)
	}
}

func TestComputePEBothEmpty(t *testing.T) {
	if pe := computePE("", ""); pe != 1.0 {
		t.Errorf("both empty: got %f, want 1.0", pe)
	}
}

func TestComputePEUnicode(t *testing.T) {
	// Unicode strings with same meaning but different characters
	oldVal := "café"
	newVal := "cafe"
	pe := computePE(oldVal, newVal)
	if pe <= 0 || pe >= 0.5 {
		t.Errorf("unicode similar: got %f, want between 0 and 0.5", pe)
	}
}

func TestComputePELongValues(t *testing.T) {
	oldVal := "use Postgres 14 with standard indexing and connection pooling set to 20"
	newVal := "use Postgres 16 with pgvector extension and connection pooling set to 50"
	pe := computePE(oldVal, newVal)
	if pe <= 0 || pe >= 0.7 {
		t.Errorf("long similar: got %f, want between 0 and 0.7", pe)
	}
}

// ── classifyPE ──────────────────────────────────────────────────────────────

var defaultPEThresholds = PEThresholds{
	Reinforce: 0.1,
	Minor:     0.4,
	Major:     0.7,
}

func TestClassifyPEReinforcement(t *testing.T) {
	if got := classifyPE(0.05, defaultPEThresholds); got != PEReinforcement {
		t.Errorf("pe=0.05: got %q, want %q", got, PEReinforcement)
	}
}

func TestClassifyPEMinorUpdate(t *testing.T) {
	if got := classifyPE(0.25, defaultPEThresholds); got != PEMinorUpdate {
		t.Errorf("pe=0.25: got %q, want %q", got, PEMinorUpdate)
	}
}

func TestClassifyPEMajorUpdate(t *testing.T) {
	if got := classifyPE(0.55, defaultPEThresholds); got != PEMajorUpdate {
		t.Errorf("pe=0.55: got %q, want %q", got, PEMajorUpdate)
	}
}

func TestClassifyPENewLearning(t *testing.T) {
	if got := classifyPE(0.85, defaultPEThresholds); got != PENewLearning {
		t.Errorf("pe=0.85: got %q, want %q", got, PENewLearning)
	}
}

func TestClassifyPEBoundaryReinforce(t *testing.T) {
	// At exactly the threshold, should be MINOR (pe >= Reinforce).
	if got := classifyPE(0.1, defaultPEThresholds); got != PEMinorUpdate {
		t.Errorf("pe=0.1 (at reinforce threshold): got %q, want %q", got, PEMinorUpdate)
	}
}

func TestClassifyPEBoundaryMajor(t *testing.T) {
	if got := classifyPE(0.4, defaultPEThresholds); got != PEMajorUpdate {
		t.Errorf("pe=0.4 (at minor threshold): got %q, want %q", got, PEMajorUpdate)
	}
}

func TestClassifyPEBoundaryNewLearning(t *testing.T) {
	if got := classifyPE(0.7, defaultPEThresholds); got != PENewLearning {
		t.Errorf("pe=0.7 (at major threshold): got %q, want %q", got, PENewLearning)
	}
}

func TestClassifyPEClampLow(t *testing.T) {
	if got := classifyPE(-0.5, defaultPEThresholds); got != PEReinforcement {
		t.Errorf("clamped low: got %q, want %q", got, PEReinforcement)
	}
}

func TestClassifyPEClampHigh(t *testing.T) {
	if got := classifyPE(1.5, defaultPEThresholds); got != PENewLearning {
		t.Errorf("clamped high: got %q, want %q", got, PENewLearning)
	}
}

func TestClassifyPECustomThresholds(t *testing.T) {
	custom := PEThresholds{Reinforce: 0.2, Minor: 0.5, Major: 0.8}
	tests := []struct {
		pe   float64
		want string
	}{
		{0.0, PEReinforcement},
		{0.19, PEReinforcement},
		{0.2, PEMinorUpdate},
		{0.49, PEMinorUpdate},
		{0.5, PEMajorUpdate},
		{0.79, PEMajorUpdate},
		{0.8, PENewLearning},
		{1.0, PENewLearning},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := classifyPE(tt.pe, custom); got != tt.want {
				t.Errorf("pe=%f: got %q, want %q", tt.pe, got, tt.want)
			}
		})
	}
}

// ── levenshtein ─────────────────────────────────────────────────────────────

func TestLevenshteinEmpty(t *testing.T) {
	if d := levenshtein(nil, []rune("abc")); d != 3 {
		t.Errorf("levenshtein(nil, abc) = %d, want 3", d)
	}
	if d := levenshtein([]rune("abc"), nil); d != 3 {
		t.Errorf("levenshtein(abc, nil) = %d, want 3", d)
	}
	if d := levenshtein(nil, nil); d != 0 {
		t.Errorf("levenshtein(nil, nil) = %d, want 0", d)
	}
}

func TestLevenshteinIdentical(t *testing.T) {
	if d := levenshtein([]rune("hello"), []rune("hello")); d != 0 {
		t.Errorf("identical: got %d, want 0", d)
	}
}

func TestLevenshteinInsert(t *testing.T) {
	if d := levenshtein([]rune("cat"), []rune("cats")); d != 1 {
		t.Errorf("insert: got %d, want 1", d)
	}
}

func TestLevenshteinDelete(t *testing.T) {
	if d := levenshtein([]rune("cats"), []rune("cat")); d != 1 {
		t.Errorf("delete: got %d, want 1", d)
	}
}

func TestLevenshteinSubstitute(t *testing.T) {
	if d := levenshtein([]rune("cat"), []rune("car")); d != 1 {
		t.Errorf("substitute: got %d, want 1", d)
	}
}

func TestLevenshteinFullDistance(t *testing.T) {
	if d := levenshtein([]rune("abc"), []rune("xyz")); d != 3 {
		t.Errorf("full distance: got %d, want 3", d)
	}
}
