# Agent Skill: Ragamuffin

Ragamuffin is a REST API that turns a directory of files into a queryable
vector store — and in v0.6, it's also the memory backend that powers your
agent's persistent, isolated cross-session recall.

Two modes:
- **Vault mode** — point at a directory, search with natural language
- **Agent memory mode** — the harness plugin handles everything; you just use
  `memory_search`/`memory_get` as normal, and Ragamuffin is the backend

## Quickstart

```bash
# Discovery — check the service is alive
curl -s http://ragamuffin:8000/health | jq .

# Quick info
curl -s http://ragamuffin:8000/stats | jq .

# Search the knowledge base
curl -s http://ragamuffin:8000/recall \
  -H "Content-Type: application/json" \
  -d '{"query":"deployment process","top_k":5}'
```

### Repo Workflow

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full CI pipeline
architecture. The key points for agents:

- **Branch from `testing`** — all feature branches (`dev/*`) branch from
  and PR into `testing`. Never PR directly to `main`.
- **CI checks** — every PR triggers `pr-check.yml` (compile, test, vet).
  Must pass before merge.
- **Tag semantics** — `chezgoulet/ragamuffin:rolling` tracks `testing`
  (pre-release), `:latest` tracks `main` (production, benchmarked).
- **Review** — robot reviews PRs against `testing`, not `main`.

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
| Session ends | `POST /v1/ingest` — saves a session summary; also auto-extracts decisions/conclusions/config/preferences as deduplicated facts (`POST /v1/facts`) |

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
- Search shared knowledge → `POST /recall` with `source_filter`
- Create and manage sessions → `POST /v1/sessions`

### Hybrid mode: don't swap your slot, just add tools

If your operator wants to keep claudemem or Honcho as the harness memory slot,
you can still use Ragamuffin directly through two additional tools the operator
gives you:

| Tool | What it does | When to use it |
|---|---|---|
| `ragamuffin_store(vault, id, text, metadata)` | Write a fact or conclusion to your agent vault | After reaching an important conclusion, making a decision, or learning something cross-agent useful |
| `ragamuffin_recall(vault, query, limit)` | Search any agent's vault for relevant context | When you need to know what another agent has been doing, or recall something you stored earlier |

Your harness slot (claudemem / Honcho) still handles turn-by-turn memory
automatically. You use the Ragamuffin tools **selectively** — only for things
worth sharing across harness boundaries or persisting beyond the session.

```
# You (dev agent): reach a design decision
# Store it so robot can reference it later
ragamuffin_store(
  vault="agent::dev",
  id="decision-qdrant-isolation-2026-05-17",
  text="Decision: use physical Qdrant collection isolation per agent, not metadata filters. Source: Reddit post on multi-tenant AI vector store risks.",
  metadata={"type": "decision", "topic": "infrastructure"}
)

# Later, robot agent: what did dev decide?
ragamuffin_recall(
  vault="agent::dev",
  query="Qdrant isolation decision",
  limit=3
)
```

**Don't store every turn** — your slot already handles that. Only store what
you'd want another agent to find. Quality over quantity.

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

| Parameter | Type | Default | Description |
|---|---|---|---|
| `query` | string | — | Natural-language search query |
| `top_k` | int | 10 | Max results (1–100) |
| `score_threshold` | float | 0.0 | Minimum similarity (0.0–1.0) |
| `source_filter` | string | — | Restrict to files under this path prefix |
| `mode` | string | `auto` | `auto` (classify), `rag` (RAG-only), `full` (load full source) |
| `time_filter` | string | `active` | `active`, `active_at:<RFC3339>`, or `all` |

### Synthesized Answers — `/ask`

For questions that span multiple files. Requires LLM configured.

```bash
curl -s http://ragamuffin:8000/ask \
  -H "Content-Type: application/json" \
  -d '{"query": "Summarize our infrastructure", "mode": "auto"}'
```

| Parameter | Type | Default | Description |
|---|---|---|---|
| `query` | string | — | Question to answer |
| `mode` | string | `auto` | `rag`, `auto`, or `full` |
| `top_k` | int | 8 | RAG results to retrieve (1–50) |
| `time_filter` | string | `active` | `active`, `active_at:<RFC3339>`, or `all` |

### Write-Back — `/draft`

Agents contribute to the knowledge base. Two modes:

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
curl -s "http://ragamuffin:8000/v1/facts?key=***"
```

**Search by text fragment (substring match on key):**
```bash
curl -s "http://ragamuffin:8000/v1/facts?prefix=db/"
```

**Filter by tag:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?tag=prod&prefix=db/"
```

**Query by graph (dependencies):**
```bash
curl -s "http://ragamuffin:8000/v1/facts/deployment%2Furl/graph"
```
Returns the fact's supersedes chain.

**Update (full replace):**
```bash
curl -s -X PUT "http://ragamuffin:8000/v1/facts?key=***" \
  -H "Content-Type: application/json" \
  -d '{"value": "postgres://new-url...", "tags": ["prod", "updated"]}'
```

**Update (partial patch):**
```bash
curl -s -X PATCH "http://ragamuffin:8000/v1/facts?key=***" \
  -H "Content-Type: application/json" \
  -d '{"value": "postgres://new-url..."}'
```

**Delete:**
```bash
curl -s -X DELETE "http://ragamuffin:8000/v1/facts?key=***"
```

**Pagination:**
```bash
curl -s "http://ragamuffin:8000/v1/facts?limit=20"
curl -s "http://ragamuffin:8000/v1/facts?limit=20&before=<next_token>"
```

Facts are versioned. A `version` integer field auto-increments on update.
Set the `version` field explicitly to enable optimistic concurrency control.

### Write-Back for Agent Observations — `POST /v1/ingest`

Index content into an agent vault without touching the filesystem. Use this
to persist observations, analysis results, or any signal that doesn't belong
in a markdown file:

```bash
curl -s -X POST http://ragamuffin:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{"vault": "agent::dev", "documents": [{"id": "obs-001", "text": "Found 3 npm vulns in the auth service", "metadata": {"source": "scout"}}]}'
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

### Sessions — `/v1/sessions`

Create and manage conversation sessions for agent memory.

**Create a session:**
```bash
curl -s -X POST http://ragamuffin:8000/v1/sessions \
  -H "Content-Type: application/json" \
  -d '{"agent": "dev", "label": "Big Sprint planning"}'
```

**Get session by ID:**
```bash
curl -s "http://ragamuffin:8000/v1/sessions/<session_id>"
```

### Snapshot — `/v1/snapshot`

Download the entire knowledge base as a gzipped tarball:
```bash
curl -s -O http://ragamuffin:8000/v1/snapshot
```

### Ingest — `/v1/ingest`

Ingest content into a vault programmatically:

```bash
curl -s -X POST http://ragamuffin:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{"vault": "agent::dev", "content": "Important decision: ...", "metadata": {"type": "decision"}}'
```

| Parameter | Type | Default | Description |
|---|---|---|---|
| `vault` | string | `default` | Target vault |
| `content` | string | — | Content text to index **(required)** |
| `source` | string | — | Source identifier |
| `tags` | array | `[]` | Metadata tags |
| `auto_extract` | bool | `false` | Enable automatic fact extraction |
| `metadata` | object | `{}` | Additional metadata |

Max body size: 10 MB.

### Review Queue — `/v1/review`

Review flagged facts (from pruner scans or manual flagging).

**View flagged facts:**
```bash
# All flagged facts
curl -s "http://ragamuffin:8000/v1/review"

# Filter by reason
curl -s "http://ragamuffin:8000/v1/review?reason=stale"

# Filter by source key
curl -s "http://ragamuffin:8000/v1/review?key=***"

# Paginate
curl -s "http://ragamuffin:8000/v1/review?limit=20&before=<next_token>"
```

**Review stats:**
```bash
curl -s "http://ragamuffin:8000/v1/review/stats"
```

**Resolve a flagged fact:**
```bash
# Confirm — mark as active, reset confidence
curl -s -X POST "http://ragamuffin:8000/v1/review?key=***&action=confirm"

# Supersede — old fact gets status=superseded, create new one
curl -s -X POST "http://ragamuffin:8000/v1/review?key=***&action=supersede" \
  -d '{"new_key": "config/new-key", "new_value": "new-value"}'

# Reject — mark as rejected (kept for audit trail)
curl -s -X POST "http://ragamuffin:8000/v1/review?key=***&action=reject"

# Reclassify — change reason tag
curl -s -X POST "http://ragamuffin:8000/v1/review?key=***&action=reclassify"
```

The pruner **never deletes** facts. It only changes their status. The review
queue is where you (or an agent) decide what to do with flagged facts.

### Audit — `/audit`

Check vault health:
```bash
curl -s http://ragamuffin:8000/audit \
  -H "Content-Type: application/json" \
  -d '{}'
```

Returns stale files, semantic conflicts (requires LLM), gaps, and duplicates.

### Auth Check — `/v1/auth/check`

Check authentication status:
```bash
curl -s http://ragamuffin:8000/v1/auth/check
```

Returns current auth mode, enforcement status, and token validation info.

### SSE Events — `/events`

Real-time event stream for fact lifecycle and broker notifications:
```bash
curl -s -N http://ragamuffin:8000/events
```

Events follow Server-Sent Events protocol. Auto-reconnect compatible.

## Agent Workflow Patterns

### Before answering a question

1. Check `/stats` to see what's indexed
2. Use `POST /recall` with the question as query to find relevant chunks
3. For complex questions, use `POST /ask` to synthesize

### After learning something new

1. For prose (markdown docs) → use `POST /draft` with `mode: "direct"`
2. For structured data (configs, URLs) → use `POST /v1/facts`
3. For a record of what you did → use `POST /v1/logs`
4. For session context → use `POST /v1/sessions`

### Periodic maintenance

1. Call `POST /audit` to check for stale files
2. Call `/v1/review/stats` to check fact health
3. Call `/v1/snapshot` to back up
4. For git-backed vaults, use `POST /draft` with `mode: "pr"` for human review

### Fact quality management

When the pruner flags facts, check the review queue:

```bash
curl -s http://ragamuffin:8000/v1/review/stats | jq .
# Check how many facts need attention
```

Then resolve flagged facts via `POST /v1/review`.

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
- `jwt` — JWT with JWKS verification (configurable issuer, audience)
- `oidc` — OIDC discovery-based auth (auto-discovers JWKS from issuer metadata)

When auth is enabled, agents must include an `Authorization: Bearer <key>` header:

```bash
# With API key
curl -H "Authorization: Bearer sk-read-abc123" http://ragamuffin:8000/recall -d '{"query":"..."}'

# With JWT
curl -H "Authorization: Bearer eyJhbGciOiJSUzI1NiIs..." http://ragamuffin:8000/recall -d '{"query":"..."}'
```

The `/events` and `/mcp` endpoints are always public for protocol compatibility.
All other endpoints enforce auth when `AUTH_MODE` is not `none`.

Read-key agents: search, browse, audit access.
Write-key agents: full access including facts, logs, draft, review.

### Web UI

A web dashboard is served at the root path:
- `GET /` — SPA dashboard with Search, Browse, Audit, Graph pages
- Search, Browse, Audit, and Graph pages all functional via REST API calls

## Events & Webhooks

Ragamuffin emits fact lifecycle events through two channels:

### Webhook (HTTP POST)

When `RAGAMUFFIN_EVENT_WEBHOOK_URL` is configured, events are pushed as
CloudEvents v1.0 structured JSON via HTTP POST:

| Event Type | When | Payload |
|---|---|---|
| `fact.created` | New fact upserted | `{"fact_key":"...", "status":"active"}` |
| `fact.updated` | Existing fact modified | `{"fact_key":"...", "new_status":"superseded"}` |
| `fact.superseded` | Fact superseded | `{"fact_key":"...", "superseded_by":"..."}` |
| `fact.rejected` | Review action: reject | `{"fact_key":"...", "reason":"outdated"}` |
| `fact.confirmed` | Review action: confirm | `{"fact_key":"...", "confidence":0.95}` |
| `fact.needs_review` | Pruner flags fact | `{"fact_key":"...", "reasons":["stale"]}` |

Delivery is fire-and-forget with `Content-Type: application/cloudevents+json`.
Consumers are responsible for durability.

### SSE Stream

Connect to `/events` for real-time streaming:

```bash
curl -s -N http://ragamuffin:8000/events
```

Same events as webhook, delivered via Server-Sent Events. Compatible with
browser `EventSource` API and any SSE client.

### Legacy Vault Events

In addition to fact lifecycle events, legacy vault events are also emitted:

| Event | Payload | When |
|---|---|---|
| `vault.file.changed` | `{"path":"...", "action":"created|modified"}` | After successful file index |
| `vault.file.deleted` | `{"path":"..."}` | After file removed from index |
| `ragamuffin.started` | `{"version":"...", "commit":"...", "host":"...", "port":"..."}` | Server boot, before listen |

## Fact-to-Chunk Bridge

Facts can reference source chunks via the `source_file` payload field.
When a source chunk is deleted or modified, the pruner's source stale scan
flags the fact for review. This keeps your knowledge graph consistent:

```
fact "db/config" → source_file="ops/setup.md" → chunk in vault
```

When `ops/setup.md` is deleted:
1. Pruner source stale scan detects the orphaned reference
2. Fact `db/config` gets flagged as `needs_review`
3. You (or an agent) resolve via review queue

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
RAGAMUFFIN_EVENT_WEBHOOK_URL=http://listener:8000/events

# v0.4 Multi-tenancy & Auth
RAGAMUFFIN_VAULTS=docs:/path/to/docs,code:/path/to/code   # Multi-tenant vaults
RAGAMUFFIN_AUTH_MODE=none                                   # none | api_key | jwt | oidc
RAGAMUFFIN_AUTH_READ_KEY=sk-...                             # Global read key
RAGAMUFFIN_AUTH_WRITE_KEY=sk-...                            # Global write key
RAGAMUFFIN_AUTH_JWT_ISSUER=https://auth.example.com         # JWT issuer
RAGAMUFFIN_AUTH_JWT_AUDIENCE=ragamuffin                     # Expected audience
RAGAMUFFIN_AUTH_JWT_JWKS_URL=https://auth.example.com/.well-known/jwks.json  # JWKS endpoint
RAGAMUFFIN_AUTH_OIDC_ISSUER=https://accounts.example.com    # OIDC issuer (auto-discovers JWKS)
RAGAMUFFIN_AUTH_OIDC_CLIENT_ID=ragamuffin                   # OIDC client ID
```

## Error Handling

All errors return:

```json
{"error": true, "code": "ERROR_CODE", "message": "Human-readable description"}
```

Common codes: `INVALID_INPUT`, `RATE_LIMITED` (with `Retry-After` header),
`NOT_FOUND`, `LLM_NOT_CONFIGURED`, `GIT_NOT_CONFIGURED`.

## Rate Limit Guidance

If your agent gets a `429` response, check the `Retry-After` header (integer
seconds) and wait before retrying. Adjust rate limit env vars per-endpoint if
needed (see SPEC.md for defaults and env vars).

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
# /v1/snapshot /health /stats /version /metrics /mcp /events
# /v1/review /v1/review/stats /v1/ingest /v1/sessions /v1/auth/check
# /v1/documents /v1/ingest/conversation /v1/pruner/auto-tune /v1/pruner/config
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
