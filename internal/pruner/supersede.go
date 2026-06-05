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
// 2. Version-based supersession: Look for active facts with an integer
//    `version` field > 0. For each group sharing the same key prefix,
//    mark any fact with a lower version as superseded by the highest.
//
// The Pruner only writes `status`, `superseded_by`, and `updated_at` fields.
func (p *Pruner) supersedeScan(ctx context.Context) {
	if p.facts == nil {
		p.logger.Warn("supersedeScan: no facts client available")
		return
	}

	p.supersedeCrossReference(ctx)
	p.supersedeByVersion(ctx)
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

// supersedeByVersion finds active facts with a `version` integer field > 0,
// groups them by key prefix (everything before the version segment), and
// marks all lower-versioned facts as superseded by the highest version in
// each group.
func (p *Pruner) supersedeByVersion(ctx context.Context) {
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
		p.logger.Error("supersedeByVersion: query failed", "error", err)
		return
	}

	// Group by prefix, using the integer `version` field from payload
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

		// Use the integer `version` payload field directly (no regex/key parsing)
		version, _ := getPayloadInt(payload, "version")
		if version <= 0 {
			continue
		}

		// Derive prefix from key (everything before /vN/)
		prefix := versionKeyPrefix(key)
		if prefix == "" {
			continue
		}

		groups[prefix] = append(groups[prefix], versionedFact{
			pointID: pt.GetId().GetUuid(),
			version: version,
			key:     key,
		})
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
				if err := p.updateFactStatusWithVersion(ctx, f.pointID, "superseded", maxVersion); err != nil {
					p.logger.Error("supersedeByVersion: failed to mark",
						"key", f.key, "error", err)
					continue
				}
				marked++
			}
		}
	}

	if marked > 0 {
		p.logger.Info("supersedeByVersion complete", "marked_as_superseded", marked)
		p.RecordFlagged(marked)
	}
}

// versionKeyPrefix extracts the key prefix (everything before /vN/).
// Returns empty string if no version segment is found.
func versionKeyPrefix(key string) string {
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
				parts = parts[:i]
				return strings.Join(parts, "/")
			}
		}
	}
	return ""
}


