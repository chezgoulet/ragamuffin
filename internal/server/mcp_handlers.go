package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/graph"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/mcp"
	pb "github.com/qdrant/go-client/qdrant"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
)

// ── MCP Tools ─────────────────────────────────────────────────────────────

// mcpVaultContext returns a context with the vault name set from MCP args.
func (s *Server) mcpVaultContext(ctx context.Context, args map[string]interface{}) context.Context {
	if v, ok := args["vault"].(string); ok && v != "" {
		ctx = context.WithValue(ctx, vaultNameKey, v)
	}
	return ctx
}

func (s *Server) mcpDispatch(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	ctx = s.mcpVaultContext(ctx, args)

	// Enforce vault scope from auth claims.
	if vaultName := vaultFromContext(ctx); vaultName != "" {
		if claims := auth.ClaimsFromContext(ctx); claims != nil {
			if !claims.HasVaultAccess(vaultName) {
				return nil, fmt.Errorf("access to vault %q denied by key scope", vaultName)
			}
		}
		// Auto-provision vault on first use — no explicit POST /vaults needed.
		s.ensureAgentVault(ctx, vaultName)
	}

	// Strip configured prefix to get the base tool name
	baseName := toolName
	if strings.HasPrefix(toolName, s.cfg.MCPToolPrefix) {
		baseName = strings.TrimPrefix(toolName, s.cfg.MCPToolPrefix)
	}

	switch baseName {
	case "recall":
		return s.mcpRecall(ctx, args)
	case "get_chunk":
		return s.mcpChunkGet(ctx, args)
	case "ask":
		return s.mcpAsk(ctx, args)
	case "store":
		return s.mcpStore(ctx, args)
	case "draft":
		return s.mcpDraft(ctx, args)
	case "fact_get":
		return s.mcpFactGet(ctx, args)
	case "fact_put":
		return s.mcpFactPut(ctx, args)
	case "fact_list":
		return s.mcpFactList(ctx, args)
	case "fact_delete":
		return s.mcpFactDelete(ctx, args)
	case "fact_graph":
		return s.mcpFactGraph(ctx, args)
	case "fact_history":
		return s.mcpFactHistory(ctx, args)
	case "fact_provenance":
		return s.mcpFactProvenance(ctx, args)
	case "review":
		return s.mcpReview(ctx, args)
	case "hybrid_search":
		return s.mcpHybridSearch(ctx, args)
	case "verify":
		return s.mcpVerify(ctx, args)
	case "context_bundle":
		return s.mcpContextBundle(ctx, args)
	case "dialectic":
		return s.mcpDialectic(ctx, args)
	case "peer_list":
		return s.mcpPeerList(ctx, args)
	case "briefing":
		return s.mcpBriefing(ctx, args)
	case "changes":
		return s.mcpChanges(ctx, args)
	case "contradictions":
		return s.mcpContradictions(ctx, args)
	case "links":
		return s.mcpLinks(ctx, args)
	case "graph_entity":
		return s.mcpGraphEntity(ctx, args)
	case "graph_edges":
		return s.mcpGraphEdges(ctx, args)
	case "graph_communities":
		return s.mcpGraphCommunities(ctx, args)
	case "audit":
		return s.mcpAudit(ctx, args)
	case "stats":
		return s.mcpStats(ctx, args)
	case "status":
		return s.mcpStatus(ctx, args)
	case "session_create":
		return s.mcpSessionCreate(ctx, args)
	case "session_get":
		return s.mcpSessionGet(ctx, args)
	case "session_list":
		return s.mcpSessionList(ctx, args)
	case "turn_append":
		return s.mcpTurnAppend(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}

}

// ── MCP Tools ─────────────────────────────────────────────────────────────

// toolDef builds an MCP tool definition with the configured prefix applied.
func (s *Server) toolDef(name, desc string, schema map[string]interface{}) mcp.ToolDefinition {
	return mcp.ToolDefinition{
		Name:        s.cfg.MCPToolPrefix + name,
		Description: desc,
		InputSchema: schema,
	}
}

func (s *Server) mcpTools() []mcp.ToolDefinition {
	return []mcp.ToolDefinition{
		s.toolDef("recall",
			"Search your knowledge base before starting work on any topic. Use this to check what's already known, find prior decisions, or locate relevant documentation. Returns ranked text chunks with source files and similarity scores.",
			map[string]interface{}{
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
			}),
		s.toolDef("ask",
			"Ask a question that spans multiple documents and get a synthesized answer. Use when you need a coherent summary rather than raw search results. Returns a prose answer with source citations.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string", "description": "The question to answer"},
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"mode":  map[string]interface{}{"type": "string", "description": "auto, rag, or full (default: auto)"},
					"top_k": map[string]interface{}{"type": "integer", "description": "RAG results to retrieve (1-50, default 8)"},
				},
				"required": []string{"query"},
			}),
		s.toolDef("store",
			"Save knowledge you've learned so it persists beyond this session. Use after reaching conclusions, discovering patterns, or completing analysis worth preserving. Returns the store result with source identifier.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]interface{}{"type": "string", "description": "Text content to ingest (markdown, plain text)"},
					"source":  map[string]interface{}{"type": "string", "description": "Origin identifier (agent name, workflow ID, file path)"},
					"vault":   map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"tags":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional tags for filtering"},
				},
				"required": []string{"content", "source"},
			}),
		s.toolDef("draft",
			"Propose a document edit. Direct mode writes immediately; PR mode opens a pull request. Use when your analysis or research leads to a concrete change. Returns the write result with file path.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       map[string]interface{}{"type": "string", "description": "Document title (PR title if PR mode)"},
					"content":     map[string]interface{}{"type": "string", "description": "File content to write. Required unless delete=true."},
					"target_path": map[string]interface{}{"type": "string", "description": "Vault path relative to vault root"},
					"mode":        map[string]interface{}{"type": "string", "description": "direct or pr (default: direct)"},
					"vault":       map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"description": map[string]interface{}{"type": "string", "description": "Optional PR body (PR mode)"},
					"delete":      map[string]interface{}{"type": "boolean", "description": "Delete the file instead of writing"},
				},
				"required": []string{"title", "target_path"},
			}),
		s.toolDef("fact_get",
			"Look up a specific known fact by its key. Use when you know the exact key and need the current value, confidence, and status. Returns the fact value with metadata.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "Exact fact key to retrieve"},
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
				"required": []string{"key"},
			}),
		s.toolDef("fact_put",
			"Record a discrete decision, preference, or configuration as a named fact. Use for small, structured knowledge — connection strings, architectural decisions, agent preferences. Facts are versioned and can be superseded.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":         map[string]interface{}{"type": "string", "description": "Unique fact key, e.g. decision/use-postgres"},
					"value":       map[string]interface{}{"type": "string", "description": "The fact value to store"},
					"vault":       map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"tags":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional tags"},
					"source":      map[string]interface{}{"type": "string", "description": "Origin reference"},
					"source_type": map[string]interface{}{"type": "string", "description": "manual, agent_observation, conversation, code_review, automated"},
					"confidence":  map[string]interface{}{"type": "number", "description": "Confidence 0.0-1.0 (default 1.0)"},
					"ttl_days":    map[string]interface{}{"type": "integer", "description": "Days until auto-expiry. 0 = never (default)."},
				},
				"required": []string{"key", "value"},
			}),
		s.toolDef("fact_list",
			"Browse existing facts by prefix, tag, or status. Use to discover what facts exist before writing new ones. Returns a list of matching facts with values and metadata.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":    map[string]interface{}{"type": "string", "description": "Exact key filter"},
					"prefix": map[string]interface{}{"type": "string", "description": "Key prefix filter"},
					"tag":    map[string]interface{}{"type": "string", "description": "Tag filter"},
					"status": map[string]interface{}{"type": "string", "description": "Lifecycle status: active, needs_review, superseded, rejected"},
					"vault":  map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max results (1-1000, default 100)"},
				},
			}),
		s.toolDef("fact_delete",
			"Permanently remove a fact by its key. For cleanup of obsolete entries. Irreversible — consider superseding for reversible deprecation.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "Exact fact key to delete"},
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
				"required": []string{"key"},
			}),
		s.toolDef("fact_graph",
			"Explore the lineage of a fact — what it supersedes, contradicts, or refines. Use to resolve conflicting information by tracing provenance. Returns a graph of related facts with relationship types.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "Fact key for the lineage graph"},
					"depth": map[string]interface{}{"type": "integer", "description": "Traversal depth (0-5, default 1)"},
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
				"required": []string{"key"},
			}),
		s.toolDef("fact_history",
			"See how a fact evolved over time. Use to verify when it was last confirmed or how it changed. Returns a chronological list of events.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "Fact key for history"},
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
				"required": []string{"key"},
			}),
		s.toolDef("fact_provenance",
			"Trace where a fact came from. Use to verify credibility or understand origin. Returns complete provenance metadata.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":   map[string]interface{}{"type": "string", "description": "Fact key for provenance"},
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
				"required": []string{"key"},
			}),
		s.toolDef("review",
			"Review facts flagged for attention. Use when alerted about contradictions, low confidence, or near-expiry entries. Resolve with confirm, supersede, or reject.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action":     map[string]interface{}{"type": "string", "description": "'list' or 'resolve'"},
					"reason":     map[string]interface{}{"type": "string", "description": "Filter by review reason"},
					"limit":      map[string]interface{}{"type": "integer", "description": "Max results (1-100, default 20)"},
					"point_id":   map[string]interface{}{"type": "string", "description": "Point ID to resolve"},
					"resolution": map[string]interface{}{"type": "string", "description": "confirm, supersede, or reject"},
					"correction": map[string]interface{}{"type": "string", "description": "Corrected value (required for supersede)"},
					"vault":      map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("hybrid_search",
			"Search documents and facts together. Use when you don't know whether the information is in a document or a fact. Returns interleaved results ranked by combined relevance.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":  map[string]interface{}{"type": "string", "description": "Natural-language search query"},
					"key":    map[string]interface{}{"type": "string", "description": "Exact fact key filter"},
					"prefix": map[string]interface{}{"type": "string", "description": "Fact key prefix filter"},
					"tag":    map[string]interface{}{"type": "string", "description": "Fact tag filter"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max results (1-100, default 20)"},
					"vault":  map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("verify",
			"Check a claim against your knowledge base. Use to detect contradictions before acting on uncertain information.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"fact":  map[string]interface{}{"type": "string", "description": "The fact statement to validate"},
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"top_k": map[string]interface{}{"type": "integer", "description": "Max results (1-50, default 10)"},
				},
				"required": []string{"fact"},
			}),
		s.toolDef("context_bundle",
			"Get a quick orientation snapshot. Use at session start or when returning to a vault after time away. Returns peer card, recent facts, and relevant memories.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_identity": map[string]interface{}{"type": "string", "description": "Agent name for peer card lookup"},
					"query":          map[string]interface{}{"type": "string", "description": "Optional query to focus recall"},
					"top_k":          map[string]interface{}{"type": "integer", "description": "Recall results to include (1-10, default 3)"},
					"vault":          map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("dialectic",
			"Generate structured reasoning prompts. Use before decisions needing thorough vetting — architectural choices, policy decisions, conflict resolution. Returns cold, warm, and hot reasoning blocks.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"depth":   map[string]interface{}{"type": "integer", "description": "Reasoning depth: 1=cold, 2=cold+warm, 3=cold+warm+hot (default: 1)"},
					"vault":   map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"context": map[string]interface{}{"type": "string", "description": "Optional context to reason about"},
				},
			}),
		s.toolDef("peer_list",
			"Discover other agents by listing peer cards. Use to find what agents exist and how to reference their vaults.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("briefing",
			"Get a summary of recent vault activity. Use when returning after an absence. Returns an event digest by type for the specified period.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault":  map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"period": map[string]interface{}{"type": "string", "description": "Time period: '24h' (default), '168h', '720h'"},
				},
			}),
		s.toolDef("changes",
			"See what's changed recently. Use to catch up on activity since your last session. Returns chronologically ordered changes with timestamps.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault":  map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"period": map[string]interface{}{"type": "string", "description": "Time period: '24h' (default), '168h', '720h'"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max results (1-50, default 20)"},
				},
			}),
		s.toolDef("contradictions",
			"Find conflicting information in your knowledge base. Use to detect inconsistencies before they cause problems.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"limit": map[string]interface{}{"type": "integer", "description": "Max pairs (1-50, default 20)"},
				},
			}),
		s.toolDef("links",
			"Navigate the link graph between documents. Use to discover related content or find backlinks.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"source": map[string]interface{}{"type": "string", "description": "Source file path"},
					"mode":   map[string]interface{}{"type": "string", "description": "'outbound', 'backlinks', or 'graph'"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max results (1-500, default 100)"},
					"vault":  map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("graph_entity",
			"Look up an entity in the knowledge graph. Use to find what's known about a person, project, or concept.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"entity_id": map[string]interface{}{"type": "string", "description": "Entity UUID or name"},
					"vault":     map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("graph_edges",
			"Query relationships between entities. Use to understand how things are connected. Returns edges with relationship types.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"entity_id": map[string]interface{}{"type": "string", "description": "Filter edges involving this entity"},
					"rel_type":  map[string]interface{}{"type": "string", "description": "Edge type filter"},
					"vault":     map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("graph_communities",
			"Discover how entities naturally cluster. Returns communities with member counts and summaries.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("audit",
			"Run a vault health check. Use periodically to find stale files, conflicts, gaps, and duplicates.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault":       map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"stale_days":  map[string]interface{}{"type": "integer", "description": "Days to flag as stale (default: 90)"},
					"checks":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "stale, semantic_conflict, gap, duplicate"},
					"sample_size": map[string]interface{}{"type": "integer", "description": "Chunk pairs to compare (1-200, default 50)"},
				},
			}),
		s.toolDef("stats",
			"Get operational metrics. File counts, chunk counts, fact counts, vault age. No side effects.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("status",
			"Check server health and connectivity. Use at session start or after errors. Returns status, version, uptime.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vault": map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
			}),
		s.toolDef("session_create",
			"Start a conversation session. Use at the beginning of a multi-turn interaction. Returns session ID for subsequent calls.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id":     map[string]interface{}{"type": "string", "description": "Agent identity"},
					"content":      map[string]interface{}{"type": "string", "description": "Optional initial message"},
					"vault":        map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
					"auto_extract": map[string]interface{}{"type": "boolean", "description": "Auto fact extraction from turns"},
				},
				"required": []string{"agent_id"},
			}),
		s.toolDef("session_get",
			"Retrieve session metadata and turns. Use to review past interactions or continue a paused conversation.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{"type": "string", "description": "Session UUID"},
					"turns":      map[string]interface{}{"type": "integer", "description": "Max turns to return (0 for all, default 50)"},
				},
				"required": []string{"session_id"},
			}),
		s.toolDef("session_list",
			"Browse active sessions. Use to find sessions needing attention or get an overview of recent activity.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id": map[string]interface{}{"type": "string", "description": "Filter by agent identity"},
					"vault":    map[string]interface{}{"type": "string", "description": "Filter by vault name"},
					"limit":    map[string]interface{}{"type": "integer", "description": "Max results (default 100)"},
					"offset":   map[string]interface{}{"type": "integer", "description": "Result offset (default 0)"},
				},
			}),
		s.toolDef("get_chunk",
			"Retrieve full text and metadata of a specific chunk. Use when recall found relevant results and you need complete context.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chunk_id": map[string]interface{}{"type": "string", "description": "UUID chunk ID from recall results"},
					"vault":    map[string]interface{}{"type": "string", "description": "Target vault name (multi-tenant)"},
				},
				"required": []string{"chunk_id"},
			}),
		s.toolDef("turn_append",
			"Add a message to a session. Use during conversations to persist exchanges for future context.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id":   map[string]interface{}{"type": "string", "description": "Session UUID"},
					"content":      map[string]interface{}{"type": "string", "description": "Message content"},
					"role":         map[string]interface{}{"type": "string", "description": "user, assistant, or system (default: user)"},
					"auto_extract": map[string]interface{}{"type": "boolean", "description": "Extract facts from this turn"},
				},
				"required": []string{"session_id", "content"},
			}),
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

	answer, sources, modeUsed, err := s.doAsk(ctx, query, mode, topK, "", false)
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

func (s *Server) mcpVerify(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	fact, _ := args["fact"].(string)
	if fact == "" {
		return nil, fmt.Errorf("fact is required")
	}
	topK := 10
	if v, ok := args["top_k"].(float64); ok && v > 0 {
		topK = int(v)
		if topK > 50 {
			topK = 50
		}
	}
	return s.doVerify(ctx, verifyRequest{Fact: fact, TopK: topK})
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

// ── Fact handlers ───────────────────────────────────────────────────────────

func (s *Server) mcpFactGet(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}
	return s.doFactsList(ctx, key, "", "", "", "", 1)
}

func (s *Server) mcpFactPut(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if key == "" || value == "" {
		return nil, fmt.Errorf("both key and value are required")
	}

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
}

func (s *Server) mcpFactList(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	prefix, _ := args["prefix"].(string)
	tag, _ := args["tag"].(string)
	status, _ := args["status"].(string)

	limit := 100
	if v, ok := args["limit"].(float64); ok && v > 0 && v <= 1000 {
		limit = int(v)
	}

	return s.doFactsList(ctx, key, prefix, "", tag, status, limit)
}

func (s *Server) mcpFactDelete(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}

	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	qc := s.factsQdrantFor(ctx2)
	filter := factKeyFilter(key)
	if err := qc.DeleteFiltered(ctx2, s.factsCollectionFor(ctx2), filter); err != nil {
		return nil, fmt.Errorf("delete fact: %w", err)
	}
	return map[string]interface{}{"deleted": true, "key": key}, nil
}

func (s *Server) mcpFactGraph(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}

	depth := 1
	if v, ok := args["depth"].(float64); ok && v >= 0 && v <= 5 {
		depth = int(v)
	}

	factsStore := s.factsQdrantFor(ctx)
	collection := s.factsCollectionFor(ctx)

	visited := make(map[string]bool)
	var nodes []factGraphNode
	var edges []factGraphEdge

	rootFact := s.fetchFactByPayloadKey(ctx, factsStore, collection, key)
	if rootFact == nil {
		return nil, fmt.Errorf("fact %q not found", key)
	}

	nodes = append(nodes, factGraphNode{Key: key, Value: rootFact.Value, FactType: "current"})
	visited[key] = true
	s.traverseFactGraph(ctx, factsStore, collection, key, depth, 0, visited, &nodes, &edges)

	return map[string]interface{}{
		"key":   key,
		"nodes": nodes,
		"edges": edges,
	}, nil
}

func (s *Server) mcpFactHistory(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}

	qrCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pointID := &pb.PointId{
		PointIdOptions: &pb.PointId_Uuid{Uuid: factKeyHash(key)},
	}
	points, err := s.facts.GetPoints(qrCtx, s.factsCollectionFor(ctx), []*pb.PointId{pointID})
	if err != nil {
		return nil, fmt.Errorf("query fact: %w", err)
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("fact %q not found", key)
	}

	payload := points[0].GetPayload()
	updates := qutil.GetPayloadStringList(payload, "update_history")

	entries := []map[string]interface{}{
		{"event": "created", "timestamp": qutil.GetPayloadStringValue(payload, "created_at")},
	}
	if ct := qutil.GetPayloadStringValue(payload, "last_confirmed_at"); ct != "" {
		entries = append(entries, map[string]interface{}{"event": "confirmed", "timestamp": ct})
	}
	for _, u := range updates {
		entries = append(entries, map[string]interface{}{"event": "updated", "timestamp": u})
	}

	return map[string]interface{}{"key": key, "history": entries}, nil
}

func (s *Server) mcpFactProvenance(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}

	qrCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pointID := &pb.PointId{
		PointIdOptions: &pb.PointId_Uuid{Uuid: factKeyHash(key)},
	}
	points, err := s.facts.GetPoints(qrCtx, s.factsCollectionFor(ctx), []*pb.PointId{pointID})
	if err != nil {
		return nil, fmt.Errorf("query fact: %w", err)
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("fact %q not found", key)
	}

	payload := points[0].GetPayload()
	var relatedChunks []string
	if rc, ok := payload["related_chunks"]; ok {
		if lv := rc.GetListValue(); lv != nil {
			for _, v := range lv.GetValues() {
				relatedChunks = append(relatedChunks, v.GetStringValue())
			}
		}
	}

	return map[string]interface{}{
		"key":            qutil.GetPayloadStringValue(payload, "fact_key"),
		"value":          qutil.GetPayloadStringValue(payload, "fact_value"),
		"source":         qutil.GetPayloadStringValue(payload, "source"),
		"source_type":    qutil.GetPayloadStringValue(payload, "source_type"),
		"created_at":     qutil.GetPayloadStringValue(payload, "created_at"),
		"updated_at":     qutil.GetPayloadStringValue(payload, "updated_at"),
		"related_chunks": relatedChunks,
	}, nil
}

// ── Review handler ──────────────────────────────────────────────────────────

func (s *Server) mcpReview(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	action, _ := args["action"].(string)
	if action == "" || action == "list" {
		limit := 20
		if v, ok := args["limit"].(float64); ok && v > 0 && v <= 100 {
			limit = int(v)
		}
		return s.doFactsList(ctx, "", "", "", "", "needs_review", limit)
	}

	if action == "resolve" {
		pointID, _ := args["point_id"].(string)
		resolution, _ := args["resolution"].(string)
		if pointID == "" || resolution == "" {
			return nil, fmt.Errorf("point_id and resolution are required for resolve action")
		}
		qc := s.factsQdrantFor(ctx)
		collection := s.factsCollectionFor(ctx)
		var payload map[string]*pb.Value
		switch resolution {
		case "confirm":
			payload = map[string]*pb.Value{
				"status":            qutil.Nv("active"),
				"conflict_resolved": qutil.Nv(true),
			}
		case "reject":
			payload = map[string]*pb.Value{
				"status": qutil.Nv("rejected"),
			}
		case "supersede":
			payload = map[string]*pb.Value{
				"status": qutil.Nv("superseded"),
			}
		default:
			return nil, fmt.Errorf("unknown resolution: %q (expected confirm, supersede, or reject)", resolution)
		}
		if err := qc.SetPayload(ctx, collection, []*pb.PointId{{
			PointIdOptions: &pb.PointId_Uuid{Uuid: pointID},
		}}, payload); err != nil {
			return nil, fmt.Errorf("resolve review: %w", err)
		}
		return map[string]interface{}{"resolved": true, "point_id": pointID}, nil
	}

	return nil, fmt.Errorf("unknown action: %q (expected list or resolve)", action)
}

// ── Hybrid search handler ───────────────────────────────────────────────────

func (s *Server) mcpHybridSearch(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	key, _ := args["key"].(string)
	prefix, _ := args["prefix"].(string)
	tag, _ := args["tag"].(string)

	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 && v <= 100 {
		limit = int(v)
	}

	if query == "" && key == "" && prefix == "" {
		return nil, fmt.Errorf("query, key, or prefix is required")
	}

	// Use doRecall for the search portion, then collect facts separately.
	recallCtx, recallCancel := context.WithTimeout(ctx, 30*time.Second)
	defer recallCancel()

	results, _, err := s.doRecall(recallCtx, recallRequest{
		Query:  query,
		TopK:   limit,
		Detail: "l1",
	})
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	facts, err := s.doFactsList(recallCtx, key, prefix, "", tag, "", limit)
	if err != nil {
		facts = nil
	}

	return map[string]interface{}{
		"chunks": results,
		"facts":  facts,
	}, nil
}

// ── Context bundle handler ──────────────────────────────────────────────────

func (s *Server) mcpContextBundle(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	agentID, _ := args["agent_identity"].(string)
	query, _ := args["query"].(string)
	topK := 3
	if v, ok := args["top_k"].(float64); ok && v > 0 && v <= 10 {
		topK = int(v)
	}

	bundle := map[string]interface{}{
		"vault": vaultFromContext(ctx),
	}

	// Peer card
	if agentID != "" {
		cardKey := fmt.Sprintf("peer/%s/card/profile", agentID)
		cardResult, err := s.doFactsList(ctx, cardKey, "", "", "", "", 1)
		if err == nil && cardResult != nil {
			bundle["peer_card"] = cardResult
		}
	}

	// Recent facts
	recentFacts, err := s.doFactsList(ctx, "", "", "", "", "", 10)
	if err == nil {
		bundle["recent_facts"] = recentFacts
	}

	// Recall if query provided
	if query != "" {
		recallCtx, recallCancel := context.WithTimeout(ctx, 15*time.Second)
		defer recallCancel()
		results, _, err := s.doRecall(recallCtx, recallRequest{
			Query:  query,
			TopK:   topK,
			Detail: "l1",
		})
		if err == nil {
			bundle["recall"] = results
		}
	}

	return bundle, nil
}

// ── Dialectic handler ─────────────────────────────────────────────────────

func (s *Server) mcpDialectic(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	depth := 1
	if v, ok := args["depth"].(float64); ok && v >= 1 && v <= 3 {
		depth = int(v)
	}

	contextStr, _ := args["context"].(string)

	passes := []map[string]interface{}{
		{
			"level":  "cold",
			"role":   "analytical",
			"prompt": "<dialectic-pass level=\"cold\" role=\"analytical\">\n# Analytical Reasoning\nReview the context below. Identify:\n- Specific verifiable facts you can extract\n- Contradictions or inconsistencies\n- Stale or out-of-date information\n- Gaps where information is missing\n</dialectic-pass>",
		},
	}
	if depth >= 2 {
		passes = append(passes, map[string]interface{}{
			"level":  "warm",
			"role":   "synthetic",
			"prompt": "<dialectic-pass level=\"warm\" role=\"synthetic\">\n# Synthetic Reasoning\nDraw connections from the context below. Consider:\n- What underlying patterns emerge?\n- What hypotheses explain the data?\n- What related information might be useful?\n- What should this agent learn next?\n</dialectic-pass>",
		})
	}
	if depth >= 3 {
		passes = append(passes, map[string]interface{}{
			"level":  "hot",
			"role":   "evaluative",
			"prompt": "<dialectic-pass level=\"hot\" role=\"evaluative\">\n# Evaluative Reasoning\nAssess the quality of information below:\n- Rate confidence in each fact (0.0-1.0)\n- Which facts are most critical to remember?\n- Which facts need verification?\n- Summarize the current state of knowledge\n</dialectic-pass>",
		})
	}

	result := map[string]interface{}{
		"depth":  depth,
		"passes": passes,
	}
	if contextStr != "" {
		result["context"] = contextStr
	}

	return result, nil
}

// ── Peer discovery ──────────────────────────────────────────────────────────

func (s *Server) mcpPeerList(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	ctx2, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.doFactsList(ctx2, "", "peer/", "", "", "", 100)
}

// ── Briefing handler ────────────────────────────────────────────────────────

func (s *Server) mcpBriefing(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	period, _ := args["period"].(string)
	if period == "" {
		period = "24h"
	}
	d, err := time.ParseDuration(period)
	if err != nil {
		return nil, fmt.Errorf("invalid period: %q", period)
	}
	since := time.Now().Add(-d)

	vault := vaultFromContext(ctx)
	if vault == "" {
		vault = "default"
	}

	if s.logStore != nil {
		entries, _, err := s.logStore.List(ctx, logstore.Filter{
			Since: since.Format(time.RFC3339),
			Limit: 100,
		})
		if err == nil {
			eventCounts := map[string]int{}
			for _, e := range entries {
				eventCounts[e.Type]++
			}
			return map[string]interface{}{
				"vault":        vault,
				"period_hours": d.Hours(),
				"total_events": len(entries),
				"events":       eventCounts,
			}, nil
		}
	}

	return map[string]interface{}{
		"vault":        vault,
		"period_hours": d.Hours(),
		"total_events": 0,
	}, nil
}

// ── Changes handler — temporal awareness: what changed recently? ────────────

func (s *Server) mcpChanges(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	period, _ := args["period"].(string)
	if period == "" {
		period = "24h"
	}
	d, err := time.ParseDuration(period)
	if err != nil {
		return nil, fmt.Errorf("invalid period: %q", period)
	}
	since := time.Now().Add(-d)

	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 && v <= 50 {
		limit = int(v)
	}

	vault := vaultFromContext(ctx)
	if vault == "" {
		vault = "default"
	}

	changes := map[string]interface{}{
		"vault":        vault,
		"period_hours": d.Hours(),
	}

	// Recent log events
	if s.logStore != nil {
		entries, _, err := s.logStore.List(ctx, logstore.Filter{
			Since: since.Format(time.RFC3339),
			Limit: limit,
		})
		if err == nil {
			events := make([]map[string]interface{}, 0, len(entries))
			for _, e := range entries {
				events = append(events, map[string]interface{}{
					"time": e.CreatedAt,
					"type": e.Type,
					"body": e.Body,
				})
			}
			changes["events"] = events
			changes["total_events"] = len(events)
		}
	}

	// Recent facts
	qrCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	factsResult, err := s.doFactsList(qrCtx, "", "", "", "", "", limit)
	if err == nil {
		changes["facts"] = factsResult
	}

	return changes, nil
}

// ── Contradictions handler ──────────────────────────────────────────────────

func (s *Server) mcpContradictions(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 && v <= 50 {
		limit = int(v)
	}

	return s.doFactsList(ctx, "", "", "", "", "needs_review", limit)
}

// ── Links handler ───────────────────────────────────────────────────────────

func (s *Server) mcpLinks(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	source, _ := args["source"].(string)
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "outbound"
	}

	ctx2, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	vault := vaultFromContext(ctx)
	if vault == "" {
		vault = "default"
	}

	if s.logStore == nil {
		return nil, fmt.Errorf("log store not available")
	}

	switch mode {
	case "backlinks":
		backlinks, err := s.logStore.GetInboundLinks(ctx2, source, vault)
		if err != nil {
			return nil, fmt.Errorf("backlinks: %w", err)
		}
		return map[string]interface{}{"mode": "backlinks", "results": backlinks}, nil
	case "graph":
		graph, err := s.logStore.GetLinkGraph(ctx2, source, "", vault)
		if err != nil {
			return nil, fmt.Errorf("link graph: %w", err)
		}
		return map[string]interface{}{"mode": "graph", "results": graph}, nil
	default:
		links, err := s.logStore.GetOutboundLinks(ctx2, source, vault)
		if err != nil {
			return nil, fmt.Errorf("links: %w", err)
		}
		return map[string]interface{}{"mode": "outbound", "results": links}, nil
	}
}

// ── Graph entity/edges/communities handlers ─────────────────────────────────

func (s *Server) mcpGraphEntity(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return nil, fmt.Errorf("entity_id is required")
	}

	if s.graph == nil {
		return nil, fmt.Errorf("graph store not configured")
	}

	ent, err := s.graph.GetEntity(ctx, entityID)
	if err != nil {
		return nil, fmt.Errorf("get entity: %w", err)
	}
	if ent == nil {
		return nil, fmt.Errorf("entity %q not found", entityID)
	}
	return ent, nil
}

func (s *Server) mcpGraphEdges(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	entityID, _ := args["entity_id"].(string)
	relType, _ := args["rel_type"].(string)

	vault := vaultFromContext(ctx)
	if vault == "" {
		vault = "default"
	}

	if s.graph == nil {
		return nil, fmt.Errorf("graph store not configured")
	}

	edges, err := s.graph.Edges(ctx, graph.EdgeQuery{
		Vault:    vault,
		EntityID: entityID,
		Type:     relType,
		Limit:    1000,
	})
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	return map[string]interface{}{"edges": edges, "count": len(edges)}, nil
}

func (s *Server) mcpGraphCommunities(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	vault := vaultFromContext(ctx)
	if vault == "" {
		vault = "default"
	}

	if s.graph == nil {
		return nil, fmt.Errorf("graph store not configured")
	}

	comms, err := s.graph.Communities(ctx, vault)
	if err != nil {
		return nil, fmt.Errorf("query communities: %w", err)
	}
	return map[string]interface{}{"communities": comms, "count": len(comms)}, nil
}

// ── Status handler ──────────────────────────────────────────────────────────

func (s *Server) mcpStatus(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	status := "ok"
	var healthErrors []string

	if s.qdrantFor(ctx) == nil {
		healthErrors = append(healthErrors, "qdrant: not configured")
	}
	if s.embeddingFor(ctx) == nil {
		healthErrors = append(healthErrors, "embedder: not configured")
	}
	if len(healthErrors) > 0 {
		status = "degraded"
	}

	return map[string]interface{}{
		"status":  status,
		"version": Version,
		"commit":  Commit,
		"errors":  healthErrors,
		"uptime":  time.Since(s.started).Seconds(),
	}, nil
}

// ensureAgentVault creates an agent vault on demand if it doesn't exist.
// Agent vaults are Qdrant-backed memory stores — no filesystem path needed.
// Safe to call on every tool dispatch; idempotent after first creation.
func (s *Server) ensureAgentVault(ctx context.Context, vaultName string) {
	if s.indexers == nil {
		return
	}
	if s.indexers.Get(vaultName) != nil {
		return // already provisioned
	}

	// Check if this is a valid agent vault prefix (agent:: or similar).
	if !strings.Contains(vaultName, ":") {
		return // not an agent vault, skip auto-provision
	}

	// Agent vaults don't need a filesystem path — create one in tmp.
	vaultPath := filepath.Join("/tmp/ragamuffin-agents", vaultName)
	if err := os.MkdirAll(vaultPath, 0755); err != nil {
		s.logger.Warn("mcp: failed to create agent vault dir", "vault", vaultName, "error", err)
		return
	}

	qc := s.qdrantFor(ctx)
	ec := s.embeddingFor(ctx)
	if qc == nil || ec == nil {
		s.logger.Warn("mcp: cannot provision agent vault — qdrant or embedder not configured", "vault", vaultName)
		return
	}

	idx := indexer.New(vaultPath, vaultName, qc, ec, s.logger)
	if err := s.indexers.Add(vaultName, idx, qc); err != nil {
		s.logger.Warn("mcp: failed to register agent vault", "vault", vaultName, "error", err)
		return
	}
	s.logger.Info("mcp: auto-provisioned agent vault", "vault", vaultName, "path", vaultPath)
}
