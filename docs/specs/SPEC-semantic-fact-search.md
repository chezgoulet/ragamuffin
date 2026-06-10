# SPEC: Semantic Fact Search for v1.0

**Status:** DRAFT
**Author:** Vigilant
**Date:** 2026-06-10
**Target:** v1.0 (must land before public release)

## Problem

Facts (`/v1/facts`) are stored with zero vectors in the `ragamuffin_facts` Qdrant collection. This means:

- `/v1/facts?query="..."` doesn't exist — you can only find facts by exact key, prefix, or tag
- `/v1/hybrid?query="..."` returns chunks from vector search but facts only from key/prefix/tag — never from the query concept
- `/ask` and `/recall` completely skip facts during retrieval; they only search document chunks
- Agents that have stored hundreds of facts (infra configs, preferences, contractor rates) cannot discover facts by *what they're about*, only by *what they're named*

Semantic fact search was documented as a deliberate limitation ("no native semantic fact search" on the website, "zero vectors — no semantic search" in code comments). The code default for `RAGAMUFFIN_FACTS_VECTOR_SIZE` actually falls back to `RAGAMUFFIN_EMBEDDING_DIMS` (default 1536), but it never uses the embedder to produce real vectors — it writes zeros and the pruner's `reembedScan` slowly backfills them.

## Current State (what the code actually does)

| Aspect | Reality |
|---|---|
| Vector dimension default | **1536** (falls back via `FACTS_VECTOR_SIZE` → `EMBEDDING_DIMS` → 1536) |
| `zeroFactVector()` size | Creates zero-filled `[]float32` of `FactsVectorSize` length |
| Embedder on facts handler | Available as `s.embedder` — used only by `linkFactToChunks` |
| Pruner `reembedScan` | Already scans all facts, detects zero vectors, embeds values, updates Qdrant vectors |
| Pruner `conflictScan` | Already embeds fact values for semantic conflict detection |
| `/v1/hybrid` | Searches chunks by vector, facts by key/prefix/tag only |
| `/ask` / `/recall` | Skips facts entirely — only searches chunk collection |
| GET `/v1/facts?key=` | Scroll by payload filter only |
| Existing facts in prod | All have zero vectors (1536-dim zeros) sitting in the facts collection |

## Design

### 1. Embed facts on upsert

In `handleFactsPost`, replace `s.zeroFactVector()` with a real embedding of `fp.Value` via `s.embedder.EmbedSingle`. If the embedder is unavailable, fall back to zero vector (as before).

Same change in `handleFactsPut` — when the value is updated, re-embed and replace the vector.

**Not affected:** PATCH `/v1/facts` uses `SetPayload` (payload-only, no vector update). Bulk updates via PATCH don't change values — they update lifecycle fields. If a bulk update *does* change `fact_value`, the pruner's reembed scan will catch it on its next cycle (24h default, or can be triggered). Acceptable: PATCH is for metadata, POST/PUT are for content.

### 2. Add `?query` parameter to GET `/v1/facts`

Add a `query` URL parameter to the `handleFactsGet` handler. When present, instead of scrolling with payload filters, embed the query text and perform a Qdrant vector search (like `/recall` does for chunks) on the facts collection.

The response format stays the same — same `factResponse` struct, same response body — just the retrieval mechanism changes from scroll+filter to vector search.

If `query` AND scalar filters (tag, status, prefix) are both present, combine them: vector search with a Qdrant filter. This allows "find facts about X that are active" queries.

**Edge case:** If `key` (exact ID lookup) and `query` are both present, `key` wins. Exact key is the canonical identifier and should take priority.

### 3. Add fact vector search to `/v1/hybrid`

In `handleHybrid`, when `?query=` is present, add a parallel vector search of the facts collection alongside the existing chunk search. Return results interleaved with `kind: "chunk"` or `kind: "fact"` as appropriate, both ranked by score.

The hybrid endpoint already has the right structure for this — it just never had a vector search path for facts.

### 4. Clean up after existing zero-vector facts

After deploy, run the pruner's reembed scan against all existing facts. The `reembedScan` already does exactly this: scroll all facts, detect zero vectors, embed, update. It runs on a 24h timer by default. We can either:

- **Trigger it immediately** by adding a `POST /v1/facts/reembed` admin endpoint, or
- **Run the scan at startup** if the collection has >0 zero-vector points

Option A (admin endpoint) is simpler and doesn't add startup latency. Option B is safer (auto-recovery on restart). Given v1.0 timing, **Option B**: if any existing facts have zero vectors, run the reembed scan synchronously at startup with a log message. Remaining zero-vector facts trickle in via the periodic scan.

### 5. Migration impact

Existing facts have 1536-dim zero vectors. The collection's vector dimension is already 1536. After the code change, new facts will have real vectors. Old zero-vector facts coexist in the same collection — they'll rank near the bottom of any vector search (near-zero cosine similarity) without being disruptive. The startup scan re-embeds them progressively.

**No Qdrant collection recreation needed.** The dimension matches, and zero vectors are valid query targets (they just score near zero).

## Files to Change

| File | Change |
|---|---|
| `internal/server/facts.go` | 1. Remove `zeroFactVector()` function. Replace calls with `embedFactValue()` (real embed, fallback to zero). 2. Add `?query=` to `handleFactsGet`. 3. Update package comment block. |
| `internal/server/hybrid.go` | Add vector search of facts collection when `?query=` is provided |
| `internal/server/server.go` | Add startup scan for zero-vector facts |
| `internal/pruner/reembed.go` | No changes needed — already handles this. Verify it uses the right collection for multi-tenant. |
| `docs/index.html` | Update "no native semantic fact search" section to reflect current/planned state |
| `SPEC.md` | Update `FACTS_VECTOR_SIZE` default (currently says `4`, actual is `1536` via chain) |
| `.env.example` | Add `RAGAMUFFIN_FACTS_VECTOR_SIZE` with comment explaining it defaults to `EMBEDDING_DIMS` |
| `internal/server/facts.go` comments | Remove "Facts use zero vectors (no semantic search)" comment block; replace with accurate description |

## ~Acceptance Criteria~ (Why Write When You Can Just Do)

1. POST `/v1/facts` stores fact with real vector — verify via Qdrant point read
2. GET `/v1/facts?query="arch linux"` returns facts about Arch Linux ranked by relevance
3. GET `/v1/facts?query="arch linux"&status=active&tag=infra` returns active Arch Linux infra facts
4. PUT `/v1/facts?key=infra/arch` with new value updates the vector
5. `/v1/hybrid?query="arch linux"` returns both chunks and facts, interleaved
6. Startup log shows "reembedScan: re-embedded N facts" for existing zero-vector facts
7. PATCH `/v1/facts` (metadata-only) does NOT re-embed (value unchanged)
8. Fallback to zero vector when embedder is unavailable (degraded mode)
