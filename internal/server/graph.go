package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

// ── Types ──────────────────────────────────────────────────────────────────────

type graphNode struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Label      string `json:"label"`
	EntityType string `json:"entity_type,omitempty"`
}

type graphEdge struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Relationship string `json:"relationship"`
}

type graphResponse struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// fileLinkEntry groups a file path with its BFS hop depth.
type fileLinkEntry struct {
	Path string
	Hop  int
}

// entityBFS performs BFS traversal over file link relationships starting from
// a root entity. Intended for building focused sub-graphs — testable without
// a Qdrant backend by setting up nodes/links/matches directly.
type entityBFS struct {
	entityID string
	depth    int
	limit    int

	nodes     map[string]graphNode
	edges     map[string]graphEdge
	visited   map[string]bool
	fileLinks map[string][]string
	queue     []fileLinkEntry
	edgeKeyFn func(s, t, rel string) string
}

// newEntityBFS initialises a BFS struct rooted at the given entity name.
func newEntityBFS(entity string, depth, limit int) *entityBFS {
	entityID := "entity:" + entity
	eb := &entityBFS{
		entityID:  entityID,
		depth:     depth,
		limit:     limit,
		nodes:     make(map[string]graphNode),
		edges:     make(map[string]graphEdge),
		visited:   make(map[string]bool),
		fileLinks: make(map[string][]string),
		queue:     nil,
		edgeKeyFn: func(s, t, rel string) string { return s + "|" + t + "|" + rel },
	}
	// Root entity node
	eb.nodes[entityID] = graphNode{
		ID:    entityID,
		Type:  "entity",
		Label: entity,
	}
	return eb
}

// AddMatch registers a file that contains the entity, connecting it to the
// root entity node via a "contains" edge. No-op if the file was already seen.
func (eb *entityBFS) AddMatch(sourceFile string) {
	if sourceFile == "" || eb.visited[sourceFile] {
		return
	}
	eb.visited[sourceFile] = true
	fileID := "file:" + sourceFile
	eb.nodes[fileID] = graphNode{
		ID:    fileID,
		Type:  "file",
		Label: displayName(sourceFile),
	}
	k := eb.edgeKeyFn(eb.entityID, fileID, "contains")
	eb.edges[k] = graphEdge{
		Source:       fileID,
		Target:       eb.entityID,
		Relationship: "contains",
	}
	eb.queue = append(eb.queue, fileLinkEntry{Path: sourceFile, Hop: 0})
}

// AddLink registers a cross-file reference from source to target.
func (eb *entityBFS) AddLink(source, target string) {
	if source == "" || target == "" {
		return
	}
	eb.fileLinks[source] = append(eb.fileLinks[source], target)
}

// Run traverses the file link graph up to the configured depth and limit.
// Call after all AddMatch / AddLink calls. May be called at most once.
func (eb *entityBFS) Run() {
	for len(eb.queue) > 0 && len(eb.nodes) < eb.limit {
		current := eb.queue[0]
		eb.queue = eb.queue[1:]

		if current.Hop >= eb.depth {
			continue
		}

		for _, targetPath := range eb.fileLinks[current.Path] {
			if targetPath == "" || eb.visited[targetPath] {
				continue
			}
			eb.visited[targetPath] = true

			targetID := "file:" + targetPath
			eb.nodes[targetID] = graphNode{
				ID:    targetID,
				Type:  "file",
				Label: displayName(targetPath),
			}

			currentFileID := "file:" + current.Path
			k := eb.edgeKeyFn(currentFileID, targetID, "links_to")
			eb.edges[k] = graphEdge{
				Source:       currentFileID,
				Target:       targetID,
				Relationship: "links_to",
			}

			if current.Hop+1 < eb.depth {
				eb.queue = append(eb.queue, fileLinkEntry{Path: targetPath, Hop: current.Hop + 1})
			}

			if len(eb.nodes) >= eb.limit {
				return
			}
		}
	}
}

// Nodes returns the accumulated node list, capped at the configured limit.
func (eb *entityBFS) Nodes() []graphNode {
	list := make([]graphNode, 0, len(eb.nodes))
	for _, n := range eb.nodes {
		list = append(list, n)
		if len(list) >= eb.limit {
			break
		}
	}
	return list
}

// Edges returns the accumulated edge list, capped at the configured limit.
func (eb *entityBFS) Edges() []graphEdge {
	list := make([]graphEdge, 0, len(eb.edges))
	for _, e := range eb.edges {
		list = append(list, e)
		if len(list) >= eb.limit {
			break
		}
	}
	return list
}

// ── Handler ────────────────────────────────────────────────────────────────────

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	entity := r.URL.Query().Get("entity")
	depth := 1
	if d := r.URL.Query().Get("depth"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed >= 1 && parsed <= 5 {
			depth = parsed
		}
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	// Determine vault
	vaultName := vaultFromContext(r.Context())
	if vaultName == "" {
		vaultName = "default"
	}
	idx := s.indexers.Get(vaultName)
	if idx == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
		return
	}

	if entity == "" {
		s.fullGraph(w, r, vaultName, limit)
	} else {
		s.entityGraph(w, r, vaultName, entity, depth, limit)
	}
}

// ── Full graph ─────────────────────────────────────────────────────────────────

func (s *Server) fullGraph(w http.ResponseWriter, r *http.Request, vaultName string, limit int) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeJSON(w, 200, graphResponse{Nodes: []graphNode{}, Edges: []graphEdge{}})
		return
	}

	nodes := make(map[string]graphNode)
	edges := make(map[string]graphEdge)
	edgeKey := func(s, t, rel string) string { return s + "|" + t + "|" + rel }

	var offset *pb.PointId
	totalNodes := 0

	for {
		scrollCtx, scrollCancel := context.WithTimeout(ctx, 10*time.Second)
		points, nextOffset, err := qc.Scroll(scrollCtx, 100, offset)
		scrollCancel()
		if err != nil {
			s.log(ctx).Warn("graph: scroll failed", "error", err)
			break
		}

		for _, point := range points {
			if totalNodes >= limit {
				break
			}

			sourceFile := point.GetPayload()["source_file"].GetStringValue()
			if sourceFile == "" {
				continue
			}

			fileID := "file:" + sourceFile
			if _, exists := nodes[fileID]; !exists {
				nodes[fileID] = graphNode{
					ID:    fileID,
					Type:  "file",
					Label: displayName(sourceFile),
				}
				totalNodes++
			}

			// Links to other files
			if linksVal := point.GetPayload()["links_to"]; linksVal != nil {
				for _, linkVal := range linksVal.GetListValue().GetValues() {
					targetFile := linkVal.GetStringValue()
					if targetFile == "" {
						continue
					}
					targetID := "file:" + targetFile
					k := edgeKey(fileID, targetID, "links_to")
					if _, exists := edges[k]; !exists {
						edges[k] = graphEdge{
							Source:       fileID,
							Target:       targetID,
							Relationship: "links_to",
						}
					}
				}
			}
		}

		if nextOffset == nil || totalNodes >= limit {
			break
		}
		offset = nextOffset
	}

	nodeList := make([]graphNode, 0, len(nodes))
	edgeList := make([]graphEdge, 0, len(edges))
	for _, n := range nodes {
		nodeList = append(nodeList, n)
		if len(nodeList) >= limit {
			break
		}
	}
	for _, e := range edges {
		edgeList = append(edgeList, e)
		if len(edgeList) >= limit {
			break
		}
	}

	writeJSON(w, 200, graphResponse{Nodes: nodeList, Edges: edgeList})
}

// ── Entity-focused graph ───────────────────────────────────────────────────────

func (s *Server) entityGraph(w http.ResponseWriter, r *http.Request, vaultName, entity string, depth, limit int) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeJSON(w, 200, graphResponse{Nodes: []graphNode{}, Edges: []graphEdge{}})
		return
	}

	eb := newEntityBFS(entity, depth, limit)

	// Single scroll pass: build entity match queue and link map simultaneously
	{
		var scrollOffset *pb.PointId
		for {
			scrollCtx, scrollCancel := context.WithTimeout(ctx, 10*time.Second)
			points, nextOffset, err := qc.Scroll(scrollCtx, 500, scrollOffset)
			scrollCancel()
			if err != nil {
				break
			}
			for _, p := range points {
				if p.GetPayload() == nil {
					continue
				}

				src := p.GetPayload()["source_file"].GetStringValue()

				if src != "" {
					// Entity match: check the text payload field directly
					if text := p.GetPayload()["text"].GetStringValue(); text != "" && strings.Contains(text, entity) {
						eb.AddMatch(src)
					}

					// Link map: collect cross-file references
					if linksVal := p.GetPayload()["links_to"]; linksVal != nil {
						for _, linkVal := range linksVal.GetListValue().GetValues() {
							eb.AddLink(src, linkVal.GetStringValue())
						}
					}
				}
			}
			if nextOffset == nil || len(eb.Nodes()) >= limit {
				break
			}
			scrollOffset = nextOffset
		}
	}

	// BFS traversal up to depth using pre-loaded link map
	eb.Run()

	writeJSON(w, 200, graphResponse{Nodes: eb.Nodes(), Edges: eb.Edges()})
}

// ── Helpers ─────────────────────────────────────────────────────────────────────

func displayName(path string) string {
	ext := filepath.Ext(path)
	if ext != "" && ext != path {
		path = strings.TrimSuffix(path, ext)
	}
	path = strings.ReplaceAll(path, "/", " / ")
	path = strings.ReplaceAll(path, "_", " ")
	path = strings.ReplaceAll(path, "-", " ")
	return strings.TrimSpace(path)
}
