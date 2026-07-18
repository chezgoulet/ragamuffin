# Temporal Awareness for /ask — Complete Specification

> **Status:** Done (v0.9.x) — temporal metadata injected into `/ask` context, fact CRUD carries timestamps, temporal recall filters, session-end summarization with timestamps.

## Problem

Facts stored in Qdrant carry `valid_from` and `valid_until` metadata, but the
`/ask` endpoint never queries the facts collection. The LLM receives only chunk
text without temporal metadata, making it impossible to reason about conflicting
information over time (e.g., "sneakers under the bed" vs "moved to shoe rack").

This is the primary cause of low scores in temporal-order (22%), temporal-reasoning
(40%), and knowledge-update (46%) benchmark categories.

## Solution

Two additions to the `/ask` pipeline:

### 1. Parallel Fact Retrieval (server-side)

After the existing chunk search in `queryContext()`, if the facts collection is
available, perform a parallel vector search on facts with the same query embedding
and `topK`. Append a formatted `── Facts ──` block containing:

- `fact_key` — short identifier
- `fact_value` — the fact text
- `created_at` — when the fact was first created
- `valid_from` — when the fact became valid (RFC 3339)
- `valid_until` — when the fact expired (RFC 3339, empty = still valid)
- `score` — relevance to the query

Format per fact:
```
[Fact: user.sneakers_location | Created: 2026-06-10T14:30:00Z | Valid from: 2026-06-10T14:30:00Z | Valid until: 2026-06-10T15:00:00Z | Score: 0.892]
The user's sneakers are under the bed.
```

### 2. Prompt Instruction (LLM-side)

Append to the `Synthesize` prompt template:

> If facts include temporal metadata (Valid from / Valid until), use it to
> determine which fact was active at the time of the question. If the question
> asks about a specific time period, only use facts whose validity range
> overlaps with that period.

## Implementation

### Files Modified

- `internal/server/handlers.go`
  - Import `qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"`
  - Call `appendFactContext()` at end of `queryContext()`, before return
  - New helper: `appendFactContext(ctx, query, topK) string`
    - Embed query → search facts Qdrant collection → format result block
    - Guarded by `s.facts != nil` check
    - Only appends if chunk context is non-empty
    - Returns empty string on any error (silent degradation)

- `internal/llm/client.go`
  - `Synthesize` prompt: append temporal-reasoning instruction

### Data Flow

```
User query
  → EmbedSingle(query)                     [embedding]
  → qdrantFor.Search(vector, topK)         [chunk retrieval]
  → factsQdrantFor.Search(vector, topK)    [fact retrieval, NEW]
  → Combine chunks + fact blocks            [formatted context]
  → LLM Synthesize(prompt, context)         [generation]
```

### Error Handling

- `s.facts != nil` guard prevents panic on missing facts collection
- `appendFactContext` returns empty string on any error (embedding failure,
  search failure, no results) — `/ask` silently degrades to chunk-only context
- Fact retrieval operates on the same Qdrant collection as chunk search,
  inheriting its error handling

## Future Work

- Per-vault fact collections: currently facts may span multiple vaults;
  future work should scope fact search to the same vault as chunk search
- Fact-scoped `?query=` parameter for /v1/facts (separate from hybrid search)
- Temporal cross-referencing: return only facts whose valid_until > now
  when the current question has no explicit time context
