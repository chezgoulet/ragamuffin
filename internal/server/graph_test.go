package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// ── displayName ──────────────────────────────────────────────────────────────

func TestDisplayName_StripsExtension(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"notes.md", "notes"},
		{"README.txt", "README"},
		{"index.html", "index"},
		{"noext", "noext"},
		{"archive.tar.gz", "archive.tar"},
	}
	for _, tt := range tests {
		got := displayName(tt.input)
		if got != tt.want {
			t.Errorf("displayName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDisplayName_ConvertsPathSeparators(t *testing.T) {
	got := displayName("projects/ragamuffin/docs/guide.md")
	want := "projects / ragamuffin / docs / guide"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDisplayName_ConvertsUnderscores(t *testing.T) {
	got := displayName("my_notes_file.md")
	want := "my notes file"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDisplayName_ConvertsHyphens(t *testing.T) {
	got := displayName("my-notes-file.md")
	want := "my notes file"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDisplayName_CombinedTransformations(t *testing.T) {
	got := displayName("vault/project_a/meeting-notes-2024.md")
	want := "vault / project a / meeting notes 2024"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDisplayName_EmptyString(t *testing.T) {
	got := displayName("")
	want := ""
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDisplayName_Dotfile(t *testing.T) {
	got := displayName(".gitignore")
	// Dotfiles are returned as-is (guard prevents stripping entire filename as extension)
	want := ".gitignore"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDisplayName_DotEnv(t *testing.T) {
	got := displayName(".env")
	want := ".env"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ── handleGraph edge cases ───────────────────────────────────────────────────

func TestHandleGraph_NonexistentVault(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph?entity=test&depth=1", nil)
	req.SetPathValue("name", "nonexistent")
	// Simulate the vault context that handleGraph checks
	// handleGraph uses vaultFromContext which calls r.Context()
	// The test server has no vaults configured, so it falls back to "default"
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Nodes == nil {
		t.Error("expected non-nil nodes")
	}
}

func TestHandleGraph_EntityDepth1(t *testing.T) {
	// entity with depth>0 returns empty graph when Qdrant is unavailable
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph?entity=test&depth=1", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(w.Body).Decode(&resp)
	// Without Qdrant, returns empty graph (depth>0 needs Qdrant scroll)
	if len(resp.Nodes) != 0 {
		t.Errorf("expected 0 nodes without Qdrant, got %d", len(resp.Nodes))
	}
}

func TestHandleGraph_EntityWithSpaces(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph?entity=John+Doe&depth=0", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	// Without a live Qdrant client, the handler returns empty graph.
	// This test confirms the endpoint responds without error even
	// when the entity has URL-encoded spaces.
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(w.Body).Decode(&resp)
	t.Logf("got %d nodes (no client = empty graph)", len(resp.Nodes))
}

func TestHandleGraph_DefaultLimit(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Nodes) > 50 {
		t.Errorf("expected <=50 nodes with default limit, got %d", len(resp.Nodes))
	}
}

func TestHandleGraph_ZeroLimit(t *testing.T) {
	// limit=0 should be clamped to >0 default (50)
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph?limit=0", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleGraph_NegativeLimit(t *testing.T) {
	// negative limit should fall back to default (50)
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph?limit=-5", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleGraph_ResponseStructure(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/graph", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	var resp graphTestResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Nodes == nil {
		t.Error("expected nodes field")
	}
	if resp.Edges == nil {
		t.Error("expected edges field")
	}
}

// ── fullGraph with nil Qdrant ────────────────────────────────────────────────

func TestFullGraph_NoQdrant_ReturnsEmpty(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	// fullGraph is called by handleGraph when entity is empty
	srv.fullGraph(rec, httptest.NewRequest("GET", "/graph", nil), "default", 50)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Nodes) != 0 {
		t.Errorf("expected empty nodes, got %d", len(resp.Nodes))
	}
	if len(resp.Edges) != 0 {
		t.Errorf("expected empty edges, got %d", len(resp.Edges))
	}
}

// ── entityGraph edge cases ───────────────────────────────────────────────────

func TestEntityGraph_Depth0_ReturnsEntityOnly(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/graph?entity=test&depth=0", nil)
	srv.entityGraph(rec, req, "default", "test", 0, 50)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(resp.Nodes))
	}
	node := resp.Nodes[0].(map[string]interface{})
	if node["id"] != "entity:test" {
		t.Errorf("expected entity:test, got %v", node["id"])
	}
	if node["type"] != "entity" {
		t.Errorf("expected type entity, got %v", node["type"])
	}
}

func TestEntityGraph_NoQdrant(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/graph?entity=test&depth=1", nil)
	srv.entityGraph(rec, req, "default", "test", 1, 50)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Nodes) != 0 {
		t.Errorf("expected 0 nodes without Qdrant, got %d", len(resp.Nodes))
	}
}

func TestEntityGraph_NoQdrantWithEmptyString(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/graph?entity=&depth=1", nil)
	srv.entityGraph(rec, req, "default", "", 1, 50)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(resp.Nodes))
	}
}

// ── graphNode / graphEdge types ──────────────────────────────────────────────

func TestGraphNode_JSONSerialization(t *testing.T) {
	n := graphNode{ID: "file:test.md", Type: "file", Label: "test"}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded graphNode
	json.Unmarshal(data, &decoded)
	if decoded.ID != "file:test.md" {
		t.Errorf("expected file:test.md, got %q", decoded.ID)
	}
	if decoded.Type != "file" {
		t.Errorf("expected file, got %q", decoded.Type)
	}
	if decoded.Label != "test" {
		t.Errorf("expected test, got %q", decoded.Label)
	}
}

func TestGraphEdge_JSONSerialization(t *testing.T) {
	e := graphEdge{Source: "file:a.md", Target: "file:b.md", Relationship: "links_to"}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded graphEdge
	json.Unmarshal(data, &decoded)
	if decoded.Source != "file:a.md" {
		t.Errorf("expected file:a.md, got %q", decoded.Source)
	}
	if decoded.Target != "file:b.md" {
		t.Errorf("expected file:b.md, got %q", decoded.Target)
	}
	if decoded.Relationship != "links_to" {
		t.Errorf("expected links_to, got %q", decoded.Relationship)
	}
}

func TestGraphResponse_Empty(t *testing.T) {
	resp := graphResponse{
		Nodes: []graphNode{},
		Edges: []graphEdge{},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded graphResponse
	json.Unmarshal(data, &decoded)
	if len(decoded.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(decoded.Nodes))
	}
	if len(decoded.Edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(decoded.Edges))
	}
}

// ── entityBFS unit tests ─────────────────────────────────────────────────────

func TestEntityBFS_RootEntity(t *testing.T) {
	eb := newEntityBFS("test-entity", 1, 50)
	nodes := eb.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 root node, got %d", len(nodes))
	}
	if nodes[0].ID != "entity:test-entity" {
		t.Errorf("expected entity:test-entity, got %q", nodes[0].ID)
	}
	if nodes[0].Type != "entity" {
		t.Errorf("expected type entity, got %q", nodes[0].Type)
	}
}

func TestEntityBFS_AddMatch_CreatesFileNodeAndEdge(t *testing.T) {
	eb := newEntityBFS("project", 1, 50)
	eb.AddMatch("docs/readme.md")

	nodes := eb.Nodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes (entity + file), got %d", len(nodes))
	}

	edges := eb.Edges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].Source != "file:docs/readme.md" {
		t.Errorf("expected source file:docs/readme.md, got %q", edges[0].Source)
	}
	if edges[0].Target != "entity:project" {
		t.Errorf("expected target entity:project, got %q", edges[0].Target)
	}
	if edges[0].Relationship != "contains" {
		t.Errorf("expected relationship contains, got %q", edges[0].Relationship)
	}
}

func TestEntityBFS_DuplicateMatch_NoOp(t *testing.T) {
	eb := newEntityBFS("topic", 1, 50)
	eb.AddMatch("file-a.md")
	eb.AddMatch("file-a.md") // duplicate
	nodes := eb.Nodes()
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes after duplicate add, got %d", len(nodes))
	}
}

func TestEntityBFS_EmptyMatch_NoOp(t *testing.T) {
	eb := newEntityBFS("topic", 1, 50)
	eb.AddMatch("")
	nodes := eb.Nodes()
	if len(nodes) != 1 {
		t.Errorf("expected only root node after empty match, got %d", len(nodes))
	}
}

func TestEntityBFS_BFSTraversal_Depth1(t *testing.T) {
	eb := newEntityBFS("core", 1, 50)
	eb.AddMatch("main.go")
	eb.AddLink("main.go", "lib.go")
	eb.AddLink("main.go", "util.go")
	eb.Run()

	nodes := eb.Nodes()
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}

	edges := eb.Edges()
	if len(edges) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(edges))
	}
}

func TestEntityBFS_BFSTraversal_Depth2(t *testing.T) {
	eb := newEntityBFS("core", 2, 50)
	eb.AddMatch("main.go")
	eb.AddLink("main.go", "lib.go")
	eb.AddLink("lib.go", "sub.go")
	eb.AddLink("sub.go", "deep.go")
	eb.Run()

	if len(eb.Nodes()) != 4 {
		t.Errorf("expected 4 nodes at depth 2, got %d", len(eb.Nodes()))
	}
}

func TestEntityBFS_BFSTraversal_Depth0_NoLinks(t *testing.T) {
	eb := newEntityBFS("core", 0, 50)
	eb.AddMatch("main.go")
	eb.AddLink("main.go", "lib.go")
	eb.Run()

	if len(eb.Nodes()) != 1 {
		t.Errorf("expected 1 node at depth 0, got %d", len(eb.Nodes()))
	}
}

func TestEntityBFS_LimitCap(t *testing.T) {
	eb := newEntityBFS("core", 1, 2)
	eb.AddMatch("main.go")
	eb.AddMatch("lib.go")
	eb.AddMatch("util.go")

	if len(eb.Nodes()) > 2 {
		t.Errorf("expected at most 2 nodes at limit=2, got %d", len(eb.Nodes()))
	}
}

func TestEntityBFS_NoLinksToNonExistent(t *testing.T) {
	eb := newEntityBFS("core", 2, 50)
	eb.AddMatch("main.go")
	eb.AddLink("main.go", "lib.go")
	eb.Run()

	if len(eb.Nodes()) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(eb.Nodes()))
	}
}

func TestEntityBFS_LinksToAlreadyVisited_NoDupes(t *testing.T) {
	eb := newEntityBFS("core", 2, 50)
	eb.AddMatch("a.go")
	eb.AddMatch("b.go")
	eb.AddLink("a.go", "b.go")
	eb.AddLink("b.go", "a.go")
	eb.Run()

	nodes := eb.Nodes()
	edges := eb.Edges()
	expectedEdges := 3
	if len(edges) != expectedEdges {
		t.Errorf("expected %d edges, got %d", expectedEdges, len(edges))
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
}
