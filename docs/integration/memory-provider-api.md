# Ragamuffin Agent Integration Guide

Ragamuffin gives agents **semantic memory, structured facts, knowledge graphs,
LLM synthesis, and quality review** — all via a single zero-dep binary. This
guide covers three integration tiers:

1. **MCP (Model Context Protocol)** — universal, no harness code required
2. **Per-harness slot** — tight lifecycle hooks for auto-injection
3. **Hybrid / direct API** — agents call HTTP directly alongside existing slot

> **Who this is for:** Harness authors, gateway operators, and anyone wiring
> Ragamuffin into an agent runtime.

---

## Quick Start

```bash
# Point your MCP client at ragamuffin's SSE endpoint
# An MCP-capable agent gets 33 tools immediately.
MCP_SERVER_URL="http://ragamuffin:8000/mcp"
```

If your harness supports MCP natively (Claude Desktop, Goose, Copilot, etc.),
you're done — skip to the [tool catalog](#mcp-tool-catalog).

If your harness has a pluggable memory slot (OpenClaw, Hermes, Honcho, etc.),
add a thin shim (~100 lines) that:
- Connects to MCP for all tools
- Implements lifecycle hooks (prefetch, session-end) in harness-native code

---

## Architecture: MCP-First, Adapter-Thin

```
                    ┌─────────────────────┐
                    │    Agent Harness     │
                    │  (OpenClaw/Hermes)   │
                    └──┬──────────────┬────┘
                       │              │
              MCP      │     Harness  │ Lifecycle hooks
           (33 tools)  │     plugin   │ (prefetch, session-end)
                       ▼              ▼
              ┌──────────────────────────┐
              │     Ragamuffin           │
              │  /mcp (SSE + JSON-RPC)   │
              │  + REST endpoints        │
              └──────────────────────────┘
```

**The MCP layer is the universal tool surface.** Any MCP-compatible host gets
33 tools with zero adapter code. Per-harness adapters add only what MCP can't:
auto-injection (prefetch, cadence-gated context) and session-end hooks.

---

## MCP Tool Catalog

Ragamuffin exposes **33 tools** via `POST /mcp` (JSON-RPC 2.0) on an SSE
stream at `GET /mcp`. Call `tools/list` to discover them, or refer to the
catalog below.

### Search & Synthesis

| Tool | What it does |
|------|-------------|
| `ragamuffin_recall` | Semantic search: ranked chunks with scores and timestamps. Supports detail levels (l0/l1/l2), score thresholds, and source filters. |
| `ragamuffin_ask` | LLM synthesis with citations: full-context question answering. Modes: auto (smart cutoff), rag (vector-only), full (whole vault). |
| `ragamuffin_hybrid_search` | Dense + BM25 hybrid search. Returns both chunks AND facts ranked by combined relevance. |
| `ragamuffin_verify` | Validate a claim against the vault. Returns confirmed/conflicts/insufficient. |

### Fact CRUD & Lineage

| Tool | What it does |
|------|-------------|
| `ragamuffin_fact_get` | Retrieve a single fact by exact key. Returns value, confidence, TTL, status. |
| `ragamuffin_fact_put` | Write or update a fact with lifecycle fields (confidence, TTL, tags, source). |
| `ragamuffin_fact_list` | List facts by key, prefix, tag, or lifecycle status. Paginated. |
| `ragamuffin_fact_delete` | Delete a fact by key. Irreversible. |
| `ragamuffin_fact_graph` | Fact lineage: supersedes, refines, contradicts. BFS traversal with configurable depth. |
| `ragamuffin_fact_history` | Fact evolution timeline: creation, confirmation, updates across time. |
| `ragamuffin_fact_provenance` | Fact origin: source, source type, creation metadata, related chunks. |

### Knowledge Graph

| Tool | What it does |
|------|-------------|
| `ragamuffin_graph_entity` | Look up an entity by ID. Returns metadata and relations. |
| `ragamuffin_graph_edges` | Query entity relationships — filter by type, entity, or time. |
| `ragamuffin_graph_communities` | List Louvain-detected knowledge communities. Each community is a cluster of related entities. |
| `ragamuffin_links` | Link index: outbound links, backlinks, or the full link graph for a source file. |

### Quality & Review

| Tool | What it does |
|------|-------------|
| `ragamuffin_review` | List flagged facts (contradictions, low confidence, expiring) or resolve a single flag. |
| `ragamuffin_contradictions` | Find contradictory fact pairs surfaced by the pruner. |
| `ragamuffin_audit` | Vault health check: staleness, semantic conflicts, coverage gaps, duplicates. |

### Context & Discovery

| Tool | What it does |
|------|-------------|
| `ragamuffin_context_bundle` | Composite context: peer card + recent facts + recall in one call. Use at turn start to orient. |
| `ragamuffin_dialectic` | Multi-pass reasoning prompts: cold (analytical), warm (synthetic), hot (evaluative). Depth 1–3. |
| `ragamuffin_peer_list` | Discover other agents from `/peer/*/profile` fact keys. Returns vault names and peer cards. |
| `ragamuffin_briefing` | Vault activity digest for a time period (24h/7d/30d). Counts events by type. |
| `ragamuffin_changes` | Recent vault activity: new/updated facts and log events with timestamps. Time-filterable. |
| `ragamuffin_store` | Ingest content into the vault. The canonical write path. |
| `ragamuffin_draft` | Write a file to the vault (direct mode) or open a PR. |

### Session Management

| Tool | What it does |
|------|-------------|
| `ragamuffin_session_create` | Create a conversation session with optional auto-fact-extraction. |
| `ragamuffin_session_get` | Get session metadata and turn history. |
| `ragamuffin_session_list` | List active sessions by agent or vault. Paginated. |
| `ragamuffin_turn_append` | Append a turn (user/assistant) to an existing session. |

### Notifications

| Notification | What it does |
|-------------|-------------|
| `notifications/session_end` | Auto-finalizes a session: builds a structured summary, indexes it as a fact, extracts decision/conclusion facts from assistant turns, and marks the session finalized (idempotent). Send after the last turn with `session_id` and optional `vault`. |

### Retrieval

| Tool | What it does |
|------|-------------|
| `ragamuffin_get_chunk` | Retrieve a single chunk by ID (from recall results). Full text + metadata. |
| `ragamuffin_stats` | Operational metrics: file count, chunk count, fact count, vault age. |
| `ragamuffin_status` | Server health: checks Qdrant, embedder, LLM connectivity. Returns version + uptime. |

> **Backward compatibility:** The old combined `ragamuffin_facts` tool (operation:
> list\|upsert) is still dispatched. The new split tools (`fact_get`, `fact_put`,
> `fact_list`, `fact_delete`) are preferred — LLMs reason better with discrete
> tools than a Swiss-army-knife parameter.

---

## Vault Naming Convention

Agent vaults use the namespace prefix `agent::`:

| Agent | Vault name | Qdrant collection |
|-------|-----------|-------------------|
| dev | `agent::dev` | `agent::dev` |
| robot | `agent::robot` | `agent::robot` |
| scout | `agent::scout` | `agent::scout` |

The prefix is configurable — just be consistent within a deployment.

---

## Harness Lifecycle Mapping

The MCP layer handles **all tool dispatch**. The per-harness adapter shim
only handles lifecycle hooks that MCP can't express:

| Harness hook | Description | Shim implementation |
|-------------|-------------|-------------------|
| `initialize(session_id)` | Agent starts, provision vault | Connect to MCP. Call `ragamuffin_session_create` via MCP. |
| `prefetch(query)` | Recall context before next turn | Call `ragamuffin_recall` or `ragamuffin_context_bundle` via MCP. Cache results. |
| `sync_turn(user_msg, asst_msg)` | Persist the exchange | Build `ragamuffin_turn_append` call. |
| `on_session_end(messages)` | Session ended, persist summary + facts | Summarize messages. Call `ragamuffin_store` + `ragamuffin_fact_put` for decisions/conclusions. |
| `get_tool_schemas()` | Expose tools to agent | Return the MCP `tools/list` result directly, or a subset. |
| `handle_tool_call(name, args)` | Dispatch a tool invocation | Forward to MCP `tools/call`. |
| `shutdown()` | Clean disconnect | Close MCP SSE connection. |

---

## Per-Harness Configuration

Both adapters use the same config keys (environment variables, taking precedence
over config file):

```yaml
ragamuffin:
  endpoint: "http://ragamuffin:8000"
  mcp_endpoint: "http://ragamuffin:8000/mcp"   # if MCP-specific
  vault_prefix: "agent::"
  auth_token: "sk-..."                        # optional
  recall_mode: "hybrid"                       # hybrid | context | tools
  save_messages: true
  injection_frequency: "every_turn"           # every_turn | first_turn
  context_cadence: 3                          # refresh base context every N turns
  dialectic_cadence: 5                        # refresh dialectic every N turns
```

---

## Agent Identity

The harness adapter needs:
1. **Agent identifier** — the agent's name, ID, or profile name
2. **Vault prefix** — configurable, defaults to `agent::`
3. **Resulting vault name** = `{prefix}{agent_id}`, e.g. `agent::dev`

Peer cards provide agent identity across harnesses. Use `ragamuffin_fact_put`
with key `peer/{agent_id}/card/profile` to set a card, and `ragamuffin_peer_list`
to discover other agents.

---

## Auth Integration

If authentication is enabled (`RAGAMUFFIN_AUTH_MODE=api_key` or `=jwt`):

**REST calls:** Include `Authorization: Bearer <key>` header.

**MCP calls:** Include the same header. The MCP transport passes it via the
SSE connection and JSON-RPC requests.

---

## Error Handling

All MCP tool calls return JSON-RPC errors with these codes:

| JSON-RPC code | Cause | Adapter action |
|--------------|-------|---------------|
| `-32602` (Invalid params) | Missing or invalid arguments | Log and abort — programming error in the adapter |
| `-32603` (Internal error) | Ragamuffin backend issue (Qdrant down, LLM fail) | Retry with backoff |
| Connection failure | Ragamuffin unreachable | Fail open — agent continues without memory |

**Fail-open principle:** If Ragamuffin is unreachable, the agent should still
operate. The adapter logs the failure and returns empty context / silently
skips persistence.

---

## Reference Implementations

- **OpenClaw plugin:** `plugins/memory-ragamuffin-openclaw/` (Node.js, ~250 lines)
- **Hermes adapter:** `adapters/hermes-memory/` (Python, ~200 lines)
- **MCP tools:** `internal/server/mcp_handlers.go` (Go, ~1500 lines)

The MCP handler is the canonical reference — it implements all 33 tools and
demonstrates the correct dispatch pattern for every endpoint.

---

## REST API (Underlying Transport)

The MCP tools call the same internal handlers as the REST API. If you need
to bypass MCP (unsupported language, custom load balancer, existing REST
integration), the full REST surface is documented in the codebase:

| Category | Endpoints |
|----------|-----------|
| Search | `POST /recall`, `POST /vault/{name}/recall`, `POST /v1/batch/recall` |
| Synthesis | `POST /ask`, `POST /vault/{name}/ask` |
| Facts | `GET/POST/PUT/DELETE /v1/facts`, `/v1/facts/{key}/graph\|history\|provenance` |
| Graph | `GET /v1/graph/entity/*`, `/v1/graph/edges`, `/v1/graph/communities` |
| Review | `GET /v1/review`, `POST /v1/review/{id}/resolve` |
| Vault | `POST /v1/vaults`, `GET /health`, `GET /stats`, `GET /v1/briefing` |
| Sessions | `POST /v1/sessions`, `POST /v1/turns`, `POST /v1/ingest` |

For new integrations, **start with MCP**. It's the maintained integration
surface. The REST endpoints are stable but not the primary contract.
