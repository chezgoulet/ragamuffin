package server

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

type conflictResult struct {
	ChunkA  map[string]string `json:"chunk_a"`
	ChunkB  map[string]string `json:"chunk_b"`
	Summary string            `json:"summary"`
}

func (s *Server) checkStaleness(staleDays int) ([]map[string]interface{}, error) {
	var stale []map[string]interface{}
	cutoff := time.Now().AddDate(0, 0, -staleDays)

	err := filepath.Walk(s.cfg.VaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
			stale = append(stale, map[string]interface{}{
				"path":         relPath,
				"last_updated": info.ModTime().Format(time.RFC3339),
				"days_stale":   int(time.Since(info.ModTime()).Hours() / 24),
			})
		}
		return nil
	})
	return stale, err
}

func (s *Server) checkGaps() []string {
	var gaps []string

	filepath.Walk(s.cfg.VaultPath, func(absPath string, info os.FileInfo, err error) error {
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
			relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
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
				relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
				if relPath != "." {
					gaps = append(gaps, relPath+"/ — directory exists but contains no indexable files")
				}
			}
		}
		return nil
	})
	return gaps
}

func (s *Server) checkDuplicates() []map[string]interface{} {
	seen := make(map[string]string) // filename → first path
	var dupes []map[string]interface{}

	filepath.Walk(s.cfg.VaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		relPath, _ := filepath.Rel(s.cfg.VaultPath, absPath)
		if first, exists := seen[name]; exists {
			dupes = append(dupes, map[string]interface{}{
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

func (s *Server) checkSemanticConflicts(ctx context.Context, sampleSize int) ([]conflictResult, int) {
	scrollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Simplified: do a large search with a generic query to get a sample
	// In production, this would use proper scroll/pagination
	vector := make([]float32, 1536) // zero vector as generic query proxy
	results, err := s.qdrant.Search(scrollCtx, vector, uint64(sampleSize*2), 0.0, "")
	if err != nil {
		s.log(ctx).Error("audit: conflict search failed", "error", err)
		return nil, 0
	}

	if len(results) < 2 {
		return nil, 0
	}

	type pair struct {
		a, b *pb.ScoredPoint
	}
	var pairs []pair
	sourceMap := make(map[string][]*pb.ScoredPoint)

	for _, r := range results {
		src := ""
		if v, ok := r.Payload["source_file"]; ok {
			src = v.GetStringValue()
		}
		sourceMap[src] = append(sourceMap[src], r)
	}

	var allChunks []*pb.ScoredPoint
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
		summary, err := s.llm.Compare(ctx, textA, textB, srcA, srcB)
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
