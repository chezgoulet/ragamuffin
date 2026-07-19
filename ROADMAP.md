# 🧣 Ragamuffin Roadmap

> Where we're going. Not promises — priorities.

This is the living development roadmap for Ragamuffin. Items are grouped by release
tier and ordered by value-to-effort ratio within each tier. Nothing ships until it's
tested and green on CI.

**Most recent update: 2026-07-18 (v0.9.6)**

---

## ✅ Completed: v0.6 — Core Infrastructure

All three items fully implemented. Session management (SQLite + Qdrant), per-vault
fact isolation (`memory.{name}_facts` collections), and configurable embedding
dimensions (`RAGAMUFFIN_FACTS_VECTOR_SIZE`).

## ✅ Completed: v0.7 — Platform Growth

Fact graphs (`/v1/facts/{key}/graph`), webhook notifications (CloudEvents v1.0),
adaptive pruner thresholds (`/v1/pruner/auto-tune`), versioned supersede.

## ✅ Completed: v0.8 — Agent Readiness

Tiered recall (`mode` parameter), review queue dashboard, MCP-to-REST adapter,
fact extraction from conversation turns, benchmark harness.

## ✅ Completed: v0.9.x — Agent Ecosystem

| Feature | Status |
|---------|--------|
| OIDC-native authentication | Done |
| 33-tool MCP surface (was 14) | Done — all fact operations split, new graph/context/session/retrieval tools |
| MCP session-end notifications | Done — auto-finalization, fact extraction, summary indexing |
| Auto-provision agent vaults | Done — vaults created on first MCP dispatch |
| SDK packages (JS + Python) | Done — zero-dep MCP clients in `sdks/` |
| OpenClaw MCP rewrite | Done — plugin uses dynamic MCP discovery |
| Adapter conformance tests | Done — `tests/mcp_conformance_test.sh` |
| Memory Provider API rewrite | Done — MCP-first, 33-tool catalog |
| Documentation audit | Done — all docs reviewed for v0.9.6 |

## v1.0 Remaining

### Configurable Embedding Dimensions
Auto-detect via probe embed at startup. `RAGAMUFFIN_EMBEDDING_DIMS` already exists.
Embed a probe string and use the resulting length. (~40 lines.)

### Fact-to-Chunk Bridge
Store the chunk vector alongside the fact payload so fact search doesn't need a
separate embedding call. Persist at write time. (~60 lines.)

### Adaptive Pruner Thresholds
Closed-loop feedback from review actions back into pruner thresholds so the
pruner tunes itself per-vault over time. (~40 lines.)

### Full Benchmark Gauntlet
The harness exists (LongMemEval, LoCoMo, NarrativeQA) but is disabled in CI.
Enable and stabilize for automated regression detection.

---

## Out of Scope (Forever)

Ragamuffin will never be:
- A general-purpose vector database (use Qdrant directly)
- An agent runtime (use OpenClaw, Hermes, or the MCP client of your choice)
- A replacement for git (use git)
- A document management system (use your CMS)

---

*Last updated: 2026-07-18*

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

### 8. Long-Term Memory Benchmarks

**The problem:** Ragamuffin has no published benchmarks. Before v1.0 release, we need
credible numbers on established long-term-memory benchmarks so operators can evaluate
Ragamuffin's capabilities and track regressions across releases.

**The approach:** A Python harness in `benchmarks/` that runs Ragamuffin against:

- **LongMemEval** (`github.com/xiaowu0162/LongMemEval`) — 500 curated questions,
  5 abilities (extraction, multi-session reasoning, temporal reasoning, knowledge
  updates, abstention). Test on the "S" setting (16K–26K token histories).
- **LoCoMo** (`github.com/Backboard-io/Backboard-Locomo-Benchmark`) — 1,986 QA pairs
  across 10 conversations, excluding category 5 (adversarial) per standard practice.

Four configurations tested against each benchmark:
| Config | Description |
|--------|-------------|
| A | Pure vector search (`/recall` only) |
| B | Recall + facts (hybrid) |
| C | Tiered recall with detail levels (depends on v0.7) |
| D | Fact lifecycle integration |

Scored via LLM-as-judge. Results published to `benchmarks/RESULTS.md` with full
configuration, date, and Ragamuffin version. Honest scores expected: 55-65%;
temporal/multi-hop will likely lag behind purpose-built memory systems.

No new Go code — Python `requests` + stdlib calling Ragamuffin's HTTP API.

**Reference:** Issue #451, SPEC-benchmark.md at `benchmarks/SPEC-benchmark.md`.

### 9. MCP-to-REST Adapter Layer

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

### 10. Shared Utility Extraction

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

### 11. Review Queue Dashboard

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

## v0.9 — Operational Maturity

Everything in v0.6–0.8 is about building the machine. v0.9 is about proving it
won't fall over. Every item in this tier was chosen because it would have caused
a real incident during the first week of unattended production on TrueNAS.

### 12. OIDC-Native Authentication

**The problem:** Ragamuffin's auth model uses static pre-shared API keys
(`AUTH_READ_KEY`, `AUTH_WRITE_KEY`). Every agent shares the same key for a given
access tier. There's no way to scope access per-agent, rotate credentials per-agent,
or integrate with the House's Authentik-based SSO layer. LibreFang will run 12+
Hands — a shared write key means any compromised Hand is a compromised system.

**The approach:** Authentik OIDC as the primary auth provider. Agents authenticate
with Authentik-issued JWT tokens. Ragamuffin validates the token against
Authentik's JWKS endpoint and maps claims to access control:

- `ragamuffin/vaults` — which vaults this agent can read/write
- `ragamuffin/write` — write permission bool
- `ragamuffin/pruner` — pruner admin permission bool

The existing API key auth (`AUTH_READ_KEY`, `AUTH_WRITE_KEY`) becomes a fallback
for deployments without OIDC. New deployments should be OIDC-first.

Also: add a `POST /v1/auth/check` endpoint that returns the caller's scoped
permissions, intended for agent startup self-diagnostics.

Storage: A `memory.agents` SQLite table maps Authentik user IDs to vault
access, or the JWT claims are self-contained (authorization at the token level).
Prefer self-contained claims — no additional database dependency for auth.

Reference: This is the credential model already established for n8n as the
House's deterministic spine; Ragamuffin inherits the same Authentik OIDC pattern.

### 12. Graceful TrueNAS Lifecycle

**The problem:** Ragamuffin assumes Qdrant is always reachable. If Qdrant
restarts (TrueNAS update, power blip, Docker restart), the in-memory Qdrant
client handle goes stale. Requests fail with opaque gRPC errors until Ragamuffin
itself restarts.

**The approach:**

1. **Qdrant reconnection with backoff.** On gRPC connection error, enter a
   reconnection loop: 1s, 5s, 30s, 60s backoff. Return 503 with
   `{"status": "degraded", "detail": "qdrant reconnecting"}` during recovery.
2. **Health-aware routing.** `GET /health` reports each dependency's status
   (`qdrant: ok | reconnecting | down`, `sqlite: ok | error`). A 200 means
   fully operational; dependencies in recovery return 200 with degraded
   status flags so monitoring still alerts without paging.
3. **Startup ordering.** On first boot, retry `provisionVault` with exponential
   backoff if Qdrant's collection isn't available yet. No crash loops on
   boot-order races.

### 13. ZFS-Native Watcher

**The problem:** The current watcher polls the filesystem at a configurable
interval. On TrueNAS ZFS, polling misses rapid write bursts (multi-file ingestion
completing between ticks) and adds latency proportional to scan depth.

**The approach:** Add an inotify-based watch mode as the default on Linux. On
TrueNAS ZFS, fall back to `inotify` on the dataset mount point — it works on
the filesystem level, not the ZFS level, and captures all file create/modify/delete
events. The polling mode stays as a configurable fallback for NFS mounts.

Also: add a `POST /v1/refresh` endpoint for manual re-scan of specific vaults
or files, so the operator can trigger a scan without waiting for the watcher
tick.

### 14. Graceful Shutdown and Logstore Hygiene

**The problem:** `logstore` writes to SQLite. SQLite doesn't rotate itself.
Without a plan, the database files grow without bound. On shutdown, in-flight
SQLite writes may be truncated.

**The approach:**

1. **Auto-vacuum.** Enable SQLite `auto_vacuum=INCREMENTAL` on the logstore
   database, with an `integrity_check` and `PRAGMA shrink_memory` on startup.
2. **Max-row soft limit.** Per-vault and system-wide configurable max row
   counts for each table (`audit_log`, `events`, `watch_history`). When
   exceeded, the oldest rows are bulk-deleted (`DELETE FROM ... WHERE id IN
   (SELECT id FROM ... ORDER BY id LIMIT ...)` — never `DELETE FROM ...` without
   a bound).
3. **SIGTERM handler.** On `SIGTERM`, flush pending writes, close SQLite
   connections gracefully (journal commit), then wait up to 5 seconds for
   in-flight Qdrant writes before exiting. If the deadline expires, log a
   warning and exit anyway.

### 15. Restore-from-Snapshot Test

**The problem:** The library lives on a TrueNAS ZFS dataset. When the dataset
is restored to yesterday's snapshot (file restore, hardware migration, rollback),
Ragamuffin's index is stale — it references chunks and facts for files that
no longer exist or have changed content. Without explicit handling, this produces
ghost chunks, hard-to-diagnose recall results, and fact-to-file desync.

**The approach:** On startup, add a consistency check between the filesystem and
the index:

1. For each vault, compare file mtimes in the index against filesystem mtimes.
2. If more than N% of files show a mismatch (configurable, default: 10%),
   flag a "possible snapshot restore" state and offer re-index.
3. Re-indexing: delete all chunks for the affected vault, re-ingest from scratch.
   Facts referencing deleted files are flagged `needs_review` with reason
   `source_deleted`.

This check runs only on startup (not every watcher tick) and only on the vault
level — not on individual files — so it's fast even for large datasets.

---

## v1.0 — The Shipping Criteria

The three definitions of "done" for Ragamuffin 1.0:

**For the operator:** Survives TrueNAS reboots, Qdrant restarts, ZFS snapshots,
and a week without attention. Graceful startup ordering, logstore auto-vacuum,
and health reporting that distinguishes "everything fine" from "working but
degraded."

**For the agents:** API-complete. Everything an agent currently does through the
old vault model works through Ragamuffin alone — sessions with history, vault
isolation, fact read/write with confidence preservation, recall, ask, and
pruner audit. No filesystem fallback needed.

**For external consumers:** SPEC-MCP.md marked `status: stable`. The MCP tool
surface is stable — no breaking changes without a deprecation period and
migration path. The REST foundation is the contract; MCP is a transport
adaptation.

Checklist:

- [ ] v0.6: Sessions, vault-isolated facts, configurable embedding dims
- [ ] v0.7: Fact-to-chunk bridge, adaptive pruner, webhooks, versioned supersede
- [ ] v0.8: Benchmarks, MCP adapter refactor, utility extraction, review dashboard
- [ ] v0.9: OIDC auth, graceful TrueNAS lifecycle, ZFS watcher, logstore hygiene,
      restore-from-snapshot recovery
- [ ] SPEC-MCP.md stable (no breaking changes on main for 30 days)
- [ ] Operator verification: Ragamuffin runs 7 days unattended on TrueNAS, survives
      2 unplanned restarts, no manual intervention required

All six must be green before the v1.0 tag is created. Nothing ships until CI
is green on `main`, and CI must pass on a TrueNAS-equivalent environment
(container restart test, Qdrant kill test, logstore rotation test).

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

*Last updated: 2026-06-06*
