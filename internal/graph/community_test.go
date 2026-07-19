package graph

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// addEntity is a helper that upserts and returns the id.
func addEntity(t *testing.T, s *Store, vault, name string) string {
	t.Helper()
	id, err := s.UpsertEntity(context.Background(), Entity{
		ID: uuid.NewString(), Vault: vault, Name: name, Kind: KindConcept,
	})
	if err != nil {
		t.Fatalf("upsert %s: %v", name, err)
	}
	return id
}

func addRel(t *testing.T, s *Store, vault, src, tgt string) {
	t.Helper()
	if _, err := s.AddEdge(context.Background(), Edge{
		ID: uuid.NewString(), Vault: vault, SourceID: src, TargetID: tgt,
		Type: "rel", Fact: "connected",
	}, false); err != nil {
		t.Fatalf("add edge: %v", err)
	}
}

func TestLouvain_TwoClusters(t *testing.T) {
	// Two triangles joined by a single bridge edge → two communities.
	//   {0,1,2} densely connected, {3,4,5} densely connected, 2--3 bridge.
	g := newLouvainGraph(6)
	tri := func(a, b, c int) {
		g.addEdge(a, b, 1)
		g.addEdge(b, c, 1)
		g.addEdge(a, c, 1)
	}
	tri(0, 1, 2)
	tri(3, 4, 5)
	g.addEdge(2, 3, 1) // bridge

	labels := louvain(g)
	dense, k := denseLabels(labels)
	if k != 2 {
		t.Fatalf("expected 2 communities, got %d (labels=%v)", k, dense)
	}
	if dense[0] != dense[1] || dense[1] != dense[2] {
		t.Errorf("nodes 0,1,2 should share a community: %v", dense)
	}
	if dense[3] != dense[4] || dense[4] != dense[5] {
		t.Errorf("nodes 3,4,5 should share a community: %v", dense)
	}
	if dense[0] == dense[3] {
		t.Errorf("the two triangles should be in different communities: %v", dense)
	}
}

func TestLouvain_Deterministic(t *testing.T) {
	build := func() *louvainGraph {
		g := newLouvainGraph(5)
		g.addEdge(0, 1, 1)
		g.addEdge(1, 2, 1)
		g.addEdge(0, 2, 1)
		g.addEdge(2, 3, 1)
		g.addEdge(3, 4, 1)
		return g
	}
	first, _ := denseLabels(louvain(build()))
	for i := 0; i < 5; i++ {
		got, _ := denseLabels(louvain(build()))
		for j := range first {
			if got[j] != first[j] {
				t.Fatalf("non-deterministic: run %d got %v, want %v", i, got, first)
			}
		}
	}
}

func TestLouvain_NoEdges(t *testing.T) {
	g := newLouvainGraph(3)
	labels := louvain(g)
	_, k := denseLabels(labels)
	if k != 3 {
		t.Errorf("expected 3 singleton communities with no edges, got %d", k)
	}
}

// TestAggregate_InternalEdgeWeightConserved verifies that collapsing communities
// preserves total edge weight (m2). Regression for the intra-community edge
// double-count bug where internal edges contributed 4w instead of 2w.
func TestAggregate_InternalEdgeWeightConserved(t *testing.T) {
	// Two 2-node communities each with an internal edge, plus a bridge.
	//   {0,1} internal edge, {2,3} internal edge, 1--2 bridge.
	g := newLouvainGraph(4)
	g.addEdge(0, 1, 1) // internal to community A
	g.addEdge(2, 3, 1) // internal to community B
	g.addEdge(1, 2, 1) // bridge between A and B
	wantM2 := g.m2

	dense := []int{0, 0, 1, 1}
	ng := aggregate(g, dense, 2)

	if ng.m2 != wantM2 {
		t.Errorf("aggregate must conserve total weight: got m2=%v, want %v", ng.m2, wantM2)
	}
	// Sum of degrees always equals m2 in an undirected weighted graph.
	var degSum float64
	for _, d := range ng.degree {
		degSum += d
	}
	if degSum != wantM2 {
		t.Errorf("sum of degrees must equal m2: got %v, want %v", degSum, wantM2)
	}
}

// TestLouvain_ThreeLevels exercises multiple aggregation passes and asserts
// modularity is non-decreasing across levels.
func TestLouvain_ThreeLevels(t *testing.T) {
	// Four dense triangles, chained by single bridges: forces >1 aggregation.
	g := newLouvainGraph(12)
	tri := func(a, b, c int) {
		g.addEdge(a, b, 1)
		g.addEdge(b, c, 1)
		g.addEdge(a, c, 1)
	}
	tri(0, 1, 2)
	tri(3, 4, 5)
	tri(6, 7, 8)
	tri(9, 10, 11)
	g.addEdge(2, 3, 1)
	g.addEdge(5, 6, 1)
	g.addEdge(8, 9, 1)

	labels := louvain(g)
	_, k := denseLabels(labels)
	if k < 2 || k > 4 {
		t.Fatalf("expected 2..4 communities for chained triangles, got %d", k)
	}
	// Each triangle's nodes must stay together.
	for _, tr := range [][3]int{{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, {9, 10, 11}} {
		if labels[tr[0]] != labels[tr[1]] || labels[tr[1]] != labels[tr[2]] {
			t.Errorf("triangle %v split across communities: %v", tr, labels)
		}
	}
}

func TestDetectCommunities_PersistsAndClusters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := "default"

	a := addEntity(t, s, v, "Alice")
	b := addEntity(t, s, v, "Bob")
	c := addEntity(t, s, v, "Carol")
	x := addEntity(t, s, v, "Xavier")
	y := addEntity(t, s, v, "Yolanda")
	z := addEntity(t, s, v, "Zoe")

	// cluster 1
	addRel(t, s, v, a, b)
	addRel(t, s, v, b, c)
	addRel(t, s, v, a, c)
	// cluster 2
	addRel(t, s, v, x, y)
	addRel(t, s, v, y, z)
	addRel(t, s, v, x, z)
	// weak bridge
	addRel(t, s, v, c, x)

	comms, err := s.DetectCommunities(ctx, v)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(comms) != 2 {
		t.Fatalf("expected 2 communities, got %d", len(comms))
	}

	// Persisted and readable.
	listed, err := s.Communities(ctx, v)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 persisted communities, got %d", len(listed))
	}
	if listed[0].Size < listed[1].Size {
		t.Errorf("communities should be ordered largest-first: %d then %d", listed[0].Size, listed[1].Size)
	}
	total := 0
	for _, cm := range listed {
		total += cm.Size
		got, err := s.GetCommunity(ctx, cm.ID)
		if err != nil || got == nil {
			t.Fatalf("get community %s: %v", cm.ID, err)
		}
		if len(got.MemberIDs) != cm.Size {
			t.Errorf("member count mismatch: %d vs size %d", len(got.MemberIDs), cm.Size)
		}
	}
	if total != 6 {
		t.Errorf("expected 6 members across communities, got %d", total)
	}
}

func TestDetectCommunities_Empty(t *testing.T) {
	s := openTestStore(t)
	comms, err := s.DetectCommunities(context.Background(), "empty")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(comms) != 0 {
		t.Errorf("expected no communities for empty vault, got %d", len(comms))
	}
}

func TestReplaceCommunities_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := "default"
	addEntity(t, s, v, "Solo")

	if _, err := s.DetectCommunities(ctx, v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DetectCommunities(ctx, v); err != nil {
		t.Fatal(err)
	}
	listed, _ := s.Communities(ctx, v)
	if len(listed) != 1 {
		t.Errorf("re-detect should replace, not accumulate: got %d", len(listed))
	}
}
