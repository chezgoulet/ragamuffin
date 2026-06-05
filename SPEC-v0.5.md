# Ragamuffin v0.5 — Fact Lifecycle & Memory Pruning

Extends [SPEC-v0.4.md](SPEC-v0.4.md). All v0.1–v0.4 endpoints and behaviors
remain unchanged unless explicitly noted.

## Overview

Ragamuffin currently stores facts and vault chunks but has no awareness of
fact quality over time. Facts accumulate. Nothing decays. Nothing gets
questioned.

v0.5 adds a **Pruner** module — a scheduled background process that walks
the fact corpus and vault index looking for:

- **Staleness** — facts that haven't been re-confirmed past their TTL
- **Contradiction** — facts that conflict with each other (semantic scan)
- **Supersession** — facts that reference now-obsolete sources or decisions
- **Low confidence** — facts with low confidence scores that need re-validation

The Pruner shares the existing Qdrant instance and fact schema. It does not
need its own database. It does not need its own embedding provider. It is a
background worker with a review queue, not a separate service.

**Key architectural principle:** The Pruner is a *reader and status updater*
only. It never deletes facts. It marks them `superseded`, `rejected`,
`needs_review`, or adjusts their `confidence` and `expires_at`. The
storage layer remains the single source of truth; pruning is an annotation
layer on top.

**Note:** Multi-tenancy, auth, and per-vault fact isolation are all implemented
as of v0.6. The Pruner works in both single-tenant and multi-tenant modes.
Per-vault facts use physically separate Qdrant collections.

---

## Phase 1: Extended Fact Data Model

### Changes to `/v1/facts` Schema

The existing fact payload is extended with lifecycle fields. All new fields
are optional — existing facts and clients that don't use them continue to
work unchanged.

**POST /v1/facts — Extended request body:**

```json
{
  "key": "org/prefer-rust-cli",
  "value": "Prefer Rust for new CLI tools",
  "tags": ["language", "tooling"],
  "source": "pr:42#discussion-r123",
  "source_type": "pr_discussion",
  "confidence": 0.85,
  "ttl_days": 180
}
```

### New Payload Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `source` | string | `""` | Origin reference (PR URL, file path, conversation ID) |
| `source_type` | string | `"manual"` | Enum: `manual`, `pr_discussion`, `agent_observation`, `file`, `conversation`, `code_review`, `automated` |
| `confidence` | float | `1.0` | How sure are we? 0.0–1.0 |
| `ttl_days` | int | `0` | Days until auto-expiry. 0 = never expires. Stored alongside computed `expires_at` in Qdrant payload — the server converts `ttl_days` to a UTC timestamp on write and re-computes when `ttl_days` is updated. |

### New Payload Fields (server-managed — not settable by clients)

| Field | Type | Description |
|---|---|---|
| `status` | string | `active`, `needs_review`, `superseded`, `rejected` |
| `supersedes` | string | Key of the fact this one replaces (empty if none) |
| `contradicts` | string[] | Keys of flags that this fact contradicts. **Write-once** — the Pruner only sets `contradicts` on the *source* fact, never on both sides. Mutual linking is handled at read time: the review handler checks if this fact's key appears in any other fact's `contradicts` array. |
| `conflict_resolved` | bool | Whether flagged contradictions have been resolved |
| `confirmation_count` | int | How many times this fact has been re-confirmed |
| `last_confirmed_at` | string | ISO8601 of most recent confirmation |
| `created_at` | string | ISO8601 of original creation (set once on first POST). Since POST /v1/facts currently upserts by key (writes the same Qdrant point ID on PUT semantics), the server must check whether the fact already exists before stamping `created_at`. This requires a `FactExists(key)` wrapper on qdrant.Client — a scroll filter on `fact_key` returning point count. |
| `updated_at` | string | ISO8601 of last update (already exists) |

### Extended Qdrant Payload

The facts collection already uses a 4-dim sentinel vector for payload-only
storage. The new fields are added as Qdrant payload keys alongside the
existing `fact_key`, `fact_value`, `fact_tags`, `updated_at`.

Payload filter indexes should be created on `status`, `source_type`,
`confidence`, `expires_at` for efficient pruner queries. The `expires_at`
index is critical — StaleScan filters on `expires_at < now` directly in
Qdrant payload, avoiding any loop arithmetic.

### GET /v1/facts — Extended Response

All server-managed fields are included in responses so clients can see
lifecycle state:

```json
{
  "key": "org/prefer-rust-cli",
  "value": "Prefer Rust for new CLI tools",
  "tags": ["language", "tooling"],
  "source": "pr:42#discussion-r123",
  "source_type": "pr_discussion",
  "confidence": 0.85,
  "status": "active",
  "supersedes": "",
  "contradicts": [],
  "conflict_resolved": true,
  "confirmation_count": 3,
  "last_confirmed_at": "2026-07-15T10:30:00Z",
  "created_at": "2026-01-20T14:00:00Z",
  "updated_at": "2026-07-15T10:30:00Z"
}
```

### PUT /v1/facts — Update Single Field (by query key)

New endpoint for targeted status updates. Enables agent writes like
"this fact is superseded by X" without re-POSTing the entire value.

**Design note:** Fact keys contain `/` characters (e.g. `org/some-decision`),
so the key is passed as a query parameter, not a path segment. This avoids
routing ambiguity with standard `http.ServeMux` where a slash in the path
would require Go 1.22 `{key...}` wildcard patterns or a prefix match.
The same approach applies to the review resolution endpoint below.

```bash
curl -X PUT 'http://ragamuffin:8000/v1/facts?key=org/some-decision' \
  -H "Content-Type: application/json" \
  -d '{"status": "superseded", "supersedes": "org/better-decision"}'
```

```json
{
  "key": "org/some-decision",
  "status": "superseded",
  "supersedes": "org/better-decision",
  "updated_at": "2026-07-20T09:00:00Z"
}
```

Accepts any subset of writable fields: `status`, `supersedes`, `confidence`,
`conflict_resolved`, `last_confirmed_at`, `confirmation_count`, `ttl_days`,
`tags`, `source`, `source_type`.

### GET /v1/facts — New `status` Filter Parameter

Once facts have lifecycle status, agents need to exclude superseded or
rejected facts from query results. The existing `GET /v1/facts` endpoint
gains a new optional filter parameter:

| Parameter | Type | Description |
|---|---|---|
| `key` | string | Exact key match (existing) |
| `prefix` | string | Key prefix filter (existing) |
| `tag` | string | Tag filter (existing) |
| `status` | string | Filter by lifecycle status: `active`, `needs_review`, `superseded`, `rejected`. Default: all statuses (backward compatible). |
| `conflict_resolved` | bool | Filter by dismissal status: `true` (operator-dismissed), `false` (unresolved). Ignored if status filter is not also set. |

```bash
# Get all active facts (exclude superseded/rejected from agent context)
curl -s 'http://ragamuffin:8000/v1/facts?status=active'

# Get review-flagged facts
curl -s 'http://ragamuffin:8000/v1/facts?status=needs_review'
```

The `status` filter is applied as a Qdrant payload filter on the facts
collection, using the index created during Phase 1 setup.

### PATCH /v1/facts — Bulk Status Update

Used by the Pruner and by agents that resolve review items in bulk. The
same `updates` object is applied to every key. Operations are NOT
transactional — partial failures return a per-key result array so the
caller knows exactly which keys succeeded and which failed.

```bash
curl -X PATCH http://ragamuffin:8000/v1/facts \
  -H "Content-Type: application/json" \
  -d '{
    "keys": ["org/old-decision", "org/older-decision", "org/missing-decision"],
    "updates": {"status": "superseded", "supersedes": "org/new-decision"}
  }'
```

```json
{
  "results": [
    {"key": "org/old-decision", "ok": true},
    {"key": "org/older-decision", "ok": true},
    {"key": "org/missing-decision", "ok": false, "error": "NOT_FOUND"}
  ],
  "total": 3,
  "succeeded": 2,
  "failed": 1
}
```

Error codes: `NOT_FOUND` (key doesn't exist), `INVALID_FIELD` (field not
writable via PATCH), `INTERNAL` (Qdrant write failure). The handler
continues processing remaining keys after any single failure — a single
bad key never blocks the rest.

---

### /recall and /ask Interaction with Fact Status

The existing `/recall` endpoint searches the *main vault chunk collection*
(1536-dim vectors), not the facts collection (4-dim sentinel vectors).
Facts do not appear in `/recall` results today, and this does not change
in v0.5. The Pruner has no effect on `/recall`.

Similarly, `/ask` synthesizes answers from vault chunk context retrieved
via `/recall`. It does not read facts directly. No interaction.

**If a future version adds semantic fact search** (by switching facts to
real embeddings), the following rule applies: `status=active` facts are
included, `status=superseded` and `status=rejected` facts are excluded.
`status=needs_review` facts are included but may be surfaced with a
confidence annotation. This is documented for future reference but is
**not implemented in v0.5**.

---

## Phase 2: Internal Pruner Module

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Ragamuffin Server                        │
│                                                               │
│  ┌──────────┐   ┌───────────┐   ┌─────────────────────────┐ │
│  │ Watcher  │──▶│ Indexer   │──▶│ Qdrant (main + facts)   │ │
│  └──────────┘   └───────────┘   └──────────┬──────────────┘ │
│                                            │                 │
│  ┌─────────────────────────────────────────┘                 │
│  │  ┌──────────────────┐                                     │
│  │  │   Pruner         │  ◀── reads/writes facts, scopes     │
│  │  │                  │       reads vault chunks            │
│  │  │  ┌────────────┐  │      ┌──────────────────┐           │
│  │  │  │ StaleScan   │──┼─────▶│ Review Queue     │           │
│  │  │  └────────────┘  │      │ (in Qdrant       │           │
│  │  │  ┌────────────┐  │      │  with status marks)           │
│  │  │  │ ConflictScan│  │      └──────────────────┘           │
│  │  │  └────────────┘  │                                     │
│  │  │  ┌────────────┐  │                                     │
│  │  │  │ SupersedeScan│ │                                     │
│  │  │  └────────────┘  │                                     │
│  │  │  ┌────────────┐  │                                     │
│  │  │  │ Scheduler  │  │  ◀── cron-like interval config       │
│  │  │  └────────────┘  │                                     │
│  │  └──────────────────┘                                     │
└─────────────────────────────────────────────────────────────┘
```

### New Internal Package: `internal/pruner/`

#### Pruner struct

```go
type Pruner struct {
    factsClient  *qdrant.Client
    vaultClient  *qdrant.Client    // main vault collection for chunk reads
    embedder     *embedding.Client // for contradiction scan embedding
    llm          *llm.Client       // for semantic contradiction detection
    logger       *slog.Logger
    cfg          *PrunerConfig
}
```

#### PrunerConfig

```go
type PrunerConfig struct {
    Enabled             bool
    StaleScanInterval   time.Duration // default: 24h
    ConflictScanInterval time.Duration // default: 72h
    SupersedeScanInterval time.Duration // default: 24h
    
    // Scan scopes
    StaleDays            int    // default: 90 (days since last confirmation)
    ConflictSampleSize   int    // default: 50 (pairs per scan cycle)
    LowConfidenceThreshold float64 // default: 0.5 (below this → needs_review)
    
    // Internal batch sizes
    BatchSize            int    // default: 100 (facts per batch for comparison)
    EmbeddingBatchSize   int    // default: 20 (matches existing indexer batch)
}
```

### Scan Modules

#### StaleScan

Walks facts collection. Uses the stored `expires_at` timestamp (computed
from `ttl_days` on write) to filter at the Qdrant level — no arithmetic
in the scan loop.

Also checks facts with `confidence < LowConfidenceThreshold` and no recent
confirmation — flags them for review regardless of TTL.

```
Scan logic:
1. Scroll facts with status=active and expires_at < now (Qdrant filter)
   → these are stale facts, mark as needs_review
2. Scroll facts with status=active and confidence < threshold
   → flag for review (they may not have TTL-based expiry)
3. Log results
```

#### ConflictScan

Uses existing `/audit` semantic conflict pattern but operates on the facts
collection instead of vault chunks. Also cross-references facts against
vault chunks when a fact appears to contradict vault content.

**Write-once contradicts rule:** When a conflict is detected, the Pruner
only writes `contradicts` on the *newer* or *lower-confidence* fact, not
both. The review queue handler reads mutual contradictions at query time
by checking if this fact's key appears in any other fact's `contradicts`
array. This avoids the partial-write problem where a server crash between
two Qdrant point updates leaves one fact pointing into a void.

**Zero-vector constraint:** The facts collection uses 4-dim sentinel
vectors `[0,0,0,0]` — there are no real embeddings stored in it.
Nearest-neighbor search on zero vectors returns garbage. ConflictScan
cannot use Qdrant vector search directly.

Solution — **Option 2: embed on-the-fly at scan time.** ConflictScan
reads fact values in batches, calls `embedder.Embed()` on each fact
value at scan time without storing the embedding. It then compares the
real embedding vectors in-memory (cosine similarity) to find candidate
pairs, then sends pairs above threshold to LLM `Compare()`. This is
slower than a Qdrant vector search (O(n²) in the number of active facts
if done naively), but avoids changing the collection schema and keeps
the sentinel storage intact for backward compatibility.

To keep runtime practical, the implementation should batch effectively:
embed 20 fact values at a time (matching `EmbeddingBatchSize`), compare
within each batch, and use the `ConflictSampleSize` config to limit how
many pairs are sent to the LLM per scan cycle. The first scan on a large
collection may be slow — document this.

> **Future option (not v0.5):** Change the facts collection to use real
> embeddings at write time. This would make ConflictScan an efficient
> Qdrant nearest-neighbor search but changes the write path and incurs
> embedding API costs on every fact upsert. Worth reconsidering when the
> pruner proves useful.

**LLM guard:** ConflictScan requires an LLM client — semantic contradiction
detection uses `llm.Compare()` which is not available when no LLM provider
is configured. Before starting, ConflictScan checks `HasLLM()`. If no LLM
is available, the scan skips entirely and logs a warning at `WARN` level:
`"conflict_scan skipped: no LLM provider configured"`. This mirrors the
existing pattern in `/ask` (which returns 503 for missing LLM) and `/audit`
(which returns `LLM_NOT_CONFIGURED` for semantic checks). The pruner is
opt-in and its scans are additive — silently skipping one scan type when
its dependencies are missing is correct behavior. ConflictScan also requires
an embedder; if the embedder is nil, it logs `"conflict_scan skipped: no
embedder configured"` at `WARN` level and skips.

**Re-flagging and the dismiss mechanism:** `conflict_resolved=true` is the
operator's dismissal — it means "I have reviewed this contradiction and it
is acceptable." Facts with `conflict_resolved=true && status=active` are
skipped by ConflictScan. Without this check, an operator who confirms a
stale+contradictory fact (because staleness was the issue, not the
contradiction) would see it re-flagged on every subsequent conflict scan
cycle until the contradiction was resolved. The field gives the operator
an explicit way to break that loop.

If a previously-conflicting fact is later superseded or rejected, the
operator can manually re-run ConflictScan via `POST /v1/pruner/rerun`
or set `conflict_resolved=false` on the dismissed fact to trigger
a fresh check on the next scheduled scan.

```
Scan logic:
1. Check HasLLM() and HasEmbedder() — if either is missing, log warning and return
2. Load active facts by scrolling facts collection (batch_size = 1000).
   Filter out facts where conflict_resolved=true — these have been
   explicitly reviewed and dismissed by the operator.
3. For facts with source_type=manual or source_type=agent_observation:
   a. Read fact values in EmbeddingBatchSize batches
   b. Call embedder.Embed() on each batch (on-the-fly, not stored)
   c. Compare real embedding vectors in-memory (cosine sim)
   d. For near pairs above threshold (top ConflictSampleSize):
      - Skip pairs where either fact already has the other in contradicts[]
        and conflict_resolved=true (already reviewed and dismissed)
      - Send remaining pairs to LLM Compare()
      - If conflict detected:
        - Set status=needs_review on the newer/lower-confidence fact
        - Add the other fact's key to contradicts[] (write-once)
        - Set conflict_resolved=false on the flagged fact
4. For facts that reference vault paths in source field:
   a. Recall related vault chunks (Qdrant search on main collection)
   b. Compare fact value against chunk content
   c. Flag if mismatch found
5. Log results and LLM call count
```

#### SupersedeScan

Identifies facts that may have been superseded. Two strategies:

1. **Source-tracking:** If a fact's `source` references a file or PR,
   check the vault index for newer versions of that source. If the source
   has been updated since the fact was recorded, flag for review.

2. **Key-pattern analysis:** For facts with keys that follow a hierarchy
   (e.g., `org/`, `team/`, `project/`), check if a newer fact with the
   same prefix exists with higher confidence.

```
Scan logic:
1. Scroll facts with status=active and source != ""
2. For each fact:
   a. Parse source reference (file path, PR URL)
   b. Check vault index for more recent content on the same source
   c. If vault chunk mtime > fact updated_at → set needs_review
3. Also check fact key hierarchy for override patterns
4. Log results
```

### Scheduler

Simple goroutine-based scheduler. Not a full cron — just `time.Ticker` with
configurable intervals.

```go
func (p *Pruner) Run(ctx context.Context, ready chan<- struct{}) {
    staleTicker := time.NewTicker(p.cfg.StaleScanInterval)
    conflictTicker := time.NewTicker(p.cfg.ConflictScanInterval)
    supersedeTicker := time.NewTicker(p.cfg.SupersedeScanInterval)
    
    defer staleTicker.Stop()
    defer conflictTicker.Stop()
    defer supersedeTicker.Stop()

    // Initial scan runs in background goroutine — does not block startup.
    // ConflictScan may spend minutes embedding + LLM-comparing facts;
    // the server should be ready to serve HTTP before that finishes.
    // Signal ready on the channel so main.go can proceed with listener
    // registration while scans run concurrently.
    close(ready)
    go p.allScans(ctx)
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-staleTicker.C:
            p.StaleScan(ctx)
        case <-conflictTicker.C:
            p.ConflictScan(ctx)
        case <-supersedeTicker.C:
            p.SupersedeScan(ctx)
        }
    }
}
```

All scans are cancellable via context and respect server shutdown. The
`ready` channel follows the same pattern as the indexer's `initialDone`
channel — the Pruner signals readiness immediately and runs the initial
scan in the background. If the initial scan fails (e.g. Qdrant not yet
available), subsequent scheduled scans will pick up when Qdrant is ready.

---

## Phase 3: Review Queue API

### GET /v1/review — List Items Needing Human Attention

Returns facts that have `status = needs_review`, ordered by priority
(most stale first, highest contradiction count first).

```bash
curl -s http://ragamuffin:8000/v1/review
```

```json
{
  "entries": [
    {
      "key": "org/prefer-rust-cli",
      "value": "Prefer Rust for new CLI tools",
      "review_reasons": [
        {"type": "stale", "detail": "Last confirmed 197 days ago (TTL: 180)"},
        {"type": "contradiction", "detail": "Conflicts with org/use-go-cli (score: 0.87)", "conflict_keys": ["org/use-go-cli"]}
      ],
      "confidence": 0.85,
      "last_confirmed_at": "2026-01-15T10:30:00Z",
      "tags": ["language", "tooling"],
      "source": "pr:42#discussion-r123",
      "created_at": "2026-01-15T10:30:00Z",
      "updated_at": "2026-01-15T10:30:00Z"
    }
  ],
  "total": 1,
  "next_token": "abc123"
}
```

Filter parameters:

| Parameter | Type | Description |
|---|---|---|
| `reason` | string | Filter by review reason: `stale`, `contradiction`, `supersession`, `low_confidence`, `all` (default) |
| `tag` | string | Filter by fact tag |
| `source_type` | string | Filter by source type |
| `min_confidence` | float | Only show facts below this confidence |
| `limit` | int | Max results (1–100, default 50) |
| `before` | string | Cursor pagination |

### POST /v1/review — Resolve Review Item (by query key)

Accept, supersede, or reject a flagged fact. Key is passed as a query
parameter for the same slash-in-path reason as PUT /v1/facts.

`GET /v1/review/stats` is registered as a separate route before the
review query handler to avoid prefix conflicts with `v1/review?key=...`.

```bash
curl -s -X POST 'http://ragamuffin:8000/v1/review?key=org/prefer-rust-cli' \
  -H "Content-Type: application/json" \
  -d '{
    "action": "confirm",
    "confidence": 0.95,
    "note": "Confirmed in team sync — still current",
    "conflict_resolved": true
  }'
```

Actions:

| Action | Effect |
|---|---|
| `confirm` | Sets `status=active`, increments `confirmation_count`, updates `last_confirmed_at`, clears review reasons. Optional `conflict_resolved: true` sets the flag to dismiss any known contradiction — the next ConflictScan will skip this fact until the flag is cleared manually. Default `false` — fact stays eligible for re-flagging on next conflict scan. |
| `supersede` | Sets `status=superseded`, sets `supersedes` to new key (provided in `new_key` field), creates new fact if `new_value` provided |
| `reject` | Sets `status=rejected`, sets rejection timestamp |
| `reclassify` | Adjusts `confidence`, `ttl_days`, `tags`, `source_type` without changing status |

**Supersede with new_value is a hidden write path.** When the `supersede`
action includes `new_value`, a new fact is created implicitly. This must
use the same validation path as `POST /v1/facts`: body size limits, fact
value length checks, tag limits, and Qdrant point insertion. Rate limiting
applies (uses the same rate limiter as POST /v1/facts). The watcher is NOT
triggered — fact updates are not file-system events. The new fact inherits
`source`, `source_type`, `tags` from the original unless overridden in the
request body.

**needs_review and the re-flagging cycle.** A fact with `status=needs_review`
may have multiple independent reasons (stale + contradiction). The
`review_reasons` array in the GET /v1/review response shows all active
reasons. When an operator resolves via POST /v1/review (e.g. `confirm`),
the action sets `status=active`, which clears the fact from the review
queue entirely — regardless of how many reasons were attached.

If a resolved fact still has an underlying issue (e.g. it was confirmed
out of staleness but still has an unresolved contradiction), the next
scheduled scan will re-flag it. Specifically:

- **StaleScan re-flag:** If the fact was re-confirmed but its `ttl_days`
  has not changed, the new `last_confirmed_at` resets the expiry clock.
  It will not be re-flagged until the new expiry passes.
- **ConflictScan re-flag:** If `conflict_resolved=false` and the semantic
  conflict still exists, the fact will be re-flagged on the next conflict
  scan cycle.
- **Dismissing a known contradiction:** If the operator has reviewed the
  contradiction and considers it acceptable, they can set
  `conflict_resolved=true` (via the `reclassify` action or PATCH).
  ConflictScan skips facts with `conflict_resolved=true && status=active`.
  The operator can always re-open by setting `conflict_resolved=false` or
  running `POST /v1/pruner/rerun`.

This is intentional — the Pruner is eventually consistent about re-flagging.
The operator should see the fact again if an unresolved condition persists.
No single action needs to address all reasons at once.

### GET /v1/review/stats — Review Queue Summary

Quick dashboard endpoint for agents and operators:

```json
{
  "total_needs_review": 5,
  "by_reason": {
    "stale": 3,
    "contradiction": 1,
    "low_confidence": 1
  },
  "by_source_type": {
    "manual": 3,
    "agent_observation": 2
  },
  "oldest_item": "2026-06-01T10:30:00Z",
  "avg_pending_days": 12.5
}
```

---

## Phase 4: Integration with Existing Audit

### Enhanced `/audit` — Pruner Integration

The existing `/audit` endpoint gains a new check type: `pruner_health`.

```json
{
  "checks": ["stale", "semantic_conflict", "gap", "duplicate", "pruner_health"]
}
```

Returns pruner status:

```json
{
  "pruner_health": {
    "enabled": true,
    "last_stale_scan": "2026-07-19T03:00:00Z",
    "last_conflict_scan": "2026-07-18T03:00:00Z",
    "last_supersede_scan": "2026-07-19T03:00:00Z",
    "total_scans_run": 47,
    "total_llm_calls": 312,
    "facts_flagged_total": 8,
    "facts_resolved_total": 5,
    "review_queue_size": 3
  }
}
```

### `/audit` Conflict Detection — Now Operates on Facts Too

The existing `checkSemanticConflicts` operates on vault chunks. The Pruner
adds a parallel path for fact-to-fact and fact-to-chunk comparison. These
are registered as additional audit checks:

| Check | Scope | Data Source |
|---|---|---|
| `semantic_conflict` | Vault chunks (existing) | Main Qdrant collection |
| `fact_conflict` | Facts (new) | Facts Qdrant collection |
| `fact_vault_conflict` | Facts vs vault chunks (new) | Both collections |

Rationale: Keeping `semantic_conflict` unchanged avoids breaking existing
callers. New checks are additive.

---

## Phase 5: Configuration

### New Environment Variables

```env
# Pruner master switch
RAGAMUFFIN_PRUNER_ENABLED=false

# Scan intervals (Go duration strings)
RAGAMUFFIN_PRUNER_STALE_INTERVAL=24h
RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL=72h
RAGAMUFFIN_PRUNER_SUPERSEDE_INTERVAL=24h

# Scan parameters
RAGAMUFFIN_PRUNER_STALE_DAYS=90
RAGAMUFFIN_PRUNER_CONFLICT_SAMPLE_SIZE=50
RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD=0.5

# Batch sizes
RAGAMUFFIN_PRUNER_FACT_BATCH_SIZE=100
RAGAMUFFIN_PRUNER_EMBED_BATCH_SIZE=20

# Rate limiting for pruner review API
RAGAMUFFIN_RATE_LIMIT_REVIEW=30
```

### Config Struct Extensions

```go
type PrunerConfig struct {
    Enabled                bool
    StaleScanInterval      string
    ConflictScanInterval   string
    SupersedeScanInterval  string
    StaleDays              int
    ConflictSampleSize     int
    LowConfidenceThreshold float64
    FactBatchSize          int
    EmbedBatchSize         int
}
```

Exists as a field on the main `Config` struct. Loaded from environment
variables alongside existing config.

### Rate Limit Registration

New rate limit key `/v1/review` with default 30 RPM, configurable via
`RAGAMUFFIN_RATE_LIMIT_REVIEW`.

---

## Phase 6: Vault Chunk Cross-Reference

### Fact Source → Vault Chunk Linkage

When a fact's `source` field references a vault-relative path (detected by
matching against `RAGAMUFFIN_VAULT_PATH` prefix), the Pruner can perform
source staleness checks:

```
1. Parse source path: "ops/deploy.md"
2. Look up vault chunk with source_file = "ops/deploy.md"
3. Compare chunk's file_last_updated against fact's last_confirmed_at
4. If vault content is newer → flag for review
```

This creates a directed dependency graph: facts depend on the accuracy of
their source documents. When the source document changes, downstream facts
need re-validation.

Implementation consideration: this scan is O(facts × lookup) rather than
batch-scrollable. To keep it efficient, cache the last-seen mtime per
source file and only re-check when the vault watcher reports a change
on that file.

### Watcher Integration

The watcher emits file-change events on a single channel that is consumed
exclusively by the indexer. A Go channel can only have one consumer — you
can't fan-out a `<-chan Event` to two goroutines natively.

**Solution: fan-out in main.go.** Before passing the event channel to the
indexer, add a fan-out multiplexer that copies each event to both the
indexer and the pruner. This requires no watcher interface changes.

```go
// In main.go:
func fanOut(ctx context.Context, src <-chan watcher.Event,
           dsts ...chan<- watcher.Event) {
    for {
        select {
        case <-ctx.Done():
            return
        case evt, ok := <-src:
            if !ok {
                return
            }
            for _, dst := range dsts {
                select {
                case dst <- evt:
                default:
                    // Non-blocking: if a consumer is backed up,
                    // the event is dropped. Prevents slow consumers
                    // from blocking the watcher.
                }
            }
        }
    }
}
```

The Pruner receives a dedicated event channel and watches for file changes
with the same pattern:

```go
func (p *Pruner) WatchEvents(ctx context.Context, events <-chan watcher.Event) {
    for {
        select {
        case <-ctx.Done():
            return
        case evt, ok := <-events:
            if !ok {
                return
            }
            // Mark facts referencing this source file for review
            p.markFactsForSourceReview(evt.Path)
        }
    }
}
```

This is additive and requires no watcher interface change. Facts get
flagged within seconds of a source file change, not hours later in the
next scheduled scan. The non-blocking send in the fan-out means a backed-up
pruner never delays the indexer.

---

## Phase 7: Metrics & Observability

### Prometheus Metrics

New metrics on the existing `/metrics` endpoint:

```
# HELP ragamuffin_pruner_scans_total Total pruner scan cycles by scan type.
# TYPE ragamuffin_pruner_scans_total counter
ragamuffin_pruner_scans_total{scan_type="stale"} 42
ragamuffin_pruner_scans_total{scan_type="conflict"} 14
ragamuffin_pruner_scans_total{scan_type="supersede"} 42

# HELP ragamuffin_pruner_facts_flagged Facts flagged by the pruner.
# TYPE ragamuffin_pruner_facts_flagged gauge
ragamuffin_pruner_facts_flagged{reason="stale"} 3
ragamuffin_pruner_facts_flagged{reason="contradiction"} 1
ragamuffin_pruner_facts_flagged{reason="low_confidence"} 1

# HELP ragamuffin_pruner_llm_calls_total LLM calls made by the pruner.
# TYPE ragamuffin_pruner_llm_calls_total counter
ragamuffin_pruner_llm_calls_total 312

# HELP ragamuffin_pruner_watcher_events_dropped_total Watcher events
#     dropped by the pruner due to non-blocking fan-out backpressure.
#     A sustained non-zero rate means the pruner cannot keep up with
#     the watcher — reduce conflict interval or increase batch sizes.
# TYPE ragamuffin_pruner_watcher_events_dropped_total counter
ragamuffin_pruner_watcher_events_dropped_total 0

# HELP ragamuffin_review_queue_size Number of facts pending review.
# TYPE ragamuffin_review_queue_size gauge
ragamuffin_review_queue_size 3
```

### Audit Logs

The Pruner logs every scan cycle to the existing `/v1/logs` endpoint with
`agent=ragamuffin`, `type=pruner_scan`, body containing scan summary as JSON.

Agents can query `GET /v1/logs?agent=ragamuffin&type=pruner_scan` to check
when the last scan ran and what it found.

---

## Migration & Backward Compatibility

### No Breaking Changes

All v0.1–v0.4 endpoints remain unchanged. The Pruner is opt-in
(`RAGAMUFFIN_PRUNER_ENABLED=false` by default). Existing facts without
lifecycle fields continue to work — they simply won't be scanned until
a client adds source/confidence/TTL data.

### Index Creation Order

Payload filter indexes (`status`, `source_type`, `confidence`, `expires_at`)
must be created **before** the migration pass runs. Creating indexes on a
large existing collection is blocking in Qdrant — if the migration runs
first and sets `status=active` on 50k facts before the index exists, the
first StaleScan will do a full collection scroll with an unindexed filter,
which may time out.

The correct order at startup: add new fields → add indexes → run migration.

### Data Migration

When the Pruner first runs against existing facts (after being enabled),
it performs a one-time stamping pass (after indexes are created):

1. If a fact has no `created_at`, set it to `updated_at`
2. If a fact has no `status`, set it to `active`
3. If a fact has no `confidence`, set it to `1.0`
4. If a fact has no `ttl_days`, set it to `0` (never expires)

This ensures all facts have the required lifecycle metadata before scanning
begins.

### Existing `/audit` Users

The existing `semantic_conflict` check lives on unchanged. The new
`fact_conflict` and `fact_vault_conflict` checks are additional values
for the `checks` array. Existing clients that don't request them won't
see them.

---

## Future Considerations (Not in v0.5)

### Automated Conflict Resolution

When contradiction confidence is very high and one fact has a clear source
advantage (newer, higher source_type authority, confirmed by more agents),
the Pruner could auto-resolve without human review. v0.5 requires human
review for all flags.

### Confidence Decay Curve

The current model uses a hard TTL boundary: facts are `active` until
expires_at, then `needs_review`. A future version should compute
`effective_confidence = raw_confidence × decay(time_since_last_confirmed,
ttl_days)` at query time. This would let agents express both how sure they
are and how quickly that certainty erodes — independently. The decay curve
could also influence the review queue sort order ("these facts are closest
to the edge"). The field structure (ttl_days, expires_at, last_confirmed_at)
is already in place for this; v0.5 just does not compute the decay.

### Learning Feedback Loop

When the operator accepts or rejects a flag, that decision could be used
to tune the Pruner's thresholds. If the operator keeps confirming facts
with confidence 0.3, the threshold should drift downward. The expected
feedback path: `confirm`/`reject` action ratios feed back into scan
parameters (LowConfidenceThreshold, scan intervals) via config reload
or online adjustment. Not needed for v0.5, but worth documenting so the
config does not harden around static defaults.

When the operator accepts or rejects a flag, that decision could be used
to tune the Pruner's thresholds. If the operator keeps confirming facts
with confidence 0.3, the threshold should drift downward.

### Escalation Chains

When a review item sits unresolved for N days, escalate: re-scan with
higher priority, notify the operator via a new channel (Mattermost DM,
review dashboard push).

### Web Dashboard

A thin read-only Web UI (referenced in v0.4 spec) could include a review
queue view — list pending items, show contradictions inline, one-click
confirm/supersede/reject.

---

## Implementation Plan

### Phase 1 (Foundation)
1. Extend fact data model with lifecycle fields
2. Add `FactExists(key)` wrapper to qdrant.Client (scroll filter on fact_key)
3. Update POST /v1/facts upsert logic: preserve `created_at` on existing facts,
   compute `expires_at` from `ttl_days` on write and on update
4. Add PUT /v1/facts?key= and PATCH /v1/facts endpoints (query-param routing)
5. Add `status` filter to GET /v1/facts
6. Document /recall and /ask not interacting with facts (no change needed)
7. **Add payload filter indexes FIRST** on status, source_type, confidence, expires_at
   — must be done in Qdrant before the migration pass (index creation on existing
   data is blocking; creating on an empty collection is fast)
8. Run migration pass for existing facts (sets defaults on missing lifecycle fields)
9. Add new env vars to Config struct and validation
10. Write unit tests for new endpoints

### Phase 2 (Pruner Core)
1. Create `internal/pruner/` package with Pruner struct and config
2. Implement StaleScan (Qdrant filter: expires_at < now)
3. Implement ConflictScan (on-the-fly embedding at scan time, in-memory
   cosine compare, write-once contradicts)
4. Implement SupersedeScan (source-tracking and key-pattern)
5. Wire scheduler into main.go — start via `go p.Run(ctx, ready)` with
   `ready` channel to signal immediate readiness (initial scan in bg)
6. Write unit tests with mock Qdrant + mock LLM

### Phase 3 (Review Queue)
1. Implement GET /v1/review?reason=...&tag=... with filters and pagination
2. Implement POST /v1/review?key= resolution endpoint (query-param routing)
   with per-action field validation
3. Implement GET /v1/review/stats summary (registered as separate route
   before the review query handler)
4. Add rate limiting for review endpoints
5. Register routes in server.go

### Phase 4 (Integration)
1. Add pruner_health check to /audit
2. Add fact_conflict and fact_vault_conflict audit checks
3. Wire Pruner metrics into /metrics endpoint
4. Log scan results to /v1/logs
5. Add fan-out multiplexer in main.go to copy watcher events to both
   indexer and pruner (non-blocking send; slow consumer drops events)
6. Wire pruner's WatchEvents goroutine consuming the fan-out copy channel

### Phase 5 (Testing & Docs)
1. Table-driven tests for all Pruner scan types
2. Integration test: Pruner + real Qdrant + mock LLM
3. Update AGENTS.md / AGENTS_SKILL.md with new endpoints
4. Update README with pruner configuration
5. Update deployment configs (compose, helm, etc.)
