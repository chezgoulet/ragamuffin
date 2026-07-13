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
| 401 | `UNAUTHORIZED` | Authentication required (no valid credentials) |
| 403 | `FORBIDDEN` | Authenticated but insufficient access level |
| 404 | `NOT_FOUND` | Requested resource does not exist |
| 405 | `METHOD_NOT_ALLOWED` | Wrong HTTP method for the endpoint |
| 500 | `INTERNAL` | Unexpected failure (check logs) |
| 502 | `QDRANT_UNREACHABLE` | Vector store is down |
| 502 | `EMBEDDING_API_ERROR` | Embedding service returned an error |
| 502 | `LLM_API_ERROR` | LLM backend returned an error |
| 502 | `GIT_PROVIDER_ERROR` | Git provider API returned an error |
| 503 | `LLM_NOT_CONFIGURED` | Endpoint requires an LLM but none is configured |
| 503 | `GIT_NOT_CONFIGURED` | `/draft` PR mode requested but git provider is not configured |
| 503 | `PRUNER_DISABLED` | Pruner is not enabled |
| 503 | `NO_LOGSTORE` | Logstore is not configured |

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
| `detail` | string | no | `l2` | Response detail level: `l0` (no text/first_paragraph), `l1` (first_paragraph only), `l2` (full) |
| `time_filter` | string | no | `active` | Temporal filter: `active` (current index state), `active_at:<RFC3339>`, or `all`. `active_at` limits to chunks indexed before a point in time. |

**Response:**
```json
{
  "results": [
    {
      "chunk_id": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
      "text": "Contractor rates are reviewed quarterly...",
      "first_paragraph": "Contractor rates are reviewed quarterly...",
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

### `/v1/chunks/{chunk_id}` — GET

Retrieve a single chunk by its unique chunk ID. Returns the full payload including
full text, source, and metadata.

**Path parameter:** `chunk_id` — UUID string from the `chunk_id` field in recall results.

**Response:**
```json
{
  "chunk_id": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
  "source_file": "contractors/rates.md",
  "header": "## Review Cycle",
  "text": "Full chunk text...",
  "first_paragraph": "Contractor rates are reviewed quarterly...",
  "chunk_index": 3,
  "file_last_updated": "2026-05-09T10:21:13Z"
}
```

**Error (not found):**
```json
{
  "error": true,
  "code": "NOT_FOUND",
  "message": "chunk with ID a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d not found"
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

### `/v1/facts` — POST

Upsert a structured fact. Facts are key-value pairs with lifecycle fields (confidence,
source, TTL, status, supersession). If the key already exists, its value and metadata
are updated — the `created_at` timestamp is preserved and `confirmation_count` is
incremented.

**Request:**
```json
{
  "key": "org/prefer-rust-cli",
  "value": "All new CLI tools should use Rust",
  "source": "engineering-review-2026-05",
  "source_type": "pr_discussion",
  "tags": ["rust", "cli", "standards"],
  "confidence": 0.85,
  "ttl_days": 365
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `key` | string | yes | — | Fact key (e.g. `org/prefer-rust-cli`). Must be unique. |
| `value` | string | yes | — | The fact value. |
| `source` | string | no | — | Origin reference. |
| `source_type` | string | no | — | One of: `manual`, `pr_discussion`, `agent_observation`, `file`, `conversation`, `code_review`, `automated`. |
| `tags` | array[string] | no | [] | Tags for filtering. |
| `confidence` | float | no | 1.0 | 0.0–1.0. |
| `ttl_days` | integer | no | 0 | Days until auto-expiry. 0 = never. |
| `valid_from` | string | no | `created_at` | RFC 3339 timestamp; when this fact becomes effective |
| `valid_until` | string | no | — | RFC 3339 timestamp; when this fact expires (null = no expiry) |

**Response (200):**
```json
{
  "key": "org/prefer-rust-cli",
  "value": "All new CLI tools should use Rust",
  "tags": ["rust", "cli", "standards"],
  "source": "engineering-review-2026-05",
  "source_type": "pr_discussion",
  "confidence": 0.85,
  "status": "active",
  "version": 0,
  "supersedes": "",
  "superseded_by": 0,
  "contradicts": null,
  "refines": "",
  "supports": null,
  "conflict_resolved": false,
  "confirmation_count": 1,
  "last_confirmed_at": "",
  "created_at": "2026-05-09T10:21:13Z",
  "updated_at": "2026-05-10T14:30:00Z",
  "expires_at": "",
  "valid_from": "2026-05-09T10:21:13Z",
  "valid_until": "",
  "related_chunks": null
}
```

The response includes the complete fact state with all lifecycle fields as stored.

**Error (requires write auth):** Returns 403 `FORBIDDEN` if the caller lacks write access.

---

### `/v1/facts` — PUT

Update/replace a fact identified by `key` query parameter. Accepts partial field updates via the request body. Fields set to null are cleared.

**Query parameter:**

| Parameter | Type | Required | Notes |
|-----------|------|----------|-------|
| `key` | string | yes | Fact key to update |

**Request:**
```json
{
  "value": "Updated CLI policy",
  "status": "active",
  "confidence": 0.95,
  "tags": ["rust", "cli"],
  "source": "review-2026-06",
  "ttl_days": 180,
  "supersedes": "",
  "refines": "",
  "conflict_resolved": false,
  "valid_from": "",
  "valid_until": ""
}
```

All fields are optional. Response is the full `factResponse` shape.

**Error (not found):** Returns `NOT_FOUND` if the fact key doesn't exist.

---

### `/v1/facts` — PATCH

Bulk partial update across multiple facts. Each key in `keys` receives the same set of field updates.

**Request:**
```json
{
  "keys": ["org/prefer-rust-cli", "db/host"],
  "updates": {
    "status": "active",
    "confidence": 0.95
  }
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `keys` | []string | yes | Fact keys to update (max 1000) |
| `updates` | object | yes | Same fields as PUT request (`valid_from`, `valid_until` supported) |

**Response:**
```json
{
  "results": [
    {"key": "org/prefer-rust-cli", "ok": true},
    {"key": "db/host", "ok": true}
  ],
  "total": 2,
  "succeeded": 2,
  "failed": 0
}
```

---

### `/v1/facts` — DELETE

Remove a fact by key.

**Query parameter:**

| Parameter | Type | Required | Notes |
|-----------|------|----------|-------|
| `key` | string | yes | Fact key to delete |

**Response:**
```json
{
  "deleted": true,
  "key": "org/prefer-rust-cli"
}
```

---

### `/v1/facts` — GET

List facts with optional filters. Supports key prefix, tag, and status filters.

**Query parameters:**

| Parameter | Type | Required | Default | Notes |
|-----------|------|----------|---------|-------|
| `key` | string | no | — | Exact key lookup. |
| `prefix` | string | no | — | Key prefix filter. |
| `tag` | string | no | — | Filter by tag value. |
| `status` | string | no | — | One of: `active`, `needs_review`, `superseded`, `rejected`. |
| `time_filter` | string | no | `active` | Temporal filter: `active` (currently valid), `active_at:<RFC3339>`, or `all`. |
| `limit` | integer | no | 100 | Max results (1–1000). |

**Response:**
```json
{
  "entries": [
    {
      "key": "org/prefer-rust-cli",
      "value": "All new CLI tools should use Rust",
      "tags": ["rust", "cli", "standards"],
      "source": "engineering-review-2026-05",
      "source_type": "pr_discussion",
      "confidence": 0.85,
      "status": "active",
      "version": 0,
      "supersedes": "",
      "superseded_by": 0,
      "contradicts": null,
      "refines": "",
      "supports": null,
      "conflict_resolved": false,
      "confirmation_count": 1,
      "last_confirmed_at": "",
      "created_at": "2026-05-09T10:21:13Z",
      "updated_at": "2026-05-10T14:30:00Z",
      "expires_at": "",
      "valid_from": "2026-05-09T10:21:13Z",
      "valid_until": "",
      "related_chunks": null
    }
  ],
  "count": 1
}
```

---

### `/v1/facts/{key}/graph` — GET

Fact-to-fact relationship graph. Given a fact key, returns a directed graph of
connected facts via BFS traversal. Traverses forward relationships (`supersedes`,
`refines`, `contradicts`, `supports`) and reverse edges (`superseded_by`,
`refined_by`, `contradicted_by`, `supported_by`).

**Path parameter:** `key` — Fact key to root the graph.

**Query parameters:**

| Parameter | Type | Required | Default | Notes |
|-----------|------|----------|---------|-------|
| `depth` | integer | no | 1 | BFS depth (0–5). Depth 0 returns just the root node. |

**Response:**
```json
{
  "key": "db/host",
  "nodes": [
    {"key": "db/host", "value": "postgres.internal", "fact_type": "current"},
    {"key": "db/host/v2", "value": "postgres-v2.internal", "fact_type": "supersedes"}
  ],
  "edges": [
    {"source": "db/host", "target": "db/host/v2", "relationship": "supersedes"}
  ]
}
```

**Error (fact not found):** Returns `NOT_FOUND` if the root fact key doesn't exist.

Routes: `GET /v1/facts/{key}/graph` (global) and `GET /vault/{name}/v1/facts/{key}/graph` (vault-scoped).

---

### `/v1/pruner/auto-tune` — GET

Returns adaptive threshold recommendations based on review resolution history.
Operators can preview changes with `?dry_run=true` (default) or apply by passing
`?dry_run=false`.

**Response (200):**
```json
{
  "dry_run": true,
  "recommendations": [
    {
      "reason_type": "conflict",
      "current": 0.85,
      "recommended": 0.90,
      "accept_rate": 0.67,
      "sample_size": 12,
      "rationale": "67% of conflict flags were rejected; consider raising similarity threshold from 0.85 to 0.90",
      "applied": false,
      "note": "conflict threshold is embedded in scanner logic; adjust manually in config"
    },
    {
      "reason_type": "low_confidence",
      "current": 0.5,
      "recommended": 0.45,
      "accept_rate": 0.72,
      "sample_size": 8,
      "rationale": "72% of low confidence flags were confirmed; consider lowering threshold from 0.50 to 0.45",
      "applied": true,
      "note": ""
    }
  ],
  "sample_count": 2
}
```

**Error (503):** Returns `PRUNER_DISABLED` if the pruner is not enabled.

---

### `/v1/pruner/config` — GET

Returns the current pruner configuration.

**Response (200):**
```json
{
  "enabled": false,
  "stale_days": 90,
  "conflict_sample_size": 50,
  "conflict_threshold": 0.85,
  "low_confidence_threshold": 0.5,
  "importance_threshold": 0.0
}
```

Requires admin access.

---

### `/v1/review` — GET, POST

Fact review queue. **GET** lists facts with `status = needs_review`. **POST** resolves a flagged fact.

#### `GET /v1/review`

**Query parameters:**

| Parameter | Type | Required | Default | Notes |
|-----------|------|----------|---------|-------|
| `reason` | string | no | — | Filter by review reason: `stale`, `contradiction`, `low_confidence`, `supersession` |
| `tag` | string | no | — | Filter by tag |
| `source_type` | string | no | — | Filter by source type |
| `min_confidence` | float | no | — | Only facts with confidence below this value |
| `limit` | int | no | 50 | Max results (1–100) |
| `before` | string | no | — | Cursor pagination token |

**Response:**
```json
{
  "entries": [
    {
      "key": "org/prefer-rust-cli",
      "value": "All new CLI tools should use Rust",
      "tags": ["rust"],
      "confidence": 0.4,
      "status": "needs_review",
      "review_reasons": [
        {"type": "low_confidence", "detail": "confidence 0.40 is below threshold 0.50"}
      ],
      "last_confirmed_at": "",
      "created_at": "2026-05-09T10:21:13Z",
      "updated_at": "2026-05-10T14:30:00Z"
    }
  ]
}
```

#### `POST /v1/review?key=<fact_key>`

Resolve a flagged fact. Requires write access.

**Request:**
```json
{
  "action": "confirm",
  "confidence": 0.9,
  "note": "Verified by manual review"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `action` | string | yes | `confirm` (approve), `supersede` (replace with new value), `reject` (mark rejected), `reclassify` (re-tag) |
| `confidence` | float | no | New confidence value |
| `note` | string | no | Review note logged to resolution history |
| `new_key` | string | conditional | Required for `supersede` — new fact key |
| `new_value` | string | conditional | Required for `supersede` — new fact value |
| `ttl_days` | int | no | Update TTL |
| `tags` | []string | no | Replace tags |

**Response:** Returns updated `factResponse` on success. Errors: `FORBIDDEN`, `NOT_FOUND`, `MISSING_ACTION`, `SUPERSEDE_CREATE_FAILED`.

---

### `/v1/review/stats` — GET

Review queue statistics. Returns counts broken down by reason type and source type.

**Response:**
```json
{
  "total_needs_review": 12,
  "by_reason": {
    "stale": 5,
    "contradiction": 3,
    "low_confidence": 3,
    "supersession": 1
  },
  "by_source_type": {
    "pr_discussion": 6,
    "agent_observation": 4,
    "manual": 2
  },
  "oldest_item": "2026-05-01T10:00:00Z",
  "avg_pending_days": 14.2
}
```

---

### `/v1/verify` — POST

Knowledge validation endpoint. Accepts a fact statement and returns validation results from the vault — whether it checks out against existing knowledge, conflicts, or needs more context. Optionally uses LLM synthesis for conflict resolution summary.

**Request:**
```json
{
  "fact": "All engineers must use 2FA",
  "top_k": 10
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `fact` | string | yes | — | The fact statement to validate |
| `top_k` | integer | no | 10 | Max sources to retrieve (1–50) |

**Response:**
```json
{
  "status": "confirmed",
  "supporting_sources": [
    {"source_file": "policies/security.md", "text": "2FA is mandatory for all engineering staff...", "score": 0.87}
  ],
  "conflicting_sources": [],
  "confidence": 1.0,
  "conflict_summary": "",
  "llm_used": false
}
```

| Status | Meaning |
|--------|---------|
| `confirmed` | More supporting than conflicting evidence |
| `conflicts` | Conflicting or borderline evidence exists; `conflict_summary` populated when LLM is configured |
| `insufficient_data` | No semantically similar chunks found |

Routes: `POST /v1/verify` (global) and `POST /vault/{name}/v1/verify` (vault-scoped).

---

### `/v1/ingest` — POST

Ingest a document or observation into the vault. Chunks are created, embedded, and stored in Qdrant. Requires write access.

**Request:**
```json
{
  "vault": "docs",
  "content": "# New Policy\n\nAll engineers must use 2FA.",
  "source": "internal-memo-2026-06",
  "tags": ["security", "policy"]
}
```

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `vault` | string | no | `default` | Target vault name (multi-tenant only) |
| `content` | string | yes | — | Document content to index |
| `source` | string | yes | — | Source path/identifier for the content |
| `tags` | []string | no | — | Optional tags |

**Response:**
```json
{
  "source": "internal-memo-2026-06",
  "chunk_count": 5
}
```

Max request body: 10 MB.

---

### `/v1/ingest/conversation` — POST

Ingest an agent conversation into the session logstore. Creates a session with turns.

**Request:**
```json
{
  "agent": "review-bot",
  "content": "Reviewed 12 pending facts, confirmed 8, rejected 4.",
  "role": "assistant",
  "tags": ["review-summary"]
}
```

---

### `/v1/sessions` — GET, POST

Agent session management. Sessions group related turns from an agent's interaction.

#### `GET /v1/sessions`

**Query parameters:**

| Parameter | Type | Required | Default | Notes |
|-----------|------|----------|---------|-------|
| `agent_id` | string | no | — | Filter by agent ID |
| `vault` | string | no | — | Filter by vault name |
| `limit` | int | no | 100 | Max results (1–1000) |
| `before` | string | no | — | Cursor pagination |

**Response:**
```json
{
  "sessions": [
    {
      "id": "uuid",
      "agent_id": "review-bot",
      "turn_count": 3,
      "source": "review-session-1",
      "vault": "default",
      "created_at": "2026-05-10T10:00:00Z",
      "updated_at": "2026-05-10T10:05:00Z"
    }
  ],
  "count": 1
}
```

#### `POST /v1/sessions`

**Request:**
```json
{
  "agent_id": "review-bot",
  "content": "Initial analysis...",
  "source": "review-session-1",
  "vault": "default"
}
```

**Response:** Returns the created `sessionResponse` with an `id` and `turn_count` of 1.

#### `GET /v1/sessions/{id}`

Retrieve a session with its turns. Returns the same shape as the list response plus a `turns` array.

#### `POST /v1/sessions/{id}/turns`

Append a turn to an existing session.

**Request:**
```json
{
  "content": "Additional analysis...",
  "role": "assistant"
}
```

#### `DELETE /v1/sessions/{id}`

Delete a session and its turns.

### `/v1/conversations` — POST (v0.8, temporarily deferred)

> **Note:** These endpoints are documented but the handler implementation was deferred during review. They will be restored in the next release.
>
> Use the sessions API (`POST /v1/sessions`, `POST /v1/sessions/{id}/turns`) instead, which supports the same `auto_extract` parameter.

Create a conversation session with optional automatic extraction.

**Request (auto-extract enabled):**
```json
{
  "agent_id": "support-bot",
  "auto_extract": true,
  "content": "Hello, how can I help you?"
}
```

**Response (201):**
```json
{
  "session_id": "uuid",
  "vault": "agent::support-bot",
  "agent_id": "support-bot",
  "turn_count": 1,
  "auto_extract": true,
  "created_at": "2026-06-06T15:00:00Z"
}
```

| Field | Type | Description |
|---|---|---|
| `agent_id` | string | **Required.** Agent identity |
| `auto_extract` | bool | Enable automatic fact extraction for all turns in this session |
| `content` | string | Optional initial message |
| `vault` | string | Target vault (default: `agent::{agent_id}`) |

### `/v1/conversations/{id}/turns` — POST (v0.8)

Append a turn to an existing conversation session. When `auto_extract` is `true` and the extraction pipeline is enabled, extraction runs asynchronously.

**Request:**
```json
{
  "content": "I prefer Rust over Go for CLI tools",
  "role": "user",
  "auto_extract": true
}
```

| Field | Type | Description |
|---|---|---|
| `content` | string | **Required.** Turn content (max 10 MB) |
| `role` | string | `user`, `assistant`, or `system` (default: `user`) |
| `auto_extract` | bool | Override session-level auto_extract for this turn |

### `/v1/extraction/stats` — GET (v0.8)

Pipeline statistics for the automatic extraction system.

**Response:**
```json
{
  "total_attempted": 12,
  "facts_created": 8,
  "facts_skipped": 3,
  "facts_rejected": 1,
  "avg_confidence": 0.72,
  "last_extraction": "2026-06-06T15:05:00Z"
}
```

---

### `/v1/logs` — GET, POST

System log store. Append entries via POST, query via GET. Backed by the SQLite logstore.

#### `POST /v1/logs`

Max body: 64 KB.

**Request:**
```json
{
  "agent": "review-bot",
  "type": "summary",
  "body": "Reviewed 12 pending facts.",
  "tags": ["review"],
  "timestamp": "2026-06-06T15:00:00Z"
}
```

**Response:**
```json
{
  "id": 12345,
  "inserted": true
}
```

#### `GET /v1/logs`

**Query parameters:**

| Parameter | Type | Notes |
|-----------|------|-------|
| `agent` | string | Filter by agent identifier |
| `type` | string | Filter by log type |
| `tag` | string | Filter by tag value |
| `since` | string | ISO8601 start time |
| `until` | string | ISO8601 end time |
| `before` | string | Cursor: entries before this ID |
| `limit` | int | Max results (1–1000, default 100) |

**Response (200):**
```json
{
  "entries": [
    {
      "id": 12345,
      "agent": "review-bot",
      "type": "summary",
      "body": "Reviewed 12 pending facts.",
      "tags": ["review"],
      "timestamp": "2026-06-06T15:00:00Z"
    }
  ],
  "count": 1
}
```

---

### `/v1/auth/check` — GET, POST

Check current authentication status. Returns access level and vault permissions.

**Response (authenticated):**
```json
{
  "authenticated": true,
  "access": "write",
  "vaults": ["docs", "code"]
}
```

**Response (unauthenticated):**
```json
{
  "authenticated": false
}
```

---

### `/v1/snapshot` — GET

Downloads the vault as a gzip-compressed tar archive. Requires write access. Skips the `.ragamuffin/` operational metadata directory.

Response is `Content-Type: application/gzip` with `Content-Disposition: attachment; filename="vault-YYYY-MM-DD.tar.gz"`.

---

### `/inbox` — POST

Create an inbox entry in the vault. Inbox entries are JSON files stored under `_inbox/` in the vault directory.

**Query parameter:** `vault` (string, optional) — vault name for multi-tenant.

**Request:**
```json
{
  "content": "New onboarding document needed for JSON-RPC API",
  "source": "meeting-notes",
  "tags": ["documentation", "api"],
  "vault": "docs"
}
```

**Response:**
```json
{
  "id": "new-onboarding-document",
  "created": true
}
```

Max request body: 256 KB.

### `/inbox/{id}` — GET, DELETE

Read or delete an inbox entry by its slugified ID.

**Response (GET):**
```json
{
  "id": "new-onboarding-document",
  "content": "New onboarding document needed for JSON-RPC API",
  "source": "meeting-notes",
  "tags": ["documentation", "api"],
  "created_at": "2026-06-06T15:00:00Z",
  "processed": false
}
```

---

### `/version` — GET

Server version information.

**Response:**
```json
{
  "version": "0.7.0",
  "commit": "abc1234",
  "build_date": "2026-06-06T12:00:00Z",
  "go_version": "go1.25.0"
}
```

---

### `/metrics` — GET

Prometheus-format metrics. Returns `ragamuffin_requests_total{endpoint,status}` counters per endpoint and HTTP status, plus indexed file and chunk counts.

```
Content-Type: text/plain; version=0.0.4

# HELP ragamuffin_requests_total Total HTTP requests by endpoint and status.
# TYPE ragamuffin_requests_total counter
ragamuffin_requests_total{endpoint="/recall",status="200"} 142
ragamuffin_requests_total{endpoint="/health",status="200"} 38
# HELP ragamuffin_files_total Total indexed files.
# TYPE ragamuffin_files_total gauge
ragamuffin_files_total 247
# HELP ragamuffin_chunks_total Total indexed chunks.
# TYPE ragamuffin_chunks_total gauge
ragamuffin_chunks_total 1893
```

---

### `/vaults` — GET, POST

List or create vaults. For multi-tenant deployments.

#### `GET /vaults`

Lists all configured vaults with their indexing stats.

**Response (single-tenant):**
```json
{
  "vaults": [
    {
      "name": "default",
      "path": "/opt/vault",
      "indexed_files": 247,
      "total_chunks": 1893,
      "last_indexed": "2026-06-06T12:00:00Z",
      "indexing": false
    }
  ]
}
```

#### `POST /vaults`

Create a new vault (requires write access).

**Request:**
```json
{
  "name": "projects",
  "path": "/data/vaults/projects"
}
```

---

### `/events` — GET

Server-Sent Events stream of vault and lifecycle CloudEvents. SSE format:

```
event: fact.created
data: {"specversion":"1.0","type":"fact.created","source":"/ragamuffin","subject":"org/prefer-rust-cli","time":"2026-06-06T15:00:00Z"}

```

Event types: `fact.created`, `fact.flagged`, `fact.reviewed`, `pruner.scan.complete`, `vault.file.changed`, `vault.file.deleted`, `vault.collection.reindexed`, `ragamuffin.started`, `ragamuffin.healthy`, `extraction_complete`.

Slow consumer protection: a buffer of 64 events per subscriber. Older events are dropped if the buffer fills.

---

### `/reindex` — POST

Force a full re-index of the vault. Requires write access. Returns 200 `{"reindexed": true}` on success. Returns 409 if already indexing.

---

### `/mcp` — SSE

Model Context Protocol SSE transport. Provides tool-based access to Ragamuffin's core operations for MCP-compatible agents (e.g., Claude Desktop). Includes tools for recall, ask, draft, fact management, chunk retrieval, and session management.

Connection is maintained as an open SSE stream. Tools are called via JSON-RPC messages over the same connection.

---

### Vault-scoped Endpoints

All global endpoints have vault-scoped variants under `/vault/{name}/`:

| Vault endpoint | Equivalent global |
|---|---|
| `POST /vault/{name}/recall` | `/recall` |
| `POST /vault/{name}/ask` | `/ask` |
| `POST /vault/{name}/draft` | `/draft` |
| `POST /vault/{name}/audit` | `/audit` |
| `GET,POST /vault/{name}/v1/facts` | `/v1/facts` |
| `GET /vault/{name}/v1/facts/{key}/graph` | `/v1/facts/{key}/graph` |
| `GET /vault/{name}/v1/logs` | `/v1/logs` |
| `GET /vault/{name}/v1/snapshot` | `/v1/snapshot` |
| `POST /vault/{name}/reindex` | `/reindex` |
| `GET /vault/{name}/graph` | `/graph` |
| `POST /vault/{name}/inbox` | `/inbox` |
| `GET,DELETE /vault/{name}/inbox/{id}` | `/inbox/{id}` |
| `POST /vault/{name}/v1/verify` | `/v1/verify` |

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

### Automatic Extraction (v0.8)

Optional — enables autonomous fact extraction from conversation turns. Defaults to disabled.

| Env var | Required | Default | Notes |
|---------|----------|---------|-------|
| `RAGAMUFFIN_EXTRACT_ENABLED` | no | `false` | Master switch |
| `RAGAMUFFIN_EXTRACT_WINDOW` | no | `10` | Preceding turns in LLM context |
| `RAGAMUFFIN_EXTRACT_MAX_CONFIDENCE` | no | `0.85` | Hard ceiling for LLM-assigned confidence |
| `RAGAMUFFIN_EXTRACT_DEDUP_THRESHOLD` | no | `0.85` | Cosine similarity threshold for duplicate detection |
| `RAGAMUFFIN_EXTRACT_CONCURRENCY` | no | `2` | Max concurrent extraction workers |
| `RAGAMUFFIN_EXTRACT_PER_SESSION_COOLDOWN` | no | `30` | Seconds between extractions for the same session |

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
RAGAMUFFIN_QDRANT_URL=http://localhost:6334
RAGAMUFFIN_EMBEDDING_API_KEY=sk-...
# Single-tenant vault path (mutually exclusive with VAULTS)
RAGAMUFFIN_VAULT_PATH=/opt/vault
# Multi-tenant vaults (mutually exclusive with VAULT_PATH)
# RAGAMUFFIN_VAULTS=docs:/path/to/docs,code:/path/to/code
# RAGAMUFFIN_VAULTS_ROOT=/opt/vaults           # Restricts runtime vault creation to this path prefix

# ── Optional — Embedding ──────────────────────────────────────────────────────
RAGAMUFFIN_EMBEDDING_PROVIDER=openai
RAGAMUFFIN_EMBEDDING_MODEL=text-embedding-3-small
RAGAMUFFIN_EMBEDDING_BASE_URL=https://api.openai.com/v1
RAGAMUFFIN_EMBEDDING_DIMS=1536
# RAGAMUFFIN_EMBEDDING_TIMEOUT=30s
RAGAMUFFIN_CHUNK_VECTOR_SIZE=0           # 0 = inherit from EMBEDDING_DIMS

# ── Optional — Qdrant ─────────────────────────────────────────────────────────
RAGAMUFFIN_QDRANT_COLLECTION=ragamuffin
RAGAMUFFIN_FACTS_COLLECTION=ragamuffin_facts
RAGAMUFFIN_FACTS_VECTOR_SIZE=4

# ── Optional — Watcher ────────────────────────────────────────────────────────
RAGAMUFFIN_WATCH_INTERVAL=60s
RAGAMUFFIN_WATCHER_MODE=poll

# ── Optional — Chunking ───────────────────────────────────────────────────────
# RAGAMUFFIN_CHUNK_STRATEGY=auto               # auto | fixed | paragraph
# RAGAMUFFIN_CHUNK_MAX_TOKENS=2000
# RAGAMUFFIN_CHUNK_FIXED_SIZE=0
# RAGAMUFFIN_CHUNK_FIXED_OVERLAP=0

# ── Optional — LLM (enables /ask, semantic conflict audit) ────────────────────
# RAGAMUFFIN_LLM_PROVIDER=openai_compatible
# RAGAMUFFIN_LLM_BASE_URL=https://api.deepseek.com
# RAGAMUFFIN_LLM_MODEL=deepseek/deepseek-chat
# RAGAMUFFIN_LLM_API_KEY=sk-...
# RAGAMUFFIN_LLM_TIMEOUT=120s

# ── Optional — Automatic Extraction ───────────────────────────────────────────
# RAGAMUFFIN_EXTRACT_ENABLED=false
# RAGAMUFFIN_EXTRACT_WINDOW=10
# RAGAMUFFIN_EXTRACT_MAX_CONFIDENCE=0.85
# RAGAMUFFIN_EXTRACT_DEDUP_THRESHOLD=0.85
# RAGAMUFFIN_EXTRACT_CONCURRENCY=2
# RAGAMUFFIN_EXTRACT_PER_SESSION_COOLDOWN=30

# ── Optional — Git Provider (enables /draft PR mode) ──────────────────────────
# RAGAMUFFIN_GIT_PROVIDER_ENABLED=true
# RAGAMUFFIN_GIT_PROVIDER=github
# RAGAMUFFIN_GIT_TOKEN=ghp_...
# RAGAMUFFIN_GIT_BASE_BRANCH=main
# RAGAMUFFIN_GIT_BASE_URL=              # Required for Gitea only
# RAGAMUFFIN_GIT_REPOS=owner/repo

# ── Optional — Auth ───────────────────────────────────────────────────────────
RAGAMUFFIN_AUTH_MODE=none                     # none | api_key | jwt | oidc
# RAGAMUFFIN_AUTH_READ_KEY=sk-...              # Read-only key (api_key mode)
# RAGAMUFFIN_AUTH_WRITE_KEY=sk-...             # Read+Write key (api_key mode)
# RAGAMUFFIN_AUTH_JWT_ISSUER=https://auth.example.com
# RAGAMUFFIN_AUTH_JWT_AUDIENCE=ragamuffin
# RAGAMUFFIN_AUTH_JWT_JWKS_URL=https://auth.example.com/.well-known/jwks.json
# RAGAMUFFIN_AUTH_OIDC_ISSUER=https://accounts.example.com
# RAGAMUFFIN_AUTH_OIDC_CLIENT_ID=ragamuffin
# RAGAMUFFIN_AUTO_PROVISION_VAULTS=true

# ── Optional — Rate Limiting ───────────────────────────────────────────────────
# RAGAMUFFIN_RATE_LIMIT_ENABLED=true
RAGAMUFFIN_RATE_LIMIT_RECALL=60
RAGAMUFFIN_RATE_LIMIT_ASK=10
RAGAMUFFIN_RATE_LIMIT_DRAFT=30
RAGAMUFFIN_RATE_LIMIT_AUDIT=5
RAGAMUFFIN_RATE_LIMIT_FACTS=30
RAGAMUFFIN_RATE_LIMIT_LOGS=60
RAGAMUFFIN_RATE_LIMIT_SNAPSHOT=5
RAGAMUFFIN_RATE_LIMIT_REINDEX=30
RAGAMUFFIN_RATE_LIMIT_INGEST=30
RAGAMUFFIN_RATE_LIMIT_CHUNKS=30
RAGAMUFFIN_RATE_LIMIT_REVIEW=30

# ── Optional — Pruner (background fact health) ────────────────────────────────
# RAGAMUFFIN_PRUNER_ENABLED=true
RAGAMUFFIN_PRUNER_STALE_INTERVAL=24h
RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL=72h
RAGAMUFFIN_PRUNER_SUPERSEDE_INTERVAL=24h
RAGAMUFFIN_PRUNER_SOURCE_STALE_INTERVAL=24h
# RAGAMUFFIN_PRUNER_EXPIRED_INTERVAL=24h — how often to scan for facts past valid_until
RAGAMUFFIN_PRUNER_EXPIRED_INTERVAL=24h
RAGAMUFFIN_PRUNER_STALE_DAYS=90
RAGAMUFFIN_PRUNER_CONFLICT_SAMPLE_SIZE=50
RAGAMUFFIN_PRUNER_CONFLICT_THRESHOLD=0.85
RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD=0.5
RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD=0.0

# ── Optional — Misc ───────────────────────────────────────────────────────────
RAGAMUFFIN_PORT=8000
RAGAMUFFIN_HOST=0.0.0.0
RAGAMUFFIN_LOG_LEVEL=info
# RAGAMUFFIN_EVENT_WEBHOOK_URL=https://hooks.example.com/ragamuffin
# RAGAMUFFIN_EVENT_WEBHOOK_EVENTS=fact.created,fact.flagged,fact.reviewed
# RAGAMUFFIN_RESTORE_MISMATCH_THRESHOLD=0.1

# ── Optional — Log Store (session/event persistence) ─────────────────────────
# RAGAMUFFIN_LOGSTORE_PATH=/opt/data/ragamuffin/logs.db
# RAGAMUFFIN_LOGSTORE_MAX_ROWS=100000

# RAGAMUFFIN_AUDIT_SAMPLE_SIZE=50
# RAGAMUFFIN_AUTO_THRESHOLD=0.75
# RAGAMUFFIN_AUDIT_ENTITY_EXTRACTION=false
```

### Full Variable Reference

| Variable | Required | Default | Notes |
|----------|----------|---------|-------|
| `RAGAMUFFIN_QDRANT_URL` | **yes** | — | Qdrant gRPC server URL |
| `RAGAMUFFIN_EMBEDDING_API_KEY` | **yes** | — | API key. Empty string if endpoint is keyless. |
| `RAGAMUFFIN_VAULT_PATH` | conditional | — | **Single-tenant:** absolute path to vault dir. Mutually exclusive with `VAULTS`. |
| `RAGAMUFFIN_VAULTS` | conditional | — | **Multi-tenant:** `name:path,name:path,...`. Mutually exclusive with `VAULT_PATH`. |
| `RAGAMUFFIN_WATCH_INTERVAL` | no | `60s` | Poll interval for file changes |
| `RAGAMUFFIN_WATCHER_MODE` | no | `poll` | `poll` or `inotify` |
| `RAGAMUFFIN_QDRANT_COLLECTION` | no | `ragamuffin` | Chunk/document Qdrant collection name |
| `RAGAMUFFIN_FACTS_COLLECTION` | no | `ragamuffin_facts` | Structured facts Qdrant collection name |
| `RAGAMUFFIN_FACTS_VECTOR_SIZE` | no | falls back to `EMBEDDING_DIMS` → `1536` | Fact point vector dimension. If unset, uses `EMBEDDING_DIMS` (default 1536). Must match existing collection dimension. |
| `RAGAMUFFIN_EMBEDDING_PROVIDER` | no | `openai` | Provider identifier |
| `RAGAMUFFIN_EMBEDDING_MODEL` | no | `text-embedding-3-small` | Model name |
| `RAGAMUFFIN_EMBEDDING_BASE_URL` | no | `https://api.openai.com/v1` | Base URL (include `/v1`) |
| `RAGAMUFFIN_EMBEDDING_DIMS` | no | `1536` | Embedding dimension (auto-detected, override to pin) |
| `RAGAMUFFIN_EMBEDDING_TIMEOUT` | no | `30s` | Embedding HTTP client timeout (Go duration, e.g. `60s`, `2m`) |
| `RAGAMUFFIN_CHUNK_VECTOR_SIZE` | no | `0` | Vector dimension for chunk/doc cols. `0` = inherit from `EMBEDDING_DIMS`. |
| `RAGAMUFFIN_CHUNK_STRATEGY` | no | `auto` | `auto` (content-aware), `fixed` (token window), `paragraph` |
| `RAGAMUFFIN_CHUNK_MAX_TOKENS` | no | `2000` | Max tokens per chunk |
| `RAGAMUFFIN_CHUNK_FIXED_SIZE` | no | `0` | Token window for `fixed` strategy |
| `RAGAMUFFIN_CHUNK_FIXED_OVERLAP` | no | `0` | Overlap tokens for `fixed` strategy |
| `RAGAMUFFIN_LLM_PROVIDER` | no | — | `openai_compatible` or `anthropic`. Unset = LLM disabled. |
| `RAGAMUFFIN_LLM_BASE_URL` | conditional | `https://api.deepseek.com` | Omit `/v1` (appended internally). |
| `RAGAMUFFIN_LLM_MODEL` | conditional | — | Required if LLM provider is set |
| `RAGAMUFFIN_LLM_API_KEY` | conditional | — | Required if LLM provider is set |
| `RAGAMUFFIN_LLM_TIMEOUT` | no | `120s` | LLM request timeout |
| `RAGAMUFFIN_GIT_PROVIDER_ENABLED` | no | `false` | Enable `/draft` PR mode |
| `RAGAMUFFIN_GIT_PROVIDER` | no | `github` | `github`, `gitlab`, or `gitea` |
| `RAGAMUFFIN_GIT_TOKEN` | conditional | — | Required if git provider enabled |
| `RAGAMUFFIN_GIT_BASE_BRANCH` | no | `main` | Target branch for PRs |
| `RAGAMUFFIN_GIT_BASE_URL` | no | — | Git provider base URL (required for Gitea) |
| `RAGAMUFFIN_GIT_REPOS` | conditional | — | Comma-separated: `owner/repo` |
| `RAGAMUFFIN_AUTH_MODE` | no | `none` | `none`, `api_key`, `jwt`, or `oidc` |
| `RAGAMUFFIN_AUTH_READ_KEY` | no | — | Read-only API key (`api_key` mode) |
| `RAGAMUFFIN_AUTH_WRITE_KEY` | no | — | Read+write API key (`api_key` mode) |
| `RAGAMUFFIN_AUTH_JWT_ISSUER` | no | — | JWT issuer URL (`jwt` mode) |
| `RAGAMUFFIN_AUTH_JWT_AUDIENCE` | no | — | Expected JWT audience (`jwt` mode) |
| `RAGAMUFFIN_AUTH_JWT_JWKS_URL` | no | — | JWKS endpoint URL (`jwt` mode) |
| `RAGAMUFFIN_AUTH_OIDC_ISSUER` | no | — | OIDC issuer URL (`oidc` mode — auto-discovers JWKS) |
| `RAGAMUFFIN_AUTH_OIDC_CLIENT_ID` | no | — | OIDC client ID (`oidc` mode) |
| `RAGAMUFFIN_AUTO_PROVISION_VAULTS` | no | `false` | Enable auto-provisioning for `/v1/ingest` (requires write access) |
| `RAGAMUFFIN_RATE_LIMIT_ENABLED` | no | `false` | Enable per-endpoint rate limiting |
| `RAGAMUFFIN_RATE_LIMIT_RECALL` | no | `60` | Requests per minute for `/recall` |
| `RAGAMUFFIN_RATE_LIMIT_ASK` | no | `10` | Requests per minute for `/ask` |
| `RAGAMUFFIN_RATE_LIMIT_DRAFT` | no | `30` | Requests per minute for `/draft` |
| `RAGAMUFFIN_RATE_LIMIT_AUDIT` | no | `5` | Requests per minute for `/audit` |
| `RAGAMUFFIN_RATE_LIMIT_FACTS` | no | `30` | Requests per minute for `/v1/facts` |
| `RAGAMUFFIN_RATE_LIMIT_LOGS` | no | `60` | Requests per minute for `/v1/logs` |
| `RAGAMUFFIN_RATE_LIMIT_SNAPSHOT` | no | `5` | Requests per minute for `/v1/snapshot` |
| `RAGAMUFFIN_RATE_LIMIT_REINDEX` | no | `30` | Requests per minute for `/reindex` |
| `RAGAMUFFIN_RATE_LIMIT_CHUNKS` | no | `30` | Requests per minute for `/v1/chunks` |
| `RAGAMUFFIN_RATE_LIMIT_INGEST` | no | `30` | Requests per minute for `/v1/ingest` |
| `RAGAMUFFIN_RATE_LIMIT_REVIEW` | no | `30` | Requests per minute for `/v1/review` |
| `RAGAMUFFIN_PRUNER_ENABLED` | no | `false` | Enable background pruner for fact health |
| `RAGAMUFFIN_PRUNER_STALE_INTERVAL` | no | `24h` | How often to scan for stale facts |
| `RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL` | no | `72h` | How often to scan for semantic conflicts |
| `RAGAMUFFIN_PRUNER_SUPERSEDE_INTERVAL` | no | `24h` | How often to scan for supersession chains |
| `RAGAMUFFIN_PRUNER_SOURCE_STALE_INTERVAL` | no | `24h` | How often to check fact source staleness |
| `RAGAMUFFIN_PRUNER_EXPIRED_INTERVAL` | no | `24h` | How often to scan for facts with `valid_until` in the past |
| `RAGAMUFFIN_PRUNER_STALE_DAYS` | no | `90` | Days without update before marked stale |
| `RAGAMUFFIN_PRUNER_CONFLICT_SAMPLE_SIZE` | no | `50` | Fact pairs to compare per conflict scan |
| `RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD` | no | `0.5` | Below this → flag `needs_review` |
| `RAGAMUFFIN_PRUNER_CONFLICT_THRESHOLD` | no | `0.85` | Cosine similarity threshold for contradiction detection |
| `RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD` | no | `0.0` | Facts above this skip stale scan (0.0 = disabled) |
| `RAGAMUFFIN_VAULTS_ROOT` | no | — | Root directory for multi-tenant vaults (used with `VAULTS` format) |
| `RAGAMUFFIN_RESTORE_MISMATCH_THRESHOLD` | no | `0.1` | Snapshot restore drift tolerance (0.0–1.0) |
| `RAGAMUFFIN_LOGSTORE_PATH` | no | heuristic | Explicit path for `logs.db` (default: vault-root `/.ragamuffin/logs.db`) |
| `RAGAMUFFIN_LOGSTORE_MAX_ROWS` | no | `100000` | Max rows before pruning session log |
| `RAGAMUFFIN_EVENT_WEBHOOK_URL` | no | — | CloudEvents v1.0 webhook for fact lifecycle |
| `RAGAMUFFIN_EVENT_WEBHOOK_EVENTS` | no | all | Comma-separated event types to POST to webhook (e.g. `fact.created,fact.reviewed`). Empty = all events. |
| `RAGAMUFFIN_AUDIT_SAMPLE_SIZE` | no | `50` | Chunk pairs to LLM-compare in semantic conflict audit |
| `RAGAMUFFIN_AUDIT_ENTITY_EXTRACTION` | no | `false` | Enable LLM entity extraction in audit |
| `RAGAMUFFIN_AUTO_THRESHOLD` | no | `0.75` | Threshold for auto-decisions |
| `RAGAMUFFIN_PORT` | no | `8000` | HTTP listen port |
| `RAGAMUFFIN_HOST` | no | `0.0.0.0` | HTTP listen address |
| `RAGAMUFFIN_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

## Non-Goals

- **OpenAI-compatible `/v1/chat` endpoint.** Ragamuffin serves knowledge, not models.
- **System-awareness reconciliation.** Querying live infrastructure against a
  documented system map is powerful but out of scope.
- **Graph database.** The knowledge graph is derived from embeddings + chunk links, not a
  dedicated graph store.
- **Full-text search.** Ragamuffin is semantic-first.
  No inverted index, no TF-IDF.
- **Data at rest encryption.** Relies on filesystem and Qdrant encryption. Ragamuffin
  handles auth at the API layer only.
- **Streaming responses.** `/ask` returns complete answers. Streaming may be added later.
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
