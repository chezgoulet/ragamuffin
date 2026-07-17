package retrieval

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type stubCompleter struct {
	resp    string
	err     error
	prompts []string
}

func (s *stubCompleter) Complete(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	return s.resp, s.err
}

func TestParseRewriteMode(t *testing.T) {
	cases := []struct {
		in   string
		want RewriteMode
		ok   bool
	}{
		{"", RewriteOff, true},
		{"off", RewriteOff, true},
		{"HyDE", RewriteHyDE, true},
		{" stepback ", RewriteStepBack, true},
		{"multiquery", RewriteMultiQuery, true},
		{"nonsense", RewriteOff, false},
	}
	for _, c := range cases {
		got, ok := ParseRewriteMode(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseRewriteMode(%q) = (%v,%v), want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRewriteOffAndNilCompleter(t *testing.T) {
	if got := Rewrite(context.Background(), &stubCompleter{}, RewriteOff, "q"); !reflect.DeepEqual(got, []string{"q"}) {
		t.Errorf("off: got %v", got)
	}
	if got := Rewrite(context.Background(), nil, RewriteHyDE, "q"); !reflect.DeepEqual(got, []string{"q"}) {
		t.Errorf("nil completer: got %v", got)
	}
}

func TestRewriteHyDE(t *testing.T) {
	c := &stubCompleter{resp: "A hypothetical answer passage."}
	got := Rewrite(context.Background(), c, RewriteHyDE, "what is x?")
	if !reflect.DeepEqual(got, []string{"A hypothetical answer passage."}) {
		t.Fatalf("got %v", got)
	}
}

func TestRewriteHyDEFallsBackOnError(t *testing.T) {
	c := &stubCompleter{err: errors.New("boom")}
	got := Rewrite(context.Background(), c, RewriteHyDE, "orig query")
	if !reflect.DeepEqual(got, []string{"orig query"}) {
		t.Fatalf("expected fallback to original, got %v", got)
	}
}

func TestRewriteHyDEEmptyFallsBack(t *testing.T) {
	c := &stubCompleter{resp: "   \n  "}
	got := Rewrite(context.Background(), c, RewriteHyDE, "orig")
	if !reflect.DeepEqual(got, []string{"orig"}) {
		t.Fatalf("empty response should fall back, got %v", got)
	}
}

func TestRewriteStepBackReturnsBoth(t *testing.T) {
	c := &stubCompleter{resp: "What are the general principles of X?"}
	got := Rewrite(context.Background(), c, RewriteStepBack, "specific detail of X?")
	want := []string{"What are the general principles of X?", "specific detail of X?"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRewriteMultiQueryParsesAndDedups(t *testing.T) {
	c := &stubCompleter{resp: "1. how to cook rice\n2. rice cooking method\n- how to cook rice\n"}
	got := Rewrite(context.Background(), c, RewriteMultiQuery, "cooking rice")
	// Two unique paraphrases + original appended.
	want := []string{"how to cook rice", "rice cooking method", "cooking rice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRewriteMultiQueryKeepsOriginalIfPresent(t *testing.T) {
	c := &stubCompleter{resp: "cooking rice\nboiling rice"}
	got := Rewrite(context.Background(), c, RewriteMultiQuery, "cooking rice")
	want := []string{"cooking rice", "boiling rice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCleanLine(t *testing.T) {
	cases := map[string]string{
		"1. hello":   "hello",
		"2) world":   "world",
		"- dash":     "dash",
		"* star":     "star",
		"\"quoted\"": "quoted",
		"  spaced  ": "spaced",
		"10. ten":    "ten",
		"plain":      "plain",
	}
	for in, want := range cases {
		if got := cleanLine(in); got != want {
			t.Errorf("cleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}
