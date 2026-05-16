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
	// filepath.Ext(".gitignore") returns ".gitignore", so displayName strips it
	want := ""
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

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp graphTestResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(resp.Nodes))
	}
	node := resp.Nodes[0].(map[string]interface{})
	if node["id"] != "entity:John Doe" {
		t.Errorf("expected entity:John Doe, got %v", node["id"])
	}
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
