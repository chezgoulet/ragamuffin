# SPEC — v0.9.x: Procedural Memory Extraction

**Status:** Draft — ready for review
**Confidence:** High — design is clear, codebase inventory complete
**Issues:** #317
**Dependencies:** None (independent of #314 and #553)

---

## Overview

Ragamuffin stores *what* agents know (facts) but not *how* they do things
(procedures). An agent that successfully debugs a service failure stores the
session trace but can't recall "last time I solved this, I checked health →
greped logs → restarted the service."

This spec adds procedural memory extraction: on session finalization, the
server optionally extracts repeatable action sequences from the session
trace and stores them as procedure facts that agents can retrieve via standard
`/v1/recall`.

---

## Approach

When a session is finalized via `POST /v1/sessions/{id}/finalize` with
`?extract_procedures=true`, a background goroutine:

1. **Scans the session turns** for action sequences of 3+ consecutive steps
   that led to a resolution (a positive outcome — test passes, service
   recovers, PR created, error resolved)
2. **Generates a procedure name** from the sequence pattern and trigger
3. **Deduplicates** against existing procedure facts (same vault, similar name)
4. **Writes as a fact** with `type: procedure` in the payload

Procedures are stored as facts in the agent's vault — no new storage backend.
They're retrieved via standard `/v1/recall {"query": "how do I recover nginx?"}`
alongside other facts.

---

## Fact Format

```json
{
  "key": "procedure-<fingerprint>",
  "value": {
    "name": "Recover nginx after config change",
    "trigger": "nginx config test fails or service won't restart",
    "steps": [
      "Run `nginx -t` to check syntax",
      "Read `journalctl -u nginx --since \"5 min ago\"` for errors",
      "Restore previous config from `/opt/backups/nginx/`",
      "Run `systemctl reload nginx`",
      "Verify with `curl -sI http://localhost | head -1`"
    ],
    "source_session": "session-abc123",
    "success_count": 3,
    "last_used": "2026-06-09T00:00:00Z"
  },
  "confidence": 0.85,
  "type": "procedure"
}
```

The `key` is deterministic: `procedure-<hash(name + trigger)[:12]>` for dedup.
`type` is stored in the Qdrant payload for filtered recall.

---

## Implementation

### New Package: `internal/procedural/`

```
internal/procedural/
  extractor.go    — sequence extraction from session turns
  dedup.go        — dedup against existing procedure facts
  writer.go       — writes procedure facts to Qdrant
  types.go        — Procedure struct, constants
  extractor_test.go
  dedup_test.go
  writer_test.go
```

### Extractor Logic (`extractor.go`)

Input: the session's turn log (list of messages with `role` and `content`).

Algorithm:
1. Collect all assistant turns (agent actions)
2. Filter to turns where the content includes action-like language:
   - Commands: `run`, `check`, `read`, `grep`, `restart`, `verify`, `curl`, etc.
   - Structured steps: numbered lists, "Step 1:", bullet sequences
3. Group consecutive action turns into sequences of 3+
4. For each sequence, check the *following* user turn (or final result) for
   positive outcome signals:
   - Contains "ok", "works", "resolved", "fixed", "pass", "success"
   - Exit code 0 in tool results
   - No error or failure keywords in the result
5. If positive outcome confirmed, emit a `Procedure` struct

```go
type Procedure struct {
    Name          string   `json:"name"`
    Trigger       string   `json:"trigger"`
    Steps         []string `json:"steps"`
    SourceSession string   `json:"source_session"`
    SuccessCount  int      `json:"success_count"`
    LastUsed      string   `json:"last_used"`
}

func Extract(sessionTurns []Turn) []Procedure { ... }
```

### Dedup Logic (`dedup.go`)

Before writing, query the vault's facts for existing procedures with similar
names. Use Qdrant's similarity search on the `name` field (embedded during
fact write — reuse the existing embedding pipeline).

If a procedure with similarity >0.85 exists:
- Increment its `success_count`
- Update `last_used`
- Do NOT create a duplicate
- Returns the existing key so writer does an update instead

```go
func Dedup(ctx context.Context, qdrant QdrantClient, vault string, proc Procedure) (existingKey string, shouldUpdate bool, err error) { ... }
```

### Writer Logic (`writer.go`)

Writes the procedure as a fact to the vault's Qdrant collection. Uses the
existing `FactStore` interface — no new storage code needed.

```go
func Write(ctx context.Context, store FactStore, vault string, proc Procedure) error
```

The `type: procedure` field is stored in the Qdrant payload, enabling filtered
recall: `recall "how do I recover nginx"` returns procedure facts alongside
regular facts.

### Session Finalization Hook

Modify `internal/server/sessions.go` — in the `handleSessionFinalize` handler
(or wherever `POST /sessions/{id}/finalize` routes):

```go
if r.URL.Query().Get("extract_procedures") == "true" {
    go s.extractProcedures(ctx, vault, sessionID)
}
```

The goroutine:
1. Loads the session turns from the logstore
2. Calls `procedural.Extract(turns)`
3. For each extracted procedure: calls `procedural.Dedup()`, then `procedural.Write()`
4. Logs the result (procedures created, updated, skipped)

The goroutine runs in the background — the finalize response is returned
immediately without waiting for extraction.

---

## Configuration

| Variable | Type | Default | Description |
|---|---|---|---|
| `RAGAMUFFIN_PROCEDURAL_ENABLED` | bool | `false` | Master toggle. Default off — opt-in per deployment |
| `RAGAMUFFIN_PROCEDURAL_MIN_STEPS` | int | `3` | Minimum action steps to form a procedure |
| `RAGAMUFFIN_PROCEDURAL_DEDUP_THRESHOLD` | float | `0.85` | Cosine similarity threshold for dedup |

---

## New / Modified Endpoints

### `POST /v1/sessions/{id}/finalize?extract_procedures=true`

**Existing endpoint — new query parameter only.** When `extract_procedures=true`
and `RAGAMUFFIN_PROCEDURAL_ENABLED=true`, triggers background extraction.

Response (unchanged — extraction is async):

```json
{
  "session_id": "session-abc123",
  "status": "finalized",
  "extracting_procedures": true
}
```

### /v1/recall (no change)

Procedure facts appear naturally in recall results because they share the same
Qdrant collection as regular facts. Agents query `"how do I recover nginx"` and
get procedure facts alongside factual answers.

For filtered recall (procedures only), agents can use existing recall parameters
— no API change needed since `type: procedure` is in the Qdrant payload.

---

## Files to Create/Modify

| File | Change |
|---|---|
| `internal/procedural/types.go` | **New** — Procedure struct, constants |
| `internal/procedural/extractor.go` | **New** — sequence extraction logic |
| `internal/procedural/extractor_test.go` | **New** — tests with mock session traces |
| `internal/procedural/dedup.go` | **New** — dedup against existing procedures |
| `internal/procedural/dedup_test.go` | **New** — dedup tests (similar, identical, new) |
| `internal/procedural/writer.go` | **New** — writes procedure facts via FactStore |
| `internal/procedural/writer_test.go` | **New** — writer integration tests |
| `internal/server/sessions.go` | Add `extract_procedures` query param, background goroutine |
| `internal/server/config.go` | Add `ProceduralEnabled`, `ProceduralMinSteps`, `ProceduralDedupThreshold` |
| `smoke_test.sh` | Add session finalize with procedure extraction |

---

## Testing Requirements

- **Extractor unit tests:** 3+ steps with positive outcome → procedure emitted.
  Steps without positive outcome → no procedure. Empty session → no procedure.
  Edge cases: single tool call, error chain (no resolution), mixed action/reasoning.
- **Dedup tests:** identical procedure → update existing. Similar (>0.85) →
  update existing. New (no match) → write new. Empty vault → write new.
- **Writer tests:** write to a real (or mocked) Qdrant collection, verify
  retrieval via /v1/recall with `type: procedure` payload.
- **Endpoint test:** POST /sessions/{id}/finalize?extract_procedures=true with
  `RAGAMUFFIN_PROCEDURAL_ENABLED` set and unset.
- **Batch test:** 500 turns, check extraction completes within 2 seconds

---

## Breaking Changes

**None.** Extraction is opt-in via:
1. `RAGAMUFFIN_PROCEDURAL_ENABLED=true` in config (default: false)
2. `?extract_procedures=true` on the finalize call

Existing deployments: no behavior change, no performance impact.

---

## Resilience

- Extraction runs in a **background goroutine** — never blocks the finalize response
- **Per-procedure isolation:** one bad procedure extraction doesn't fail the batch
- **Dedup before write:** prevents procedure explosion from repeated sessions
- **Configurable min steps:** avoids extracting noise from short interactions
- **Configurable dedup threshold:** tune for precision vs recall per deployment
- **Master toggle:** zero overhead when disabled — the goroutine doesn't spawn
