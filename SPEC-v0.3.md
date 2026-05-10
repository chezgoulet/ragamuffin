# Ragamuffin v0.3 — Smart Vault

Extends [SPEC-v0.2.md](SPEC-v0.2.md). All v0.1 and v0.2 endpoints and
behaviors remain unchanged unless explicitly noted.

## Overview

v0.3 teaches the vault to test itself. Three pillars:

1. **Entity-level contradiction detection** — catch conflicts between
   unrelated sections that v0.1's random-pair comparison misses
2. **Pluggable chunking strategies** — markdown headings are a good default,
   but not the only way to chunk
3. **Multi-file draft PRs** — agents often need to update multiple files
   atomically

Plus scheduled auditing so the vault doesn't need an external cron job.

---

## New Feature: Entity Extraction & Contradiction Detection

### The Problem v0.1 Doesn't Solve

v0.1's semantic conflict detection pairs random chunks and asks the LLM if
they contradict. This catches conflicts when chunks happen to be paired:

```
✓ budget.md: "Q2 marketing budget is $5,000/month"
  vs.
✗ actuals.md: "Marketing spend running at $7,200/month"
  → Caught if Qdrant pairs these chunks. Missed if not.
```

But it misses conflicts between chunks that Qdrant doesn't pair:

```
✗ budget.md line 43: "Server costs: $500/month"
  vs.
✗ actuals.md line 12: "Linode bill: $1,200/month"
  → Same thing (infrastructure cost), different phrasing,
    different files, different sections. Qdrant won't pair them.
```

v0.3 extracts entities (numbers, dates, currency values, named entities) from
every chunk during indexing. At audit time, chunks that share entities but
differ in values are flagged — regardless of semantic similarity.

### Entity Types Extracted

| Entity type | Extraction method | Example |
|---|---|---|
| Currency | Regex: `\$[\d,]+(\.\d{2})?` | `$5,000/month` |
| Date | Regex: ISO 8601 + common formats | `2026-05-10`, `May 10, 2026` |
| Number | Regex: standalone numbers with units | `500/month`, `3 servers` |
| Percentage | Regex: `\d+(\.\d+)?%` | `15% increase` |
| Named entity | LLM-assisted (sampled) | "Linode", "Qdrant", "OpenAI" |

Entities are stored in Qdrant payload as structured key-value pairs:

```json
{
  "text": "Server costs: $500/month for 3 servers...",
  "source_file": "finance/budget.md",
  "entities": [
    {"type": "currency", "value": "$500", "context": "Server costs"},
    {"type": "number", "value": "3", "context": "servers"}
  ]
}
```

Entity extraction runs during indexing. LLM-assisted named entity extraction
is sampled (one chunk per file) to control costs. Regex entities are free.

### `/audit` Response Changes

The `/audit` response gains a new `entity_conflicts` field:

```json
{
  "stale_files": [...],
  "semantic_conflicts": [...],
  "entity_conflicts": [
    {
      "entity_type": "currency",
      "entity_context": "Linode / server costs",
      "conflicts": [
        {
          "source_file": "finance/budget.md",
          "text": "Server costs: $500/month",
          "value": "$500/month"
        },
        {
          "source_file": "finance/actuals.md",
          "text": "Linode bill: $1,200/month",
          "value": "$1,200/month"
        }
      ],
      "summary": "Both chunks describe infrastructure/server costs but differ by $700/month."
    }
  ],
  "gaps": [...],
  "duplicates": [...]
}
```

Entity conflict detection does NOT require an LLM. It's a pure entity-match +
value-compare operation. The `summary` field uses an LLM if configured, or a
template-generated message if not ("Two values found for currency 'Linode/server':
$500/month vs $1,200/month").

### Configuration

| Env var | Default | Notes |
|---|---|---|
| `RAGAMUFFIN_AUDIT_ENTITY_EXTRACTION` | `true` | Enable entity extraction during indexing |
| `RAGAMUFFIN_AUDIT_ENTITY_LLM` | `false` | Use LLM for named entity extraction (costs tokens) |

---

## New Feature: Pluggable Chunking Strategies

v0.1/v0.2 chunk by markdown headings. v0.3 makes chunking configurable so
different vault structures get appropriate treatment.

### Strategies

| Strategy | Best for | Behavior |
|---|---|---|
| `heading` (default) | Markdown knowledge bases | Split on H1–H3. H4+ stays in parent. |
| `paragraph` | Prose, legal docs, transcripts | Split on double newline. No heading awareness. |
| `fixed` | Logs, code, structured data | Split every N tokens with M-token overlap. |

### Strategy Interface

```go
type ChunkStrategy interface {
    // Chunk splits text into chunks, each with an optional header.
    Chunk(text, sourcePath string, modTime time.Time) []Chunk
}
```

New strategies are registered in `internal/chunker/`. Adding a strategy is a
single file implementing the interface + a case in the factory switch.

### Configuration

| Env var | Default | Notes |
|---|---|---|
| `RAGAMUFFIN_CHUNK_STRATEGY` | `heading` | `heading`, `paragraph`, or `fixed` |
| `RAGAMUFFIN_CHUNK_FIXED_SIZE` | `1000` | Tokens per chunk (fixed strategy only) |
| `RAGAMUFFIN_CHUNK_FIXED_OVERLAP` | `200` | Token overlap between chunks (fixed strategy only) |

Changing the chunk strategy requires a full re-index. Ragamuffin detects the
change at startup and re-indexes automatically. A log warning is emitted:

```
WARN: chunk strategy changed (heading → paragraph). Full re-index will run.
```

---

## Changed: Better Source Filtering

v0.1/v0.2 use Qdrant `Match_Text` for `source_filter`, which does substring
matching. A filter of `team/` matches `other-team/file.md`.

v0.3 creates a Qdrant payload index on `source_file` at collection creation
time and switches to `Match_Keyword` with an exact prefix workaround.
The workaround uses Qdrant's range-based filtering:

```
Key: "source_file"
Match: keyword >= "team/" AND keyword < "team0"  (0 is the next ASCII char after /)
```

This gives exact prefix matching: `team/` matches `team/file.md` but NOT
`other-team/file.md`.

### Backward Compatibility

The new filter is automatic when `source_filter` is provided. No config
change. The old behavior (substring match) had the same API surface — this
is a correctness fix, not a feature.

---

## New Feature: Multi-File Draft PRs

v0.1/v0.2 `/draft` accepts a single file. v0.3 accepts multiple files in a
single PR.

### Request (new `files` field)

```json
{
  "title": "update contractor rates and cross-references",
  "files": [
    {
      "path": "contractors/rates.md",
      "content": "# Contractor Rates\n\nUpdated Q2 2026..."
    },
    {
      "path": "contractors/index.md",
      "content": "# Contractors\n\n- [Rates](rates.md) — updated Q2 2026\n..."
    }
  ],
  "mode": "pr",
  "description": "Market adjustment per Q2 review. Updates rates and index."
}
```

The `target_path` + `content` fields still work for single-file drafts:

```json
{
  "title": "update rates",
  "content": "# New rates...",
  "target_path": "contractors/rates.md",
  "mode": "direct"
}
```

### Response (multi-file PR mode)

```json
{
  "mode": "pr",
  "pr_url": "https://github.com/chezgoulet/vault/pull/42",
  "branch": "ragamuffin/draft-abc123",
  "files_changed": 2
}
```

Deletion: set `content` to `""` in any file entry to delete that file.

### Validation

- `files` and `target_path` are mutually exclusive. Providing both returns
  `INVALID_REQUEST`.
- At least one file must have non-empty content (can't create an empty PR).
- Path traversal protection applies to every file path individually.

---

## New Feature: Scheduled Auditing

v0.1–v0.2 require an agent or cron job to call `/audit`. v0.3 adds an
internal scheduler so the vault tests itself.

### Configuration

| Env var | Default | Notes |
|---|---|---|
| `RAGAMUFFIN_AUDIT_SCHEDULE` | — | Cron expression. Unset = no scheduled audit. |
| `RAGAMUFFIN_AUDIT_WEBHOOK_URL` | — | POST audit results to this URL. Unset = log only. |

### Behavior

When `RAGAMUFFIN_AUDIT_SCHEDULE` is set, Ragamuffin runs the full audit
(all four checks) on that schedule. Results are logged at INFO level and
optionally POSTed to a webhook.

```json
// POSTed to RAGAMUFFIN_AUDIT_WEBHOOK_URL
{
  "timestamp": "2026-05-11T03:00:00Z",
  "stale_files": 3,
  "semantic_conflicts": 1,
  "entity_conflicts": 2,
  "gaps": 0,
  "duplicates": 0,
  "details_url": "http://ragamuffin:8000/audit"
}
```

The webhook payload is a summary, not the full audit response. The `details_url`
points back to Ragamuffin's `/audit` endpoint for the full report.

If the webhook POST fails, Ragamuffin logs the error and continues.
Scheduled audits do not retry.

### Startup Validation

If `RAGAMUFFIN_AUDIT_SCHEDULE` is set but not a valid cron expression,
Ragamuffin logs a warning and disables scheduled auditing. It does not
fail startup — scheduled auditing is optional.

---

## Docker Compose Changes (v0.3)

No new services. The existing `ragamuffin` service gains new env vars:

```yaml
services:
  ragamuffin:
    environment:
      RAGAMUFFIN_CHUNK_STRATEGY: ${RAGAMUFFIN_CHUNK_STRATEGY:-heading}
      RAGAMUFFIN_AUDIT_ENTITY_EXTRACTION: ${RAGAMUFFIN_AUDIT_ENTITY_EXTRACTION:-true}
      RAGAMUFFIN_AUDIT_ENTITY_LLM: ${RAGAMUFFIN_AUDIT_ENTITY_LLM:-false}
      RAGAMUFFIN_AUDIT_SCHEDULE: ${RAGAMUFFIN_AUDIT_SCHEDULE:-}
      RAGAMUFFIN_AUDIT_WEBHOOK_URL: ${RAGAMUFFIN_AUDIT_WEBHOOK_URL:-}
```

---

## New Environment Variables (v0.3)

| Variable | Required | Default | Notes |
|---|---|---|---|
| `RAGAMUFFIN_CHUNK_STRATEGY` | no | `heading` | `heading`, `paragraph`, or `fixed` |
| `RAGAMUFFIN_CHUNK_FIXED_SIZE` | no | `1000` | Tokens per chunk (fixed strategy) |
| `RAGAMUFFIN_CHUNK_FIXED_OVERLAP` | no | `200` | Token overlap (fixed strategy) |
| `RAGAMUFFIN_AUDIT_ENTITY_EXTRACTION` | no | `true` | Enable entity extraction |
| `RAGAMUFFIN_AUDIT_ENTITY_LLM` | no | `false` | Use LLM for named entity extraction |
| `RAGAMUFFIN_AUDIT_SCHEDULE` | no | — | Cron expression for scheduled audit |
| `RAGAMUFFIN_AUDIT_WEBHOOK_URL` | no | — | Webhook for audit result delivery |

---

## Testing Requirements (v0.3)

- All v0.1 and v0.2 tests must still pass.
- New: entity extraction unit tests (currency, date, number, percentage regex).
- New: chunk strategy tests (paragraph and fixed strategies).
- New: multi-file `/draft` integration tests.
- New: source filter prefix-match tests (does `team/` match `other-team/file.md`?).
- New: scheduled audit unit tests (cron parsing, webhook delivery).
- Coverage target: ≥ 80% on `internal/chunker/`, `internal/audit/`,
  `internal/scheduler/`.

---

## Breaking Changes

None. `target_path` + `content` still works. `source_filter` behavior
improves (substring → prefix) but in a way that only reduces false
positives — no previously-working filter breaks.
