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

// ── Handler ────────────────────────────────────────────────────────────────────

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	entity := r.URL.Query().Get("entity")
	depth := 1
	if d := r.URL.Query().Get("depth"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed >= 0 && parsed <= 3 {
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

	// Depth 0 doesn't need any backend — just return the entity node
	if depth == 0 {
		writeJSON(w, 200, graphResponse{
			Nodes: []graphNode{{
				ID: "entity:" + entity, Type: "entity", Label: entity,
			}},
			Edges: []graphEdge{},
		})
		return
	}

	if qc == nil {
		writeJSON(w, 200, graphResponse{Nodes: []graphNode{}, Edges: []graphEdge{}})
		return
	}

	nodes := make(map[string]graphNode)
	edges := make(map[string]graphEdge)
	edgeKey := func(s, t, rel string) string { return s + "|" + t + "|" + rel }

	entityID := "entity:" + entity
	nodes[entityID] = graphNode{
		ID:    entityID,
		Type:  "entity",
		Label: entity,
	}

	// Find files containing this entity from vault collection
	// Scroll entire vault collection, search payload for entity match
	type fileEntry struct {
		path string
		hop  int
	}

	visited := make(map[string]bool)
	var queue []fileEntry

	// Single scroll pass: build entity match queue and link map simultaneously
	fileLinks := make(map[string][]string) // source_file → linked files
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

				// Entity match: check the text payload field directly
				if src != "" && !visited[src] {
					if text := p.GetPayload()["text"].GetStringValue(); text != "" && strings.Contains(text, entity) {
						visited[src] = true
						queue = append(queue, fileEntry{path: src, hop: 0})
						fileID := "file:" + src
						nodes[fileID] = graphNode{
							ID:    fileID,
							Type:  "file",
							Label: displayName(src),
						}
						k := edgeKey(entityID, fileID, "contains")
						edges[k] = graphEdge{
							Source:       fileID,
							Target:       entityID,
							Relationship: "contains",
						}
					}
				}

				// Link map: collect cross-file references
				if src != "" {
					if linksVal := p.GetPayload()["links_to"]; linksVal != nil {
						for _, linkVal := range linksVal.GetListValue().GetValues() {
							tgt := linkVal.GetStringValue()
							if tgt != "" {
								fileLinks[src] = append(fileLinks[src], tgt)
							}
						}
					}
				}
			}
			if nextOffset == nil {
				break
			}
			scrollOffset = nextOffset
		}
	}

	// BFS traversal up to depth using pre-loaded map
	for len(queue) > 0 && len(nodes) < limit {
		current := queue[0]
		queue = queue[1:]

		if current.hop >= depth {
			continue
		}

		for _, targetPath := range fileLinks[current.path] {
			if targetPath == "" || visited[targetPath] {
				continue
			}
			visited[targetPath] = true

			targetID := "file:" + targetPath
			nodes[targetID] = graphNode{
				ID:    targetID,
				Type:  "file",
				Label: displayName(targetPath),
			}

			currentFileID := "file:" + current.path
			k := edgeKey(currentFileID, targetID, "links_to")
			edges[k] = graphEdge{
				Source:       currentFileID,
				Target:       targetID,
				Relationship: "links_to",
			}

			if current.hop+1 < depth {
				queue = append(queue, fileEntry{path: targetPath, hop: current.hop + 1})
			}

			if len(nodes) >= limit {
				break
			}
		}
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
