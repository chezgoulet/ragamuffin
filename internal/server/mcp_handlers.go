package server

import (
	"context"
	"fmt"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
)

// ── MCP Tools ─────────────────────────────────────────────────────────────

func (s *Server) mcpTools() []mcp.ToolDefinition {
	return []mcp.ToolDefinition{
		{
			Name:        "ragamuffin_recall",
			Description: "Semantic search across the vault. Returns ranked chunks with source paths, scores, and timestamps.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":           map[string]interface{}{"type": "string", "description": "Natural-language search query"},
					"vault":           map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"top_k":           map[string]interface{}{"type": "integer", "description": "Max results (1-100, default 10)"},
					"score_threshold": map[string]interface{}{"type": "number", "description": "Minimum similarity score 0.0-1.0"},
					"source_filter":   map[string]interface{}{"type": "string", "description": "Restrict to files under this path prefix"},
					"detail":          map[string]interface{}{"type": "string", "description": "Response detail level: l0 (header only), l1 (first paragraph), l2 (full text, default)", "enum": []interface{}{"l0", "l1", "l2"}},
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
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"mode":  map[string]interface{}{"type": "string", "description": "auto, rag, or full (default: auto)"},
					"top_k": map[string]interface{}{"type": "integer", "description": "RAG results to retrieve (1-50, default 8)"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "ragamuffin_store",
			Description: "Ingest structured content into the vault. The canonical Tier 1 write path — agents contribute knowledge, session summaries, observations, and annotations without going through the filesystem.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]interface{}{"type": "string", "description": "Text content to ingest (markdown, plain text)"},
					"source":  map[string]interface{}{"type": "string", "description": "Origin identifier (agent name, workflow ID, file path)"},
					"vault":   map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"tags":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional tags for filtering"},
				},
				"required": []string{"content", "source"},
			},
		},
		{
			Name:        "ragamuffin_draft",
			Description: "Write a file to the vault. Direct mode writes immediately; PR mode opens a pull request.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       map[string]interface{}{"type": "string", "description": "PR title if PR mode"},
					"content":     map[string]interface{}{"type": "string", "description": "File content to write. Required unless delete=true."},
					"target_path": map[string]interface{}{"type": "string", "description": "Vault path relative to vault root"},
					"mode":        map[string]interface{}{"type": "string", "description": "direct or pr (default: direct)"},
					"vault":       map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"description": map[string]interface{}{"type": "string", "description": "Optional PR body"},
					"delete":      map[string]interface{}{"type": "boolean", "description": "Delete the file instead of writing"},
				},
				"required": []string{"title", "target_path"},
			},
		},
		{
			Name:        "ragamuffin_facts",
			Description: "Read or write structured key-value facts. List facts by key/prefix/tag/status, or upsert a fact by key. Facts have lifecycle fields (confidence, source, TTL, status, supersession).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"operation":   map[string]interface{}{"type": "string", "description": "list or upsert"},
					"key":         map[string]interface{}{"type": "string", "description": "Fact key. Required for both operations. Example: org/prefer-rust-cli"},
					"value":       map[string]interface{}{"type": "string", "description": "Fact value. Required for upsert."},
					"vault":       map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"tags":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Tags for filtering (upsert)"},
					"prefix":      map[string]interface{}{"type": "string", "description": "Key prefix filter (list only)"},
					"tag":         map[string]interface{}{"type": "string", "description": "Tag filter (list only)"},
					"status":      map[string]interface{}{"type": "string", "description": "Lifecycle status filter: active, needs_review, superseded, rejected (list only)"},
					"source":      map[string]interface{}{"type": "string", "description": "Origin reference (upsert)"},
					"source_type": map[string]interface{}{"type": "string", "description": "manual, pr_discussion, agent_observation, file, conversation, code_review, automated (upsert)"},
					"confidence":  map[string]interface{}{"type": "number", "description": "How sure? 0.0-1.0 (upsert, default 1.0)"},
					"ttl_days":    map[string]interface{}{"type": "integer", "description": "Days until auto-expiry. 0 = never. (upsert)"},
				},
				"required": []string{"operation"},
			},
		},
		{
			Name:        "ragamuffin_audit",
			Description: "Vault health check. Scans for staleness, semantic conflicts, gaps, and duplicates.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault":       map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"stale_days":  map[string]interface{}{"type": "integer", "description": "Days since last update to flag as stale (default: 90)"},
					"checks":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Which checks to run: stale, semantic_conflict, gap, duplicate"},
					"sample_size": map[string]interface{}{"type": "integer", "description": "Chunk pairs to LLM-compare (1-200, default 50)"},
				},
			},
		},
		{
			Name:        "ragamuffin_graph",
			Description: "Entity and link graph from the vault. Returns node-relationship data showing entity co-occurrence, file cross-references, and knowledge clustering.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault":          map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"entity":         map[string]interface{}{"type": "string", "description": "Focus on a specific entity (BFS traversal from this entity)"},
					"depth":          map[string]interface{}{"type": "integer", "description": "BFS traversal depth (1-5, default 1). Ignored if entity is empty."},
					"limit":          map[string]interface{}{"type": "integer", "description": "Max nodes to return (1-500, default 100)"},
					"min_confidence": map[string]interface{}{"type": "number", "description": "Minimum entity co-occurrence confidence (0.0-1.0)"},
				},
			},
		},
		{
			Name:        "ragamuffin_stats",
			Description: "Operational metrics for the vault. Returns file counts, chunk counts, fact counts, and vault age.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			},
		},
		{
			Name:        "ragamuffin_session_create",
			Description: "Create a conversation session. Returns the session ID for subsequent turn appends.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id":     map[string]interface{}{"type": "string", "description": "Agent identity for the session"},
					"content":      map[string]interface{}{"type": "string", "description": "Optional initial message content"},
					"vault":        map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"auto_extract": map[string]interface{}{"type": "boolean", "description": "Enable automatic fact extraction from turns"},
				},
				"required": []string{"agent_id"},
			},
		},
		{
			Name:        "ragamuffin_session_get",
			Description: "Get session metadata and conversation turns.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "Session UUID"},
					"turns":      map[string]interface{}{"type": "integer", "description": "Max turns to return (0 for all, default 50)"},
				},
				"required": []string{"session_id"},
			},
		},
		{
			Name:        "ragamuffin_session_list",
			Description: "List active sessions, optionally filtered by agent_id or vault.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id": map[string]interface{}{"type": "string", "description": "Filter by agent identity"},
					"vault":    map[string]interface{}{"type": "string", "description": "Filter by vault name"},
					"limit":    map[string]interface{}{"type": "integer", "description": "Max results (default 100)"},
					"offset":   map[string]interface{}{"type": "integer", "description": "Result offset (default 0)"},
				},
			},
		},
		{
			Name:        "ragamuffin_get_chunk",
			Description: "Retrieve a single chunk by its chunk_id. Returns full text, source, and metadata. Use chunk_id from ragamuffin_recall results.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chunk_id": map[string]interface{}{"type": "string", "description": "UUID chunk ID from recall results"},
					"vault":    map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
				"required": []string{"chunk_id"},
			},
		},
		{
			Name:        "ragamuffin_turn_append",
			Description: "Append a turn to an existing session.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id":   map[string]interface{}{"type": "string", "description": "Session UUID"},
					"content":      map[string]interface{}{"type": "string", "description": "Message content"},
					"role":         map[string]interface{}{"type": "string", "description": "user, assistant, or system (default: user)"},
					"auto_extract": map[string]interface{}{"type": "boolean", "description": "Extract facts from this turn (default: fallback to session setting)"},
				},
				"required": []string{"session_id", "content"},
			},
		},
	}
}

// mcpVaultContext returns a context with the vault name set from MCP args.
func (s *Server) mcpVaultContext(ctx context.Context, args map[string]interface{}) context.Context {
	if v, ok := args["vault"].(string); ok && v != "" {
		ctx = context.WithValue(ctx, vaultNameKey, v)
	}
	return ctx
}

// ── Dispatch ─────────────────────────────────────────────────────────────────

func (s *Server) mcpDispatch(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	ctx = s.mcpVaultContext(ctx, args)

	// Enforce vault scope from auth claims. A scoped key that only grants
	// access to vault "default" must not be able to read/write any other
	// vault via MCP tool arguments. This mirrors the REST withVault middleware.
	if vaultName := vaultFromContext(ctx); vaultName != "" {
		if claims := auth.ClaimsFromContext(ctx); claims != nil {
			if !claims.HasVaultAccess(vaultName) {
				return nil, fmt.Errorf("access to vault %q denied by key scope", vaultName)
			}
		}
	}

	switch toolName {
	case "ragamuffin_recall":
		return s.mcpRecall(ctx, args)
	case "ragamuffin_get_chunk":
		return s.mcpChunkGet(ctx, args)
	case "ragamuffin_ask":
		return s.mcpAsk(ctx, args)
	case "ragamuffin_store":
		return s.mcpStore(ctx, args)
	case "ragamuffin_draft":
		return s.mcpDraft(ctx, args)
	case "ragamuffin_facts":
		return s.mcpFacts(ctx, args)
	case "ragamuffin_audit":
		return s.mcpAudit(ctx, args)
	case "ragamuffin_graph":
		return s.mcpGraph(ctx, args)
	case "ragamuffin_stats":
		return s.mcpStats(ctx, args)
	case "ragamuffin_session_create":
		return s.mcpSessionCreate(ctx, args)
	case "ragamuffin_session_get":
		return s.mcpSessionGet(ctx, args)
	case "ragamuffin_session_list":
		return s.mcpSessionList(ctx, args)
	case "ragamuffin_turn_append":
		return s.mcpTurnAppend(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}
}

// ── Adapter Handlers ──────────────────────────────────────────────────────────
// Each mcp* handler is a thin adapter: extract typed args, call the shared
// service method from service.go, format the MCP response map.

func (s *Server) mcpRecall(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	topK := 10
	if v, ok := args["top_k"].(float64); ok {
		topK = int(v)
	}

	var scoreThreshold float64
	if v, ok := args["score_threshold"].(float64); ok {
		scoreThreshold = v
	}

	sourceFilter, _ := args["source_filter"].(string)

	detail, _ := args["detail"].(string)
	if detail == "" {
		detail = "l2"
	}
	if detail != "l0" && detail != "l1" && detail != "l2" {
		return nil, fmt.Errorf("detail must be one of: l0, l1, l2")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	results, topScore, err := s.doRecall(ctx, recallRequest{
		Query:          query,
		TopK:           topK,
		ScoreThreshold: scoreThreshold,
		SourceFilter:   sourceFilter,
		Detail:         detail,
	})
	if err != nil {
		return nil, err
	}

	// Format as MCP map response (snake_case keys for MCP consumers)
	out := make([]map[string]interface{}, 0, len(results))
	for _, r := range results {
		m := map[string]interface{}{
			"score":             r.Score,
			"chunk_id":          r.ChunkID,
			"source_file":       r.SourceFile,
			"header":            r.Header,
			"chunk_index":       r.ChunkIndex,
			"file_last_updated": r.FileLastUpdated,
		}
		if detail != "l1" && detail != "l0" {
			m["text"] = r.Text
		}
		if detail == "l2" {
			m["first_paragraph"] = r.FirstParagraph
		}
		out = append(out, m)
	}

	return map[string]interface{}{
		"results":   out,
		"top_score": topScore,
	}, nil
}

func (s *Server) mcpChunkGet(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	chunkID, _ := args["chunk_id"].(string)
	if chunkID == "" {
		return nil, fmt.Errorf("chunk_id is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return s.doGetChunk(ctx, chunkID)
}

func (s *Server) mcpAsk(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "auto"
	}

	topK := 8
	if v, ok := args["top_k"].(float64); ok {
		topK = int(v)
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	answer, sources, modeUsed, err := s.doAsk(ctx, query, mode, topK)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"answer":    answer,
		"sources":   sources,
		"mode_used": modeUsed,
	}, nil
}

func (s *Server) mcpDraft(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	targetPath, _ := args["target_path"].(string)
	mode, _ := args["mode"].(string)
	description, _ := args["description"].(string)
	doDelete, _ := args["delete"].(bool)

	result, err := s.doDraft(ctx, draftRequest{
		Title:       title,
		Content:     content,
		TargetPath:  targetPath,
		Mode:        mode,
		Description: description,
		Delete:      doDelete,
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Server) mcpStore(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if claims := auth.ClaimsFromContext(ctx); claims != nil && !claims.HasAccess("write") {
		return nil, fmt.Errorf("write access required")
	}

	content, _ := args["content"].(string)
	source, _ := args["source"].(string)

	var tags []string
	if raw, ok := args["tags"].([]interface{}); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	vaultName := vaultFromContext(ctx)

	return s.doStore(ctx, content, source, vaultName, tags)
}

func (s *Server) mcpFacts(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	operation, _ := args["operation"].(string)
	if operation == "" {
		return nil, fmt.Errorf("operation is required: list or upsert")
	}

	switch operation {
	case "list":
		key, _ := args["key"].(string)
		prefix, _ := args["prefix"].(string)
		tag, _ := args["tag"].(string)
		status, _ := args["status"].(string)

		limit := 100
		if v, ok := args["limit"].(float64); ok && v > 0 && v <= 1000 {
			limit = int(v)
		}

		return s.doFactsList(ctx, key, prefix, "", tag, status, limit)

	case "upsert":
		if claims := auth.ClaimsFromContext(ctx); claims != nil && !claims.HasAccess("write") {
			return nil, fmt.Errorf("write access required")
		}

		key, _ := args["key"].(string)
		value, _ := args["value"].(string)
		source, _ := args["source"].(string)
		sourceType, _ := args["source_type"].(string)

		var tags []string
		if raw, ok := args["tags"].([]interface{}); ok {
			for _, t := range raw {
				if s, ok := t.(string); ok {
					tags = append(tags, s)
				}
			}
		}

		confidence := 1.0
		if v, ok := args["confidence"].(float64); ok && v >= 0 && v <= 1.0 {
			confidence = v
		}

		ttlDays := 0
		if v, ok := args["ttl_days"].(float64); ok && v >= 0 {
			ttlDays = int(v)
		}

		return s.doFactsUpsert(ctx, key, value, source, sourceType, tags, confidence, ttlDays)

	default:
		return nil, fmt.Errorf("unknown operation: %q (expected list or upsert)", operation)
	}
}

func (s *Server) mcpAudit(ctx context.Context, args map[string]interface{}) (interface{}, error) {
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

	sampleSize := 50
	if v, ok := args["sample_size"].(float64); ok {
		sampleSize = int(v)
	}

	return s.doAudit(ctx, auditRequest{
		StaleDays:  staleDays,
		Checks:     checks,
		SampleSize: sampleSize,
	})
}

func (s *Server) mcpGraph(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	entity, _ := args["entity"].(string)

	depth := 1
	if v, ok := args["depth"].(float64); ok && v >= 1 && v <= 5 {
		depth = int(v)
	}

	limit := 100
	if v, ok := args["limit"].(float64); ok && v > 0 && v <= 500 {
		limit = int(v)
	}

	vaultName := vaultFromContext(ctx)
	if vaultName == "" {
		vaultName = "default"
	}

	idx := s.indexers.Get(vaultName)
	if idx == nil {
		return nil, fmt.Errorf("vault %q not found", vaultName)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if entity == "" {
		// Full graph — scroll all chunks
		return s.doGraphFull(ctx, vaultName, limit)
	}

	return s.doGraphEntity(ctx, vaultName, entity, depth, limit)
}

func (s *Server) mcpStats(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return s.doStats(ctx)
}

func (s *Server) mcpSessionCreate(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	agentID, _ := args["agent_id"].(string)
	content, _ := args["content"].(string)
	vault, _ := args["vault"].(string)

	var autoExtract *bool
	if ae, ok := args["auto_extract"].(bool); ok {
		autoExtract = &ae
	}

	// Resolve vault from context if not in args
	if vault == "" {
		if vn := vaultFromContext(ctx); vn != "" {
			vault = vn
		}
	}

	return s.doCreateSession(ctx, agentID, content, vault, "mcp", autoExtract)
}

func (s *Server) mcpSessionGet(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	turns := 50
	if v, ok := args["turns"].(float64); ok && v >= 0 {
		turns = int(v)
	}

	return s.doGetSession(ctx, sessionID, turns)
}

func (s *Server) mcpSessionList(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	agentID, _ := args["agent_id"].(string)
	vault, _ := args["vault"].(string)

	limit := 100
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	offset := 0
	if v, ok := args["offset"].(float64); ok && v >= 0 {
		offset = int(v)
	}

	sessions, err := s.doListSessions(ctx, agentID, vault, limit, offset)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"sessions": sessions,
		"count":    len(sessions),
	}, nil
}

func (s *Server) mcpTurnAppend(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	content, _ := args["content"].(string)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	role, _ := args["role"].(string)

	var autoExtract *bool
	if ae, ok := args["auto_extract"].(bool); ok {
		autoExtract = &ae
	}

	return s.doAppendTurn(ctx, sessionID, content, role, autoExtract)
}
