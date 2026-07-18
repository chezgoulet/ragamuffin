# Ragamuffin MCP Tool Manifest — Specification

**Status:** Superseded — MCP tool surface now has 33 tools (see `internal/server/mcp_handlers.go` or `docs/integration/memory-provider-api.md`). This spec describes the original 8-tool v0.5 design.
**Spec version:** 2026-05-22
**REST foundation:** v0.4 (multi-tenant, auth, graph)

This document defines the complete Model Context Protocol (MCP) tool surface
for Ragamuffin. It is the contract that all Tier 1 (REST/MCP) clients depend
on — LibreFang, Claude Code, n8n, shell scripts, and any future HTTP-speaking
client.

---

## 1. Design Principles

1. **REST is the foundation.** Every MCP tool wraps an existing REST endpoint.
   No capability is available via MCP that isn't also available via curl. The
   REST API is stable and versioned; MCP is a thin transport adaptation.

2. **One tool per verb.** Tools map to Ragamuffin's four verbs (Read,
   Understand, Write, Audit) plus two auxiliary verbs (Explore, Monitor).
   No tool does two things.

3. **Vault routing is explicit.** Multi-tenant deployments pass a `vault`
   parameter to target the correct vault. Single-tenant deployments can
   omit it. The server resolves the default.

4. **JSON-RPC 2.0 over SSE.** Conforms to MCP spec 2024-11-05. The server
   exposes `GET /mcp` (SSE stream) and `POST /mcp` (JSON-RPC requests).
   All tools are stateless — the SSE connection provides notifications and
   streaming, not session state.

5. **Graceful degradation.** Tools that depend on optional infrastructure
   (LLM for `ask`, Qdrant for `facts`) return descriptive errors rather
   than crashing. Clients should handle `"error"` responses gracefully.

---

## 2. Tool Manifest

### 2.1 `ragamuffin_recall` — Semantic Search (Read verb)

Wraps `POST /recall`.

**Description:** Semantic search across the vault. Returns ranked chunks
with source paths, scores, and timestamps. The universal entry point —
every agent queries here before calling its LLM.

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | yes | — | Natural-language search query |
| `vault` | string | no | default | Target vault name (multi-tenant) |
| `top_k` | integer | no | 10 | Max results (1–100) |
| `score_threshold` | number | no | 0.0 | Minimum similarity score (0.0–1.0) |
| `source_filter` | string | no | — | Restrict to files under this path prefix |

**Output:**

```json
{
  "results": [
    {
      "score": 0.89,
      "text": "...",
      "source_file": "03-knowledge-base/internal/policy.md",
      "header": "## Deployment Policy",
      "chunk_index": 3,
      "file_last_updated": "2026-04-12T10:30:00Z"
    }
  ],
  "top_score": 0.89
}
```

**curl test:**
```bash
curl -s http://ragamuffin:8000/recall \
  -d '{"query":"deployment policy","top_k":5}'
```

---

### 2.2 `ragamuffin_ask` — LLM Synthesis (Understand verb)

Wraps `POST /ask`.

**Description:** Full-context synthesis via LLM. Retrieves relevant chunks
for the query, feeds them as context to a configured LLM, and returns a
prose answer with source citations. Falls back to structured recall results
if no LLM is configured.

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | yes | — | The question to answer |
| `vault` | string | no | default | Target vault name (multi-tenant) |
| `mode` | string | no | `"auto"` | Retrieval mode: `"auto"`, `"rag"`, or `"full"` |
| `top_k` | integer | no | 8 | RAG results to retrieve (1–50) |

**Output:**

```json
{
  "answer": "The deployment policy requires all services to run in Docker containers with health checks...",
  "sources": ["03-knowledge-base/internal/policy.md#L42", "03-knowledge-base/internal/deploy.md#L15"],
  "mode_used": "rag"
}
```

**curl test:**
```bash
curl -s http://ragamuffin:8000/ask \
  -d '{"query":"What is our deployment policy?"}'
```

---

### 2.3 `ragamuffin_store` — Structured Ingest (Write verb)

Wraps `POST /v1/ingest`.

**Description:** Write structured content into the vault. The canonical
Tier 1 write path — agents contribute knowledge, session summaries,
observations, and annotations without going through the filesystem. Content
is chunked and indexed immediately.

This is the primary write tool for LibreFang agents, n8n workflow outputs,
and Claude Code session results. For file-level writes (multi-file PRs,
editing existing vault files), use `ragamuffin_draft` instead.

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `content` | string | yes | — | Text content to ingest (markdown, plain text) |
| `source` | string | yes | — | Origin identifier (agent name, workflow ID, file path) |
| `vault` | string | no | default | Target vault name (multi-tenant) |
| `tags` | array[string] | no | [] | Optional tags for filtering and discovery |

**Output:**

```json
{
  "status": "indexed",
  "vault": "default",
  "source": "librefang::news-digest",
  "chunk_count": 3
}
```

**curl test:**
```bash
curl -s http://ragamuffin:8000/v1/ingest \
  -d '{"content":"# Daily Digest\n\nThe team finalized the deployment policy...", "source":"librefang::news-digest", "tags":["daily","digest"]}'
```

**Design note — write-back governance:**

The caller is responsible for choosing the write path:
- **`ragamuffin_store`** (MCP) / `POST /v1/ingest` (REST) — for agent-generated
  signals, session results, and structured observations. Bypasses the file
  watcher. Content is chunked and indexed immediately.
- **`ragamuffin_draft`** (MCP) / `POST /draft` (REST) — for file-level writes
  to the vault filesystem. Respects the file watcher lifecycle. Optional PR
  mode for human review.

The file watcher is for human-maintained vault files (runbooks, policies,
documentation). Agent-generated signals use the direct ingest path.

---

### 2.4 `ragamuffin_draft` — File Write & PR (Write verb)

Wraps `POST /draft`.

**Description:** Write a file to the vault filesystem or open a pull request.
Direct mode writes immediately (useful for scaffolding and templates). PR mode
opens a GitHub pull request for human review (useful when the vault is version-
controlled and changes need review).

For structured agent signals (session summaries, observations, annotations)
that don't need to be filesystem files, use `ragamuffin_store` instead.

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `title` | string | yes | — | PR title or file description |
| `target_path` | string | yes | — | Vault path relative to vault root |
| `content` | string | no* | — | File content to write. Required unless `delete=true`. |
| `mode` | string | no | `"direct"` | `"direct"` or `"pr"` |
| `vault` | string | no | default | Target vault name (multi-tenant) |
| `description` | string | no | — | Optional PR body (PR mode only) |
| `delete` | boolean | no | false | Delete the file instead of writing |

**Output (direct mode):**

```json
{
  "mode": "direct",
  "path": "docs/deployment-policy.md",
  "written": true
}
```

**Output (PR mode):**

```json
{
  "mode": "pr",
  "pr_url": "https://github.com/chezgoulet/vault/pull/42",
  "branch": "ragamuffin-draft/docs-deployment-policy"
}
```

**curl test:**
```bash
# Direct write
curl -s http://ragamuffin:8000/draft \
  -d '{"title":"Add deployment policy","target_path":"docs/deploy.md","content":"# Deployment Policy\n...","mode":"direct"}'

# PR mode
curl -s http://ragamuffin:8000/draft \
  -d '{"title":"Add deployment policy","target_path":"docs/deploy.md","content":"# Deployment Policy\n...","mode":"pr"}'
```

---

### 2.5 `ragamuffin_facts` — Fact Management (Write / Read)

Wraps `GET /v1/facts` and `POST /v1/facts`.

**Description:** Read or write structured key-value facts. Facts are separate
from vault chunks — they live in a dedicated Qdrant collection with lifecycle
fields (confidence, source, TTL, status, supersession). Agents use facts for
decisions, conventions, and learned knowledge that should outlive a session.

Two operations:
- **`list`** — query facts by key, prefix, tag, or status
- **`upsert`** — create or update a fact by key

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `operation` | string | yes | — | `"list"` or `"upsert"` |
| `key` | string | conditional | — | Fact key. Required for both operations. Example: `"org/prefer-rust-cli"` |
| `value` | string | conditional | — | Fact value. Required for `upsert`. |
| `vault` | string | no | default | Target vault name (multi-tenant) |
| `tags` | array[string] | no | — | Tags for filtering (upsert) |
| `prefix` | string | no | — | Key prefix filter (list only) |
| `tag` | string | no | — | Tag filter (list only) |
| `status` | string | no | — | Lifecycle status filter: `"active"`, `"needs_review"`, `"superseded"`, `"rejected"` (list only) |
| `source` | string | no | — | Origin reference (upsert) |
| `source_type` | string | no | `"manual"` | `"manual"`, `"pr_discussion"`, `"agent_observation"`, `"file"`, `"conversation"`, `"code_review"`, `"automated"` (upsert) |
| `confidence` | number | no | 1.0 | How sure are we? 0.0–1.0 (upsert) |
| `ttl_days` | integer | no | 0 | Days until auto-expiry. 0 = never. (upsert) |

**Output (list):**

```json
{
  "facts": [
    {
      "key": "org/prefer-rust-cli",
      "value": "Prefer Rust for new CLI tools",
      "tags": ["language", "tooling"],
      "confidence": 0.85,
      "status": "active",
      "source": "pr:42#discussion-r123",
      "source_type": "pr_discussion",
      "supersedes": "",
      "contradicts": [],
      "confirmation_count": 3,
      "created_at": "2026-01-20T14:00:00Z",
      "updated_at": "2026-04-15T09:30:00Z"
    }
  ]
}
```

**Output (upsert):**

```json
{
  "key": "org/prefer-rust-cli",
  "value": "Prefer Rust for new CLI tools",
  "status": "active",
  "created": false
}
```

The `created` field indicates whether this was a new fact (`true`) or an
update to an existing fact (`false`).

**curl test:**
```bash
# List active facts
curl -s 'http://ragamuffin:8000/v1/facts?status=active'

# Upsert a fact
curl -s http://ragamuffin:8000/v1/facts \
  -d '{"key":"org/prefer-rust-cli","value":"Prefer Rust for new CLI tools","tags":["language","tooling"],"confidence":0.85}'
```

---

### 2.6 `ragamuffin_audit` — Vault Health Check (Audit verb)

Wraps `POST /audit`.

**Description:** Vault health self-test. Scans for staleness, semantic
conflicts, gaps, and duplicates. Agents can trigger on a schedule and
forward results to notification channels. The vault tests itself.

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `vault` | string | no | default | Target vault name (multi-tenant) |
| `stale_days` | integer | no | 90 | Days since last update to flag as stale |
| `checks` | array[string] | no | all | Which checks to run: `"stale"`, `"semantic_conflict"`, `"gap"`, `"duplicate"` |
| `sample_size` | integer | no | 50 | Chunk pairs to LLM-compare (1–200) |

**Output:**

```json
{
  "checks_run": ["stale", "semantic_conflict", "gap", "duplicate"],
  "stale_files": [{"path": "...", "days_since_update": 95}],
  "semantic_conflicts": [...],
  "semantic_conflict_llm_calls": 25,
  "gaps": [{"directory": "...", "reason": "empty"}],
  "duplicates": [{"path": "...", "duplicate_of": "...", "similarity": 0.97}]
}
```

**curl test:**
```bash
curl -s http://ragamuffin:8000/audit \
  -d '{"stale_days": 60, "checks": ["stale", "gap"]}'
```

---

### 2.7 `ragamuffin_graph` — Knowledge Graph (Explore verb)

Wraps `GET /graph`.

**Description:** Entity and link graph from the vault. Returns node-relationship
data showing which entities co-occur, which files reference each other, and how
knowledge clusters. Useful for agents that need to understand the *structure* of
knowledge — not just search it.

Clients that can render graphs (Claude Code with graphviz, web UIs) can
visualize the vault's entity topology directly from this output.

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `vault` | string | no | default | Target vault name (multi-tenant) |
| `entity` | string | no | — | Focus on a specific entity (BFS traversal from this entity) |
| `depth` | integer | no | 2 | BFS traversal depth (1–5). Ignored if `entity` is empty. |
| `limit` | integer | no | 100 | Max nodes to return (1–500) |
| `min_confidence` | number | no | 0.0 | Minimum entity co-occurrence confidence (0.0–1.0) |

**Output:**

```json
{
  "nodes": [
    {"id": "entity:ragamuffin", "type": "entity", "label": "Ragamuffin", "entity_type": "project"},
    {"id": "file:internal/server/server.go", "type": "file", "label": "server.go"},
    {"id": "entity:qdrant", "type": "entity", "label": "Qdrant", "entity_type": "service"}
  ],
  "edges": [
    {"source": "entity:ragamuffin", "target": "file:internal/server/server.go", "relationship": "implemented_in"},
    {"source": "entity:ragamuffin", "target": "entity:qdrant", "relationship": "depends_on"}
  ]
}
```

**curl test:**
```bash
# Full graph
curl -s http://ragamuffin:8000/graph

# Focused BFS from an entity
curl -s 'http://ragamuffin:8000/graph?entity=ragamuffin&depth=2'
```

---

### 2.8 `ragamuffin_stats` — Operational Health (Monitor verb)

Wraps `GET /stats`.

**Description:** Operational metrics for the vault. Returns file counts,
chunk counts, fact counts, embedding status, and vault age. Useful for
monitoring agents (LibreFang, n8n) that track vault health over time.

**Input Schema:**

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `vault` | string | no | default | Target vault name (multi-tenant) |

**Output:**

```json
{
  "vault": "default",
  "indexed_files": 142,
  "total_chunks": 1847,
  "total_facts": 89,
  "vault_age_days": 120,
  "oldest_file_days": 120,
  "newest_file_days": 0
}
```

**curl test:**
```bash
curl -s http://ragamuffin:8000/stats
```

---

## 3. Error Handling

All errors follow JSON-RPC 2.0 error codes:

| Code | Name | When |
|---|---|---|
| `-32700` | Parse Error | Invalid JSON in request body |
| `-32600` | Invalid Request | Missing or invalid `jsonrpc` field |
| `-32601` | Method Not Found | Unknown tool name |
| `-32602` | Invalid Params | Missing required parameter, wrong type |
| `-32603` | Internal Error | Server-side failure (embedding, Qdrant, LLM) |
| `-32000` | Auth Error | Invalid or missing API key |
| `-32001` | Vault Not Found | Specified vault does not exist |

Error responses:

```json
{
  "jsonrpc": "2.0",
  "id": "req-1",
  "error": {
    "code": -32602,
    "message": "Invalid params: 'query' is required"
  }
}
```

---

## 4. Vault Routing

In single-tenant mode, the `vault` parameter is ignored — all operations
target the single configured vault.

In multi-tenant mode, the `vault` parameter is optional. If omitted, the
server uses the default vault (the first vault in configuration). Clients
are encouraged to always pass `vault` explicitly for clarity:

```json
{
  "vault": "agent::dev",
  "query": "what happened last session"
}
```

---

## 5. Auth & Security

MCP inherits Ragamuffin's auth model:

- **Mode `none`:** No auth required. Suitable for trusted Tailscale
  networks behind a reverse proxy.
- **Mode `api_key`:** Pass `Authorization: Bearer <key>` as an HTTP header.
  Read-only keys can call `recall`, `ask`, `facts` (list), `graph`, `stats`.
  Write keys can also call `store`, `draft`, `facts` (upsert), `audit`.
- **Mode `jwt`:** Validate a JWT from the configured issuer. Claims carry
  vault access scopes.

In MCP, auth headers are passed as HTTP headers on the initial `POST /mcp`
connection. The SSE stream inherits the auth context from the HTTP request
that established it.

---

## 6. Client Integration Guide

### Claude Code

```bash
claude --mcp-server ragamuffin=http://ragamuffin:8000/mcp
```

Claude discovers the full tool manifest on connect. All eight tools are
available via `claude --mcp-server`. Recommended prompt template for
sessions:

```
Before answering any question about the cooperative's operations, tools, or
policies, query Ragamuffin's vault using ragamuffin_recall or ragamuffin_ask.
Cite sources in your answer.
```

### LibreFang

Define a Hand configuration (`HAND.toml`) for Ragamuffin tools:

```toml
[[tools]]
name = "ragamuffin_recall"
url = "http://ragamuffin:8000/mcp"
transport = "streamable-http"
[[tools]]
name = "ragamuffin_store"
url = "http://ragamuffin:8000/mcp"
transport = "streamable-http"
```

Recommended agent pattern:

1. Start: query vault via `ragamuffin_recall` to establish context
2. Reason: process with LLM using vault context
3. End: write synthesis back via `ragamuffin_store`

### n8n

Use the HTTP Request node with `POST http://ragamuffin:8000/v1/ingest` for
direct REST calls, or the MCP node if n8n's MCP integration supports tool
discovery.

Recommended n8n pattern: any workflow that produces structured output
(nightly digest, invoice summary, telemetry report) ends with a
Ragamuffin `POST /v1/ingest` call that writes the result to the shared vault.

---

## 7. Tool Surface Summary

| Tool | REST Endpoint | Verb | MCP Status |
|---|---|---|---|
| `ragamuffin_recall` | `POST /recall` | Read | ✅ Existing |
| `ragamuffin_ask` | `POST /ask` | Understand | ✅ Existing |
| `ragamuffin_store` | `POST /v1/ingest` | Write | ❌ Gap — spec only |
| `ragamuffin_draft` | `POST /draft` | Write | ✅ Existing |
| `ragamuffin_facts` | `GET/POST /v1/facts` | Write/Read | ❌ Gap — spec only |
| `ragamuffin_audit` | `POST /audit` | Audit | ✅ Existing |
| `ragamuffin_graph` | `GET /graph` | Explore | ❌ Gap — spec only |
| `ragamuffin_stats` | `GET /stats` | Monitor | ❌ Gap — spec only |

---

## 8. Implementation Notes

### Tool Registration (Go)

The MCP handler in `internal/mcp/mcp.go` supports the manifest format. Each
tool definition maps to a `mcp.ToolDefinition`. The dispatch function in
`internal/server/mcp_handlers.go` routes tool calls to handler methods.

When implementing the gap tools (`store`, `facts`, `graph`, `stats`), follow
the existing pattern in `mcp_handlers.go`:
- Add a `ToolDefinition` entry in `mcpTools()`
- Add a case in the `switch` in `mcpDispatch()`
- Write a handler method using the same `ctx` context, vault resolution, and
  Qdrant clients as the REST handlers

### Non-Goals

This spec does not define:
- The SSE transport protocol (inherits from MCP spec 2024-11-05)
- Streaming responses for long-running tools (deferred to future spec)
- Resource templates or prompts (MCP additions not yet scoped for Ragamuffin)
- Tool versioning (tools are backward-compatible within v0.x; breaking changes
  require a new major version)
