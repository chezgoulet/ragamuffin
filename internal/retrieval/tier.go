package retrieval

import (
	"sort"
	"strings"
)

// Source-tier boost: a small static preference for authoritative document
// classes over ephemeral ones (issue #877). Library/reference docs should
// outrank session notes and transcripts when both match a query equally well.
// The boost is applied on fused RRF ranks — never on raw similarity scores —
// so it composes with dense, sparse, and hybrid modes without recalibration.
//
// Tiers are matched from the chunk's `source_file` payload path. Matching is
// deliberately substring/path-segment based and case-insensitive: sources are
// free-form operator-provided paths, not a controlled vocabulary.

// Tier is a source class with an additive RRF boost.
type Tier struct {
	Name  string
	Match []string // substrings; matched against the lowercased source path
	Boost float32  // added to the RRF score of matching documents
}

// DefaultTiers is the tier ladder applied by ApplyTierBoost when no custom
// tiers are configured. Ordered most-authoritative first; first match wins.
var DefaultTiers = []Tier{
	{
		Name:  "library",
		Match: []string{"docs/", "documentation/", "library/", "reference/", "/docs", "handbook/", "manual/", "spec"},
		Boost: 0.15,
	},
	{
		Name:  "session",
		Match: []string{"session", "transcript", "chat", "conversation", "notes"},
		Boost: -0.05,
	},
}

// TieredID pairs a ranked document ID with the source path used for
// classification. Callers build these from recall results before boosting.
type TieredID struct {
	ID     string
	Source string
}

// ApplyTierBoost reorders a fused ranking using the source tiers. Ties in RRF
// score are common (identical ranks across lists), so boosted entries move up
// stably: sorting is by (score + boost) descending with stable order preserved
// for equal keys. Documents whose source matches no tier keep their base score.
//
// A nil/empty ranking returns nil; nil tiers is treated as DefaultTiers.
func ApplyTierBoost(ranking []RankedID, docs map[string]string, tiers []Tier) []RankedID {
	if len(ranking) == 0 {
		return nil
	}
	if len(tiers) == 0 {
		tiers = DefaultTiers
	}
	out := make([]RankedID, len(ranking))
	copy(out, ranking)
	for i := range out {
		src := strings.ToLower(docs[out[i].ID])
		if src == "" {
			continue
		}
		out[i].Score += tierBoostFor(src, tiers)
	}
	// Stable sort keeps equal-boost entries in their original fused order,
	// which is itself deterministic under Fuse's total ordering.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// tierBoostFor returns the boost of the highest-priority tier matching src.
func tierBoostFor(src string, tiers []Tier) float32 {
	for _, t := range tiers {
		for _, m := range t.Match {
			if m != "" && strings.Contains(src, strings.ToLower(m)) {
				return t.Boost
			}
		}
	}
	return 0
}

// TierName classifies a source path into its tier name ("" if unclassified).
func TierName(source string, tiers []Tier) string {
	if len(tiers) == 0 {
		tiers = DefaultTiers
	}
	src := strings.ToLower(source)
	if src == "" {
		return ""
	}
	for _, t := range tiers {
		for _, m := range t.Match {
			if m != "" && strings.Contains(src, strings.ToLower(m)) {
				return t.Name
			}
		}
	}
	return ""
}
