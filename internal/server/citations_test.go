package server

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
)

// TestDoAskCitedNoEmbedder verifies doAskCited returns an error rather than
// panicking when the LLM is configured but no embedder is wired.
func TestDoAskCitedNoEmbedder(t *testing.T) {
	s := &Server{
		cfg:         &config.Config{LLMProvider: "openai", LLMAPIKey: "sk-test"},
		shutdownCtx: context.Background(),
		logger:      slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	_, _, _, err := s.doAskCited(context.Background(), "q", 5)
	if err == nil {
		t.Fatal("expected error when embedder is not configured, got nil")
	}
}

func TestBuildCitedContext(t *testing.T) {
	chunks := []citedChunk{
		{chunkID: "id-1", sourceFile: "a.md", chunkIndex: 0, score: 0.9, text: "alpha"},
		{chunkID: "", sourceFile: "skip.md", text: "no id, skipped"},
		{chunkID: "id-2", sourceFile: "b.md", chunkIndex: 3, score: 0.5, text: "beta"},
	}
	ctx, lookup := buildCitedContext(chunks)

	if len(lookup) != 2 {
		t.Fatalf("expected 2 lookup entries, got %d", len(lookup))
	}
	if _, ok := lookup["id-1"]; !ok {
		t.Error("id-1 missing from lookup")
	}
	if _, ok := lookup[""]; ok {
		t.Error("empty chunk id should not be in lookup")
	}
	for _, want := range []string{"[chunk_id: id-1", "alpha", "[chunk_id: id-2", "beta"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("context missing %q\n%s", want, ctx)
		}
	}
	if strings.Contains(ctx, "no id, skipped") {
		t.Error("chunk without id should be excluded from context")
	}
}

func TestParseCitations(t *testing.T) {
	lookup := map[string]citedChunk{
		"id-1": {chunkID: "id-1", sourceFile: "a.md", chunkIndex: 0, score: 0.9, text: "alpha"},
		"id-2": {chunkID: "id-2", sourceFile: "b.md", chunkIndex: 3, score: 0.5, text: "beta"},
	}

	tests := []struct {
		name   string
		answer string
		want   []string
	}{
		{"no markers", "plain answer", nil},
		{"single", "The sky is blue [cite: id-1].", []string{"id-1"}},
		{"multiple in one marker", "Both [cite: id-1, id-2] agree.", []string{"id-1", "id-2"}},
		{"dedup preserves order", "X [cite: id-2]. Y [cite: id-1]. Z [cite: id-2].", []string{"id-2", "id-1"}},
		{"none sentinel dropped", "Unknown [cite: none].", nil},
		{"hallucinated id dropped", "Fake [cite: id-999].", nil},
		{"mixed valid and invalid", "A [cite: id-1, id-999, none].", []string{"id-1"}},
		{"whitespace tolerant", "A [cite:  id-1 ].", []string{"id-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCitations(tt.answer, lookup)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d citations, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, id := range tt.want {
				if got[i].ChunkID != id {
					t.Errorf("citation[%d] = %q, want %q", i, got[i].ChunkID, id)
				}
			}
		})
	}
}

func TestParseCitations_ResolvesMetadata(t *testing.T) {
	lookup := map[string]citedChunk{
		"id-1": {chunkID: "id-1", sourceFile: "a.md", chunkIndex: 7, score: 0.42, text: "alpha"},
	}
	got := parseCitations("Fact [cite: id-1].", lookup)
	if len(got) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(got))
	}
	c := got[0]
	if c.SourceFile != "a.md" || c.ChunkIndex != 7 || c.Score != 0.42 || c.Text != "alpha" {
		t.Errorf("metadata not resolved: %+v", c)
	}
}
