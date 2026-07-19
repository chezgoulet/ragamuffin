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
