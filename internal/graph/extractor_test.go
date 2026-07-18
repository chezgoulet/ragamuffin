package graph

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type mockSynth struct {
	resp string
	err  error
}

func (m *mockSynth) Synthesize(_ context.Context, _, _ string) (string, error) {
	return m.resp, m.err
}
func (m *mockSynth) Compare(_ context.Context, _, _, _, _ string) (string, error) { return "", nil }
func (m *mockSynth) Health(_ context.Context) error                               { return nil }

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestParseExtraction(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantEnts int
		wantRels int
		wantErr  bool
	}{
		{
			name:     "plain json",
			raw:      `{"entities":[{"name":"Alice","kind":"person"}],"relations":[{"source":"Alice","target":"Proj","type":"works_on","fact":"x"}]}`,
			wantEnts: 1, wantRels: 1,
		},
		{
			name:     "markdown fenced",
			raw:      "```json\n{\"entities\":[{\"name\":\"A\",\"kind\":\"org\"}],\"relations\":[]}\n```",
			wantEnts: 1, wantRels: 0,
		},
		{
			name:    "invalid json",
			raw:     `not json`,
			wantErr: true,
		},
		{
			name:     "empty object",
			raw:      `{}`,
			wantEnts: 0, wantRels: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseExtraction(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Entities) != tt.wantEnts {
				t.Errorf("entities: got %d, want %d", len(got.Entities), tt.wantEnts)
			}
			if len(got.Relations) != tt.wantRels {
				t.Errorf("relations: got %d, want %d", len(got.Relations), tt.wantRels)
			}
		})
	}
}

func TestNormalizeKind(t *testing.T) {
	cases := map[string]EntityKind{
		"person": KindPerson, "People": KindPerson,
		"company": KindOrg, "team": KindOrg,
		"repo": KindProject, "product": KindProject,
		"idea": KindConcept, "": KindConcept,
	}
	for in, want := range cases {
		if got := normalizeKind(in); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRelType(t *testing.T) {
	cases := map[string]string{
		"works on":    "works_on",
		"Reports-To":  "reports_to",
		"depends_on!": "depends_on",
		"  founded ":  "founded",
	}
	for in, want := range cases {
		if got := normalizeRelType(in); got != want {
			t.Errorf("normalizeRelType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIngestText(t *testing.T) {
	s := newTestStore(t)
	ex := NewExtractor(s, &mockSynth{
		resp: `{"entities":[{"name":"Alice","kind":"person"},{"name":"Ragamuffin","kind":"project"}],"relations":[{"source":"Alice","target":"Ragamuffin","type":"works_on","fact":"Alice works on Ragamuffin"}]}`,
	}, nil)

	ents, edges, err := ex.IngestText(context.Background(), "v", "Alice works on Ragamuffin.")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if ents != 2 || edges != 1 {
		t.Fatalf("got ents=%d edges=%d, want 2/1", ents, edges)
	}

	got, err := s.Edges(context.Background(), EdgeQuery{Vault: "v"})
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	if len(got) != 1 || got[0].Type != "works_on" {
		t.Errorf("unexpected edges: %+v", got)
	}
}

func TestIngestTextDanglingRelationSkipped(t *testing.T) {
	s := newTestStore(t)
	// Relation references an entity not in the entities list → skipped.
	ex := NewExtractor(s, &mockSynth{
		resp: `{"entities":[{"name":"Alice","kind":"person"}],"relations":[{"source":"Alice","target":"Ghost","type":"knows","fact":"x"}]}`,
	}, nil)
	ents, edges, err := ex.IngestText(context.Background(), "v", "text")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if ents != 1 || edges != 0 {
		t.Errorf("dangling relation should be skipped: ents=%d edges=%d", ents, edges)
	}
}

func TestIngestTextLLMError(t *testing.T) {
	s := newTestStore(t)
	ex := NewExtractor(s, &mockSynth{err: errors.New("boom")}, nil)
	if _, _, err := ex.IngestText(context.Background(), "v", "text"); err == nil {
		t.Error("expected error when LLM fails")
	}
}

func TestIngestTextEmpty(t *testing.T) {
	s := newTestStore(t)
	ex := NewExtractor(s, &mockSynth{}, nil)
	ents, edges, err := ex.IngestText(context.Background(), "v", "   ")
	if err != nil || ents != 0 || edges != 0 {
		t.Errorf("empty text should no-op: ents=%d edges=%d err=%v", ents, edges, err)
	}
}

func TestIngestTextNoLLM(t *testing.T) {
	s := newTestStore(t)
	ex := NewExtractor(s, nil, nil)
	if _, _, err := ex.IngestText(context.Background(), "v", "text"); err == nil {
		t.Error("expected error without LLM")
	}
}
