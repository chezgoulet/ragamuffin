package server

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	pb "github.com/qdrant/go-client/qdrant"
)

type conflictResult struct {
	ChunkA  map[string]string `json:"chunk_a"`
	ChunkB  map[string]string `json:"chunk_b"`
	Summary string            `json:"summary"`
}

func (s *Server) checkStaleness(vaultPath string, staleDays int) ([]map[string]any, error) {
	var stale []map[string]any
	cutoff := time.Now().AddDate(0, 0, -staleDays)

	err := filepath.Walk(vaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			relPath, _ := filepath.Rel(vaultPath, absPath)
			stale = append(stale, map[string]any{
				"path":         relPath,
				"last_updated": info.ModTime().Format(time.RFC3339),
				"days_stale":   int(time.Since(info.ModTime()).Hours() / 24),
			})
		}
		return nil
	})
	return stale, err
}

func (s *Server) checkGaps(vaultPath string) []string {
	var gaps []string

	filepath.Walk(vaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}

		entries, err := os.ReadDir(absPath)
		if err != nil {
			return nil
		}

		hasFiles := false
		for _, e := range entries {
			if !e.IsDir() {
				hasFiles = true
				break
			}
		}

		if !hasFiles && len(entries) == 0 {
			relPath, _ := filepath.Rel(vaultPath, absPath)
			if relPath != "." {
				gaps = append(gaps, relPath+"/ — directory exists but is empty")
			}
		} else if !hasFiles && len(entries) > 0 {
			hasIndexable := false
			filepath.Walk(absPath, func(subPath string, subInfo os.FileInfo, subErr error) error {
				if subErr != nil || subInfo.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(subPath))
				if ext == ".md" || ext == ".txt" || ext == ".org" || ext == ".rst" || ext == "" {
					hasIndexable = true
					return filepath.SkipAll
				}
				return nil
			})
			if !hasIndexable {
				relPath, _ := filepath.Rel(vaultPath, absPath)
				if relPath != "." {
					gaps = append(gaps, relPath+"/ — directory exists but contains no indexable files")
				}
			}
		}
		return nil
	})
	return gaps
}

func (s *Server) checkDuplicates(vaultPath string) []map[string]any {
	seen := make(map[string]string) // filename → first path
	var dupes []map[string]any

	filepath.Walk(vaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		relPath, _ := filepath.Rel(vaultPath, absPath)
		if first, exists := seen[name]; exists {
			dupes = append(dupes, map[string]any{
				"filename": name,
				"path_a":   first,
				"path_b":   relPath,
			})
		} else {
			seen[name] = relPath
		}
		return nil
	})
	return dupes
}

func (s *Server) checkSemanticConflicts(ctx context.Context, qc *qdrant.Client, sampleSize int) ([]conflictResult, int) {
	if qc == nil {
		qc = s.qdrant
	}

	scrollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Use Scroll API for a deterministic random sample — no embedding call needed.
	// Scroll returns points ordered by ID; we fetch sampleSize*2 and shuffle.
	results, _, err := qc.Scroll(scrollCtx, uint32(sampleSize*2), nil)
	if err != nil {
		s.log(ctx).Error("audit: scroll failed", "error", err)
		return nil, 0
	}

	if len(results) < 2 {
		return nil, 0
	}

	type pair struct {
		a, b *pb.RetrievedPoint
	}
	var pairs []pair
	sourceMap := make(map[string][]*pb.RetrievedPoint)

	for _, r := range results {
		src := ""
		if v, ok := r.Payload["source_file"]; ok {
			src = v.GetStringValue()
		}
		sourceMap[src] = append(sourceMap[src], r)
	}

	var allChunks []*pb.RetrievedPoint
	for _, chunks := range sourceMap {
		allChunks = append(allChunks, chunks...)
	}

	// Shuffle and pair
	rand.Shuffle(len(allChunks), func(i, j int) {
		allChunks[i], allChunks[j] = allChunks[j], allChunks[i]
	})

	for i := 0; i < len(allChunks)-1 && len(pairs) < sampleSize; i += 2 {
		a, b := allChunks[i], allChunks[i+1]
		srcA := ""
		srcB := ""
		if v, ok := a.Payload["source_file"]; ok {
			srcA = v.GetStringValue()
		}
		if v, ok := b.Payload["source_file"]; ok {
			srcB = v.GetStringValue()
		}
		if srcA != srcB && srcA != "" && srcB != "" {
			pairs = append(pairs, pair{a, b})
		}
	}

	// LLM compare each pair
	var conflicts []conflictResult
	llmCalls := 0

	for _, p := range pairs {
		textA := ""
		textB := ""
		if v, ok := p.a.Payload["text"]; ok {
			textA = v.GetStringValue()
		}
		if v, ok := p.b.Payload["text"]; ok {
			textB = v.GetStringValue()
		}
		srcA := ""
		srcB := ""
		if v, ok := p.a.Payload["source_file"]; ok {
			srcA = v.GetStringValue()
		}
		if v, ok := p.b.Payload["source_file"]; ok {
			srcB = v.GetStringValue()
		}

		if textA == "" || textB == "" {
			continue
		}

		llmCalls++
		summary, err := s.llmFor(ctx).Compare(ctx, textA, textB, srcA, srcB)
		if err != nil {
			s.log(ctx).Warn("audit: LLM compare failed", "error", err)
			continue
		}
		if summary != "" {
			conflicts = append(conflicts, conflictResult{
				ChunkA:  map[string]string{"source_file": srcA, "text": truncate(textA, 200)},
				ChunkB:  map[string]string{"source_file": srcB, "text": truncate(textB, 200)},
				Summary: summary,
			})
		}
	}

	return conflicts, llmCalls
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// checkFactConflicts returns all facts with non-empty contradicts lists
// (unresolved semantic contradictions detected by the Pruner).
func (s *Server) checkFactConflicts(ctx context.Context) []map[string]any {
	if s.facts == nil {
		return nil
	}

	// Query facts with non-empty contradicts AND conflict_resolved = false
	filter := &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "conflict_resolved",
						Match: &pb.Match{
							MatchValue: &pb.Match_Bool{
								Bool: false,
							},
						},
					},
				},
			},
		},
		MustNot: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "contradicts",
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

	points, err := s.facts.ScrollFiltered(ctx, s.cfg.FactsCollection, filter, 0, "")
	if err != nil {
		s.log(ctx).Error("fact conflict check: query failed", "error", err)
		return nil
	}

	conflicts := make([]map[string]any, 0, len(points))
	for _, pt := range points {
		payload := pt.GetPayload()
		key, _ := getPayloadString(payload, "fact_key")
		value, _ := getPayloadString(payload, "fact_value")
		contradicts := getPayloadStringList(payload, "contradicts")

		conflicts = append(conflicts, map[string]any{
			"key":         key,
			"value":       truncate(value, 200),
			"contradicts": contradicts,
			"status":      getPayloadStringValue(payload, "status"),
		})
	}
	return conflicts
}

// checkFactVaultConflicts compares fact values against vault chunks using LLM.
// Samples vault chunks and recently updated facts, then asks the LLM to
// identify semantic contradictions between stored knowledge and vault content.
func (s *Server) checkFactVaultConflicts(ctx context.Context, sampleSize int) ([]map[string]any, int) {
	if s.facts == nil || s.embedder == nil {
		return nil, 0
	}

	// Sample active facts
	factFilter := &pb.Filter{
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
	factPoints, err := s.facts.ScrollFiltered(ctx, s.cfg.FactsCollection, factFilter, uint32(sampleSize), "")
	if err != nil || len(factPoints) == 0 {
		s.log(ctx).Warn("fact_vault_conflict: no active facts found", "error", err)
		return nil, 0
	}

	// Sample vault chunks (use first vault's client)
	vaultName := "default"
	if names := listVaultNames(s.cfg); len(names) > 0 {
		vaultName = names[0]
	}
	vaultQc := s.indexers.GetClient(vaultName)
	if vaultQc == nil {
		return nil, 0
	}
	chunkPoints, _, err := vaultQc.Scroll(ctx, uint32(sampleSize), nil)
	if err != nil || len(chunkPoints) == 0 {
		return nil, 0
	}

	// Pair each fact with a random chunk and compare via LLM
	llmCalls := 0
	conflicts := make([]map[string]any, 0)

	for i, fp := range factPoints {
		if i >= len(chunkPoints) {
			break
		}

		factPayload := fp.GetPayload()
		factKey, _ := getPayloadString(factPayload, "fact_key")
		factValue, _ := getPayloadString(factPayload, "fact_value")
		if factKey == "" || factValue == "" {
			continue
		}

		chunkPayload := chunkPoints[i].GetPayload()
		chunkText, _ := getPayloadString(chunkPayload, "text")
		if chunkText == "" {
			continue
		}

		if !s.cfg.HasLLM() {
			continue
		}

		llmCalls++
		summary, err := s.llmFor(ctx).Compare(ctx, factValue, chunkText, "fact:"+factKey, "vault")
		if err != nil {
			s.log(ctx).Warn("fact_vault_conflict: LLM compare failed", "error", err)
			continue
		}
		if summary != "" {
			conflicts = append(conflicts, map[string]any{
				"fact_key":   factKey,
				"fact_value": truncate(factValue, 200),
				"chunk_text": truncate(chunkText, 200),
				"summary":    summary,
			})
		}
	}

	return conflicts, llmCalls
}

// listVaultNames returns all configured vault names.
func listVaultNames(cfg *config.Config) []string {
	if cfg.Vaults == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Vaults))
	for name := range cfg.Vaults {
		names = append(names, name)
	}
	return names
}
