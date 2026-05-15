# 🧣 ragamuffin

> *noun.* A person, typically a child, in ragged, dirty clothes.
> In our case: a scrappy little knowledge tool that agents can actually use.

---

**Ragamuffin** is what happens when you get tired of your RAG stack being held together with Python async bugs, MCP bridges, and two-hop architectures that fail at 1 AM.

Point it at a directory. It watches for changes, indexes everything into [Qdrant](https://qdrant.tech), and serves a REST API that any agent can curl. No bridge. No translation layer. One binary.

```bash
curl -s http://localhost:8000/recall \
  -d '{"query":"what do we know about that thing?"}'
```

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

Active development. v0.3.4 with all REST endpoints, structured facts and logs,
panic recovery, Prometheus metrics, rate limiting, request tracing, and MCP support.

### v0.3.x Release History

| Version | Highlights |
|---|---|
| v0.3.4 | ldflags for `/version`, panic recovery middleware, LLM base URL normalization, CountFiles sync from Qdrant on restart |
| v0.3.3 | Tags fix for facts POST (`qdrant.NewValue` 2-value return), deployment fixes |
| v0.3.2 | (skipped — build failure) |
| v0.3.1 | UUID point IDs, Qdrant gRPC port (6334), healthcheck improvements |
| v0.3.0 | Facts endpoint, Logs endpoint, Snapshot endpoint, code-review fixes batch |

Named with affection by [Christopher Goulet](https://github.com/chezgoulet).

---

*"The monkey paw used curl. It was super effective!"*
