package server

import (
	"context"
	"net/http"
	"strconv"

	pb "github.com/qdrant/go-client/qdrant"
	store "github.com/chezgoulet/ragamuffin/internal/qdrant"
)

// ── Graph types (prefixed to avoid conflict with existing graph.go) ─────

type factGraphNode struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	FactType string `json:"fact_type"` // e.g. "current", "supersedes", "refines"
}

type factGraphEdge struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Relationship string `json:"relationship"`
}

type factGraphResponse struct {
	Key   string          `json:"key"`
	Nodes []factGraphNode `json:"nodes"`
	Edges []factGraphEdge `json:"edges"`
}

// ── Handler ────────────────────────────────────────────────────────────

func (s *Server) handleFactGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeError(w, 400, "MISSING_KEY", "fact key is required")
		return
	}

	depth := 1
	if d := r.URL.Query().Get("depth"); d != "" {
		if v, err := strconv.Atoi(d); err == nil && v >= 0 {
			depth = v
		}
	}

	ctx := r.Context()
	factsStore := s.factsQdrantFor(ctx)
	collection := s.factsCollectionFor(ctx)

	visited := make(map[string]bool)
	var nodes []factGraphNode
	var edges []factGraphEdge

	rootFact := s.fetchFactByPayloadKey(ctx, factsStore, collection, key)
	if rootFact == nil {
		writeError(w, 404, "NOT_FOUND", "fact not found")
		return
	}

	nodes = append(nodes, factGraphNode{
		Key:      key,
		Value:    rootFact.Value,
		FactType: "current",
	})
	visited[key] = true

	s.traverseFactGraph(ctx, factsStore, collection, key, depth, 0, visited, &nodes, &edges)

	writeJSON(w, 200, factGraphResponse{Key: key, Nodes: nodes, Edges: edges})
}

// fetchFactByPayloadKey scrolls the facts collection for a point matching fact_key.
func (s *Server) fetchFactByPayloadKey(ctx context.Context, store store.FactStore, collection, key string) *factResponse {
	points, err := store.ScrollFiltered(ctx, collection, factKeyFilter(key), 1, "")
	if err != nil || len(points) == 0 {
		return nil
	}
	return pointToFact(points[0])
}

// traverseFactGraph does BFS traversal of forward + reverse edges up to maxDepth.
func (s *Server) traverseFactGraph(ctx context.Context, factsStore store.FactStore, collection, key string, maxDepth, currentDepth int, visited map[string]bool, nodes *[]factGraphNode, edges *[]factGraphEdge) {
	if currentDepth >= maxDepth {
		return
	}

	fact := s.fetchFactByPayloadKey(ctx, factsStore, collection, key)
	if fact == nil {
		return
	}

	// ── Forward edges ─────────────────────────────────────────────────────
	if fact.Supersedes != "" {
		*edges = append(*edges, factGraphEdge{Source: key, Target: fact.Supersedes, Relationship: "supersedes"})
		s.tryVisit(ctx, factsStore, collection, fact.Supersedes, "supersedes", maxDepth, currentDepth+1, visited, nodes, edges)
	}
	if fact.Refines != "" {
		*edges = append(*edges, factGraphEdge{Source: key, Target: fact.Refines, Relationship: "refines"})
		s.tryVisit(ctx, factsStore, collection, fact.Refines, "refines", maxDepth, currentDepth+1, visited, nodes, edges)
	}
	for _, target := range fact.Contradicts {
		if target == "" {
			continue
		}
		*edges = append(*edges, factGraphEdge{Source: key, Target: target, Relationship: "contradicts"})
		s.tryVisit(ctx, factsStore, collection, target, "contradicts", maxDepth, currentDepth+1, visited, nodes, edges)
	}
	for _, target := range fact.Supports {
		if target == "" {
			continue
		}
		*edges = append(*edges, factGraphEdge{Source: key, Target: target, Relationship: "supports"})
		s.tryVisit(ctx, factsStore, collection, target, "supports", maxDepth, currentDepth+1, visited, nodes, edges)
	}

	// ── Reverse edges ─────────────────────────────────────────────────────
	for _, re := range s.findReverseEdges(ctx, factsStore, collection, key) {
		*edges = append(*edges, factGraphEdge{Source: re.key, Target: key, Relationship: re.relationship})
		s.tryVisit(ctx, factsStore, collection, re.key, re.relationship, maxDepth, currentDepth+1, visited, nodes, edges)
	}
}

func (s *Server) tryVisit(ctx context.Context, factsStore store.FactStore, collection, target, factType string, maxDepth, nextDepth int, visited map[string]bool, nodes *[]factGraphNode, edges *[]factGraphEdge) {
	if visited[target] {
		return
	}
	visited[target] = true
	tFact := s.fetchFactByPayloadKey(ctx, factsStore, collection, target)
	if tFact == nil {
		return
	}
	*nodes = append(*nodes, factGraphNode{Key: target, Value: tFact.Value, FactType: factType})
	s.traverseFactGraph(ctx, factsStore, collection, target, maxDepth, nextDepth, visited, nodes, edges)
}

// ── Reverse edge discovery ─────────────────────────────────────────────

type reverseEdge struct {
	key          string
	relationship string
}

func (s *Server) findReverseEdges(ctx context.Context, factsStore store.FactStore, collection, key string) []reverseEdge {
	filter := &pb.Filter{
		Should: []*pb.Condition{
			makeKeywordMatch("supersedes", key),
			makeKeywordMatch("refines", key),
			makeKeywordMatch("contradicts", key),
			makeKeywordMatch("supports", key),
		},
	}

	points, err := factsStore.ScrollFiltered(ctx, collection, filter, 100, "")
	if err != nil {
		s.logger.Error("reverse edge scroll failed", "error", err, "key", key)
		return nil
	}

	var results []reverseEdge
	for _, p := range points {
		if p == nil || p.Payload == nil {
			continue
		}
		factKey, _ := getPayloadString(p.Payload, "fact_key")
		if factKey == "" {
			continue
		}
		if sv, _ := getPayloadString(p.Payload, "supersedes"); sv == key {
			results = append(results, reverseEdge{key: factKey, relationship: "superseded_by"})
		}
		if sv, _ := getPayloadString(p.Payload, "refines"); sv == key {
			results = append(results, reverseEdge{key: factKey, relationship: "refined_by"})
		}
		if stringContains(getPayloadStringList(p.Payload, "contradicts"), key) {
			results = append(results, reverseEdge{key: factKey, relationship: "contradicted_by"})
		}
		if stringContains(getPayloadStringList(p.Payload, "supports"), key) {
			results = append(results, reverseEdge{key: factKey, relationship: "supported_by"})
		}
	}
	return results
}

// ── Helpers ────────────────────────────────────────────────────────────

func makeKeywordMatch(field, value string) *pb.Condition {
	return &pb.Condition{
		ConditionOneOf: &pb.Condition_Field{
			Field: &pb.FieldCondition{
				Key: field,
				Match: &pb.Match{
					MatchValue: &pb.Match_Keyword{Keyword: value},
				},
			},
		},
	}
}

func stringContains(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}
