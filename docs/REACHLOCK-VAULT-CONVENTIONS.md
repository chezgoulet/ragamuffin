# REACHLOCK Vault Conventions

This document describes the conventions that REACHLOCK uses on top of
Ragamuffin's general-purpose REST surface. The conventions are *profiling*
of existing behaviour: any client (REACHLOCK, hermes-agent, a CLI) can use
the same endpoints, and the conventions below describe what the REACHLOCK
profile expects to find in the vault.

If you are integrating a new client, you can follow these conventions and
be confident a Ragamuffin deployment will serve you. If you need something
the conventions do not provide, propose it as a *general* Ragamuffin
feature — never as a client-specific fork.

## Vault naming

- **One vault per soul:** `soul_<npc_id>` (e.g. `soul_tib`).
  Instantiating a soul ingests its `memory_seeds` (soul schema v1) as
  first-person documents into the matching vault.
- **One shared `lore` vault:** world knowledge any literate NPC could
  know. The same vault is shared across all souls in a deployment.
- **Vault names** are validated by `config.ValidVaultName`: lowercase
  `[a-z0-9-:]`, max 64 chars. The `soul_` and `lore` prefixes are
  advisory, not enforced.

### Status: `soul_<npc_id>` uses underscores — `ValidVaultName` rejects them

The `soul_<npc_id>` convention above uses an underscore between
`soul` and the NPC id, but `ValidVaultName` (as of v0.9.3) only
accepts `[a-z0-9-:]`. So the literal names `soul_tib`,
`soul_mara`, etc. **fail vault creation and auto-provisioning
today.** Workarounds:

- Use the hyphen form: `soul-tib`, `soul-mara`. The convention
  becomes `soul-<npc_id>`. The pruner and briefing endpoints work
  unchanged.
- Wait for the underscore to be added to `ValidVaultName`. The
  change is one line in `internal/config/config.go` and is purely
  additive (no existing valid name becomes invalid). Tracked in the
  REACHLOCK backlog; the R4 contract test uses `soul_a` / `soul_b`
  to remain compatible with the current validator.

## Per-vault isolation

Ragamuffin indexes every vault into its own collection, both for chunks
(`ragamuffin_<name>` by default) and for facts (`ragamuffin_<name>_facts`).
A query against `soul_a` never returns `soul_b` content. This isolation is
a property of the vector store, not a server-side filter; the contract
test in `internal/server/contract_test.go` (R4) proves it holds across
both the Qdrant and the embedded backends.

## Memory record shape

Documents are markdown with YAML front-matter. The server treats front-
matter fields as metadata; the rest of the document is the chunked
content.

```markdown
---
importance: 0.7
tags: [preference, relationship]
tick: 10450
---

Tib prefers dark roast, ground fine, brewed slow. The player noticed
this when they shared a thermos at the ruin's edge.
```

The front-matter fields the REACHLOCK profile uses:

- `importance` (float 0..1) — drives the pruner'"'"'s importance threshold
  (see `RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD`). A memory with
  `importance: 0` is pruneable on the next cycle; `importance: 1` is
  kept indefinitely. The pruner is the implementation of
  GAME-DESIGN.md'"'"'s "relationships decay if you'"'"'re gone too long" — low-
  importance memories fade, contradicted beliefs get superseded, and a
  soul that hasn'"'"'t seen the player in two in-game years genuinely
  half-remembers them.
- `tags` (list of strings) — open vocabulary. REACHLOCK uses values like
  `preference`, `relationship`, `event`, `goal`, `identity`, `opinion`
  (the categories the LLM-extraction prompt also returns). The server
  stores tags in the Qdrant payload'"'"'s `tags` field and indexes them for
  filter queries.
- `tick` (int) — the in-game clock, not wall time. Ragamuffin'"'"'s pruner
  reads `tick` from the source file'"'"'s front-matter (or falls back to the
  file'"'"'s mtime) so recall and prune can reason about the game'"'"'s
  calendar, not just "days since last edit." For souls, the `tick` is
  recorded per-message by the host and propagated to all memory records
  the conversation produces.

The content below the front-matter is chunked by `chunker.ChunkFile`
with the configured strategy (default token-based) and indexed in the
vault'"'"'s collection.

### Status: front-matter parsing on `POST /v1/documents`

**As of the v0.9.3 main, `POST /v1/documents` does NOT parse the
front-matter from the `content` field.** The whole markdown string
(front-matter + body) is chunked and indexed as text. The
front-matter is *searchable* — `tags: preference` becomes searchable
content — but the structured fields are not extracted into the chunk
payload.

Structured fields can be sent through:

- `tags: []` — the documents endpoint accepts tags directly. They are
  stored on every chunk produced by the document.
- `meta: {...}` — accepted by `Indexer.Ingest` (the lower-level API
  used by `/v1/ingest`), but not by `/v1/documents` itself today.

To get `importance` and `tick` onto the chunk payload via the REST
surface, either:

1. Send through `POST /v1/ingest` (which accepts a `meta` map and
   merges every key into the chunk payload — see
   `internal/indexer/indexer.go:Ingest`).
2. Wait for `POST /v1/documents` to grow a `meta` field. The R4
   contract test does not assert on this today, so adding it is a
   pure additive change.

The pruner'"'"'s `importance` filter today (`RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD`)
operates on the pruner-computed importance (derived from access
recency and confirmation count), not on a user-supplied `importance`
field. Memory records that want to drive the threshold through the
front-matter convention need a follow-up that threads
`meta.importance` through the indexer → pruner pipeline. Tracked in
the REACHLOCK backlog.

## Endpoints (binding)

The full wire shape REACHLOCK binds to lives in the contract test
(`internal/server/contract_test.go`) and in MEMORY-INTERFACE.md. The
short version:

| Direction | Endpoint                                    | Body / query                                    | Returns |
|-----------|---------------------------------------------|-------------------------------------------------|---------|
| write     | `POST /v1/documents`                        | `{vault, content, source, tags, auto_extract}`  | `{status, vault, source}` |
| read      | `GET /vault/{name}/v1/hybrid?query=&limit=` | query string                                    | `{results: [{kind, score, content, source, ...}]}` |
| ingest    | `POST /v1/ingest/conversation`              | `{vault, messages[], context}`                 | `{status, conversation_id, fact_count, facts[]}` |
| orient    | `GET /vault/{name}/v1/briefing?agent_id=`   | query string                                    | `{version, vaults[], ...}` |

All four are part of Ragamuffin's public surface; nothing in this
document introduces a REACHLOCK-specific path. If a new capability is
needed, it becomes a new endpoint or a new metadata field on the
existing surface — never a breaking change.

## What the pruner handles for you

The pruner is Ragamuffin's lifecycle machinery: confidence decay,
supersession, staleness, conflict review. You don't need any client-
side memory-management code. Configure it via:

- `RAGAMUFFIN_PRUNER_ENABLED=true`
- `RAGAMUFFIN_PRUNER_STALE_DAYS=90` (when does a memory start fading)
- `RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD=0.0` (minimum `importance`
  that survives a scan; memories with `importance: 0` are gone in a day)
- `RAGAMUFFIN_PRUNER_CONFLICT_THRESHOLD=0.85` (how similar two
  contradicting facts need to be before the LLM review triggers)
- `RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL=72h` (how often the review runs)

For a REACHLOCK soul, the practical effect is: stop mentioning a fact
for `STALE_DAYS` and it quietly fades; contradict it once and the new
version supersedes the old; a soul that hasn't seen the player in two
in-game years genuinely half-remembers them.

## See also

- `docs/MEMORY-INTERFACE.md` — the upstream REACHLOCK spec; this file is
  the Ragamuffin-side mirror.
- `internal/server/contract_test.go` — R4: the contract test that
  proves the binding above keeps working in Ragamuffin's own CI.
- `docs/fact-lifecycle.md` — the pruner model in detail.
- `docs/ARCHITECTURE.md` — overall Ragamuffin architecture.
