package tokenutil

import (
	"testing"
)

func TestEstTokens_Empty(t *testing.T) {
	if got := EstTokens(""); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestEstTokens_Simple(t *testing.T) {
	// "hello world" = 2 words × 1.3 = 2 (integer truncation)
	got := EstTokens("hello world")
	if got != 2 {
		t.Errorf("expected 2, got %d", got)
	}
}

func TestEstTokens_LongText(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog" // 9 words
	got := EstTokens(text)                                // 9 * 1.3 = 11.7 → 11
	if got != 11 {
		t.Errorf("expected 11, got %d", got)
	}
}

func TestEstTokens_Repeat100(t *testing.T) {
	// Build 100 words
	words := ""
	for i := 0; i < 100; i++ {
		if i > 0 {
			words += " "
		}
		words += "word"
	}
	got := EstTokens(words) // 100 * 1.3 = 130
	if got != 130 {
		t.Errorf("expected 130, got %d", got)
	}
}

func TestEstTokens_SingleWord(t *testing.T) {
	got := EstTokens("hello")
	if got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}

func TestEstTokens_SpecialChars(t *testing.T) {
	// Punctuation attached to words doesn't split differently in strings.Fields
	got := EstTokens("hello, world!")
	if got != 2 {
		t.Errorf("expected 2, got %d", got)
	}
}

func TestEstTokens_OnlyWhitespace(t *testing.T) {
	got := EstTokens("   \n\t   ")
	if got != 0 {
		t.Errorf("expected 0 for whitespace-only, got %d", got)
	}
}

func TestEstTokens_NewlinesAndTabs(t *testing.T) {
	got := EstTokens("hello\nworld\tfoo")
	if got != 3 {
		t.Errorf("expected 3, got %d", got)
	}
}
