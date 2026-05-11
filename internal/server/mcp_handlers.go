package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/mcp"
)

// ── MCP Tools ──────────────────────────────────────────────────────────────────

func (s *Server) mcpTools() []mcp.ToolDefinition {
	return []mcp.ToolDefinition{
		{
			Name:        "ragamuffin_recall",
			Description: "Semantic search across the vault. Returns ranked chunks with source paths, scores, and timestamps.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":           map[string]interface{}{"type": "string", "description": "Natural-language search query"},
					"top_k":           map[string]interface{}{"type": "integer", "description": "Max results (1-100, default 10)"},
					"score_threshold": map[string]interface{}{"type": "number", "description": "Minimum similarity score 0.0-1.0"},
					"source_filter":   map[string]interface{}{"type": "string", "description": "Restrict to files under this path prefix"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "ragamuffin_ask",
			Description: "Full-context synthesis via LLM. Returns a prose answer with source citations.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string", "description": "The question to answer"},
					"mode":  map[string]interface{}{"type": "string", "description": "auto, rag, or full (default: auto)"},
					"top_k": map[string]interface{}{"type": "integer", "description": "RAG results to retrieve (1-50, default 8)"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "ragamuffin_draft",
			Description: "Write a file to the vault. Direct mode writes immediately; PR mode opens a pull request.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       map[string]interface{}{"type": "string", "description": "PR title if PR mode"},
					"content":     map[string]interface{}{"type": "string", "description": "Complete file content. Empty string to delete."},
					"target_path": map[string]interface{}{"type": "string", "description": "Vault path relative to vault root"},
					"mode":        map[string]interface{}{"type": "string", "description": "direct or pr (default: direct)"},
					"description": map[string]interface{}{"type": "string", "description": "Optional PR body"},
				},
				"required": []string{"title", "content", "target_path"},
			},
		},
		{
			Name:        "ragamuffin_audit",
			Description: "Vault health check. Scans for staleness, semantic conflicts, gaps, and duplicates.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"stale_days":  map[string]interface{}{"type": "integer", "description": "Days since last update to flag as stale (default: 90)"},
					"checks":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Which checks to run: stale, semantic_conflict, gap, duplicate"},
					"sample_size": map[string]interface{}{"type": "integer", "description": "Chunk pairs to LLM-compare (1-200, default 50)"},
				},
			},
		},
	}
}

func (s *Server) mcpDispatch(toolName string, args map[string]interface{}) (interface{}, error) {
	switch toolName {
	case "ragamuffin_recall":
		return s.mcpRecall(args)
	case "ragamuffin_ask":
		return s.mcpAsk(args)
	case "ragamuffin_draft":
		return s.mcpDraft(args)
	case "ragamuffin_audit":
		return s.mcpAudit(args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}
}

func (s *Server) mcpRecall(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	topK := 10
	if v, ok := args["top_k"].(float64); ok {
		topK = int(v)
	}

	var scoreThreshold float32
	if v, ok := args["score_threshold"].(float64); ok {
		scoreThreshold = float32(v)
	}

	sourceFilter, _ := args["source_filter"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vector, err := s.embedder.EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	results, err := s.qdrant.Search(ctx, vector, uint64(topK), scoreThreshold, sourceFilter)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	out := make([]map[string]interface{}, 0, len(results))
	var topScore float32
	for _, r := range results {
		res := map[string]interface{}{
			"score": r.Score,
		}
		if r.Score > topScore {
			topScore = r.Score
		}
		if v, ok := r.Payload["text"]; ok {
			res["text"] = v.GetStringValue()
		}
		if v, ok := r.Payload["source_file"]; ok {
			res["source_file"] = v.GetStringValue()
		}
		if v, ok := r.Payload["header"]; ok {
			res["header"] = v.GetStringValue()
		}
		if v, ok := r.Payload["chunk_index"]; ok {
			res["chunk_index"] = int(v.GetIntegerValue())
		}
		if v, ok := r.Payload["file_last_updated"]; ok {
			res["file_last_updated"] = v.GetStringValue()
		}
		out = append(out, res)
	}

	return map[string]interface{}{
		"results":   out,
		"top_score": topScore,
	}, nil
}

func (s *Server) mcpAsk(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if !s.cfg.HasLLM() {
		return nil, fmt.Errorf("LLM not configured — set RAGAMUFFIN_LLM_PROVIDER and RAGAMUFFIN_LLM_API_KEY")
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "auto"
	}

	topK := 8
	if v, ok := args["top_k"].(float64); ok {
		topK = int(v)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	modeUsed := mode
	var contextText string
	var sources []string

	if mode == "rag" || mode == "auto" {
		vector, err := s.embedder.EmbedSingle(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("embedding failed: %w", err)
		}
		results, err := s.qdrant.Search(ctx, vector, uint64(topK), 0.0, "")
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}

		seenSources := make(map[string]bool)
		var topScore float32
		var b strings.Builder
		for _, r := range results {
			if r.Score > topScore {
				topScore = r.Score
			}
			if src, ok := r.Payload["source_file"]; ok {
				s := src.GetStringValue()
				if !seenSources[s] {
					sources = append(sources, s)
					seenSources[s] = true
				}
			}
			if text, ok := r.Payload["text"]; ok {
				b.WriteString(text.GetStringValue())
				b.WriteString("\n\n")
			}
		}
		contextText = b.String()

		if mode == "auto" && topScore >= 0.75 {
			modeUsed = "rag"
		} else if mode == "auto" {
			modeUsed = "full"
			contextText = ""
		}
	}

	if modeUsed == "full" && contextText == "" {
		vector, err := s.embedder.EmbedSingle(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("embedding failed: %w", err)
		}
		results, err := s.qdrant.Search(ctx, vector, 50, 0.0, "")
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
		seenSources := make(map[string]bool)
		var b strings.Builder
		for _, r := range results {
			if src, ok := r.Payload["source_file"]; ok {
				s := src.GetStringValue()
				if !seenSources[s] {
					sources = append(sources, s)
					seenSources[s] = true
				}
			}
			if text, ok := r.Payload["text"]; ok {
				b.WriteString(text.GetStringValue())
				b.WriteString("\n\n")
			}
		}
		contextText = b.String()
	}

	answer, err := s.llm.Synthesize(ctx, query, contextText)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	return map[string]interface{}{
		"answer":    answer,
		"sources":   sources,
		"mode_used": modeUsed,
	}, nil
}

func (s *Server) mcpDraft(args map[string]interface{}) (interface{}, error) {
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	targetPath, _ := args["target_path"].(string)
	mode, _ := args["mode"].(string)
	description, _ := args["description"].(string)

	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if targetPath == "" {
		return nil, fmt.Errorf("target_path is required")
	}
	if mode == "" {
		mode = "direct"
	}

	cleanPath := filepath.Clean(targetPath)
	if strings.HasPrefix(cleanPath, "..") {
		return nil, fmt.Errorf("target_path must not escape vault root")
	}

	if mode == "pr" {
		prURL, branch, err := s.createPR(title, content, cleanPath, description)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"mode":   "pr",
			"pr_url": prURL,
			"branch": branch,
		}, nil
	}

	fullPath := filepath.Join(s.cfg.VaultPath, cleanPath)

	if content == "" {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("delete failed: %w", err)
		}
	} else {
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir failed: %w", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write failed: %w", err)
		}
	}

	return map[string]interface{}{
		"mode":    mode,
		"path":    cleanPath,
		"written": true,
	}, nil
}

func (s *Server) mcpAudit(args map[string]interface{}) (interface{}, error) {
	staleDays := 90
	if v, ok := args["stale_days"].(float64); ok {
		staleDays = int(v)
	}

	var checks []string
	if raw, ok := args["checks"].([]interface{}); ok {
		for _, c := range raw {
			if s, ok := c.(string); ok {
				checks = append(checks, s)
			}
		}
	}
	if len(checks) == 0 {
		checks = []string{"stale", "semantic_conflict", "gap", "duplicate"}
	}

	sampleSize := 50
	if v, ok := args["sample_size"].(float64); ok {
		sampleSize = int(v)
	}

	resp := map[string]interface{}{
		"checks_run": checks,
	}

	checkSet := make(map[string]bool)
	for _, c := range checks {
		checkSet[c] = true
	}

	if checkSet["stale"] {
		staleFiles, err := s.checkStaleness(staleDays)
		if err != nil {
			s.logger.Error("MCP audit: staleness check failed", "error", err)
		}
		resp["stale_files"] = staleFiles
	}

	if checkSet["gap"] {
		gaps := s.checkGaps()
		resp["gaps"] = gaps
	}

	if checkSet["duplicate"] {
		dupes := s.checkDuplicates()
		resp["duplicates"] = dupes
	}

	if checkSet["semantic_conflict"] {
		if !s.cfg.HasLLM() {
			resp["semantic_conflicts"] = []interface{}{}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			conflicts, llmCalls := s.checkSemanticConflicts(ctx, sampleSize)
			resp["semantic_conflicts"] = conflicts
			resp["semantic_conflict_llm_calls"] = llmCalls
		}
	}

	return resp, nil
}
