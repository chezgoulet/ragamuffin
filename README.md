# 🧣 ragamuffin

> *noun.* A person, typically a child, in ragged, dirty clothes.
> In our case: a scrappy little knowledge tool that agents can actually use.

---

**Ragamuffin** serves two roles in one binary:

1. **Vault Knowledge Server** — point it at a directory, it watches for changes, indexes everything into [Qdrant](https://qdrant.tech), and serves a REST API that any agent can curl. No bridge. No translation layer.
2. **Agent Memory Backend (v0.5)** — plug it into OpenClaw or Hermes via a harness plugin adapter, and every agent gets isolated, persistent, cross-session memory backed by per-agent Qdrant collections. No agent discipline required.

```bash
# Vault mode: semantic search over watched directories
curl -s http://localhost:8000/recall \
  -d '{"query":"what do we know about that thing?"}'

# Agent memory mode: recall from a specific agent's private vault
curl -s http://localhost:8000/v1/recall \
  -d '{"vault":"agent::dev","query":"what did Christopher ask about?"}'
```

Scroll down to [Two Patterns](#two-patterns) for the full picture.

## Quick Start

```bash
# 1. Qdrant — the only runtime dependency
docker run -d -p 6334:6334 qdrant/qdrant

# 2. Run Ragamuffin
docker run -d \
  -p 8000:8000 \
  -v /path/to/vault:/opt/vault:ro \
  -e RAGAMUFFIN_VAULT_PATH=/opt/vault \
  -e RAGAMUFFIN_QDRANT_URL=http://host.docker.internal:6334 \
  -e RAGAMUFFIN_EMBEDDING_API_KEY=sk-... \
  chezgoulet/ragamuffin:latest

# 3. Wait for indexing, then search
curl -s http://localhost:8000/recall \
  -d '{"query":"what should I know about this project?"}'

# Online docs: https://raw.githubusercontent.com/chezgoulet/ragamuffin/main/README.md
```

---

## Two Patterns

Ragamuffin serves two distinct use cases. They can be used independently or together.

### Pattern 1: Vault Knowledge Server

Point Ragamuffin at a filesystem directory. It watches for changes (poll or inotify),
chunks files, embeds them, and indexes into Qdrant. Agents search the vault with
natural language queries. Optional LLM-backed answer synthesis.

```
┌──────────────────┐     ┌──────────────┐     ┌──────────────┐
│  File System     │────▶│  Ragamuffin  │────▶│  Qdrant      │
│  (vault dir)     │     │  (Go binary) │     │  collections │
│  docs/           │     │              │     └──────────────┘
│  ops/            │     │  ┌─────────┐ │
│  policies/       │     │  │ SQLite  │ │
└──────────────────┘     │  │ (logs)  │ │
                          │  └─────────┘ │
                          └──────┬───────┘
                                 │
                    ┌────────────┴────────────┐
                    ▼                         ▼
               ┌──────────┐            ┌──────────┐
               │ Agent A  │            │ Agent B  │
               │ (search) │            │ (search) │
               └──────────┘            └──────────┘
```

**Who this is for:** Teams that want agents to search shared documentation,
runbooks, policies, or codebases — any directory of files that needs to be
queryable by natural language.

### Pattern 2: Agent Memory Backend

Plug Ragamuffin into your agent harness (OpenClaw or Hermes) via a memory
plugin adapter. Every agent gets its own isolated Qdrant collection, persistent
session memory, and cross-agent recall — with zero agent discipline required.
The harness routes memory operations transparently: no `curl`, no tool calls
from the agent itself.

```
┌──────────────────┐     ┌──────────────┐     ┌──────────────────────┐
│  OpenClaw        │     │  Ragamuffin  │     │  Qdrant              │
│  (dev agent)     │────▶│              │────▶│  agent::dev          │
│  memory-ragamuffin│    │  POST /v1/   │     │  ┌────────────────┐  │
│  plugin          │     │  ingest      │     │  │ session turns  │  │
├──────────────────┤     │              │     │  │ recalled facts │  │
│  Hermes          │     │  POST /v1/   │     │  │ summaries      │  │
│  (robot agent)   │────▶│  recall      │     │  └────────────────┘  │
│  memory-ragamuffin│    │              │────▶├──────────────────────┤
│  plugin          │     │              │     │  Qdrant              │
└──────────────────┘     │  POST /v1/   │     │  agent::robot        │
                          │  recall?     │     │  ┌────────────────┐  │
                          │  vault=agent::│    │  │ session turns  │  │
   (cross-agent)          │  robot       │     │  │ recalled facts │  │
                          │             │      │  │ summaries      │  │
                          └──────────────┘     │  └────────────────┘  │
                                               └──────────────────────┘
```

**Who this is for:** Operators running multi-agent systems who need:
- **Guaranteed persistence** — the harness enforces memory writes, agents don't
  have to remember to call a tool
- **Per-agent isolation** — each agent's Qdrant collection is physically separate;
  metadata filter bugs can't leak data across agents
- **Cross-agent recall** — agent A can query agent B's memory via `agent_recall`
  (privileged tool provided by the harness)
- **Single infrastructure** — one Ragamuffin instance, one Qdrant cluster, all agents

### Side-by-side

| Dimension | Vault Knowledge Server | Agent Memory Backend |
|---|---|---|
| Data source | Filesystem directory | Session turn content from harness |
| Isolation | Per-vault Qdrant collections | Per-agent Qdrant collections (`agent::<name>`) |
| Agent setup | Curl / MCP from any agent | Plugin adapter installed in harness |
| Persistence | File watcher detects changes | Harness calls `POST /v1/ingest` each turn |
| Cross-agent | N/A | Privileged cross-agent recall tool |
| Hardware | One Ragamuffin | One Ragamuffin for all agents |

Both patterns can coexist: file-based vaults for shared knowledge, agent vaults
for session memory, all on the same Ragamuffin instance.

### Hybrid Pattern 3: Ragamuffin as Cross-Harness Bridge

Keep your existing harness memory slots (claudemem in OpenClaw, Honcho in
Hermes) while using Ragamuffin exclusively as a **cross-harness recall bridge**
and **structured fact store**. Agents get two additional tools that call
Ragamuffin's API directly — no plugin swap required.

```
┌──────────────────┐     ┌──────────────┐
│  OpenClaw        │     │  Claudemem   │ ← per-turn auto-persist (unchanged)
│  (dev agent)     │────▶│              │
│                  │     └──────────────┘
│  ragamuffin_     │
│  recall/store    ├───▶┌──────────────┐
│  (agent tools)   │     │  Ragamuffin  │ ← cross-harness bridge
└──────────────────┘     │              │
                          │  agent::dev │
┌──────────────────┐     │  agent::robot│
│  Hermes          │     │  agent::scout│
│  (robot agent)   │────▶│              │
│                  │     └──────────────┘
│  ragamuffin_     │
│  recall/store    ├───▶┌──────────────┐
│  (agent tools)   │     │  Honcho      │ ← per-turn auto-persist (unchanged)
└──────────────────┘     └──────────────┘
```

**Why run this way:**
- Zero migration — your existing memory setup stays exactly as-is
- Cross-harness recall works across the boundary: OpenClaw dev can ask what
  Hermes robot found in the last scan
- Agents write selectively — only important conclusions and shared facts go
  to Ragamuffin, not every turn
- Gentler operational path: validate Ragamuffin in production before committing
  to a full slot swap

**What it costs:**
- No auto-persistence — agents must explicitly call their store tool to write
- No auto-prefetch — Ragamuffin context won't appear in the system prompt
  automatically; agents must use recall when they need it
- Agent discipline is required — if the agent forgets to store something, it's
  lost (mitigated by the existing slot backend catching everything per-turn)

| Dimension | Full plugin (slot) | Hybrid (agent tools) |
|---|---|---|
| Harness slot | Swap to memory-ragamuffin | Keep claudemem/Honcho |
| Turn persistence | Automatic | Slot handles this |
| Cross-harness recall | Built-in | Via ragamuffin_recall tool |
| Agent writes | Zero-touch | Explicit tool calls |
| Migration risk | Swap and validate | Additive, zero-risk |
| End state | Full Ragamuffin | Ragamuffin as bridge layer |

All three patterns use the same Ragamuffin instance and the same per-agent
Qdrant collections. The difference is who calls the API — the harness plugin
or the agent itself.

---

## API Reference

### Core RAG Endpoints

#### `POST /recall` — Semantic search

```bash
curl -s http://localhost:8000/recall \
  -H "Content-Type: application/json" \
  -d '{"query":"deployment process","top_k":10,"score_threshold":0.5,"source_filter":"ops/"}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `query` | string | — | Natural-language search query **(required)** |
| `top_k` | int | 10 | Max results (1–100) |
| `score_threshold` | float | 0.0 | Minimum similarity (0.0–1.0) |
| `source_filter` | string | — | Restrict to files under this path prefix |

**Response:**
```json
{
  "results": [
    {
      "text": "Deployment uses GitHub Actions...",
      "source_file": "ops/deploy.md",
      "header": "## Deployment",
      "chunk_index": 2,
      "score": 0.89,
      "file_last_updated": "2026-05-10T14:30:00Z"
    }
  ],
  "top_score": 0.89
}
```

#### `POST /ask` — Synthesized answer (requires LLM config)

```bash
curl -s http://localhost:8000/ask \
  -H "Content-Type: application/json" \
  -d '{"query":"What is our deployment strategy?","mode":"auto","top_k":8}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `query` | string | — | Question to answer **(required)** |
| `mode` | string | `auto` | `rag` (RAG-only), `auto` (RAG→full fallback), or `full` (load full source files) |
| `top_k` | int | 8 | RAG results to retrieve (1–50) |

Returns `mode_used` so callers can see if auto-mode chose RAG or full.

#### `POST /draft` — Write files to the vault

```bash
# Direct mode — writes immediately to the vault filesystem
curl -s http://localhost:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title":"Database Schema","content":"...","target_path":"ops/schema.md","mode":"direct"}'

# PR mode — opens a git pull request (requires git config)
curl -s http://localhost:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title":"Update schema","content":"...","target_path":"ops/schema.md","mode":"pr","description":"Add new table"}'

# Delete a file
curl -s http://localhost:8000/draft \
  -H "Content-Type: application/json" \
  -d '{"title":"Delete","target_path":"ops/old.md","mode":"direct","delete":true}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `title` | string | — | File or PR title **(required)** |
| `content` | string | — | File content (omit or `""` if `delete=true`) |
| `target_path` | string | — | Path relative to vault root **(required)** |
| `mode` | string | `direct` | `direct` or `pr` |
| `description` | string | — | PR body (PR mode only) |
| `delete` | bool | `false` | Delete the file instead of writing |

Security: path traversal is blocked — resolved paths must stay under the vault root.

#### `POST /audit` — Vault health check

```bash
curl -s http://localhost:8000/audit \
  -H "Content-Type: application/json" \
  -d '{"stale_days":90,"checks":["stale","semantic_conflict","gap","duplicate"],"sample_size":50}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `stale_days` | int | 90 | Days since last update to flag as stale |
| `checks` | array | all | Which checks: `stale`, `semantic_conflict`, `gap`, `duplicate` |
| `sample_size` | int | 50 | Chunk pairs to LLM-compare (1–200, requires LLM) |

### Agent Memory Endpoints (v0.5)

These endpoints support the agent memory backend pattern. Harness plugin adapters
call them transparently — agents don't curl these directly. But you can, for
debugging and manual inspection.

#### `POST /v1/vaults` — Create or confirm an agent vault

Each agent gets its own isolated vault. Calling this endpoint ensures the vault
exists (idempotent — returns `created: false` if already present).

```bash
curl -s -X POST http://localhost:8000/v1/vaults \
  -H "Content-Type: application/json" \
  -d '{"name":"agent::dev","label":"Dev agent working memory"}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | — | Vault name **(required)** — use `agent::<name>` convention |
| `label` | string | `""` | Human-readable label for the vault |

**Response:**
```json
{
  "name": "agent::dev",
  "label": "Dev agent working memory",
  "created": true,
  "collection": "agent::dev"
}
```

`created: false` means the vault already existed. Vault operations are
deterministic — name hashes to a Qdrant collection name. Safe to call on
every agent startup.

#### `POST /v1/ingest` — Index content into an agent vault

Persist session content, turn transcripts, or any text into an agent's vault.
Called by the harness plugin after each completed turn.

```bash
curl -s -X POST http://localhost:8000/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "vault": "agent::dev",
    "documents": [
      {
        "id": "turn-2026-05-17-001",
        "text": "User asked about Hermes integration. I explained the MemoryProvider ABC patterns...",
        "metadata": {
          "source": "session",
          "agent": "dev",
          "session_id": "sess_abc123"
        }
      }
    ]
  }'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `vault` | string | — | Target vault name **(required)** |
| `documents` | array | `[]` | List of documents to index **(required)** |
| `documents[].id` | string | — | Unique doc/session ID **(required)** |
| `documents[].text` | string | — | Content text **(required)** |
| `documents[].metadata` | object | `{}` | Optional metadata for filtering |

**Response:**
```json
{
  "indexed": 1,
  "vault": "agent::dev"
}
```

#### `POST /v1/recall` — Semantic search across vaults

Extended recall that targets a specific vault by name. Same semantics as `/recall`
but with vault targeting.

```bash
# Recall from the calling agent's own vault
curl -s -X POST http://localhost:8000/v1/recall \
  -H "Content-Type: application/json" \
  -d '{"vault":"agent::dev","query":"what did we decide about Qdrant isolation?","limit":5}'

# Cross-agent recall — query another agent's vault
curl -s -X POST http://localhost:8000/v1/recall \
  -H "Content-Type: application/json" \
  -d '{"vault":"agent::robot","query":"what vulnerabilities were found?","limit":3}'
```

| Field | Type | Default | Description |
|---|---|---|---|
| `vault` | string | — | Vault to search **(required)** |
| `query` | string | — | Natural-language query **(required)** |
| `limit` | int | 10 | Max results (1–100) |
| `min_score` | float | 0.0 | Minimum similarity threshold (0.0–1.0) |

**Response:**
```json
{
  "results": [
    {
      "text": "Use physical Qdrant collection isolation, not metadata filters...",
      "score": 0.89,
      "metadata": {
        "source": "session",
        "agent": "dev",
        "session_id": "sess_abc123"
      }
    }
  ],
  "vault": "agent::dev"
}
```

#### `GET /v1/vaults/:name/health` — Check vault readiness

Used during agent startup to confirm the vault is reachable and ready.

```bash
curl -s http://localhost:8000/v1/vaults/agent::dev/health
```

**Response:**
```json
{
  "name": "agent::dev",
  "exists": true,
  "collection": "agent::dev",
  "indexed": 142
}
```

### Structured Data Endpoints (v0.3)

#### `POST /v1/facts` — Upsert a structured fact

```bash
curl -s -X POST http://localhost:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{"key":"deployment/url","value":"https://app.example.com","tags":["prod","staging"]}'
```

| Field | Type | Limits | Description |
|---|---|---|---|
| `key` | string | 1024 bytes | Fact key **(required)** |
| `value` | string | 64 KB | Fact value **(required)** |
| `tags` | array | optional | String tags for filtering |

Key is hashed (SHA-256) → deterministic UUID is used as the Qdrant point ID. Re-inserting the same key upserts.

#### `GET /v1/facts` — Retrieve facts

```bash
# Exact key lookup
curl -s "http://localhost:8000/v1/facts?key=deployment/url"

# Search by text fragment (full-text / substring match on fact_key)
curl -s "http://localhost:8000/v1/facts?prefix=deploy"

# Filter by tag
curl -s "http://localhost:8000/v1/facts?tag=prod&prefix=deploy"

# Paginate
curl -s "http://localhost:8000/v1/facts?limit=20"
curl -s "http://localhost:8000/v1/facts?limit=20&before=<next_token>"
```

| Param | Description | Default |
|---|---|---|
| `key` | Exact fact_key match | — |
| `prefix` | Full-text/substring match on fact_key (see note) | — |
| `tag` | Exact tag keyword filter | — |
| `limit` | Max results per page (1–1000) | 100 |
| `before` | Cursor from previous response `next_token` | — |

> ⚠️ `prefix=` performs Qdrant full-text/substring matching, not true prefix matching.
> A query for `prefix=user/` will also match `prefix=superuser/settings` due to Qdrant's
> tokenizer behavior. For exact prefix filtering, a Qdrant payload index with a keyword
> tokenizer would be needed.

**Response:**
```json
{
  "entries": [
    {"key": "deployment/url", "value": "https://app.example.com", "tags": ["prod"], "updated_at": "2026-05-15T12:00:00Z"}
  ],
  "next_token": "uuid-for-next-page"
}
```

#### `DELETE /v1/facts` — Delete a fact

```bash
curl -s -X DELETE "http://localhost:8000/v1/facts?key=deployment/url"
```

| Param | Description |
|---|---|
| `key` | Fact key to delete **(required)** |

#### `POST /v1/logs` — Append a log entry

```bash
curl -s -X POST http://localhost:8000/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"agent":"scout","type":"scan","body":"Found 3 vulnerabilities","tags":["security","npm"],"timestamp":"2026-05-15T12:00:00Z"}'
```

| Field | Type | Limits | Description |
|---|---|---|---|
| `agent` | string | 256 bytes | Agent or service identifier **(required)** |
| `type` | string | 256 bytes | Log type/category **(required)** |
| `body` | string | 64 KB | Log content **(required)** |
| `tags` | array | ≤50 entries, 256 bytes each | Optional string tags |
| `timestamp` | string | ISO 8601 / RFC 3339 | Optional; server time if omitted |

#### `GET /v1/logs` — Query log entries

```bash
# All logs for an agent
curl -s "http://localhost:8000/v1/logs?agent=scout"

# Filter by type and tag
curl -s "http://localhost:8000/v1/logs?type=scan&tag=security"

# Time range (ISO 8601 / RFC 3339)
curl -s "http://localhost:8000/v1/logs?since=2026-05-01T00:00:00Z&until=2026-05-15T00:00:00Z"

# Paginate
curl -s "http://localhost:8000/v1/logs?limit=50"
curl -s "http://localhost:8000/v1/logs?limit=50&before=<hex_cursor>"
```

| Param | Description | Default |
|---|---|---|
| `agent` | Filter by agent | — |
| `type` | Filter by type | — |
| `tag` | Exact tag filter (uses `json_each`, not `LIKE`) | — |
| `since` | Return entries after this timestamp (RFC 3339) | — |
| `until` | Return entries before this timestamp (RFC 3339) | — |
| `before` | Cursor: entries before this ID (hex rowid) | — |
| `limit` | Max results per page (1–1000) | 100 |

#### `GET /v1/snapshot` — Download vault as gzipped tarball

```bash
curl -s -O http://localhost:8000/v1/snapshot
# → vault-2026-05-15.tar.gz
```

Streaming download at `/v1/snapshot`. Best-effort consistency — files may change during the walk. Skips the `.ragamuffin/` directory (operational metadata).

### Observability Endpoints

#### `GET /health` — Service health

```bash
curl -s http://localhost:8000/health
```

Returns `200 OK` with Qdrant reachable check. Returns `200` with `status: "indexing"` during initial reindex. Returns `502` if Qdrant is unreachable.

#### `GET /stats` — Indexer metrics

```bash
curl -s http://localhost:8000/stats
```

Returns vault path, indexed file count, total chunks (from Qdrant, authoritative), last indexed time, embedding provider, uptime.

> **Multi-tenant note:** In multi-tenant mode `/v1/facts`, `/v1/logs`, and `/v1/snapshot` are **global** endpoints — they operate on the
> first-configured vault, not per-vault. Use the vault-prefixed routes
> (`/vault/{name}/v1/facts`, `/vault/{name}/v1/logs`, `/vault/{name}/v1/snapshot`)
> for per-vault access.

#### `GET /version` — Build info

```bash
curl -s http://localhost:8000/version
```

Returns version, commit hash, build date, Go version (set via `-ldflags`).

#### `GET /metrics` — Prometheus endpoint

```bash
curl -s http://localhost:8000/metrics
```

Plain-text Prometheus format with counters for requests, durations, indexed files/chunks.

### Agent Protocol Endpoint

#### `GET /mcp` / `POST /mcp` — Model Context Protocol (SSE transport)

Agents that support MCP can connect via the SSE stream at `/mcp`. Implements:
- `initialize` — protocol handshake
- `tools/list` — discover available tools
- `tools/call` — invoke `ragamuffin_recall`, `ragamuffin_ask`, `ragamuffin_draft`, `ragamuffin_audit`

The MCP tools mirror the REST endpoints above. Client disconnect cancels in-flight operations.

---

### v0.4 Endpoints

#### `GET /vaults` — List configured vaults (v0.4)

```bash
curl -s http://localhost:8000/vaults
```

In single-tenant mode, returns a single "default" vault. In multi-tenant mode, returns all configured vaults with status.

#### `GET /graph` — Knowledge graph (v0.4)

```bash
# Full graph
curl -s http://localhost:8000/graph

# Entity-focused
curl -s 'http://localhost:8000/graph?entity=Qdrant&depth=2'
```

| Parameter | Type | Default | Description |
|---|---|---|---|
| `entity` | string | — | Focus on a specific entity |
| `depth` | int | 1 | Graph traversal depth (0–3) |
| `limit` | int | 50 | Max nodes to return (1–200) |

Returns nodes (files and entities) and edges (contains, links_to).

#### `POST /reindex` — Full reindex (v0.4)

```bash
curl -s -X POST http://localhost:8000/reindex
```

Triggers a full re-index of the vault. Non-blocking — returns immediately and reindex runs asynchronously.

### Multi-tenant Mode (v0.4)

When `RAGAMUFFIN_VAULTS` is set, all content endpoints are prefixed with `/vault/{name}/`:

```bash
curl -s 'http://localhost:8000/vault/docs/recall?query=deploy'
curl -s 'http://localhost:8000/vault/docs/graph'
```

Available vault-prefixed endpoints:
- `/vault/{name}/recall`
- `/vault/{name}/ask`
- `/vault/{name}/draft`
- `/vault/{name}/audit`
- `/vault/{name}/v1/facts`
- `/vault/{name}/v1/logs`
- `/vault/{name}/v1/snapshot`
- `/vault/{name}/reindex`
- `/vault/{name}/graph`

### Authentication (v0.4)

Three modes controlled by `RAGAMUFFIN_AUTH_MODE`:

| Mode | Description |
|---|---|
| `none` | No authentication (default) |
| `api_key` | Static API keys from environment variables |
| `jwt` | JWT tokens validated via JWKS endpoint |

**API Key mode:**
- `RAGAMUFFIN_AUTH_READ_KEY` — global read key
- `RAGAMUFFIN_AUTH_WRITE_KEY` — global write key
- `RAGAMUFFIN_AUTH_READ_KEY_{VAULT}` — per-vault scoped read key
- `RAGAMUFFIN_AUTH_WRITE_KEY_{VAULT}` — per-vault scoped write key

**JWT mode:**
- `RAGAMUFFIN_AUTH_JWT_ISSUER` — expected JWT issuer
- `RAGAMUFFIN_AUTH_JWT_AUDIENCE` — expected audience
- `RAGAMUFFIN_AUTH_JWT_JWKS_URL` — JWKS endpoint for key discovery

JWT must include a `ragamuffin` claim with an `access` field (`read` or `read_write`).

### Web UI (v0.4)

Ragamuffin ships an embedded web UI served at the root path:
- `GET /` — SPA dashboard with Search, Browse, Audit, and Graph pages
- `GET /static/*` — Static assets (CSS, JS)

API routes take priority over static file serving.

---

## Harness Integration (v0.5)

Ragamuffin ships as the memory backend for both OpenClaw and Hermes agents.
The adapters are reference implementations — any harness with a pluggable memory
backend can adopt the same API contract.

### OpenClaw — `plugins.slots.memory = "memory-ragamuffin"`

Configure in `openclaw.json`:

```json5
{
  plugins: {
    slots: {
      memory: "memory-ragamuffin",
    },
    entries: {
      "memory-ragamuffin": {
        enabled: true,
        config: {
          endpoint: "http://ragamuffin:8080",
          vaultPrefix: "agent::",
          autoRecall: true,
          autoCapture: true,
        },
      },
    },
  },
}
```

That's it. Restart OpenClaw and every agent's memory is automatically
Ragamuffin-backed — per-agent Qdrant isolation, session persistence,
and cross-agent recall. Agents write zero code.

**Don't want to swap slots yet?** See [Hybrid: Ragamuffin as cross-harness
bridge](#hybrid-pattern-3-ragamuffin-as-cross-harness-bridge) — you can add
Ragamuffin as agent tools alongside your existing memory backend with zero
migration.

### Hermes — `memory.provider: "ragamuffin"`

Configure in `config.yaml`:

```yaml
memory:
  provider: ragamuffin
  ragamuffin:
    endpoint: "http://ragamuffin:8080"
    vault_prefix: "agent::"
```

Hermes discovers the plugin from `plugins/memory/ragamuffin/`. The adapter
implements the `MemoryProvider` ABC — `initialize`, `prefetch`, `sync_turn`,
`get_tool_schemas`, `on_session_end`, `shutdown`.

### Lifecycle mapping

Both adapters implement the same mapping from harness hooks to Ragamuffin API calls:

| Harness hook | Ragamuffin API call |
|---|---|
| Plugin load / agent start | `POST /v1/vaults` — create/confirm agent vault |
| Pre-turn recall | `POST /v1/recall` — semantic search against agent vault |
| Post-turn persist | `POST /v1/ingest` — index the completed turn |
| Session end | `POST /v1/ingest` — index a session summary artifact |
| Cross-agent recall | `POST /v1/recall?vault=agent::robot` — query another agent's vault |

### Writing an adapter for another harness

See [docs/integration/memory-provider-api.md](docs/integration/memory-provider-api.md)
for the full HTTP contract, OpenAPI spec, error handling guide, and agent identity
conventions. The adapters are ~200 lines each — the contract is the hard part.

---

## Rate Limits

Per-endpoint rate limiting via environemnt variables. Disabled by default; enable with `RAGAMUFFIN_RATE_LIMIT_ENABLED=true`.

| Endpoint | Env Var | Default (req/min) |
|---|---|---|
| `/recall` | `RAGAMUFFIN_RATE_LIMIT_RECALL` | 60 |
| `/ask` | `RAGAMUFFIN_RATE_LIMIT_ASK` | 10 |
| `/draft` | `RAGAMUFFIN_RATE_LIMIT_DRAFT` | 30 |
| `/audit` | `RAGAMUFFIN_RATE_LIMIT_AUDIT` | 5 |
| `/v1/facts` | `RAGAMUFFIN_RATE_LIMIT_FACTS` | 30 |
| `/v1/logs` | `RAGAMUFFIN_RATE_LIMIT_LOGS` | 60 |
| `/v1/snapshot` | `RAGAMUFFIN_RATE_LIMIT_SNAPSHOT` | 5 |
| `/reindex` | `RAGAMUFFIN_RATE_LIMIT_REINDEX` | 30 |

When rate-limited, responds with `429 Too Many Requests` and a `Retry-After` header.

---

## Storage

### Qdrant Collections

| Collection | Env Var | Default | Vector Size | Purpose |
|---|---|---|---|---|
| Main index | `RAGAMUFFIN_QDRANT_COLLECTION` | `ragamuffin` | 1536 (default) | File chunk embeddings for /recall |
| Facts | `RAGAMUFFIN_FACTS_COLLECTION` | `ragamuffin_facts` | 4 (configurable) | Structured key-value facts, zero-vector sentinel |

The facts collection uses a 4-dim zero vector `[0,0,0,0]` by default — payload-only storage
that satisfies Qdrant's vector requirement without embedding costs.

### SQLite Database

Ragamuffin creates a SQLite database at `<vault>/.ragamuffin/logs.db` for the structured log
store. Uses WAL mode and `synchronous=NORMAL` for concurrent access. Pure Go —
uses `modernc.org/sqlite`, no CGo dependency.

### Request Body Limits

| Endpoint | Limit |
|---|---|
| `/recall`, `/ask`, `/audit` | 64 KB |
| `/v1/facts` (POST) | 256 KB |
| `/v1/logs` (POST) | 64 KB |
| `/draft` | 10 MB |

---

## Configuration

### Required

| Env Var | Description |
|---|---|
| `RAGAMUFFIN_VAULT_PATH` | Path to the knowledge base directory |
| `RAGAMUFFIN_QDRANT_URL` | Qdrant gRPC endpoint (e.g. `http://localhost:6334`) |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | API key for the embedding service |

### Embedding

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_EMBEDDING_PROVIDER` | `openai` | Embedding API provider |
| `RAGAMUFFIN_EMBEDDING_MODEL` | `text-embedding-3-small` | Model name |
| `RAGAMUFFIN_EMBEDDING_BASE_URL` | `https://api.openai.com/v1` | API base URL (for proxies) |
| `RAGAMUFFIN_EMBEDDING_DIMS` | `1536` | Output dimensions |

### LLM

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_LLM_PROVIDER` | — | LLM provider (e.g. `openai`) |
| `RAGAMUFFIN_LLM_BASE_URL` | `https://api.deepseek.com` | API base URL without `/v1` — the LLM client appends `/v1/chat/completions` internally. For LiteLLM proxy use `http://litellm:4000`. See [URL convention](#url-conventions). |
| `RAGAMUFFIN_LLM_MODEL` | — | Model name (e.g. `gpt-4o`, `deepseek-chat`, `deepseek-v4-flash`) |
| `RAGAMUFFIN_LLM_API_KEY` | — | LLM API key |
| `RAGAMUFFIN_LLM_TIMEOUT` | `120s` | LLM request timeout (Go duration) |

### URL Conventions

Ragamuffin has two API clients with **opposite base URL conventions** — this is by design after normalization.

| Client | Appends to base URL | Example `RAGAMUFFIN_*_BASE_URL` |
|---|---|---|
| **Embedding** | `/embeddings` | `https://api.openai.com/v1` (include `/v1`) |
| **LLM** | `/v1/chat/completions` | `https://api.deepseek.com` (omit `/v1`) |

For a LiteLLM proxy (`http://litellm:4000`), set:
- `RAGAMUFFIN_EMBEDDING_BASE_URL=http://litellm:4000/v1` (LiteLLM proxies `/v1/embeddings`)
- `RAGAMUFFIN_LLM_BASE_URL=http://litellm:4000` (LiteLLM handles `/v1/chat/completions`)

### Qdrant

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_QDRANT_COLLECTION` | `ragamuffin` | Main index collection name |
| `RAGAMUFFIN_FACTS_COLLECTION` | `ragamuffin_facts` | Facts collection name |
| `RAGAMUFFIN_FACTS_VECTOR_SIZE` | `4` | Facts collection vector dimensionality |

### Watcher

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_WATCH_INTERVAL` | `60s` | Poll interval (poll mode) |
| `RAGAMUFFIN_WATCHER_MODE` | `poll` | `poll` or `inotify` (Linux only) |

### Chunking

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_CHUNK_MAX_TOKENS` | `2000` | Max tokens per chunk (0 = no limit) |

### Git

| Env Var | Description |
|---|---|
| `RAGAMUFFIN_GIT_PROVIDER_ENABLED` | Enable PR mode (`true`/`false`) |
| `RAGAMUFFIN_GIT_PROVIDER` | `github` (default) |
| `RAGAMUFFIN_GIT_TOKEN` | Git provider access token |
| `RAGAMUFFIN_GIT_BASE_BRANCH` | `main` (default) |
| `RAGAMUFFIN_GIT_BASE_URL` | API base URL (for self-hosted) |
| `RAGAMUFFIN_GIT_REPOS` | Repository list |

### Events (v0.4)

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_EVENT_WEBHOOK_URL` | — | Webhook URL for CloudEvents v1.0 (empty = disabled) |

When configured, Ragamuffin emits CloudEvents v1.0 structured JSON via HTTP POST
with `Content-Type: application/cloudevents+json`. Delivery is fire-and-forget (async).

| Event Type | When |
|---|---|
| `vault.file.changed` | File created or modified (after successful index) |
| `vault.file.deleted` | File deleted from index |
| `ragamuffin.started` | Server boot, before listen |

### Server

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_HOST` | `0.0.0.0` | HTTP listen host |
| `RAGAMUFFIN_PORT` | `8000` | HTTP listen port |
| `RAGAMUFFIN_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |

All handlers are wrapped in a panic recovery middleware that logs stack traces
via slog and returns JSON 500 errors instead of silent connection drops.

### Tuning

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_AUDIT_SAMPLE_SIZE` | `50` | Default sample size for audit checks |
| `RAGAMUFFIN_AUTO_THRESHOLD` | `0.75` | Auto-mode RAG→full fallback threshold |
| `RAGAMUFFIN_RATE_LIMIT_ENABLED` | `false` | Enable per-endpoint rate limiting |

---

## Architecture

```
┌────────────┐     ┌──────────────┐     ┌──────────┐
│   Agent    │────▶│  Ragamuffin  │────▶│  Qdrant  │
│ (curl/MCP) │◀────│  (Go binary) │◀────│ (vector) │
└────────────┘     │              │     └──────────┘
                   │  ┌─────────┐ │
                   │  │ SQLite  │ │
                   │  │ (logs)  │ │
                   │  └─────────┘ │
                   │  ┌─────────┐ │
                   │  │ Filesys │ │
                   │  │ (vault) │ │
                   │  └─────────┘ │
                   └──────────────┘
```

- **All endpoints return JSON** with a uniform error format: `{"error": true, "code": "ERROR_CODE", "message": "..."}`
- **MCP is a bolt-on** — the REST API is the primary interface. MCP mirrors REST tools.
- **No bridge needed** — agents talk HTTP directly. Ragamuffin manages indexing, chunking, embedding, and storage.
- **LLM is optional** — `/recall` and facts/logs work without it. `/ask` and semantic conflict audit require it.

---

## Design

- **Go.** Single static binary. No runtime, no pip, no `asyncio.create_task` at module level.
- **REST-first.** MCP is a bolt-on. The curl test is the test that matters.
- **Optional everything.** Only Qdrant and an embedding API are mandatory. LLM? Optional. Git? Optional. Auth? Trust the proxy.
- **Write-back built in.** Agents learn things. The vault should grow.
- **Structured data, too.** Facts (key-value) and logs (append-only) extend the vault beyond flat files.

---

## Status

Active development. v0.5 adds agent memory backend support — per-agent Qdrant
collections, session ingest endpoint, cross-agent recall, and harness plugin
adapters for OpenClaw and Hermes.

### Release History

| Version | Highlights |
|---|---|
| v0.5 | Agent memory backend — per-agent Qdrant collections, session ingest (`POST /v1/ingest`), vault provisioning (`POST /v1/vaults`), cross-agent recall, OpenClaw + Hermes plugin adapters, integration docs |
| v0.4 | Multi-tenancy, authentication (API key + JWT), knowledge graph, CloudEvents, LLM timeout config, embedded web UI, built-in web dashboard |
| v0.3.4 | ldflags for `/version`, panic recovery middleware, LLM base URL normalization, CountFiles sync from Qdrant on restart |
| v0.3.3 | Tags fix for facts POST (`qdrant.NewValue` 2-value return), deployment fixes |
| v0.3.2 | (skipped — build failure) |
| v0.3.1 | UUID point IDs, Qdrant gRPC port (6334), healthcheck improvements |
| v0.3.0 | Facts endpoint, Logs endpoint, Snapshot endpoint, code-review fixes batch |

Named with affection by [Christopher Goulet](https://github.com/chezgoulet).

---

*"The monkey paw used curl. It was super effective!"*
