package graph

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chezgoulet/ragamuffin/internal/llm"
)

// Louvain modularity-maximizing community detection over the current
// (non-invalidated) edge projection of the graph. Pure-Go, no dependencies.
//
// The algorithm (Blondel et al. 2008) greedily assigns nodes to communities to
// maximize modularity Q, then aggregates each community into a super-node and
// repeats until Q stops improving. Node iteration order is fixed (by node index,
// which follows the deterministic ListEntities id ordering) so results are
// reproducible. Reference for graph-community summaries: Edge et al.
// arXiv:2404.16130 (GraphRAG).

// louvainGraph is a weighted undirected graph over integer node indices.
type louvainGraph struct {
	n        int
	adj      []map[int]float64 // node -> neighbor -> weight
	selfLoop []float64         // aggregated self-loop weight per node
	degree   []float64         // weighted degree (including self-loops counted twice)
	m2       float64           // 2 * total edge weight
}

func newLouvainGraph(n int) *louvainGraph {
	g := &louvainGraph{n: n, adj: make([]map[int]float64, n), selfLoop: make([]float64, n), degree: make([]float64, n)}
	for i := range g.adj {
		g.adj[i] = make(map[int]float64)
	}
	return g
}

func (g *louvainGraph) addEdge(u, v int, w float64) {
	if u == v {
		g.selfLoop[u] += w
		g.degree[u] += 2 * w
		g.m2 += 2 * w
		return
	}
	g.adj[u][v] += w
	g.adj[v][u] += w
	g.degree[u] += w
	g.degree[v] += w
	g.m2 += 2 * w
}

// louvain runs the algorithm and returns a community label per input node.
func louvain(g *louvainGraph) []int {
	// comm[node] = community index for the original nodes.
	comm := make([]int, g.n)
	for i := range comm {
		comm[i] = i
	}
	cur := g
	// mapping from current super-node -> set of original nodes it represents.
	nodeToOrig := make([][]int, g.n)
	for i := range nodeToOrig {
		nodeToOrig[i] = []int{i}
	}

	for {
		labels, improved := louvainOnePass(cur)
		if !improved {
			break
		}
		// Renumber labels to a dense 0..k-1 range, deterministically.
		dense, k := denseLabels(labels)
		// Rebuild original-node membership grouped by super-node.
		newNodeToOrig := make([][]int, k)
		for cn := 0; cn < cur.n; cn++ {
			c := dense[cn]
			newNodeToOrig[c] = append(newNodeToOrig[c], nodeToOrig[cn]...)
		}
		for c := 0; c < k; c++ {
			for _, orig := range newNodeToOrig[c] {
				comm[orig] = c
			}
		}
		if k == cur.n {
			break // no aggregation possible
		}
		cur = aggregate(cur, dense, k)
		nodeToOrig = newNodeToOrig
	}
	return comm
}

// louvainOnePass performs local moving until no node move improves modularity.
func louvainOnePass(g *louvainGraph) ([]int, bool) {
	label := make([]int, g.n)
	commTot := make([]float64, g.n) // sum of degrees of nodes in community
	for i := 0; i < g.n; i++ {
		label[i] = i
		commTot[i] = g.degree[i]
	}
	if g.m2 == 0 {
		return label, false
	}

	improvedAny := false
	for {
		moved := false
		for u := 0; u < g.n; u++ {
			cu := label[u]
			// remove u from its community
			commTot[cu] -= g.degree[u]

			// weights from u to each neighboring community
			wToComm := make(map[int]float64)
			for v, w := range g.adj[u] {
				wToComm[label[v]] += w
			}

			bestC := cu
			bestGain := 0.0
			// evaluate staying (community cu) and neighbor communities
			candidates := make([]int, 0, len(wToComm)+1)
			candidates = append(candidates, cu)
			for c := range wToComm {
				candidates = append(candidates, c)
			}
			sort.Ints(candidates)
			seen := make(map[int]bool)
			for _, c := range candidates {
				if seen[c] {
					continue
				}
				seen[c] = true
				gain := wToComm[c] - commTot[c]*g.degree[u]/g.m2
				if gain > bestGain+1e-12 {
					bestGain = gain
					bestC = c
				}
			}
			commTot[bestC] += g.degree[u]
			if bestC != cu {
				label[u] = bestC
				moved = true
				improvedAny = true
			}
		}
		if !moved {
			break
		}
	}
	return label, improvedAny
}

// denseLabels renumbers arbitrary labels into 0..k-1 in first-appearance order.
func denseLabels(label []int) ([]int, int) {
	remap := make(map[int]int)
	out := make([]int, len(label))
	next := 0
	for i, l := range label {
		d, ok := remap[l]
		if !ok {
			d = next
			remap[l] = d
			next++
		}
		out[i] = d
	}
	return out, next
}

// aggregate collapses each community into a single super-node.
func aggregate(g *louvainGraph, dense []int, k int) *louvainGraph {
	ng := newLouvainGraph(k)
	for u := 0; u < g.n; u++ {
		cu := dense[u]
		ng.selfLoop[cu] += g.selfLoop[u]
		ng.degree[cu] += 0 // recomputed via addEdge below
		for v, w := range g.adj[u] {
			if u < v { // count each undirected edge once
				cv := dense[v]
				ng.addEdge(cu, cv, w)
			}
		}
	}
	// fold self-loops into m2/degree
	for c := 0; c < k; c++ {
		if ng.selfLoop[c] > 0 {
			ng.degree[c] += 2 * ng.selfLoop[c]
			ng.m2 += 2 * ng.selfLoop[c]
		}
	}
	return ng
}

// DetectCommunities recomputes the community structure for a vault from its
// current edges and persists the result. Entities with no current edges each
// form a singleton community. Returns the detected communities (without
// summaries — call Summarize separately, or pass an Extractor to
// DetectAndSummarize).
func (s *Store) DetectCommunities(ctx context.Context, vault string) ([]Community, error) {
	entities, err := s.ListEntities(ctx, vault)
	if err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		if err := s.ReplaceCommunities(ctx, vault, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}

	idx := make(map[string]int, len(entities))
	for i, e := range entities {
		idx[e.ID] = i
	}

	edges, err := s.Edges(ctx, EdgeQuery{Vault: vault, Limit: 1000})
	if err != nil {
		return nil, err
	}

	g := newLouvainGraph(len(entities))
	for _, e := range edges {
		u, ok1 := idx[e.SourceID]
		v, ok2 := idx[e.TargetID]
		if !ok1 || !ok2 {
			continue
		}
		g.addEdge(u, v, 1.0)
	}

	labels := louvain(g)
	dense, k := denseLabels(labels)

	members := make([][]string, k)
	for i, e := range entities {
		c := dense[i]
		members[c] = append(members[c], e.ID)
	}

	comms := make([]Community, 0, k)
	for c := 0; c < k; c++ {
		comms = append(comms, Community{
			ID:        uuid.NewString(),
			Vault:     vault,
			Label:     c,
			MemberIDs: members[c],
			Size:      len(members[c]),
		})
	}
	sort.Slice(comms, func(i, j int) bool {
		if comms[i].Size != comms[j].Size {
			return comms[i].Size > comms[j].Size
		}
		return comms[i].MemberIDs[0] < comms[j].MemberIDs[0]
	})

	if err := s.ReplaceCommunities(ctx, vault, comms); err != nil {
		return nil, err
	}
	return comms, nil
}

// CommunitySummarizer generates a natural-language summary for a community from
// its member entities and the facts connecting them.
type CommunitySummarizer struct {
	store  *Store
	lm     llm.Synthesizer
	logger *slog.Logger
}

// NewCommunitySummarizer builds a summarizer. lm may be nil, in which case
// Summarize is a no-op (communities keep empty summaries).
func NewCommunitySummarizer(store *Store, lm llm.Synthesizer, logger *slog.Logger) *CommunitySummarizer {
	if logger == nil {
		logger = slog.Default()
	}
	return &CommunitySummarizer{store: store, lm: lm, logger: logger.With("component", "graph.community")}
}

// SummarizeVault generates and persists summaries for every community in a vault.
// Returns the number summarized. No-op (0, nil) when no LLM is configured.
func (cs *CommunitySummarizer) SummarizeVault(ctx context.Context, vault string) (int, error) {
	if cs.lm == nil {
		return 0, nil
	}
	comms, err := cs.store.Communities(ctx, vault)
	if err != nil {
		return 0, err
	}
	entities, err := cs.store.ListEntities(ctx, vault)
	if err != nil {
		return 0, err
	}
	nameByID := make(map[string]string, len(entities))
	for _, e := range entities {
		nameByID[e.ID] = e.Name
	}
	edges, err := cs.store.Edges(ctx, EdgeQuery{Vault: vault, Limit: 1000})
	if err != nil {
		return 0, err
	}

	summarized := 0
	for _, c := range comms {
		prompt := buildCommunityPrompt(c, nameByID, edges)
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		summary, serr := cs.lm.Synthesize(cctx, prompt, "")
		cancel()
		if serr != nil {
			cs.logger.Warn("community summarize failed", "community", c.ID, "error", serr)
			continue
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			continue
		}
		if err := cs.store.UpdateCommunitySummary(ctx, c.ID, summary); err != nil {
			cs.logger.Warn("community summary persist failed", "community", c.ID, "error", err)
			continue
		}
		summarized++
	}
	return summarized, nil
}

func buildCommunityPrompt(c Community, nameByID map[string]string, edges []Edge) string {
	memberSet := make(map[string]bool, len(c.MemberIDs))
	var names []string
	for _, id := range c.MemberIDs {
		memberSet[id] = true
		if n := nameByID[id]; n != "" {
			names = append(names, n)
		}
	}
	var facts []string
	for _, e := range edges {
		if memberSet[e.SourceID] && memberSet[e.TargetID] && strings.TrimSpace(e.Fact) != "" {
			facts = append(facts, e.Fact)
		}
	}

	var b strings.Builder
	b.WriteString("Summarize this community of related entities from a knowledge graph ")
	b.WriteString("in 1-3 sentences. Focus on what connects them and their shared theme. ")
	b.WriteString("Return only the summary, no preamble.\n\n")
	b.WriteString("Entities: ")
	b.WriteString(strings.Join(names, ", "))
	b.WriteString("\n")
	if len(facts) > 0 {
		b.WriteString("\nRelationships:\n")
		for _, f := range facts {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
	}
	return b.String()
}
