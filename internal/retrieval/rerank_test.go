package retrieval

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func docs(ids ...string) []RerankDoc {
	out := make([]RerankDoc, len(ids))
	for i, id := range ids {
		out[i] = RerankDoc{ID: id, Text: "text for " + id}
	}
	return out
}

func TestRerankReordersByModelOutput(t *testing.T) {
	c := &stubCompleter{resp: "3,1,2"}
	got := Rerank(context.Background(), c, "q", docs("a", "b", "c"))
	want := []string{"c", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRerankNilCompleterKeepsOrder(t *testing.T) {
	got := Rerank(context.Background(), nil, "q", docs("a", "b"))
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRerankErrorKeepsOrder(t *testing.T) {
	c := &stubCompleter{err: errors.New("boom")}
	got := Rerank(context.Background(), c, "q", docs("a", "b", "c"))
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRerankEmptyInput(t *testing.T) {
	if got := Rerank(context.Background(), &stubCompleter{}, "q", nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestRerankSingleDocSkipsLLM(t *testing.T) {
	c := &stubCompleter{resp: "1"}
	got := Rerank(context.Background(), c, "q", docs("only"))
	if !reflect.DeepEqual(got, []string{"only"}) {
		t.Fatalf("got %v", got)
	}
	if len(c.prompts) != 0 {
		t.Errorf("LLM should not be called for a single doc")
	}
}

func TestRerankAppendsOmitted(t *testing.T) {
	// Model only ranks 2 of 3; the omitted one must be appended.
	c := &stubCompleter{resp: "2,1"}
	got := Rerank(context.Background(), c, "q", docs("a", "b", "c"))
	want := []string{"b", "a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRerankIgnoresOutOfRangeAndDupes(t *testing.T) {
	c := &stubCompleter{resp: "9, 2, 2, 0, 1"}
	got := Rerank(context.Background(), c, "q", docs("a", "b"))
	want := []string{"b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseRankOrder(t *testing.T) {
	got := parseRankOrder("[3] > [1] > [2]", 3)
	want := []int{2, 0, 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSnippetTruncates(t *testing.T) {
	long := strings.Repeat("word ", 300)
	if got := snippet(long); len(got) != rerankSnippetLen {
		t.Errorf("snippet len = %d, want %d", len(got), rerankSnippetLen)
	}
}

func TestSnippetCollapsesWhitespace(t *testing.T) {
	if got := snippet("a\n\n  b\tc"); got != "a b c" {
		t.Errorf("got %q", got)
	}
}

func TestAccessBoostLiftOnly(t *testing.T) {
	ranked := []RankedID{
		{ID: "a", Score: 0.5},
		{ID: "b", Score: 0.4},
		{ID: "c", Score: 0.3},
	}
	counts := map[string]int64{"a": 10, "b": 0}
	got := AccessBoost(ranked, counts, 3, 2.0)
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	// a has boost (~+0.69), should still be first; b has no boost; c unchanged.
	if got[0].ID != "a" {
		t.Errorf("top result = %q, want 'a'", got[0].ID)
	}
	if got[0].Score <= 0.5 {
		t.Errorf("a's score %f should be boosted above 0.5", got[0].Score)
	}
	if got[1].ID != "b" || got[1].Score != 0.4 {
		t.Errorf("b should be unchanged at 0.4, got %s=%f", got[1].ID, got[1].Score)
	}
}

func TestAccessBoostNilCounts(t *testing.T) {
	ranked := []RankedID{{ID: "a", Score: 0.5}}
	got := AccessBoost(ranked, nil, 1, 2.0)
	if len(got) != 1 || got[0].Score != 0.5 {
		t.Errorf("nil counts should not mutate: got %+v", got)
	}
}

func TestAccessBoostEmpty(t *testing.T) {
	if got := AccessBoost(nil, map[string]int64{}, 5, 2.0); got != nil {
		t.Errorf("nil input should return nil, got %+v", got)
	}
}

func TestAccessBoostTopKTruncation(t *testing.T) {
	ranked := []RankedID{
		{ID: "a", Score: 0.1},
		{ID: "b", Score: 0.2},
		{ID: "c", Score: 0.3},
		{ID: "d", Score: 0.4},
		{ID: "e", Score: 0.5},
	}
	counts := map[string]int64{"a": 1000}
	got := AccessBoost(ranked, counts, 2, 2.0)
	if len(got) != 2 {
		t.Errorf("topK=2 should return 2 results, got %d", len(got))
	}
	// a should be boosted to top.
	if got[0].ID != "a" {
		t.Errorf("boosted a should be first, got %q", got[0].ID)
	}
}

func TestAccessBoostFetchMultiplier(t *testing.T) {
	ranked := []RankedID{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.1},
	}
	counts := map[string]int64{"b": 1000}
	// fetchMultiplier=1.0 should still work (no extra fetch window).
	got := AccessBoost(ranked, counts, 2, 1.0)
	if len(got) != 2 {
		t.Errorf("expected 2 results, got %d", len(got))
	}
	// b gets boost but might not beat a with 0.9
}
