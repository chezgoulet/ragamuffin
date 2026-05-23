package pruner

import (
	"context"
	"strings"

	pb "github.com/qdrant/go-client/qdrant"
)

// supersedeScan performs two checks:
//
// 1. Cross-reference check: For facts with non-empty `supersedes`, verify the
//    superseded key still has a `status = "active"` fact. If so, mark the
//    superseded fact as `superseded`.
//
// 2. Key-pattern supersession: Look for facts whose keys share a prefix and
//    contain version-like segments (e.g., org/v2/decision vs org/v1/decision).
//    If a higher-versioned active fact exists alongside a lower-versioned one,
//    mark the lower one as superseded.
//
// The Pruner only writes `supersedes` and `status` fields — it never deletes.
func (p *Pruner) supersedeScan(ctx context.Context) {
	if p.facts == nil {
		p.logger.Warn("supersedeScan: no facts client available")
		return
	}

	p.supersedeCrossReference(ctx)
	p.supersedeKeyPattern(ctx)
}

// supersedeCrossReference checks that facts with supersedes set point to
// existing keys, and marks the target as superseded if still active.
func (p *Pruner) supersedeCrossReference(ctx context.Context) {
	// Find facts with a non-empty supersedes field
	filter := &pb.Filter{
		MustNot: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "supersedes",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: "",
							},
						},
					},
				},
			},
		},
	}

	points, err := p.scrollFilteredFacts(ctx, filter, 0)
	if err != nil {
		p.logger.Error("supersedeCrossReference: query failed", "error", err)
		return
	}

	marked := 0
	for _, pt := range points {
		payload := pt.GetPayload()
		targetKey, _ := getPayloadString(payload, "supersedes")
		if targetKey == "" {
			continue
		}

		// Check if the target fact exists
		targetFilter := &pb.Filter{
			Must: []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "fact_key",
							Match: &pb.Match{
								MatchValue: &pb.Match_Keyword{
									Keyword: targetKey,
								},
							},
						},
					},
				},
			},
		}

		targets, err := p.scrollFilteredFacts(ctx, targetFilter, 1)
		if err != nil || len(targets) == 0 {
			// Target doesn't exist or error — log at debug level
			p.logger.Debug("supersedeCrossReference: target not found",
				"supersedes", targetKey, "error", err)
			continue
		}

		// Check if target is still active
		targetPayload := targets[0].GetPayload()
		targetStatus, _ := getPayloadString(targetPayload, "status")
		if targetStatus != "active" {
			continue // already marked
		}

		// Mark the target as superseded
		targetID := targets[0].GetId().GetUuid()
		if targetID == "" {
			continue
		}
		if err := p.updateFactStatus(ctx, targetID, "superseded"); err != nil {
			p.logger.Error("supersedeCrossReference: failed to mark target",
				"target_key", targetKey, "error", err)
			continue
		}
		marked++
	}

	if marked > 0 {
		p.logger.Info("supersedeCrossReference complete", "marked_as_superseded", marked)
		p.RecordFlagged(marked)
	}
}

// supersedeKeyPattern looks for facts with version-like key patterns and
// marks lower versions as superseded if a higher version exists.
//
// Detects patterns like: org/v2/decision vs org/v1/decision,
// project/feature/v3 vs project/feature/v2, etc.
func (p *Pruner) supersedeKeyPattern(ctx context.Context) {
	// Fetch all active facts
	activeFilter := &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "status",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: "active",
							},
						},
					},
				},
			},
		},
	}

	points, err := p.scrollFilteredFacts(ctx, activeFilter, 0)
	if err != nil {
		p.logger.Error("supersedeKeyPattern: query failed", "error", err)
		return
	}

	// Group by common prefix (up to the version segment)
	type versionedFact struct {
		pointID string
		version int
		key     string
	}

	groups := make(map[string][]versionedFact)

	for _, pt := range points {
		payload := pt.GetPayload()
		key, _ := getPayloadString(payload, "fact_key")
		if key == "" {
			continue
		}

		// Look for /vN/ or /vN pattern at any path depth
		// e.g., "org/v2/decision" → prefix "org/", version 2
		prefix, version := parseVersionedKey(key)
		if version >= 1 {
			groups[prefix] = append(groups[prefix], versionedFact{
				pointID: pt.GetId().GetUuid(),
				version: version,
				key:     key,
			})
		}
	}

	if len(groups) == 0 {
		return
	}

	marked := 0
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}

		// Find the max version
		maxVersion := 0
		for _, f := range g {
			if f.version > maxVersion {
				maxVersion = f.version
			}
		}

		// Mark all lower versions as superseded
		for _, f := range g {
			if f.version < maxVersion {
				if err := p.updateFactStatus(ctx, f.pointID, "superseded"); err != nil {
					p.logger.Error("supersedeKeyPattern: failed to mark",
						"key", f.key, "error", err)
					continue
				}
				marked++
			}
		}
	}

	if marked > 0 {
		p.logger.Info("supersedeKeyPattern complete", "marked_as_superseded", marked)
		p.RecordFlagged(marked)
	}
}

// parseVersionedKey detects version segments in a fact key and returns
// the prefix (everything before the version segment) and version number.
// Returns version 0 if no version pattern is found.
//
// Recognized patterns: /vN/, /vN (at end), vN/ at start
// where N is a positive integer.
func parseVersionedKey(key string) (prefix string, version int) {
	parts := strings.Split(key, "/")
	for i, part := range parts {
		if len(part) > 1 && part[0] == 'v' {
			var v int
			for _, c := range part[1:] {
				if c < '0' || c > '9' {
					v = 0
					break
				}
				v = v*10 + int(c-'0')
			}
			if v >= 1 {
				// Reconstruct prefix from parts before the version segment
				parts = parts[:i] // drop version and everything after
				prefix = strings.Join(parts, "/")
				return prefix, v
			}
		}
	}
	return "", 0
}
