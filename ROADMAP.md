# 🧣 Ragamuffin Roadmap

> Where we're going. Not promises — priorities.

This is the living development roadmap for Ragamuffin. Items are grouped by release
tier and ordered by value-to-effort ratio within each tier. Nothing ships until it's
tested and green on CI.

---

## v0.6 — Core Infrastructure

Unblocks both plugin adapters, fixes the multi-tenant isolation gap, and removes the
only hard requirement for OpenAI. Everything in this tier is foundational — nothing
else works well without it.

### 1. Session Management

**The problem:** `/v1/sessions` endpoints return 503 today. Both plugin adapters
(OpenClaw and Hermes) are designed around sessions that don't exist yet. Agents
have no way to persist conversation history — they either lose it or shoehorn it
into facts, which aren't designed for temporal, ordered data.

**The approach:** SQLite for metadata and ordering + Qdrant for semantic search
over turn content. Ship SQLite-only first (FTS5 for basic recall), add Qdrant-backed
semantic search in a follow-up.

```
POST   /v1/sessions              — Create session, returns UUID v7
POST   /v1/sessions/{id}/turns   — Append turns
GET    /v1/sessions/{id}         — Metadata + recent turns (paginated)
GET    /v1/sessions              — List by vault, ordered by activity
DELETE /v1/sessions/{id}         — Soft-delete (archive)
```

Storage: Extend `logstore` with `sessions` and `session_turns` tables.
New package: `internal/session/` wrapping SQLite + Qdrant session collection.
MCP tools: `ragamuffin_session_create`, `ragamuffin_session_append`,
`ragamuffin_session_recall`.

**Reference:** Issue #1 (placeholder for detailed tracking).

### 2. Per-Vault Fact Isolation

**The problem:** Facts are global — every vault shares one Qdrant collection.
Agent alice can see agent bob's facts. This breaks the multi-tenant isolation
model that chunks already have.

**The approach:** Each vault gets its own facts collection named
`ragamuffin_{name}_facts` (parallel to the existing chunk collection naming
from `provisionVault`). A `FactsCollectionFor(vault)` config method resolves
the collection name. Existing facts on the global collection stay readable via
`/v1/facts` for backward compatibility; new facts created scoped to a vault go
to their own collection.

Migration: New facts get vault-isolated from the start. Existing facts remain
in the global collection — a post-release migration script handles moving them.

**Reference:** The chunk pattern is already established in `internal/server/ingest.go`.
The config field `FactsCollection` becomes the default/fallback.

### 3. Configurable Embedding Dimensions

**The problem:** `provisionVault` hardcodes 1536 as the vector dimension when
creating Qdrant collections. This only works for OpenAI's `text-embedding-3-small`.
BGE-M3 uses 1024, Voyage uses 1024, all-MiniLM-L6-v2 uses 384. Anyone using a
non-OpenAI embedder has to change source code.

**The approach:** Auto-detect via probe embed at startup. If
`RAGAMUFFIN_EMBEDDING_DIMS` is set, use it directly. Otherwise, embed a probe
string on first `provisionVault` call and use the resulting vector length. Cache
the result in the server struct.

If no embedder is configured, use 4 (the sentinel dimension for payload-only
storage). The facts collection dimension stays hardcoded at 4 — it's intentionally
small for payload-only facts.

Also: add a collection-info check to `qdrant.New` so a dimension mismatch between
config and existing collection produces an error instead of silent corruption.

**Lines of code:** ~40. The config field already exists. `EmbedSingle` is already
on the interface. This is a one-function-plus-wiring change.

---

## v0.7 — Intelligence Layer

Builds on v0.6's infrastructure. Facts become connected to their source documents,
the pruner learns from operator feedback, and external systems get push notifications.

### 4. Fact-to-Chunk Bridge

**The problem:** Facts and chunks are disconnected. A fact like `db/host =
postgres.internal` might appear in five vault documents, but there's no way to
trace the relationship. The graph endpoint shows file-to-file links via entities
but doesn't include facts.

**The approach:** On fact upsert, fire a background goroutine that embeds the
fact value, searches the vault's chunk collection for high-similarity matches
(score > 0.7), and stores the top-N chunk IDs in a `related_chunks` payload
field. The graph endpoint gains an additional query on the facts collection for
nodes matching the entity string.

`related_chunks` is fully replaced on each upsert (not appended) to keep
payloads bounded. The goroutine uses `context.WithTimeout` and logs errors but
never blocks the upsert response.

Also adds a `SourceStaleScan` to the pruner: for each active fact with a
non-empty `source` field, check whether that source file still exists in the
vault's chunk collection. If the file was deleted or reindexed with different
content, flag the fact as `needs_review` with reason `source_deleted` or
`source_changed`.

### 5. Adaptive Pruner Thresholds

**The problem:** The pruner uses static thresholds — 0.85 cosine similarity for
contradictions, 0.5 confidence for low-confidence flags. When operators resolve
review items, the signal is discarded. The pruner can't learn.

**The approach:** Add a `review_resolutions` SQLite table that records every
resolution (action, fact key, reason type, similarity where applicable). Add a
`ThresholdRecommendations()` method that queries the table and suggests
threshold adjustments based on resolution history (e.g., "78% of contradiction
flags in the 0.85-0.90 range were dismissed — consider raising threshold to
0.90").

Also adds a `POST /v1/pruner/auto-tune?dry_run=true` endpoint that returns
recommendations without applying them. Operators review and apply manually.

The resolutions table is the primitive worth shipping first — threshold
recommendations need a release cycle of data before they're meaningful.

### 6. Webhook Notifications

**The problem:** The SSE `/events` endpoint streams file changes, but there's
no push mechanism for fact lifecycle events. Teams using Ragamuffin as agent
infrastructure can't integrate with Slack, dashboards, or auto-remediation
pipelines.

**The approach:** The `Emitter` infrastructure already exists in
`internal/events/emitter.go` — `NewEmitter`, `Emit`, `EmitSync`, CloudEvent
format, webhook URL from config, retry with 1s/5s/30s backoff. It has no callers,
only the SSE broker uses it. The work is:

1. Add event types: `fact.created`, `fact.flagged`, `fact.reviewed`,
   `pruner.scan.complete`
2. Wire `s.emitter.Emit()` into `handleFactsPost`, `handleReviewPost`,
   and the pruner's `LogScanFn` callback
3. Add a `WebhookEvents` config field for event-type filtering

**Lines of code:** ~80, all wiring.

### 7. Versioned Supersede (supersede scan refactor)

**The problem:** The supersede scan parses version patterns from fact key strings
(e.g., `org/v2/decision` → version 2). This is fragile — it can't distinguish a
version component from a semantic part of the key.

**The approach:** Add an integer `version` payload field (default 0 =
unversioned) to the fact schema. The supersede scan queries by fact_key +
vault, compares version integers directly, and marks lower versions as
superseded. No regex parsing needed.

One field, not two — vault isolation already serves as namespace.

---

## v0.8 — Developer Experience

Debt cleanup, operational UX, and internal quality. No new features for end-users.

### 8. MCP-to-REST Adapter Layer

**The problem:** `mcp_handlers.go` is 983 lines that largely duplicate REST
handler logic with minor type conversions. Every REST bugfix needs manual
mirroring to the MCP path. The Round 5 API migration (qdrant.\* → pb.\*) proved
this is a real maintenance cost.

**The approach:** Replace MCP tool handlers with thin adapters that construct
an `http.Request` via `httptest.NewRecorder`, call the REST handler, and parse
the response. Tool definitions (names, schemas) stay in `mcp_handlers.go` — only
the dispatch logic changes.

A `syntheticRequest` helper builds `*http.Request` from context + method + path +
body. Error translation maps REST status codes to MCP error types. Auth context
is injected from the MCP caller's identity.

Expected reduction: ~983 lines → ~500 lines.

### 9. Shared Utility Extraction

**The problem:** Several utility functions are duplicated across packages. The
`getPayloadString` family, `isIndexable`, and response helpers are copied across
`internal/server/facts.go`, `internal/pruner/pruner.go`, `internal/watcher/`, and
`internal/indexer/`. This has already caused divergence bugs.

**The approach:**

- `internal/qdrantutil/payload.go` — consolidate all payload reader functions
  (`PayloadString`, `PayloadFloat`, `PayloadBool`, etc.)
- `internal/fileutil/indexable.go` — extract `isIndexable` from watcher + indexer
- `internal/server/response.go` — extract `writeError`, `writeJSON`, `errResp`
  from server.go (intra-package reorganization)

### 10. Review Queue Dashboard

**The problem:** The review queue is only accessible via curl or MCP tools. The
embedded web UI (`web/`) is minimal. Operators need a lightweight dashboard to
see flagged facts, approve/reject them, and monitor system health.

**The approach:** A single `index.html` with embedded JS (no build tooling) that
`fetch()`es the existing REST API. No new backend endpoints — everything is
already exposed via `GET /v1/facts`, `GET /v1/review`, `GET /v1/review/stats`,
`GET /vaults`, and `GET /metrics`.

Views:
- Review queue — paginated cards with inline approve/reject/reclassify
- Pruner status — last scan times, flagged/resolved counts
- Vault overview — file counts, chunk counts, per-vault stats
- Fact browser — searchable, filterable list with status badges

Auth: Session-only bearer token storage (not localStorage), with a simple
login form when auth is enabled.

---

## Deferred

Items that are real but don't make the current priority cut.

| Item | Why deferred |
|---|---|
| **Fact namespacing (full)** | Vault isolation already provides namespace. The `version` field covers the supersede use case. A separate `namespace` payload field adds index overhead with no clear benefit. |
| **Fact-to-chunk bidirectional graph** | The bridge stores chunk references on facts. The reverse (chunk → facts) is useful but not blocking anything. |
| **Per-vault pruner scheduling** | With the payload-field isolation approach, a single pruner pass with vault filter is sufficient. Per-vault scan scheduling is optimization. |

---

## How items get on this list

1. A ticket is filed in the issue tracker with concrete implementation guidance.
2. The ticket has a clear "done when" — tests pass, CI green, specific API behavior.
3. The change goes through PR review like everything else.
4. It ships when it ships. Nothing is promised until CI is green on `main`.

---

*Last updated: 2026-05-23*
