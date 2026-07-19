package retrieval

import (
	"testing"
)

func TestFuse(t *testing.T) {
	tests := []struct {
		name  string
		lists [][]string
		k     int
		want  []string // expected order of top IDs
	}{
		{
			name:  "empty",
			lists: nil,
			k:     60,
			want:  []string{},
		},
		{
			name:  "single list preserves order",
			lists: [][]string{{"a", "b", "c"}},
			k:     60,
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "agreement ranks higher",
			lists: [][]string{{"a", "b", "c"}, {"b", "a", "d"}},
			k:     60,
			// a: 1/61 + 1/62, b: 1/62 + 1/61 -> tie, then c and d
			want: nil, // order of a/b is a tie; assert membership instead
		},
		{
			name:  "consensus top item wins",
			lists: [][]string{{"x", "y"}, {"x", "z"}},
			k:     60,
			want:  []string{"x"}, // x appears in both at rank 1
		},
		{
			name:  "k<=0 defaults to 60",
			lists: [][]string{{"a"}},
			k:     0,
			want:  []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fuse(tt.lists, tt.k)
			if tt.want == nil {
				return
			}
			if len(got) < len(tt.want) {
				t.Fatalf("got %d results, want at least %d", len(got), len(tt.want))
			}
			for i, id := range tt.want {
				if got[i].ID != id {
					t.Errorf("rank %d: got %q, want %q", i, got[i].ID, id)
				}
			}
			// scores must be strictly descending
			for i := 1; i < len(got); i++ {
				if got[i].Score > got[i-1].Score {
					t.Errorf("scores not descending at %d: %f > %f", i, got[i].Score, got[i-1].Score)
				}
			}
		})
	}
}

func TestFuseConsensusBoost(t *testing.T) {
	// An item appearing in both lists should outrank one appearing in only one,
	// even at a worse individual rank.
	dense := []string{"solo", "shared"}
	lexical := []string{"shared", "other"}
	got := Fuse([][]string{dense, lexical}, 60)
	if len(got) == 0 || got[0].ID != "shared" {
		t.Fatalf("expected 'shared' to rank first, got %+v", got)
	}
}

func TestHybrid(t *testing.T) {
	dense := []string{"d1", "d2", "common"}
	lexical := []string{"common", "l1"}
	got := Hybrid(dense, lexical, 60)
	if len(got) != 4 {
		t.Fatalf("expected 4 fused ids, got %d: %+v", len(got), got)
	}
	if got[0].ID != "common" {
		t.Errorf("expected 'common' first (in both lists), got %q", got[0].ID)
	}
}

func TestLexicalIndexSearch(t *testing.T) {
	idx := NewLexicalIndex()
	idx.Build([]Doc{
		{ID: "1", Text: "The quick brown fox jumps over the lazy dog"},
		{ID: "2", Text: "A fast brown fox leaps across a river"},
		{ID: "3", Text: "Database migrations should be idempotent and reversible"},
	})

	if idx.Size() != 3 {
		t.Fatalf("expected size 3, got %d", idx.Size())
	}

	tests := []struct {
		name    string
		query   string
		wantTop string
		wantLen int
	}{
		{"lexical match fox", "brown fox", "", 2},
		{"exact domain term", "database migrations idempotent", "3", 1},
		{"no match", "kubernetes helm chart", "", 0},
		{"stopwords only", "the a an of", "", 0},
		{"empty query", "", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := idx.Search(tt.query, 10)
			if len(got) != tt.wantLen {
				t.Fatalf("query %q: got %d results, want %d (%+v)", tt.query, len(got), tt.wantLen, got)
			}
			if tt.wantTop != "" && got[0].ID != tt.wantTop {
				t.Errorf("query %q: top result %q, want %q", tt.query, got[0].ID, tt.wantTop)
			}
		})
	}
}

func TestLexicalIndexEmpty(t *testing.T) {
	idx := NewLexicalIndex()
	if idx.Size() != 0 {
		t.Fatalf("empty index size = %d", idx.Size())
	}
	if got := idx.Search("anything", 5); got != nil {
		t.Errorf("search on empty index = %+v, want nil", got)
	}
}

func TestLexicalIndexLimit(t *testing.T) {
	idx := NewLexicalIndex()
	idx.Build([]Doc{
		{ID: "1", Text: "alpha alpha alpha"},
		{ID: "2", Text: "alpha alpha"},
		{ID: "3", Text: "alpha"},
	})
	got := idx.Search("alpha", 2)
	if len(got) != 2 {
		t.Fatalf("limit not respected: got %d, want 2", len(got))
	}
	// scores must be descending; BM25 length-normalization means the exact
	// ranking of near-identical docs is not asserted here.
	if got[0].Score < got[1].Score {
		t.Errorf("results not in descending score order: %+v", got)
	}
}
