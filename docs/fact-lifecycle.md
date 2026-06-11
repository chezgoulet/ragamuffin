# Fact Lifecycle

Ragamuffin facts have a formal lifecycle: they are written, reviewed, and either confirmed
or retired. The **review queue** and **pruner** work together to maintain fact quality
without requiring manual curation of every fact.

## Lifecycle Overview

```
  Write (upsert)          Review Queue              Resolution
       │                       │                        │
       ▼                       ▼                        ▼
  ┌──────────┐   flag    ┌────────────┐   confirm   ┌──────────┐
  │  active  │ ───────→  │needs_review│ ──────────→  │  active  │
  └──────────┘           └────────────┘              └──────────┘
                              │  reject                   │
                              ├─────────────────────────→ │rejected │
                              │  supersede                │
                              ├─────────────────────────→ │superseded│
                              │  reclassify               │
                              └─────────────────────────→ │  active  │
                                                          │(updated) │
                                                          └──────────┘
```

### Stages

1. **Write** — Facts are upserted via `POST /v1/facts` or `POST /v1/ingest` with
   `auto_extract: true`. Initially set to `status: active`.

2. **Flag** — The pruner's periodic scans flag facts for review by setting
   `status: needs_review`. Facts can be flagged for several reasons:

   | Reason | Trigger | Details |
   |---|---|---|
   | **stale** | `expires_at` in the past | TTL-based expiry from fact metadata |
   | **contradiction** | Vector similarity exceeding conflict threshold | Cosine similarity > `conflict_threshold` with another active fact |
   | **low_confidence** | Confidence < `low_confidence_threshold` | Auto-flagging of low-confidence extractions |
   | **supersession** | Fact has a `supersedes` value | Indicates it replaced another fact |
   | **unread** | Never read or last read >30 days | Read-tracking via `read_count` / `last_read_at` |

3. **Review** — Operators or agents query the review queue, inspect facts, and resolve them.

4. **Resolution** — Each flagged fact gets one of four actions:
   - **confirm** — Accept as-is, reset to `active`, bumps `confidence` + `confirmation_count`
   - **reject** — Mark as `rejected` (archived, removed from recall results)
   - **supersede** — Mark as `superseded` with a pointer to the replacement fact
   - **reclassify** — Update key/value/tags/confidence and return to `active`

---

## Review Queue API

### `GET /v1/review` — List facts needing review

Returns paginated facts with `status: needs_review`, with computed review reasons.

**Query parameters:**

| Param | Type | Description |
|---|---|---|
| `reason` | string | Filter by reason type: `stale`, `contradiction`, `low_confidence`, `supersession`, `unread`, or `all` |
| `tag` | string | Filter by exact tag keyword |
| `source_type` | string | Filter by source type (e.g. `extraction`, `document`, `manual`) |
| `min_confidence` | float | Only show facts with confidence below this value |
| `limit` | int | Max results (1–100, default 50) |
| `before` | string | Cursor from `next_token` in previous response |

**Example — daily archival check:**

```bash
# Find stale facts tagged "infrastructure"
curl -s "http://localhost:8000/v1/review?reason=stale&tag=infrastructure"
```

**Response:**

```json
{
  "entries": [
    {
      "key": "infra/deploy-url",
      "value": "Jenkins at https://ci.example.com",
      "tags": ["infrastructure", "prod"],
      "source_type": "extraction",
      "confidence": 0.85,
      "status": "needs_review",
      "review_reasons": [
        {"type": "stale", "detail": "Expired 3d12h ago (TTL: 90 days)"}
      ],
      "last_confirmed_at": "2026-03-01T10:00:00Z",
      "created_at": "2026-01-15T08:30:00Z",
      "updated_at": "2026-06-10T12:00:00Z",
      "read_count": 5,
      "last_read_at": "2026-05-01T09:00:00Z"
    }
  ],
  "total": 1,
  "next_token": "uuid-..."
}
```

**Example — check for unresolved contradictions:**

```bash
curl -s "http://localhost:8000/v1/review?reason=contradiction"
```

Each contradiction entry includes `conflict_keys` — the keys of conflicting facts:

```json
{
  "review_reasons": [
    {
      "type": "contradiction",
      "detail": "Conflicts with 2 other facts",
      "conflict_keys": ["infra/db-host-old", "infra/db-host-v2"]
    }
  ]
}
```

---

### `POST /v1/review?key=<fact_key>` — Resolve a flagged fact

Resolves a `needs_review` fact. Requires `write` access.

| Query Param | Required | Description |
|---|---|---|
| `key` | Yes | Fact key to resolve |

**Request body** (`Content-Type: application/json`):

| Field | Required | Type | Description |
|---|---|---|---|
| `action` | Yes | string | One of: `confirm`, `reject`, `supersede`, `reclassify` |
| `confidence` | No | float | New confidence (confirm/reclassify only) |
| `new_key` | No | string | New key for the replacement (supersede only) |
| `new_value` | No | string | New value for the replacement (supersede only) |
| `note` | No | string | Resolution note (logged to event stream) |
| `conflict_resolved` | No | bool | Mark contradiction as resolved |
| `ttl_days` | No | int | New TTL for the fact (reclassify only) |
| `tags` | No | string[] | Updated tag set (reclassify only) |
| `source` | No | string | Updated source (reclassify only) |
| `source_type` | No | string | Updated source type (reclassify only) |

**Actions in detail:**

| Action | Effect | Response |
|---|---|---|
| `confirm` | Sets `status: active`, increments `confirmation_count`, optionally bumps `confidence` | `{"status": "confirmed", "key": "..."}` |
| `reject` | Sets `status: rejected`, records rejection note | `{"status": "rejected", "key": "..."}` |
| `supersede` | Marks old fact as `superseded`, creates a **new fact** with the given key/value | `{"status": "superseded", "key": "...", "new_key": "..."}` |
| `reclassify` | Updates key/value/tags/confidence/expiry, returns to `active` | `{"status": "reclassified", "key": "..."}` |

**Example — confirm a stale fact with updated confidence:**

```bash
curl -s -X POST "http://localhost:8000/v1/review?key=infra/deploy-url" \
  -H "Content-Type: application/json" \
  -d '{"action": "confirm", "confidence": 0.95}'
```

**Example — supersede an outdated fact:**

```bash
curl -s -X POST "http://localhost:8000/v1/review?key=infra/db-host-old" \
  -H "Content-Type: application/json" \
  -d '{
    "action": "supersede",
    "new_key": "infra/db-host",
    "new_value": "postgresql://db.internal:5432/prod"
  }'
```

**Example — reject a low-confidence extraction:**

```bash
curl -s -X POST "http://localhost:8000/v1/review?key=fact/unverified-123" \
  -H "Content-Type: application/json" \
  -d '{"action": "reject", "note": "hallucinated value, not in source"}'
```

---

### `GET /v1/review/stats` — Review queue statistics

Returns aggregate stats about the current review queue. No auth required.

```bash
curl -s http://localhost:8000/v1/review/stats
```

**Response:**

```json
{
  "total_needs_review": 47,
  "by_reason": {
    "stale": 23,
    "low_confidence": 12,
    "contradiction": 7,
    "supersession": 5
  },
  "by_source_type": {
    "extraction": 30,
    "manual": 10,
    "document": 7
  },
  "oldest_item": "2026-03-10T14:22:00Z",
  "avg_pending_days": 12.4
}
```

---

## Pruner API

The pruner runs periodic scans that flag facts for review. Its configuration is
**runtime-adjustable** — changes take effect immediately without restart.

### `GET /v1/pruner/config` — Current pruner configuration

Returns the active pruner settings. Requires `admin` access. Returns `503` if
the pruner is disabled.

```bash
curl -s http://localhost:8000/v1/pruner/config
```

**Response:**

```json
{
  "enabled": true,
  "stale_days": 90,
  "conflict_sample_size": 50,
  "conflict_threshold": 0.85,
  "low_confidence_threshold": 0.5,
  "importance_threshold": 0.0
}
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `false` | Master switch |
| `stale_days` | `90` | Days past TTL before a fact is flagged stale |
| `conflict_sample_size` | `50` | Fact pairs per contradiction scan cycle |
| `conflict_threshold` | `0.85` | Cosine similarity above this = contradiction |
| `low_confidence_threshold` | `0.5` | Confidence below this → `needs_review` |
| `importance_threshold` | `0.0` | Facts above this importance skip stale scan (0=disabled) |

### `GET /v1/pruner/auto-tune` — Threshold recommendations

Analyses review resolution history and recommends optimal thresholds. Requires
`admin` access.

**Query parameters:**

| Param | Default | Description |
|---|---|---|
| `dry_run` | `true` | Preview recommendations without applying. Set to `false` to apply changes. |

```bash
# Preview recommendations
curl -s "http://localhost:8000/v1/pruner/auto-tune?dry_run=true"

# Apply recommended thresholds
curl -s "http://localhost:8000/v1/pruner/auto-tune?dry_run=false"
```

**Response:**

```json
{
  "dry_run": true,
  "recommendations": [
    {
      "reason_type": "low_confidence",
      "current": 0.5,
      "recommended": 0.35,
      "accept_rate": 0.82,
      "sample_size": 45,
      "rationale": "82% of low-confidence flags were confirmed — threshold may be too aggressive"
    },
    {
      "reason_type": "conflict",
      "current": 0.85,
      "recommended": 0.92,
      "accept_rate": 0.31,
      "sample_size": 18,
      "rationale": "Only 31% of conflict flags were accepted — similarity threshold may be too loose"
    }
  ],
  "sample_count": 2
}
```

When `dry_run=false`, the server applies `low_confidence` and `conflict` threshold
changes immediately. Other reason types note that changes must be applied manually.

---

## Worked Examples

### Daily archival review (operator script)

```bash
#!/usr/bin/env bash
# Review flagged facts — show stale items needing attention
RAGAMUFFIN="http://localhost:8000"

echo "=== Review Queue ==="
curl -s "$RAGAMUFFIN/v1/review/stats" | python3 -m json.tool

echo ""
echo "=== Stale facts ==="
curl -s "$RAGAMUFFIN/v1/review?reason=stale&limit=5" | python3 -m json.tool
```

### Resolving a contradiction from an agent

When an agent discovers that two facts contradict each other:

1. Query the review queue for contradictions:
   ```bash
   curl -s "http://localhost:8000/v1/review?reason=contradiction"
   ```

2. Inspect the conflicting facts by key:
   ```bash
   curl -s "http://localhost:8000/v1/facts?key=infra/db-host-old"
   curl -s "http://localhost:8000/v1/facts?key=infra/db-host-v2"
   ```

3. Supersede the outdated one with a corrected fact:
   ```bash
   curl -s -X POST "http://localhost:8000/v1/review?key=infra/db-host-old" \
     -H "Content-Type: application/json" \
     -d '{
       "action": "supersede",
       "new_key": "infra/db-host",
       "new_value": "postgresql://db.internal:5432/prod",
       "note": "resolved contradiction — db-host-old was stale"
     }'
   ```

4. Confirm the correct fact and mark the contradiction resolved:
   ```bash
   curl -s -X POST "http://localhost:8000/v1/review?key=infra/db-host-v2" \
     -H "Content-Type: application/json" \
     -d '{"action": "confirm", "confidence": 0.95, "conflict_resolved": true}'
   ```

### Tuning pruner thresholds

When too many false positives or false negatives appear in the review queue:

1. Check current config:
   ```bash
   curl -s http://localhost:8000/v1/pruner/config
   ```

2. Preview auto-tune recommendations:
   ```bash
   curl -s "http://localhost:8000/v1/pruner/auto-tune?dry_run=true"
   ```

3. Apply if the recommendations make sense:
   ```bash
   curl -s "http://localhost:8000/v1/pruner/auto-tune?dry_run=false"
   ```

---

## Supersession Chain on Upsert

When a fact is upserted with the same key as an existing active fact, the existing
fact is automatically marked as `superseded` and the new fact takes its place.
This creates a chain that can be traced via `GET /v1/facts/{key}/graph`.

```bash
# Upsert with same key
curl -s -X POST "http://localhost:8000/v1/facts" \
  -H "Content-Type: application/json" \
  -d '{"key": "infra/deploy-url", "value": "https://deploy-v2.example.com"}'

# Trace the chain
curl -s "http://localhost:8000/v1/facts/infra/deploy-url/graph"
```

## Pruner Env Vars

| Env Var | Default | Description |
|---|---|---|
| `RAGAMUFFIN_PRUNER_ENABLED` | `false` | Master switch |
| `RAGAMUFFIN_PRUNER_STALE_DAYS` | `90` | TTL before stale flagging |
| `RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD` | `0.5` | Low-confidence threshold |
| `RAGAMUFFIN_PRUNER_CONFLICT_THRESHOLD` | `0.85` | Cosine similarity for contradiction detection |
| `RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD` | `0.0` | Importance skip for stale scan |
| `RAGAMUFFIN_PRUNER_SCAN_INTERVAL` | `24h` | Stale/conflict/supersede scan interval |
