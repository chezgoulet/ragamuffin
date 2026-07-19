package retrieval

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// RerankDoc is a candidate passed to the listwise reranker. ID is the stable
// identifier (chunk/fact id) returned in the reranked order; Text is the
// content shown to the LLM judge.
type RerankDoc struct {
	ID   string
	Text string
}

// maxRerankCandidates caps how many candidates are sent to the LLM in one
// listwise pass. RankGPT degrades and costs more beyond this; the fused top-k
// is expected to already be 20–50 (A1).
const maxRerankCandidates = 50

// rerankSnippetLen bounds each candidate's text in the prompt to keep the
// window small and the judgement focused on the lead of each passage.
const rerankSnippetLen = 500

// Rerank reorders candidates by relevance to query using listwise LLM
// reranking (RankGPT, arXiv:2311.16720). It returns candidate IDs in the new
// order. It degrades gracefully: a nil completer, empty input, an LLM error,
// or an unparseable response all yield the original order so retrieval never
// fails because reranking failed.
//
// Only the first maxRerankCandidates are reranked; any beyond that are
// appended after the reranked head in their original order.
func Rerank(ctx context.Context, c Completer, query string, docs []RerankDoc) []string {
	if len(docs) == 0 {
		return nil
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	if c == nil || strings.TrimSpace(query) == "" || len(docs) == 1 {
		return ids
	}

	head := docs
	var tailIDs []string
	if len(docs) > maxRerankCandidates {
		head = docs[:maxRerankCandidates]
		for _, d := range docs[maxRerankCandidates:] {
			tailIDs = append(tailIDs, d.ID)
		}
	}

	out, err := c.Complete(ctx, rankGPTPrompt(query, head))
	if err != nil {
		return ids
	}
	order := parseRankOrder(out, len(head))
	if len(order) == 0 {
		return ids
	}

	ranked := make([]string, 0, len(docs))
	seen := make(map[int]struct{}, len(order))
	for _, idx := range order {
		if idx < 0 || idx >= len(head) {
			continue
		}
		if _, dup := seen[idx]; dup {
			continue
		}
		seen[idx] = struct{}{}
		ranked = append(ranked, head[idx].ID)
	}
	// Append any head candidates the model omitted, preserving original order,
	// so no result is silently dropped.
	for i, d := range head {
		if _, ok := seen[i]; !ok {
			ranked = append(ranked, d.ID)
		}
	}
	ranked = append(ranked, tailIDs...)
	return ranked
}

func rankGPTPrompt(query string, docs []RerankDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"You are a search relevance judge. Rank the %d passages below by how "+
			"well they answer the query. Output only a comma-separated list of "+
			"passage numbers from most to least relevant, e.g. \"3,1,2\". Include "+
			"every number exactly once. No explanation.\n\nQuery: %s\n\n",
		len(docs), query)
	for i, d := range docs {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, snippet(d.Text))
	}
	return b.String()
}

func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > rerankSnippetLen {
		return s[:rerankSnippetLen]
	}
	return s
}

// AccessBoost lifts scores based on cumulative access_count from the fact
// payload. The boost is lift-only: the output score is max(original, boosted),
// so frequently accessed facts rank higher but never below unaccessed peers.
// The fetchMultiplier (default 2.0) governs the top_k × N window used:
// we request top_k × fetchMultiplier results, apply the boost, then keep top_k.
// A nil accessCounts map or empty results returns the input unchanged.
func AccessBoost(ranked []RankedID, accessCounts map[string]int64, topK int, fetchMultiplier float64) []RankedID {
	if len(ranked) == 0 || len(accessCounts) == 0 {
		return ranked
	}
	if fetchMultiplier <= 1.0 {
		fetchMultiplier = 2.0
	}
	fetchWindow := int(math.Ceil(float64(topK) * fetchMultiplier))
	if fetchWindow > len(ranked) {
		fetchWindow = len(ranked)
	}
	boosted := make([]RankedID, fetchWindow)
	for i, r := range ranked[:fetchWindow] {
		boost := 0.0
		if count, ok := accessCounts[r.ID]; ok && count > 0 {
			// Logarithmic boost: ~+0.3 at 1 access, ~+1.0 at 10, ~+2.0 at 100
			boost = math.Log1p(float64(count)) * 0.3
		}
		boosted[i] = RankedID{ID: r.ID, Score: max(r.Score, r.Score+float32(boost))}
	}
	if fetchWindow < len(ranked) {
		boosted = append(boosted, ranked[fetchWindow:]...)
	}
	sort.Slice(boosted, func(i, j int) bool { return boosted[i].Score > boosted[j].Score })
	if len(boosted) > topK {
		boosted = boosted[:topK]
	}
	return boosted
}

// parseRankOrder extracts 0-based indices from a model response of the form
// "3,1,2" (or newline/space separated). Numbers are 1-based in the prompt and
// converted here; out-of-range numbers are dropped by the caller.
func parseRankOrder(s string, n int) []int {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '[' || r == ']' || r == '.'
	})
	var out []int
	for _, f := range fields {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			continue
		}
		if v >= 1 && v <= n {
			out = append(out, v-1)
		}
	}
	return out
}
