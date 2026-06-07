# Ragamuffin Benchmark Results

**Run:** `YYYY-MM-DD HH:MM UTC`
**Ragamuffin version:** `v0.8.x`
**Evaluator LLM:** GPT-4o-mini

## LongMemEval (Setting S, ~500 questions)

| Config | Accuracy | Notes |
|--------|----------|-------|
| **A — Baseline** | — | Pure vector search |
| **B — Recall + Facts** | — | Hybrid retrieval |
| **C — Tiered Recall** | — | Efficiency variant (score ≈ A) |
| **D — Full Stack** | — | Relationship traversal |

### Per-ability breakdown (Config _)

| Ability | Accuracy |
|---------|----------|
| Extraction | — |
| Multi-session reasoning | — |
| Temporal reasoning | — |
| Knowledge updates | — |
| Abstention | — |

## LoCoMo (~1,986 QA pairs, Cat 1-4)

| Config | Token F1 | Notes |
|--------|----------|-------|
| **A — Baseline** | — | Pure vector search |
| **B — Recall + Facts** | — | Hybrid retrieval |
| **C — Tiered Recall** | — | Efficiency variant (score ≈ A) |
| **D — Full Stack** | — | Relationship traversal |

## Comparison

| System | LongMemEval (S) | LoCoMo (F1) | Notes |
|--------|-----------------|-------------|-------|
| Ragamuffin (Config A) | — | — | Baseline |
| Ragamuffin (Config D) | — | — | Full stack |
| Published SOTA | — | — | From respective papers |
