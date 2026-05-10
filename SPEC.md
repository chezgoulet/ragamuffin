# Ragamuffin — Specification

A scrappy little knowledge tool for thirsty agents. RAG-first, REST-native, zero-dependency binary.

## Overview

Ragamuffin turns a directory of text files into a queryable knowledge base that agents
can read from and write back to. Point it at a directory. It watches for changes, indexes
everything, and serves a REST API that any agent can curl.

**The deployment is two containers:** Ragamuffin (the binary) + Qdrant (the vector store).
Agents only talk to Ragamuffin. Qdrant is an implementation detail.

## What Ragamuffin Does

**Read.** Semantic search across every file in the vault. Agents ask natural-language
questions and get back ranked chunks with source paths and scores. No keyword matching,
no grep — the agent asks what it wants to know and Ragamuffin finds the relevant text
regardless of phrasing.

**Understand.** For complex questions that span multiple files, Ragamuffin loads the
relevant context and synthesizes an answer via an LLM. The agent gets a prose response
with source citations, not a list of chunks to piece together itself.

**Write.** Agents learn things. A pattern surfaces, a fact changes, a gap gets filled.
Without write-back, that knowledge lives in the agent's run log and disappears. With
write-back, the agent posts to Ragamuffin and the vault grows. Direct mode writes
immediately to the filesystem — simple, universal, zero config. For teams that version
their vault with git, an optional PR mode opens a pull request instead, preserving the
human review gate.

**Self-audit.** Knowledge bases rot. Files go stale, sections contradict each other,
directories sit empty. Ragamuffin can scan itself for these problems and report them.
An agent or cron job calls `/audit` and gets back a list of what needs attention — the
vault tests itself.

## Design Principles

1. **The curl test is the test that matters.** If `curl localhost:PORT/recall -d '{"query":"..."}'`
   returns results, any agent framework can use it.
2. **Write-back is first-class.** Agents contribute to the vault, not just consume it.
   The feedback loop closes without manual intervention.
3. **Freshness is surfaced.** Every chunk carries a timestamp. Staleness is discoverable
   via `/audit`.
4. **Zero-dependency binary.** `go build` produces a static binary. No runtime, no
   virtualenv, no pip, no CGo. The binary requires external services (Qdrant, embedding
   API, optionally an LLM API and a git provider) but ships nothing of its own.
5. **Protocols are bolt-ons.** REST is the foundation. MCP and anything else agents
   adopt are adapters on top of the REST API. The core is plain HTTP/JSON.

## File Support (v0.1)

Two formats, detected by extension:

| Extension | Chunking strategy |
|-----------|-------------------|
| `.md` | Split on H1, H2, and H3 headings. H4+ stays in parent chunk. |
| `.txt`, `.org`, `.rst`, no extension | Split on double-newline (paragraph boundaries). |

All other files are ignored. Maximum chunk size: 2,000 tokens. Oversized chunks are
split at the nearest paragraph boundary below the limit. A configurable chunking
strategy may be added in a later version.

## MVP Endpoints (v0.1)

All endpoints accept and return JSON. Every endpoint defines both success and error
response shapes. Authentication is deferred — trust the reverse proxy for v0.1.

### Error Response (all endpoints)

Every endpoint returns this shape on failure. HTTP status code indicates the error class.

```json
{
  "error": true,
  "code": "QDRANT_UNREACHABLE",
  "message": "Qdrant at http://localhost:6333 did not respond within 2s"
}
```

Standard error codes:

| HTTP | Code | Meaning |
|------|------|---------|
| 400 | `INVALID_REQUEST` | Missing or malformed parameters |
| 502 | `QDRANT_UNREACHABLE` | Vector store is down |
| 502 | `EMBEDDING_API_ERROR` | Embedding service returned an error |
| 503 | `LLM_NOT_CONFIGURED` | Endpoint requires an LLM but none is configured |
| 502 | `LLM_API_ERROR` | LLM backend returned an error |
| 503 | `GIT_NOT_CONFIGURED` | `/draft` PR mode requested but git provider is not configured |
| 502 | `GIT_PROVIDER_ERROR` | Git provider API returned an error |
| 500 | `INTERNAL` | Unexpected failure (check logs) |

---

### `/health` — GET

Health check. Returns service status, Qdrant connectivity, and indexing state.

**Normal state:**
```json
{
  "status": "ok",
  "qdrant": "reachable",
  "indexing": false
}
```

**During initial indexing (vault is being processed for the first time):**
```json
{
  "status": "indexing",
  "qdrant": "reachable",
  "indexing": true,
  "indexed_files": 142,
  "total_files": 500,
  "progress_pct": 28
}
```

During indexing, `/recall` returns results for files that have been indexed so far.
Agents should check `/health` and expect partial results while `indexing` is `true`.

Error (Qdrant down):
```json
{
  "error": true,
  "code": "QDRANT_UNREACHABLE",
  "message": "Qdrant at http://localhost:6333 did not respond within 2s"
}
```

---

### `/stats` — GET

Operational statistics. For dashboards and debugging.

```json
{
  "vault_path": "/opt/vault",
  "indexed_files": 247,
  "total_chunks": 1893,
  "last_indexed": "2026-05-10T01:30:00Z",
  "qdrant_collection": "ragamuffin",
  "embedding_provider": "openai",
  "uptime_seconds": 84321
}
```

---

### `/recall` — POST

Semantic search. Returns top-k chunks with source paths, scores, and timestamps.

**Request:**
```json
{
  "query": "what is the policy on contractor rates?",
  "top_k": 10,
  "score_threshold": 0.0,
  "source_filter": "contractors/"
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `query` | string | yes | — | Natural-language search query |
| `top_k` | integer | no | 10 | Max results (1–100) |
| `score_threshold` | float | no | 0.0 | Minimum similarity score (0.0–1.0) |
| `source_filter` | string | no | — | Restrict to files under this path prefix |

**Response:**
```json
{
  "results": [
    {
      "text": "Contractor rates are reviewed quarterly...",
      "source_file": "contractors/rates.md",
      "header": "## Review Cycle",
      "chunk_index": 3,
      "score": 0.87,
      "file_last_updated": "2026-05-09T10:21:13Z"
    }
  ],
  "top_score": 0.87
}
```

---

### `/ask` — POST

Full-context synthesis via LLM. Returns a prose answer with source citations.

**Requires an LLM to be configured.** If `RAGAMUFFIN_LLM_API_KEY` is not set, returns
`LLM_NOT_CONFIGURED`. All other endpoints work without an LLM.

**Modes:**

| Mode | Behavior |
|------|----------|
| `auto` | Run RAG first. If the highest score is ≥ 0.75, use RAG context only. If below, load the full vault. |
| `rag` | Always use RAG context only. Faster, cheaper, narrower. |
| `full` | Load all chunks from the top-ranked files (enough to fill the LLM context window). More comprehensive, more expensive. |

**Request:**
```json
{
  "query": "what factors determine contractor rate adjustments?",
  "mode": "auto",
  "top_k": 8
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `query` | string | yes | — | The question to answer |
| `mode` | string | no | `auto` | `auto`, `rag`, or `full` |
| `top_k` | integer | no | 8 | RAG results to retrieve (1–50) |

**Response:**
```json
{
  "answer": "Contractor rates are determined by three factors: market benchmarks...",
  "sources": [
    "contractors/rates.md",
    "finance/budget-guidelines.md"
  ],
  "mode_used": "rag"
}
```

---

### `/draft` — POST

Write a file to the vault. Two modes:

- **Direct mode** (default): Writes immediately to the vault filesystem. Zero config. Works
  everywhere. Creates parent directories as needed. Overwrites existing files at the same path.
- **PR mode** (opt-in): Opens a pull request via the configured git provider's REST API.
  Requires git provider env vars. Supported providers: GitHub, GitLab, Gitea/Forgejo.

Direct mode is the universal path — every Ragamuffin deployment supports it. PR mode is
for teams that version their vault and want a human review gate on agent contributions.

**Deletion:** To delete a file, set `content` to an empty string and `target_path` to the
file to remove. In direct mode, the file is deleted from disk. In PR mode, a PR is opened
that removes the file. (Deleting files that don't exist is a no-op, not an error.)

**Request:**
```json
{
  "title": "update contractor rates for Q2 2026",
  "content": "# Contractor Rates\n\nUpdated quarterly...",
  "target_path": "contractors/rates.md",
  "mode": "direct",
  "description": "Market adjustment per Q2 review."
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `title` | string | yes | — | PR title if PR mode; ignored in direct mode |
| `content` | string | yes | — | Complete file content. Empty string to delete. |
| `target_path` | string | yes | — | Vault path relative to vault root |
| `mode` | string | no | `direct` | `direct` or `pr` |
| `description` | string | no | — | Optional PR body (PR mode only) |

**Response (direct mode):**
```json
{
  "mode": "direct",
  "path": "contractors/rates.md",
  "written": true
}
```

**Response (PR mode):**
```json
{
  "mode": "pr",
  "pr_url": "https://github.com/org/vault/pull/42",
  "branch": "ragamuffin/draft-abc123"
}
```

Error (PR mode requested but not configured):
```json
{
  "error": true,
  "code": "GIT_NOT_CONFIGURED",
  "message": "PR mode requires RAGAMUFFIN_GIT_PROVIDER_ENABLED=true and a configured git provider"
}
```

---

### `/audit` — POST

Vault health check. Scans for staleness, semantic conflicts, gaps, and duplicates.

All checks are optional — request only the ones you need. The `stale` check requires no
LLM. The `semantic_conflict` check requires an LLM (random sample of chunk pairs → LLM
comparison, bounded by `sample_size`).

**What semantic conflict detection catches:** Two chunks that are semantically similar
(according to Qdrant) but factually inconsistent (according to the LLM). Example: one file
says "budget is $5,000/month" and another says "budget is $8,000/month" — if both chunks
are about budgets, Qdrant pairs them and the LLM flags the discrepancy.

**What it doesn't catch:** Structured data conflicts across unrelated sections. If
`budget.md` line 43 says "$5,000" and `actuals.md` line 12 says "$50,000" but the chunks
are about different line items, Qdrant won't pair them. That class of contradiction
requires entity extraction and cross-file reconciliation — a Phase 2 feature.

**Request:**
```json
{
  "stale_days": 90,
  "checks": ["stale", "semantic_conflict", "gap", "duplicate"],
  "sample_size": 50
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `stale_days` | integer | no | 90 | Days since last update to flag as stale |
| `checks` | array | no | all four | Which checks to run. `semantic_conflict` requires LLM. |
| `sample_size` | integer | no | 50 | Chunk pairs to LLM-compare for semantic conflicts (1–200) |

**Response:**
```json
{
  "stale_files": [
    {"path": "contractors/old-client.md", "last_updated": "2025-11-03T10:00:00Z", "days_stale": 188}
  ],
  "semantic_conflicts": [
    {
      "chunk_a": {"source_file": "finance/budget.md", "text": "Budget is $5,000/month..."},
      "chunk_b": {"source_file": "finance/actuals.md", "text": "Current spending is $7,200/month..."},
      "summary": "Budget states $5,000/month but actuals report $7,200 — possible overspend or outdated budget."
    }
  ],
  "gaps": [
    "team/engineering/ — directory exists but contains no indexable files"
  ],
  "duplicates": [],
  "checks_run": ["stale", "semantic_conflict", "gap", "duplicate"],
  "sample_size": 50,
  "llm_calls": 3
}
```

---

## Architecture

```
                          ┌──────────────────────┐
                          │       Qdrant          │
                          │    (vector store)     │
                          └──────────┬───────────┘
                                     │
                                     ▼
┌────────────────────────────────────────────────────┐
│                    ragamuffin                        │
│                                                      │
│  ┌──────────┐   ┌──────────┐   ┌────────────────┐  │
│  │ Watcher  │   │ Indexer  │   │  Query Engine  │  │
│  │ (poller) │──▶│ (chunker │──▶│  (RAG, audit,  │  │
│  │          │   │  +embed) │   │   synthesize)  │  │
│  └──────────┘   └──────────┘   └───────┬────────┘  │
│                                        │            │
│                               ┌────────▼────────┐   │
│                               │   HTTP Server   │   │
│                               │   (REST + MCP)  │   │
│                               └────────┬────────┘   │
│                                        │            │
└────────────────────────────────────────┼────────────┘
                                         │
                                    Agents curl
```

### Components

**Watcher** — Polls the vault directory for modified, added, and removed files. Poll interval
is set by `RAGAMUFFIN_WATCH_INTERVAL` (default: 60s). Polling is chosen over inotify because
container filesystem mounts make inotify unreliable. Comparison is by mtime.

- **Added files:** Queued for indexing.
- **Modified files:** Old chunks removed from Qdrant, file re-indexed.
- **Deleted files:** All chunks for that file removed from Qdrant. This is critical — without
  deletion handling, `/recall` returns phantom results for files that no longer exist.
- **Edge cases:** Files modified mid-poll are picked up next cycle. Files added then deleted
  before the next poll are never indexed (no false entries). Files with future timestamps are
  logged but still indexed.

A native file watcher (inotify/fanotify/kqueue) may be added as a Phase 2 option for
host-mounted vaults.

**Indexer** — Splits files into chunks, generates embeddings via the configured embedding API,
and upserts into Qdrant. Chunking strategy depends on file extension (see File Support above).
Incremental — only processes files the watcher flags as changed. On startup, checks Qdrant
for an existing index; if the collection is empty, performs a full re-index. During initial
indexing, `/recall` returns partial results and `/health` reports `"status": "indexing"`.

**Query Engine** — Handles `/recall` (RAG search), `/ask` (LLM synthesis), and `/audit`
(health checks). Runs concurrently with indexing — queries don't block, and index operations
don't lock.

**HTTP Server** — Serves REST endpoints. MCP SSE transport bolts on as an additional route
(`/mcp`). The server is the only entry point — no sidecar, no bridge, no separate process.

## Dependencies

Ragamuffin requires exactly one external service: **Qdrant.** Everything else is optional.

| Service | Required? | What needs it |
|---------|-----------|---------------|
| Qdrant | **Yes** | All search and indexing |
| Embedding API | **Yes** | Indexing and search |
| LLM API | No | `/ask` and `/audit` semantic conflict detection |
| Git provider | No | `/draft` PR mode |

If no LLM is configured, Ragamuffin starts normally. `/recall`, `/stats`, `/health`,
`/draft` (direct mode), and `/audit` (stale/gap/duplicate checks only) all work.
`/ask` and `semantic_conflict` checks return `LLM_NOT_CONFIGURED`.

### Embedding Model

Configured via environment variables. Any OpenAI-compatible embedding API works.

| Env var | Required | Default | Notes |
|---------|----------|---------|-------|
| `RAGAMUFFIN_EMBEDDING_PROVIDER` | no | `openai` | Provider identifier |
| `RAGAMUFFIN_EMBEDDING_MODEL` | no | `text-embedding-3-small` | Model name |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | yes | — | API key. Set to empty string for keyless endpoints (local Ollama, etc.) |
| `RAGAMUFFIN_EMBEDDING_BASE_URL` | no | `https://api.openai.com/v1` | Base URL |

Local embedding is a Phase 2 goal. No production-ready pure-Go embedding library exists
as of May 2026. ONNX requires CGo (static binary lost), pure-Go options are experimental.
When the ecosystem matures, local becomes the default.

### LLM Backend

Optional. Configure via env vars. Supports OpenAI-compatible and Anthropic.

| Env var | Required | Default | Notes |
|---------|----------|---------|-------|
| `RAGAMUFFIN_LLM_PROVIDER` | no | — | `openai_compatible` or `anthropic`. Leave unset to disable LLM features. |
| `RAGAMUFFIN_LLM_BASE_URL` | conditional | — | Required if provider is `openai_compatible` |
| `RAGAMUFFIN_LLM_MODEL` | conditional | — | Required if provider is set |
| `RAGAMUFFIN_LLM_API_KEY` | conditional | — | Required if provider is set |

If `RAGAMUFFIN_LLM_PROVIDER` is unset or empty, LLM features are disabled. Ragamuffin
starts without an LLM and returns `LLM_NOT_CONFIGURED` for `/ask` and semantic conflict
audit checks.

### Git Provider (for /draft PR mode)

Optional. If unconfigured, `/draft` only supports direct mode.

| Env var | Required | Default | Notes |
|---------|----------|---------|-------|
| `RAGAMUFFIN_GIT_PROVIDER_ENABLED` | no | `false` | Set to `true` to enable PR mode |
| `RAGAMUFFIN_GIT_PROVIDER` | no | `github` | `github`, `gitlab`, or `gitea` |
| `RAGAMUFFIN_GIT_TOKEN` | conditional | — | Required if enabled |
| `RAGAMUFFIN_GIT_BASE_BRANCH` | no | `main` | Target branch for PRs |
| `RAGAMUFFIN_GIT_REPOS` | conditional | — | Comma-separated: `owner/repo` |

## Configuration

All configuration is via environment variables. No config file, no YAML, no CLI flags.

```bash
# ── Required ───────────────────────────────────────────────────────────────────
RAGAMUFFIN_VAULT_PATH=/opt/vault
RAGAMUFFIN_QDRANT_URL=http://localhost:6333
RAGAMUFFIN_EMBEDDING_API_KEY=sk-...

# ── Optional ───────────────────────────────────────────────────────────────────
RAGAMUFFIN_WATCH_INTERVAL=60s
RAGAMUFFIN_QDRANT_COLLECTION=ragamuffin
RAGAMUFFIN_EMBEDDING_PROVIDER=openai
RAGAMUFFIN_EMBEDDING_MODEL=text-embedding-3-small
RAGAMUFFIN_EMBEDDING_BASE_URL=https://api.openai.com/v1

# Uncomment to enable LLM features (/ask, semantic conflict audit)
# RAGAMUFFIN_LLM_PROVIDER=openai_compatible
# RAGAMUFFIN_LLM_BASE_URL=https://api.deepseek.com
# RAGAMUFFIN_LLM_MODEL=deepseek/deepseek-chat
# RAGAMUFFIN_LLM_API_KEY=sk-...

# Uncomment to enable PR mode (/draft with mode=pr)
# RAGAMUFFIN_GIT_PROVIDER_ENABLED=true
# RAGAMUFFIN_GIT_PROVIDER=github
# RAGAMUFFIN_GIT_TOKEN=ghp_...
# RAGAMUFFIN_GIT_BASE_BRANCH=main
# RAGAMUFFIN_GIT_REPOS=org/vault

RAGAMUFFIN_PORT=8000
RAGAMUFFIN_HOST=0.0.0.0
RAGAMUFFIN_AUDIT_SAMPLE_SIZE=50
RAGAMUFFIN_LOG_LEVEL=info
```

### Full Variable Reference

| Variable | Required | Default | Notes |
|----------|----------|---------|-------|
| `RAGAMUFFIN_VAULT_PATH` | **yes** | — | Absolute path to the vault directory |
| `RAGAMUFFIN_QDRANT_URL` | **yes** | — | Qdrant server URL |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | **yes** | — | API key. Empty string if endpoint is keyless. |
| `RAGAMUFFIN_WATCH_INTERVAL` | no | `60s` | Poll interval for file changes |
| `RAGAMUFFIN_QDRANT_COLLECTION` | no | `ragamuffin` | Qdrant collection name |
| `RAGAMUFFIN_EMBEDDING_PROVIDER` | no | `openai` | Provider identifier |
| `RAGAMUFFIN_EMBEDDING_MODEL` | no | `text-embedding-3-small` | Model name |
| `RAGAMUFFIN_EMBEDDING_BASE_URL` | no | `https://api.openai.com/v1` | Base URL |
| `RAGAMUFFIN_LLM_PROVIDER` | no | — | `openai_compatible` or `anthropic`. Unset = LLM disabled. |
| `RAGAMUFFIN_LLM_BASE_URL` | conditional | — | Required if LLM provider is `openai_compatible` |
| `RAGAMUFFIN_LLM_MODEL` | conditional | — | Required if LLM provider is set |
| `RAGAMUFFIN_LLM_API_KEY` | conditional | — | Required if LLM provider is set |
| `RAGAMUFFIN_PORT` | no | `8000` | HTTP listen port |
| `RAGAMUFFIN_HOST` | no | `0.0.0.0` | HTTP listen address |
| `RAGAMUFFIN_GIT_PROVIDER_ENABLED` | no | `false` | Enable `/draft` PR mode |
| `RAGAMUFFIN_GIT_PROVIDER` | no | `github` | `github`, `gitlab`, or `gitea` |
| `RAGAMUFFIN_GIT_TOKEN` | conditional | — | Required if enabled |
| `RAGAMUFFIN_GIT_BASE_BRANCH` | no | `main` | Target branch for PRs |
| `RAGAMUFFIN_GIT_REPOS` | conditional | — | Comma-separated: `owner/repo` |
| `RAGAMUFFIN_AUDIT_SAMPLE_SIZE` | no | `50` | Chunk pairs to LLM-compare in semantic conflict audit |
| `RAGAMUFFIN_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

## Non-Goals (v0.1)

- **Multi-tenancy.** One vault per instance. Run multiple containers for multiple vaults.
- **User authentication.** Trust the reverse proxy. API keys may be added later.
- **Chat history.** Each query is stateless. Agents maintain their own context.
- **OpenAI-compatible `/v1/chat` endpoint.** Ragamuffin serves knowledge, not models.
- **System-awareness reconciliation.** Phase 2. Querying live infrastructure against a
  documented system map is powerful but out of scope.
- **Native file watcher.** Polling is simpler and more reliable across Docker mounts.
- **Local embedding inference.** No mature pure-Go library exists. API-based is the
  pragmatic default. This is the first Phase 2 priority.
- **Scheduled auditing.** v0.1 requires an agent or cron job to call `/audit`.
- **Structured contradiction detection.** Entity extraction + cross-file reconciliation
  (catching conflicts between unrelated sections) is Phase 2.
- **Plugin system.**
- **Web UI.**
- **Rust rewrite.**

## Language: Go

Go is chosen over Python and Rust. The decision is final for v0 and v1.

1. **Zero-dependency binary.** `go build` produces a static binary. `FROM scratch` with
   one file. No runtime, no virtualenv, no CGo.
2. **Concurrency without bugs.** Goroutines handle the watcher, indexer, and HTTP server
   in the same process without event-loop races. No `asyncio.create_task` at module level.
3. **Official Qdrant client.** `github.com/qdrant/go-client` is maintained by Qdrant.
   No community crate risk.

**Why not Rust:** No official Qdrant client as of May 2026. Community crates lag.
Rust's performance advantages don't apply to an I/O-bound service. This decision
reopens only if the Qdrant Rust ecosystem ships an official client AND Go embedding
support fails to materialize — two conditions unlikely to coincide.

## Testing

Every endpoint must pass the curl smoke test before merge:

```bash
# Health
curl -sf http://localhost:${RAGAMUFFIN_PORT:-8000}/health

# Recall
curl -s -X POST http://localhost:${RAGAMUFFIN_PORT:-8000}/recall \
  -d '{"query":"contractor rates","top_k":3}'

# Ask (requires LLM)
curl -s -X POST http://localhost:${RAGAMUFFIN_PORT:-8000}/ask \
  -d '{"query":"how are rates determined?","mode":"rag"}'

# Draft (direct mode — universal, zero config)
curl -s -X POST http://localhost:${RAGAMUFFIN_PORT:-8000}/draft \
  -d '{"title":"test","content":"# test","target_path":"test.md","mode":"direct"}'

# Audit (staleness only — no LLM needed)
curl -s -X POST http://localhost:${RAGAMUFFIN_PORT:-8000}/audit \
  -d '{"checks":["stale"],"stale_days":365}'
```

`go test ./...` must pass with ≥ 80% coverage on the query engine and indexer packages.
The test suite spins up a real Qdrant instance via `testcontainers-go` — no mocks.
Integration tests verify the full pipeline: index → recall → ask (if LLM configured) →
audit → draft (direct mode) → delete.

## Repository

`github.com/chezgoulet/ragamuffin` — new repo, not a subdirectory of infra.

## Deployment Notes

Ragamuffin is designed to be deployed as a single container alongside a Qdrant instance.
A sample `docker-compose.yml` and `Dockerfile` will ship in the repo.

For teams migrating from an existing RAG setup (e.g., a Python-based librarian with a
separate MCP bridge), the migration path is:

1. Deploy Ragamuffin alongside the existing stack.
2. Update agent configurations to point at Ragamuffin's `/recall` and `/ask` endpoints.
3. Verify that request and response shapes are compatible or adjust agent code.
4. Once all agents are migrated, retire the old stack.

Ragamuffin does not replicate legacy endpoint paths. It's a clean break — one URL
change per agent, then the old stack comes down.
