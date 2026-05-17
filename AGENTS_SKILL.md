# Agent Skill: Ragamuffin Vault

Know an agent that needs to read, search, and write to a knowledge base?
Ragamuffin is a REST API that turns a directory of files into a queryable
vector store — and in v0.5, it's also the memory backend that powers your
agent's persistent, isolated cross-session recall.

Two modes:
- **Vault mode** — point at a directory, search with natural language
- **Agent memory mode** — the harness plugin handles everything; you just use
  `memory_search`/`memory_get` as normal, and Ragamuffin is the backend

## Quickstart

```bash
# Discovery — check the vault is alive
curl -s http://ragamuffin:8000/health | jq .

# Quick info
curl -s http://ragamuffin:8000/stats | jq .
```

## Agent Memory Mode (v0.5)

When your harness is configured with the `memory-ragamuffin` plugin, you
don't need to curl any Ragamuffin endpoints yourself. The harness does it
for you:

### What happens automatically

| Event | Harness action |
|---|---|
| Agent starts | `POST /v1/vaults` — creates/confirms your vault `agent::<your_name>` |
| Before each turn | `POST /v1/recall` — searches your vault for relevant context |
| After each turn | `POST /v1/ingest` — saves the turn to your vault |
| Session ends | `POST /v1/ingest` — saves a session summary |

Your memory tools (`memory_search`, `memory_get`, etc.) work exactly as before —
they're just backed by Ragamuffin now instead of file-based storage.

### Cross-agent recall — `agent_recall`

If your harness exposes a privileged `agent_recall` tool, you can query another
agent's memory:

```
# What has robot been working on?
agent_recall(vault="agent::robot", query="what issues have been identified?", limit=3)
```

This is a **privileged** tool — the harness controls whether your agent can
access other vaults. It's not something you can curl (even if you knew the
endpoint), because the harness authenticates and authorizes the request.

### What you should still do directly

- Write structured facts → `POST /v1/facts` (for small, persistent data)
- Write log entries → `POST /v1/logs` (for what you did, when)
- Read shared vaults → `POST /recall` with `source_filter`

---

## Endpoints (Agent-Friendly Cheat Sheet)

### Semantic Search — `/recall`

Ask natural-language questions. Get back ranked chunks with source paths.

```bash
curl -s http://ragamuffin:8000/recall \
  -H "Content-Type: application/json" \
  -d '{"query": "What is our deployment process?", "top_k": 5}'
```

```json
{
  "results": [
    {"text": "Deployment uses GitHub Actions...", "source_file": "ops/deploy.md", "score": 0.89}
  ],
  "top_score": 0.89
}
```

`top_k` max is 100. `score_threshold` (0.0–1.0) filters by minimum similarity.
`source_filter` restricts results to files under a path prefix.

### Synthesized Answers — `/ask`

For questions that span multiple files. Requires LLM configured.

```bash
curl -s http://ragamuffin:8000/ask \
  -H "Content-Type: application/json" \
  -d '{"query": "Summarize our infrastructure", "mode": "auto"}'
```

Modes: `rag` (RAG only), `auto` (RAG, fallback to full if low confidence),
`full` (load entire source files for context).

### Write-Back — `/draft`

Agents contribute to the vault. Two modes:

**Direct** — writes immediately to the filesystem:
```bash
curl -s http://ragamuffin:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title": "New Notes", "content": "# Notes\n...", "target_path": "ops/notes.md"}'
```

**PR** — opens a pull request (requires git config):
```bash
curl -s http://ragamuffin:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title": "Update schema", "content": "...", "target_path": "ops/schema.md", "mode": "pr"}'
```

**Delete** — set `"delete": true` to remove a file:
```bash
curl -s http://ragamuffin:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title": "Remove", "target_path": "ops/old.md", "delete": true}'
```

### Structured Facts — `/v1/facts`

Use these for small, structured pieces of knowledge that don't belong
in a markdown file (e.g. connection strings, URLs, configurations).

**Upsert:**
```bash
curl -s -X POST http://ragamuffin:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"key": "db/url", "value": "postgres://...", "tags": ["prod"]}'
```

**Retrieve by exact key:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?key=db/url"
```

**Search by text fragment (substring match on key):**
```bash
curl -s "http://ragamuffin:8000/v1/facts?prefix=db/"
```

**Filter by tag:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?tag=prod&prefix=db/"
```

**Delete:**
```bash
curl -s -X DELETE "http://ragamuffin:8000/v1/facts?key=db/url"
```

**Pagination:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?limit=20"
curl -s "http://ragamuffin:8000/v1/facts?limit=20&before=<next_token>"
```

### Structured Logging — `/v1/logs`

Append-only log stream. Every entry has an agent, type, body, optional
tags and timestamp. Great for agents recording what they did.

**Append:**
```bash
curl -s -X POST http://ragamuffin:8000/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"agent": "scout", "type": "scan", "body": "Found 3 npm vulns", "tags": ["security"]}'
```

**Query with filters:**
```bash
curl -s "http://ragamuffin:8000/v1/logs?agent=scout&type=scan&tag=security&limit=10"
```

### Snapshot — `/v1/snapshot`

Download the entire vault as a gzipped tarball:
```bash
curl -s -O http://ragamuffin:8000/v1/snapshot
```

### Audit — `/audit`

Check vault health:
```bash
curl -s http://ragamuffin:8000/audit \
  -H "Content-Type: application/json" \
  -d '{}'
```

Returns stale files, semantic conflicts (requires LLM), gaps, and duplicates.

## Agent Workflow Patterns

### Before answering a question

1. Check `/stats` to see what's indexed
2. Use `/recall` with the question as query to find relevant chunks
3. For complex questions, use `/ask` to synthesize

### After learning something new

1. For prose (markdown docs) → use `/draft` with `mode: "direct"`
2. For structured data (configs, URLs) → use POST `/v1/facts`
3. For a record of what you did → use POST `/v1/logs`

### Periodic maintenance

1. Call `/audit` to check for stale files
2. Call `/v1/snapshot` to back up the vault
3. For git-backed vaults, use `/draft` with `mode: "pr"` for human review

## v0.4 Endpoints

### Multi-Tenant Vault Routing

In multi-tenant mode (set `RAGAMUFFIN_VAULTS`), all content endpoints are
prefixed with `/vault/{name}/`:

```bash
curl -s 'http://ragamuffin:8000/vault/docs/recall?query=deploy'
curl -s 'http://ragamuffin:8000/vault/docs/audit'
```

Available vault-prefixed endpoints:
- `/vault/{name}/recall` — semantic search in a specific vault
- `/vault/{name}/ask` — synthesized answer from a specific vault
- `/vault/{name}/draft` — write files to a specific vault
- `/vault/{name}/audit` — check health of a specific vault
- `/vault/{name}/v1/facts` — structured facts per vault
- `/vault/{name}/v1/logs` — log entries per vault
- `/vault/{name}/v1/snapshot` — download a vault as tarball
- `/vault/{name}/reindex` — trigger re-index of a vault
- `/vault/{name}/graph` — knowledge graph for a vault

### Vault Discovery

```bash
curl -s http://ragamuffin:8000/vaults | jq .
# Returns list of vaults with name, status, and file counts
```

### Knowledge Graph

```bash
# Full graph
curl -s http://ragamuffin:8000/graph | jq .

# Entity-focused with traversal depth
curl -s 'http://ragamuffin:8000/graph?entity=Qdrant&depth=2'
```

Returns nodes (files and entities) and edges (contains, links_to).

### Reindex

```bash
curl -s -X POST http://ragamuffin:8000/reindex
# Returns immediately — reindex runs asynchronously
```

For multi-tenant mode:
```bash
curl -s -X POST http://ragamuffin:8000/vault/docs/reindex
```

### Authentication

Set `RAGAMUFFIN_AUTH_MODE` to one of:
- `none` — no auth (default)
- `api_key` — static keys via `RAGAMUFFIN_AUTH_READ_KEY` / `RAGAMUFFIN_AUTH_WRITE_KEY`
- `jwt` — JWT with JWKS verification

When auth is enabled, agents must include an `Authorization: Bearer <key>` header.

### Web UI

A web dashboard is served at the root path:
- `GET /` — SPA dashboard with Search, Browse, Audit, Graph pages
- Search, Browse, Audit, and Graph pages all functional via REST API calls

## Configuration for Agents

The following env vars are available to agents composing Ragamuffin deployment:

```yaml
RAGAMUFFIN_VAULT_PATH=/opt/vault                # Where your knowledge lives
RAGAMUFFIN_QDRANT_URL=http://qdrant:6334            # Vector DB (gRPC port)
RAGAMUFFIN_EMBEDDING_API_KEY=sk-...                 # For embedding text into vectors
RAGAMUFFIN_LLM_API_KEY=sk-...                       # For /ask and audit (optional)
RAGAMUFFIN_LLM_MODEL=deepseek-v4-flash              # Which model to use
RAGAMUFFIN_LLM_BASE_URL=https://api.deepseek.com    # LLM API base URL (omit /v1)
RAGAMUFFIN_EMBEDDING_BASE_URL=https://api.openai.com/v1  # Embedding base URL (include /v1)
# URL convention: LLM appends /v1/chat/completions, embedding appends /embeddings
# LiteLLM proxy (http://litellm:4000):
#   LLM_BASE_URL=http://litellm:4000  EMBEDDING_BASE_URL=http://litellm:4000/v1
RAGAMUFFIN_LLM_TIMEOUT=120s                    # LLM request timeout (optional)
RAGAMUFFIN_GIT_TOKEN=ghp_...                        # For PR mode (optional)

# v0.4 Events — subscribe to vault changes via CloudEvents v1.0 webhook
# When set, Ragamuffin emits vault.file.changed, vault.file.deleted, and
# ragamuffin.started events as HTTP POST with Content-Type: application/cloudevents+json
RAGAMUFFIN_EVENT_WEBHOOK_URL=http://listener:8080/events

# v0.4 Multi-tenancy & Auth
RAGAMUFFIN_VAULTS=docs:/path/to/docs,code:/path/to/code   # Multi-tenant vaults
RAGAMUFFIN_AUTH_MODE=none                                   # none | api_key | jwt
RAGAMUFFIN_AUTH_READ_KEY=sk-...                             # Global read key
RAGAMUFFIN_AUTH_WRITE_KEY=sk-...                            # Global write key
RAGAMUFFIN_AUTH_JWT_ISSUER=https://auth.example.com         # JWT issuer
RAGAMUFFIN_AUTH_JWT_AUDIENCE=ragamuffin                     # Expected audience
RAGAMUFFIN_AUTH_JWT_JWKS_URL=https://auth.example.com/.well-known/jwks.json  # JWKS endpoint
```

## Error Handling

All errors return:

```json
{"error": true, "code": "ERROR_CODE", "message": "Human-readable description"}
```

Common codes: `INVALID_INPUT`, `RATE_LIMITED` (with `Retry-After` header),
`NOT_FOUND`, `LLM_NOT_CONFIGURED`, `GIT_NOT_CONFIGURED`.

## Rate Limit Guidance

If your agent gets a `429` response, check the `Retry-After` header and
wait before retrying. Adjust rate limit env vars per-endpoint if needed
(see README.md for defaults and env vars).

## Discovery

```bash
# What version is running?
curl -s http://ragamuffin:8000/version
# Returns version, commit, build date, Go version (requires ldflags at build time)

# Is the server healthy and Qdrant reachable?
curl -s http://ragamuffin:8000/health

# How many files and chunks are indexed?
curl -s http://ragamuffin:8000/stats

# What endpoints are available?
# Ragamuffin doesn't have a discovery endpoint — this document IS the discovery.
# The full API surface is: /recall /ask /draft /audit /v1/facts /v1/logs
# /v1/snapshot /health /stats /version /metrics /mcp
# /vaults /graph /reindex /vault/{name}/... (v0.4)
```

## Events (CloudEvents v0.4)

When `RAGAMUFFIN_EVENT_WEBHOOK_URL` is configured, Ragamuffin pushes
CloudEvents v1.0 structured JSON events to the webhook URL. Use this to
react to vault changes without polling.

| Event | Payload | When |
|---|---|---|
| `vault.file.changed` | `{"path":"...", "action":"created|modified"}` | After successful file index |
| `vault.file.deleted` | `{"path":"..."}` | After file removed from index |
| `ragamuffin.started` | `{"version":"...", "commit":"...", "host":"...", "port":"..."}` | Server boot, before listen |

Delivery is HTTP POST with `Content-Type: application/cloudevents+json`.
Fire-and-forget — no retry, no persistence. Message consumers are responsible
for durability.
