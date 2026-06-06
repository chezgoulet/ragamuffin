package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	pb "github.com/qdrant/go-client/qdrant"
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
					"operation":    map[string]interface{}{"type": "string", "description": "list or upsert"},
					"key":          map[string]interface{}{"type": "string", "description": "Fact key. Required for both operations. Example: org/prefer-rust-cli"},
					"value":        map[string]interface{}{"type": "string", "description": "Fact value. Required for upsert."},
					"vault":        map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"tags":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Tags for filtering (upsert)"},
					"prefix":       map[string]interface{}{"type": "string", "description": "Key prefix filter (list only)"},
					"tag":          map[string]interface{}{"type": "string", "description": "Tag filter (list only)"},
					"status":       map[string]interface{}{"type": "string", "description": "Lifecycle status filter: active, needs_review, superseded, rejected (list only)"},
					"source":       map[string]interface{}{"type": "string", "description": "Origin reference (upsert)"},
					"source_type":  map[string]interface{}{"type": "string", "description": "manual, pr_discussion, agent_observation, file, conversation, code_review, automated (upsert)"},
					"confidence":   map[string]interface{}{"type": "number", "description": "How sure? 0.0-1.0 (upsert, default 1.0)"},
					"ttl_days":     map[string]interface{}{"type": "integer", "description": "Days until auto-expiry. 0 = never. (upsert)"},
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
// If no vault arg is present and the context is empty, returns the original context.
func (s *Server) mcpVaultContext(ctx context.Context, args map[string]interface{}) context.Context {
	if v, ok := args["vault"].(string); ok && v != "" {
		ctx = context.WithValue(ctx, vaultNameKey, v)
	}
	return ctx
}

func (s *Server) mcpDispatch(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	// Resolve vault from args for all tools that support multi-tenant routing
	ctx = s.mcpVaultContext(ctx, args)

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

func (s *Server) mcpRecall(ctx context.Context, args map[string]interface{}) (interface{}, error) {
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

	detail, _ := args["detail"].(string)
	if detail == "" {
		detail = "l2"
	}
	if detail != "l0" && detail != "l1" && detail != "l2" {
		return nil, fmt.Errorf("detail must be one of: l0, l1, l2")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	vector, err := s.embeddingFor(ctx).EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	results, err := s.qdrantFor(ctx).Search(ctx, vector, uint64(topK), scoreThreshold, sourceFilter, nil)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	out := make([]map[string]interface{}, 0, len(results))
	var topScore float32
	for _, r := range results {
		res := map[string]interface{}{
			"score":    r.Score,
			"chunk_id": r.Id.GetUuid(),
		}
		if r.Score > topScore {
			topScore = r.Score
		}
		if v, ok := r.Payload["text"]; ok {
			res["text"] = v.GetStringValue()
		}
		if v, ok := r.Payload["first_paragraph"]; ok {
			res["first_paragraph"] = v.GetStringValue()
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

		// Apply detail-level field filtering
		switch detail {
		case "l0":
			delete(res, "text")
			delete(res, "first_paragraph")
		case "l1":
			delete(res, "text")
		}
		// l2: include everything

		out = append(out, res)
	}

	return map[string]interface{}{
		"results":   out,
		"top_score": topScore,
	}, nil
}

// ── ragamuffin_get_chunk ───────────────────────────────────────────────────────

func (s *Server) mcpChunkGet(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	chunkID, _ := args["chunk_id"].(string)
	if chunkID == "" {
		return nil, fmt.Errorf("chunk_id is required")
	}

	uid, err := uuid.Parse(chunkID)
	if err != nil {
		return nil, fmt.Errorf("chunk_id must be a valid UUID")
	}
	pointID := pb.NewIDUUID(uid.String())

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	qc := s.qdrantFor(ctx)
	pts, err := qc.GetPoints(ctx, qc.Collection(), []*pb.PointId{pointID})
	if err != nil {
		return nil, fmt.Errorf("chunk retrieval failed: %w", err)
	}
	if len(pts) == 0 {
		return nil, fmt.Errorf("chunk with ID %s not found", chunkID)
	}

	pt := pts[0]
	payload := pt.GetPayload()

	resp := map[string]interface{}{
		"chunk_id":   chunkID,
		"source_file": "",
		"header":     "",
		"text":       "",
		"chunk_index": 0,
		"file_last_updated": "",
	}

	if v, ok := payload["source_file"]; ok {
		resp["source_file"] = v.GetStringValue()
	}
	if v, ok := payload["header"]; ok {
		resp["header"] = v.GetStringValue()
	}
	if v, ok := payload["text"]; ok {
		resp["text"] = v.GetStringValue()
	}
	if v, ok := payload["first_paragraph"]; ok {
		resp["first_paragraph"] = v.GetStringValue()
	}
	if v, ok := payload["chunk_index"]; ok {
		resp["chunk_index"] = int(v.GetIntegerValue())
	}
	if v, ok := payload["file_last_updated"]; ok {
		resp["file_last_updated"] = v.GetStringValue()
	}

	return resp, nil
}

func (s *Server) mcpAsk(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if !s.cfg.HasLLM() {
		return nil, fmt.Errorf("LLM not configured")
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

	contextText, sources, modeUsed, err := s.queryContext(ctx, query, mode, topK)
	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}

	answer, err := s.llmFor(ctx).Synthesize(ctx, query, contextText)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	return map[string]interface{}{
		"answer":    answer,
		"sources":   sources,
		"mode_used": modeUsed,
	}, nil
}

func (s *Server) mcpDraft(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Enforce write access — same as handleDraft in handlers.go
	if claims := auth.ClaimsFromContext(ctx); claims != nil && !claims.HasAccess("write") {
		return nil, fmt.Errorf("write access required")
	}

	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	targetPath, _ := args["target_path"].(string)
	mode, _ := args["mode"].(string)
	description, _ := args["description"].(string)
	doDelete, _ := args["delete"].(bool)

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
	vaultPath := s.vaultPathFromContext(ctx)
	fullPath, err := safeVaultPath(vaultPath, cleanPath)
	if err != nil {
		return nil, err
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

	if doDelete {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("delete failed: %w", err)
		}
	} else if content == "" {
		return nil, fmt.Errorf("content required unless delete=true")
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

// ── ragamuffin_store ───────────────────────────────────────────────────────────

func (s *Server) mcpStore(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Enforce write access — same as handleDraft in handlers.go
	if claims := auth.ClaimsFromContext(ctx); claims != nil && !claims.HasAccess("write") {
		return nil, fmt.Errorf("write access required")
	}

	content, _ := args["content"].(string)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}

	source, _ := args["source"].(string)
	if source == "" {
		return nil, fmt.Errorf("source is required")
	}

	var tags []string
	if raw, ok := args["tags"].([]interface{}); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	vaultName := vaultFromContext(ctx)
	if vaultName == "" {
		vaultName = "default"
	}

	// Get the indexer for this vault, auto-provisioning if needed (consistent with REST)
	idx := s.indexers.Get(vaultName)
	if idx == nil {
		idx = s.provisionVault(ctx, vaultName)
		if idx == nil {
			return nil, fmt.Errorf("vault %q not found and could not be provisioned", vaultName)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := idx.Ingest(ctx, content, source, tags); err != nil {
		return nil, fmt.Errorf("ingest failed: %w", err)
	}

	_, chunkCount, _, _, _, _ := idx.Stats()

	return map[string]interface{}{
		"status":      "ok",
		"vault":       vaultName,
		"source":      source,
		"chunk_count": chunkCount,
	}, nil
}

// ── ragamuffin_facts ───────────────────────────────────────────────────────────

func (s *Server) mcpFacts(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	operation, _ := args["operation"].(string)
	if operation == "" {
		return nil, fmt.Errorf("operation is required: list or upsert")
	}

	switch operation {
	case "list":
		return s.mcpFactsList(ctx, args)
	case "upsert":
		return s.mcpFactsUpsert(ctx, args)
	default:
		return nil, fmt.Errorf("unknown operation: %q (expected list or upsert)", operation)
	}
}

func (s *Server) mcpFactsList(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	prefix, _ := args["prefix"].(string)
	tagVal, _ := args["tag"].(string)
	status, _ := args["status"].(string)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Exact key lookup
	if key != "" {
		points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), factKeyFilter(key), 1, "")
		if err != nil {
			return nil, fmt.Errorf("facts query failed: %w", err)
		}
		if len(points) == 0 {
			return nil, fmt.Errorf("fact not found: %s", key)
		}
		fr := pointToFact(points[0])
		if fr == nil {
			return nil, fmt.Errorf("corrupt fact data for key: %s", key)
		}
		return map[string]interface{}{"facts": []interface{}{factToMap(fr)}}, nil
	}

	// Build list filter
	var conditions []*pb.Condition
	if prefix != "" {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "fact_key",
					Match: &pb.Match{
						MatchValue: &pb.Match_Text{Text: prefix},
					},
				},
			},
		})
	}
	if tagVal != "" {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "fact_tags",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: tagVal},
					},
				},
			},
		})
	}
	if status != "" {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "status",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: status},
					},
				},
			},
		})
	}

	var filter *pb.Filter
	if len(conditions) > 0 {
		filter = &pb.Filter{Must: conditions}
	}

	limit := uint32(100)
	if v, ok := args["limit"].(float64); ok && v > 0 && v <= 1000 {
		limit = uint32(v)
	}

	points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), filter, limit, "")
	if err != nil {
		return nil, fmt.Errorf("facts query failed: %w", err)
	}

	facts := make([]interface{}, 0, len(points))
	for _, p := range points {
		if fr := pointToFact(p); fr != nil {
			facts = append(facts, factToMap(fr))
		}
	}

	return map[string]interface{}{"facts": facts, "count": len(facts)}, nil
}

func (s *Server) mcpFactsUpsert(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Enforce write access — same as handleFactsPost in facts.go
	if claims := auth.ClaimsFromContext(ctx); claims != nil && !claims.HasAccess("write") {
		return nil, fmt.Errorf("write access required")
	}

	key, _ := args["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("key is required for upsert")
	}
	value, _ := args["value"].(string)
	if value == "" {
		return nil, fmt.Errorf("value is required for upsert")
	}

	var tags []string
	if raw, ok := args["tags"].([]interface{}); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	source, _ := args["source"].(string)
	sourceType, _ := args["source_type"].(string)

	// The handleFactsPost in facts.go handles all the complex logic
	// (created_at preservation, UUID generation, payload construction).
	// Rather than duplicating it, we reuse the same factPayload path.
	// Build a synthetic "POST" to the REST handler's logic.

	created := false

	// Check if fact exists (reuse the same pattern from handleFactsPost)
	pointID := factKeyHash(key)
	exists, err := s.factExists(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to check fact existence: %w", err)
	}
	if !exists {
		created = true
	}

	// Build the fact payload (mirrors handleFactsPost's logic)
	now := time.Now().UTC().Format(time.RFC3339)
	var createdAt string
	var confirmationCount int64 = 1
	var lastConfirmedAt string

	if exists {
		// Preserve created_at; read confirmation count
		points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), factKeyFilter(key), 1, "")
		if err == nil && len(points) > 0 {
			createdAt, _ = qutil.GetPayloadString(points[0].GetPayload(), "created_at")
			if cc, ok := points[0].GetPayload()["confirmation_count"]; ok {
				confirmationCount = cc.GetIntegerValue() + 1
			}
			lastConfirmedAt = now
		}
	}
	if createdAt == "" {
		createdAt = now
	}

	confidence := 1.0
	if v, ok := args["confidence"].(float64); ok && v >= 0 && v <= 1.0 {
		confidence = v
	}

	ttlDays := 0
	if v, ok := args["ttl_days"].(float64); ok && v >= 0 {
		ttlDays = int(v)
	}

		expiresAt := computeExpiresAt(ttlDays)
	var expiresAtUnix float64
	if ttlDays > 0 {
		expiresAtUnix = float64(time.Now().UTC().AddDate(0, 0, ttlDays).Unix())
	}

	payload := pb.NewValueMap(map[string]interface{}{
		"fact_key":           key,
		"fact_value":         value,
		"source":             source,
		"source_type":        sourceType,
		"confidence":         confidence,
		"status":             "active",
		"supersedes":         "",
		"conflict_resolved":  true,
		"confirmation_count": confirmationCount,
		"last_confirmed_at":  lastConfirmedAt,
		"created_at":         createdAt,
		"updated_at":         now,
		"ttl_days":           int64(ttlDays),
		"expires_at":         expiresAt,
		"expires_at_unix":    expiresAtUnix,
	})
	payload["contradicts"] = &pb.Value{
		Kind: &pb.Value_ListValue{
			ListValue: &pb.ListValue{Values: []*pb.Value{}},
		},
	}

	if len(tags) > 0 {
		tagVals := make([]*pb.Value, len(tags))
		for i, t := range tags {
			tagVals[i] = qutil.Nv(t)
		}
		payload["fact_tags"] = &pb.Value{
			Kind: &pb.Value_ListValue{
				ListValue: &pb.ListValue{Values: tagVals},
			},
		}
	}

	point := &pb.PointStruct{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{Uuid: pointID},
		},
		Payload: payload,
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{Data: []float32{0, 0, 0, 0}},
			},
		},
	}

	if err := s.facts.Upsert(ctx, []*pb.PointStruct{point}); err != nil {
		return nil, fmt.Errorf("failed to store fact: %w", err)
	}

	return map[string]interface{}{
		"key":     key,
		"value":   value,
		"status":  "active",
		"created": created,
	}, nil
}

// factToMap converts a *factResponse to a plain map for MCP JSON serialization.
func factToMap(fr *factResponse) map[string]interface{} {
	m := map[string]interface{}{
		"key":                fr.Key,
		"value":              fr.Value,
		"confidence":         fr.Confidence,
		"status":             fr.Status,
		"supersedes":         fr.Supersedes,
		"conflict_resolved":  fr.ConflictResolved,
		"confirmation_count": fr.ConfirmationCount,
		"created_at":         fr.CreatedAt,
		"updated_at":         fr.UpdatedAt,
	}
	if len(fr.Tags) > 0 {
		tags := make([]string, len(fr.Tags))
		copy(tags, fr.Tags)
		m["tags"] = tags
	}
	if fr.Source != "" {
		m["source"] = fr.Source
	}
	if fr.SourceType != "" {
		m["source_type"] = fr.SourceType
	}
	if len(fr.Contradicts) > 0 {
		m["contradicts"] = fr.Contradicts
	}
	if fr.LastConfirmedAt != "" {
		m["last_confirmed_at"] = fr.LastConfirmedAt
	}
	return m
}

// ── ragamuffin_graph ───────────────────────────────────────────────────────────

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
		// Full graph
		qc := s.indexers.GetClient(vaultName)
		if qc == nil {
			return map[string]interface{}{"nodes": []interface{}{}, "edges": []interface{}{}}, nil
		}

		nodes := make(map[string]graphNode)
		edges := make(map[string]graphEdge)
		edgeKey := func(s, t, rel string) string { return s + "|" + t + "|" + rel }

		var offset *pb.PointId
		totalNodes := 0

		for {
			scrollCtx, scrollCancel := context.WithTimeout(ctx, 10*time.Second)
			points, nextOffset, err := qc.Scroll(scrollCtx, 100, offset)
			scrollCancel()
			if err != nil {
				break
			}

			for _, point := range points {
				if totalNodes >= limit {
					break
				}

				sourceFile := point.GetPayload()["source_file"].GetStringValue()
				if sourceFile == "" {
					continue
				}

				fileID := "file:" + sourceFile
				if _, exists := nodes[fileID]; !exists {
					nodes[fileID] = graphNode{
						ID:    fileID,
						Type:  "file",
						Label: displayName(sourceFile),
					}
					totalNodes++
				}

				if linksVal := point.GetPayload()["links_to"]; linksVal != nil {
					for _, linkVal := range linksVal.GetListValue().GetValues() {
						targetFile := linkVal.GetStringValue()
						if targetFile == "" {
							continue
						}
						targetID := "file:" + targetFile
						k := edgeKey(fileID, targetID, "links_to")
						if _, exists := edges[k]; !exists {
							edges[k] = graphEdge{
								Source:       fileID,
								Target:       targetID,
								Relationship: "links_to",
							}
						}
					}
				}
			}

			if nextOffset == nil || totalNodes >= limit {
				break
			}
			offset = nextOffset
		}

		nodeList := make([]map[string]interface{}, 0, len(nodes))
		edgeList := make([]map[string]interface{}, 0, len(edges))
		for _, n := range nodes {
			nodeList = append(nodeList, map[string]interface{}{
				"id": n.ID, "type": n.Type, "label": n.Label,
			})
			if len(nodeList) >= limit {
				break
			}
		}
		for _, e := range edges {
			edgeList = append(edgeList, map[string]interface{}{
				"source": e.Source, "target": e.Target, "relationship": e.Relationship,
			})
			if len(edgeList) >= limit {
				break
			}
		}

		return map[string]interface{}{"nodes": nodeList, "edges": edgeList}, nil
	}

	// Entity-focused graph
	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		return map[string]interface{}{"nodes": []interface{}{}, "edges": []interface{}{}}, nil
	}

	eb := newEntityBFS(entity, depth, limit)

	{
		var scrollOffset *pb.PointId
		for {
			scrollCtx, scrollCancel := context.WithTimeout(ctx, 10*time.Second)
			points, nextOffset, err := qc.Scroll(scrollCtx, 500, scrollOffset)
			scrollCancel()
			if err != nil {
				break
			}
			for _, p := range points {
				if p.GetPayload() == nil {
					continue
				}

				src := p.GetPayload()["source_file"].GetStringValue()
				if src != "" {
					if text := p.GetPayload()["text"].GetStringValue(); text != "" && strings.Contains(text, entity) {
						eb.AddMatch(src)
					}
					if linksVal := p.GetPayload()["links_to"]; linksVal != nil {
						for _, linkVal := range linksVal.GetListValue().GetValues() {
							eb.AddLink(src, linkVal.GetStringValue())
						}
					}
				}
			}
			if nextOffset == nil || len(eb.Nodes()) >= limit {
				break
			}
			scrollOffset = nextOffset
		}
	}

	eb.Run()

	nodeList := make([]map[string]interface{}, 0, len(eb.Nodes()))
	edgeList := make([]map[string]interface{}, 0, len(eb.Edges()))
	for _, n := range eb.Nodes() {
		nodeList = append(nodeList, map[string]interface{}{
			"id": n.ID, "type": n.Type, "label": n.Label,
		})
	}
	for _, e := range eb.Edges() {
		edgeList = append(edgeList, map[string]interface{}{
			"source": e.Source, "target": e.Target, "relationship": e.Relationship,
		})
	}

	return map[string]interface{}{"nodes": nodeList, "edges": edgeList}, nil
}

// ── ragamuffin_stats ───────────────────────────────────────────────────────────

func (s *Server) mcpStats(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	vaultName := vaultFromContext(ctx)
	if vaultName == "" {
		vaultName = "default"
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var fileCount, chunkCount int
	var lastIndexed time.Time

	idx := s.indexers.Get(vaultName)
	if idx != nil {
		fileCount, chunkCount, lastIndexed, _, _, _ = idx.Stats()
	} else if !s.cfg.IsMultiTenant() {
		// Fallback to server-wide indexer
		idx2 := s.indexerFor(ctx)
		if idx2 != nil {
			fileCount, chunkCount, lastIndexed, _, _, _ = idx2.Stats()
		}
	}

	// Get total facts count
	factsCtx, factsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer factsCancel()
	totalFacts, err := s.facts.Count(factsCtx)
	if err != nil {
		s.log(ctx).Warn("mcp stats: facts count failed", "error", err)
		totalFacts = 0
	}

	// vaultAgeDays: approximate from lastIndexed (Stats doesn't expose oldest/newest)
	vaultAgeDays := 0
	if !lastIndexed.IsZero() {
		vaultAgeDays = int(time.Since(lastIndexed).Hours() / 24)
	}

	return map[string]interface{}{
		"vault":           vaultName,
		"indexed_files":   fileCount,
		"total_chunks":    chunkCount,
		"total_facts":     totalFacts,
		"last_indexed":    lastIndexed.Format(time.RFC3339),
		"vault_age_days":  vaultAgeDays,
	}, nil
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
	if len(checks) == 0 {
		checks = []string{"stale", "semantic_conflict", "gap", "duplicate"}
	}

	sampleSize := 50
	if v, ok := args["sample_size"].(float64); ok {
		sampleSize = int(v)
	}

	// Resolve vault path and Qdrant client from context (MCP is global, no vault context)
	vaultPath := s.vaultPathFromContext(ctx)
	vaultName := vaultFromContext(ctx)
	var qc qdrant.FactStore
	if vaultName != "" {
		qc = s.indexers.GetClient(vaultName)
	}

	resp := map[string]interface{}{
		"checks_run": checks,
	}

	checkSet := make(map[string]bool)
	for _, c := range checks {
		checkSet[c] = true
	}

	if checkSet["stale"] {
		staleFiles, err := s.checkStaleness(vaultPath, staleDays)
		if err != nil {
			s.log(ctx).Error("MCP audit: staleness check failed", "error", err)
		}
		resp["stale_files"] = staleFiles
	}

	if checkSet["gap"] {
		gaps := s.checkGaps(vaultPath)
		resp["gaps"] = gaps
	}

	if checkSet["duplicate"] {
		dupes := s.checkDuplicates(vaultPath)
		resp["duplicates"] = dupes
	}

	if checkSet["semantic_conflict"] {
		if !s.cfg.HasLLM() {
			resp["semantic_conflicts"] = []interface{}{}
		} else {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			conflicts, llmCalls := s.checkSemanticConflicts(ctx, qc, sampleSize)
			resp["semantic_conflicts"] = conflicts
			resp["semantic_conflict_llm_calls"] = llmCalls
		}
	}

	return resp, nil
}

// ── MCP Session Tools ───────────────────────────────────────────────────────────

func (s *Server) mcpSessionCreate(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	agentID, _ := args["agent_id"].(string)
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	content, _ := args["content"].(string)
	vault, _ := args["vault"].(string)

	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}

	resolvedVault := vault
	if resolvedVault == "" {
		resolvedVault = fmt.Sprintf("agent::%s", agentID)
	}

	sessionID := uuid.New().String()
	sess, err := s.logStore.CreateSession(ctx, sessionID, resolvedVault, agentID, "mcp")
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Register auto_extract
	autoExtract := false
	if ae, ok := args["auto_extract"].(bool); ok && ae {
		autoExtract = true
		if s.extractor != nil {
			s.extractor.SetSessionAutoExtract(sessionID, autoExtract)
		}
	}

	if content != "" {
		if _, err := s.logStore.AppendTurn(ctx, sessionID, content, "user"); err != nil {
			return nil, fmt.Errorf("create session with initial turn: %w", err)
		}
		sess.TurnCount = 1
	}

	return map[string]interface{}{
		"session_id":   sess.ID,
		"vault":        sess.Vault,
		"agent_id":     sess.AgentID,
		"turn_count":   sess.TurnCount,
		"created_at":   sess.CreatedAt,
		"auto_extract": autoExtract,
	}, nil
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

	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}

	sess, turnsList, err := s.logStore.GetSession(ctx, sessionID, turns)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	turnData := make([]map[string]interface{}, len(turnsList))
	for i, t := range turnsList {
		turnData[i] = map[string]interface{}{
			"id":         t.ID,
			"content":    t.Content,
			"role":       t.Role,
			"created_at": t.CreatedAt,
		}
	}

	return map[string]interface{}{
		"session_id": sess.ID,
		"vault":      sess.Vault,
		"agent_id":   sess.AgentID,
		"turn_count": sess.TurnCount,
		"created_at": sess.CreatedAt,
		"updated_at": sess.UpdatedAt,
		"turns":      turnData,
	}, nil
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

	// If agent_id specified but not vault, resolve vault
	if agentID != "" && vault == "" {
		vault = fmt.Sprintf("agent::%s", agentID)
	}

	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}

	sessions, err := s.logStore.ListSessions(ctx, vault, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
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
	if role == "" {
		role = "user"
	}

	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}

	turn, err := s.logStore.AppendTurn(ctx, sessionID, content, role)
	if err != nil {
		return nil, fmt.Errorf("append turn: %w", err)
	}

	// Trigger extraction if auto_extract is set
	extract := false
	if ae, ok := args["auto_extract"].(bool); ok {
		extract = ae
	} else if s.extractor != nil {
		extract = s.extractor.SessionAutoExtract(sessionID)
	}
	if extract && s.extractor != nil && s.extractor.Enabled() {
		go s.extractor.Extract(context.Background(), sessionID, content, role)
	}

	return map[string]interface{}{
		"turn_id":    turn.ID,
		"session_id": turn.SessionID,
		"role":       turn.Role,
		"created_at": turn.CreatedAt,
	}, nil
}
