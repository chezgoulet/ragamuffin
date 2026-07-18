package graph

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertEntityDedup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id1, err := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "Alice", Kind: KindPerson})
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	id2, err := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "Alice", Kind: KindPerson})
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if id1 != id2 {
		t.Errorf("same (vault,name,kind) should dedup: %q != %q", id1, id2)
	}

	// Different vault → different entity.
	id3, err := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "other", Name: "Alice", Kind: KindPerson})
	if err != nil {
		t.Fatalf("upsert 3: %v", err)
	}
	if id3 == id1 {
		t.Error("different vault should not dedup")
	}
}

func TestAddEdgeAndCurrentQuery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "Alice", Kind: KindPerson})
	p, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "Proj", Kind: KindProject})

	if _, err := s.AddEdge(ctx, Edge{ID: uuid.NewString(), Vault: "v", SourceID: a, TargetID: p, Type: "works_on", Fact: "Alice works on Proj"}, false); err != nil {
		t.Fatalf("add edge: %v", err)
	}

	edges, err := s.Edges(ctx, EdgeQuery{Vault: "v"})
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("want 1 current edge, got %d", len(edges))
	}
	if edges[0].Type != "works_on" || edges[0].InvalidatedAt != "" {
		t.Errorf("unexpected edge: %+v", edges[0])
	}
}

func TestInvalidateNotDelete_TimeTravel(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "Alice", Kind: KindPerson})
	acme, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "Acme", Kind: KindOrg})
	globex, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "Globex", Kind: KindOrg})

	// t0: Alice works_at Acme.
	t0 := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := s.AddEdge(ctx, Edge{ID: uuid.NewString(), Vault: "v", SourceID: a, TargetID: acme, Type: "works_at", ValidFrom: t0}, false); err != nil {
		t.Fatalf("add edge acme: %v", err)
	}

	mid := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)

	// now: Alice works_at Globex — invalidates the Acme edge.
	if _, err := s.AddEdge(ctx, Edge{ID: uuid.NewString(), Vault: "v", SourceID: a, TargetID: globex, Type: "works_at"}, true); err != nil {
		t.Fatalf("add edge globex: %v", err)
	}

	// Current view: only the Globex edge is valid.
	current, err := s.Edges(ctx, EdgeQuery{Vault: "v"})
	if err != nil {
		t.Fatalf("current edges: %v", err)
	}
	if len(current) != 1 || current[0].TargetID != globex {
		t.Fatalf("current view should show only Globex, got %+v", current)
	}

	// Time-travel to mid: Alice still works_at Acme (Globex not yet known).
	past, err := s.Edges(ctx, EdgeQuery{Vault: "v", AsOf: mid})
	if err != nil {
		t.Fatalf("as_of edges: %v", err)
	}
	if len(past) != 1 || past[0].TargetID != acme {
		t.Fatalf("as_of=%s should show Acme, got %+v", mid, past)
	}

	// Nothing was deleted: total rows (via stats) counts both edges.
	_, edges, invalidated, err := s.Stats(ctx, "v")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if edges != 2 {
		t.Errorf("want 2 total edges (invalidate never deletes), got %d", edges)
	}
	if invalidated != 1 {
		t.Errorf("want 1 invalidated edge, got %d", invalidated)
	}
}

func TestEdgesFilterByType(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "A", Kind: KindPerson})
	b, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "B", Kind: KindPerson})
	s.AddEdge(ctx, Edge{ID: uuid.NewString(), Vault: "v", SourceID: a, TargetID: b, Type: "knows"}, false)
	s.AddEdge(ctx, Edge{ID: uuid.NewString(), Vault: "v", SourceID: a, TargetID: b, Type: "manages"}, false)

	edges, err := s.Edges(ctx, EdgeQuery{Vault: "v", Type: "manages"})
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	if len(edges) != 1 || edges[0].Type != "manages" {
		t.Errorf("type filter failed: %+v", edges)
	}
}

func TestEntityAsOfNotFound(t *testing.T) {
	s := openTestStore(t)
	view, err := s.EntityAsOf(context.Background(), "missing", "")
	if err != nil {
		t.Fatalf("EntityAsOf: %v", err)
	}
	if view != nil {
		t.Errorf("expected nil view for missing entity, got %+v", view)
	}
}

func TestInvalidateEdge(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "A", Kind: KindPerson})
	b, _ := s.UpsertEntity(ctx, Entity{ID: uuid.NewString(), Vault: "v", Name: "B", Kind: KindPerson})
	id, _ := s.AddEdge(ctx, Edge{ID: uuid.NewString(), Vault: "v", SourceID: a, TargetID: b, Type: "knows"}, false)

	if err := s.InvalidateEdge(ctx, id); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	// Second invalidate should error (already invalidated).
	if err := s.InvalidateEdge(ctx, id); err == nil {
		t.Error("expected error invalidating already-invalidated edge")
	}
	current, _ := s.Edges(ctx, EdgeQuery{Vault: "v"})
	if len(current) != 0 {
		t.Errorf("invalidated edge should not appear in current view, got %+v", current)
	}
}
