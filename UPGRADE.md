# Upgrade Guide

## v0.9.0 → v1.0.0

### Fact Re-Embed Migration

**What changed:** Facts are now stored with actual Qdrant vector embeddings for
semantic search. Prior to v1.0.0, facts were written with zero-vectors; hybrid
recall (`/v1/hybrid`) and semantic fact search now depend on real embeddings
for meaningful vector-match results.

**Impact during transition:**

- Facts created *before* the upgrade have zero vectors until the re-embed
  pruner pass runs against them. Against a real query vector they score ~0 and
  sort last in hybrid results.
- Hybrid fact search uses score threshold 0.0, so stale zero-vector facts can
  still surface with meaningless scores during the transition.
- Early hybrid query results may not reflect real ranking quality until
  migration completes.

**What to expect:**

- The pruner re-embeds stale facts on its normal schedule (see
  `RAGAMUFFIN_PRUNER_STALE_INTERVAL`, default 24h).
- Operators on large fact stores may want to reduce the interval temporarily
  to accelerate migration:
  ```bash
  RAGAMUFFIN_PRUNER_STALE_INTERVAL=1h
  ```
  Restore the default after the first pass completes.

**Verification:**

Check a fact's vector state via the facts API. Zero-vector facts will have a
`vector` field of all zeros or be absent from hybrid vector-match results.
After the pruner pass, previously zero-vector facts will carry valid embeddings
and appear in semantic search with meaningful scores.

**No data loss.** Facts are not deleted or modified beyond embedding. The
migration is purely additive — existing key/prefix/tag lookups are unaffected.
