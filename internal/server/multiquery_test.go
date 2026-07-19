package server

import (
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/retrieval"
)

func TestUseMultiQueryFanout(t *testing.T) {
	tests := []struct {
		name    string
		mode    retrieval.RewriteMode
		reqMode string
		count   int
		want    bool
	}{
		{"dense multiquery fans out", retrieval.RewriteMultiQuery, "", 3, true},
		{"dense mode explicit", retrieval.RewriteMultiQuery, "dense", 2, true},
		{"hybrid preserves lexical", retrieval.RewriteMultiQuery, "hybrid", 3, false},
		{"sparse preserves lexical", retrieval.RewriteMultiQuery, "sparse", 3, false},
		{"single rewrite no fanout", retrieval.RewriteMultiQuery, "", 1, false},
		{"non-multiquery mode", retrieval.RewriteHyDE, "", 3, false},
		{"off", retrieval.RewriteOff, "", 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := useMultiQueryFanout(tt.mode, tt.reqMode, tt.count); got != tt.want {
				t.Errorf("useMultiQueryFanout(%v,%q,%d) = %v, want %v", tt.mode, tt.reqMode, tt.count, got, tt.want)
			}
		})
	}
}

func TestIsLexicalMode(t *testing.T) {
	for _, m := range []string{"hybrid", "sparse"} {
		if !isLexicalMode(m) {
			t.Errorf("isLexicalMode(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "dense"} {
		if isLexicalMode(m) {
			t.Errorf("isLexicalMode(%q) = true, want false", m)
		}
	}
}
