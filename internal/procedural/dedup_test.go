package procedural

import (
	"testing"
)

func TestNameSimilarityExact(t *testing.T) {
	sim := nameSimilarity("Fix nginx config", "Fix nginx config")
	if sim != 1.0 {
		t.Errorf("expected 1.0 for exact match, got %f", sim)
	}
}

func TestNameSimilaritySimilar(t *testing.T) {
	sim := nameSimilarity("Fix nginx config after error", "Fix nginx config syntax")
	if sim < 0.5 {
		t.Errorf("expected >0.5 for similar names, got %f", sim)
	}
}

func TestNameSimilarityDifferent(t *testing.T) {
	sim := nameSimilarity("Fix nginx config", "Install postgres database")
	if sim > 0.3 {
		t.Errorf("expected <0.3 for different names, got %f", sim)
	}
}

func TestNameSimilarityEmpty(t *testing.T) {
	sim := nameSimilarity("", "Fix nginx")
	if sim != 0.0 {
		t.Errorf("expected 0.0 for empty input, got %f", sim)
	}
	sim = nameSimilarity("Fix nginx", "")
	if sim != 0.0 {
		t.Errorf("expected 0.0 for empty input, got %f", sim)
	}
}

func TestNameSimilaritySameVerb(t *testing.T) {
	a := "Restart nginx service"
	b := "Restart postgres database"
	sim := nameSimilarity(a, b)
	if sim < 0.3 {
		t.Errorf("expected >0.3 for same-verb names, got %f", sim)
	}
	// Should have boost from same verb
	noVerbSim := 0.3 // approximate
	if sim <= noVerbSim {
		t.Errorf("expected verb boost to raise score above %f, got %f", noVerbSim, sim)
	}
}

func TestBigramSimilarityExact(t *testing.T) {
	sim := bigramSimilarity("hello", "hello")
	if sim != 1.0 {
		t.Errorf("expected 1.0 for exact match, got %f", sim)
	}
}

func TestBigramSimilarityPartial(t *testing.T) {
	sim := bigramSimilarity("hello", "hallo")
	if sim <= 0 {
		t.Errorf("expected >0 for similar strings, got %f", sim)
	}
}

func TestBigramSimilarityDifferent(t *testing.T) {
	sim := bigramSimilarity("abc", "xyz")
	if sim != 0.0 {
		t.Errorf("expected 0.0 for different strings, got %f", sim)
	}
}

func TestBigramSimilarityShort(t *testing.T) {
	sim := bigramSimilarity("a", "b")
	if sim != 0.0 {
		t.Errorf("expected 0.0 for single-char strings, got %f", sim)
	}
}

func TestFirstVerb(t *testing.T) {
	tests := []struct {
		words []string
		want  string
	}{
		{[]string{"Fix", "nginx", "config"}, "Fix"},
		{[]string{"Install", "postgres"}, "Install"},
		{[]string{"The", "system", "check", "failed"}, ""},
		{[]string{"run", "check"}, "run"},
	}
	for _, tt := range tests {
		got := firstVerb(tt.words)
		if got != tt.want {
			t.Errorf("firstVerb(%v) = %q, want %q", tt.words, got, tt.want)
		}
	}
}

func TestExtractProcedureNameFromValue(t *testing.T) {
	// Can't easily test without constructing full pb.Value maps.
	// Smoke test for nil payload.
	name := extractProcedureNameFromValue(nil)
	if name != "" {
		t.Errorf("expected empty for nil payload, got %q", name)
	}
}
