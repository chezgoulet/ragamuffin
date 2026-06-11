# Ragamuffin — Benchmark Specification v0.1

**Status:** Draft — no active development
**Spec version:** 2026-06-07
**Author:** robot-chezgoulet

## Overview

Benchmark Ragamuffin's retrieval capabilities against two established long-term
memory benchmarks — **LongMemEval** and **LoCoMo** — to establish a baseline,
identify strengths and weaknesses, and quantify what each feature (pure vector
search, fact graph, tiered recall, review queue) contributes.

This is an honest assessment, not a marketing exercise. Publish results regardless
of outcome.

## Why Benchmark

1. **Establish a baseline** before optimizing. Without a number, every change is
   a guess.
2. **Compare feature configurations** — does the fact system improve recall over
   pure vector search? By how much?
3. **Identify weaknesses** — temporal reasoning, multi-hop, abstention — to guide
   future development.
4. **Market positioning.** An honest 55–65% with a clear architectural description
   is more credible than silence or marketing.

## Benchmarks

### LongMemEval

- **Source:** github.com/xiaowu0162/LongMemEval
- **Scale:** 500 curated questions, embedded in scalable multi-session chat histories
- **Skills tested:** Information extraction, multi-session reasoning, temporal
  reasoning, knowledge updates, abstention
- **Reporting standard:** "S" setting — shorter conversation histories (16K–26K
  tokens). This is what everyone reports for comparability.
- **Scoring:** LLM-as-judge (GPT-4o or GPT-4o-mini, pick one and be consistent).
  The benchmark repo includes evaluation prompts and scoring scripts.
- **Prompt template:** "I will give you several related memories between you and
  a user. Please answer the question based on the relevant memories."

### LoCoMo

- **Source:** github.com/Backboard-io/Backboard-Locomo-Benchmark (Backboard's
  reference implementation)
- **Scale:** 10 conversations (19–32 sessions each), 1,986 QA pairs
- **Skills tested:** Single-hop, multi-hop, temporal, open-ended
- **Excluded category:** Category 5 (adversarial) — following standard practice
- **Reporting standard:** Token-level F1 with Porter stemming. Exclude adversarial
  category.
- **Scoring:** LLM-as-judge, same evaluator model as LongMemEval

## Harness Design

A Python script (~200 lines) wrapping Ragamuffin's HTTP API. Both benchmarks are
Python-based — the harness integrates with their evaluation infrastructure.

### Core Loop

```
for each conversation in dataset:
    1. Create fresh vault (or clear + reindex)
    2. Ingest each session via POST /v1/ingest/conversation or ragamuffin_store
    3. Wait for indexing to complete (poll /health for indexing=false)
    
    for each question in conversation:
        4. Call POST /recall with question text
        5. Format results into benchmark prompt template
        6. Call configured LLM for answer
        7. Collect response
    
    8. Score against ground truth using benchmark's evaluation protocol
```

### Configurations to Test

The interesting variable is which Ragamuffin features are active:

| Config | Features | What it tests |
|--------|----------|---------------|
| A — Baseline | Pure vector search via `/recall` | Raw semantic retrieval, no structured knowledge |
| B — Recall + Facts | `/recall` + `/v1/facts` retrieval | Hybrid: does combining vector and structured facts improve recall? |
| C — Tiered Recall | `/recall?detail=l0` + `/v1/chunks/{id}` drill-down (once v0.7 ships) | Does the two-pass pattern (search wide, drill deep) change outcomes? |
| D — Full Stack | All of the above + fact graph traversal | Does relationship traversal help multi-hop questions? |

Each configuration gets its own score per benchmark. Report all four.

### Scoring Protocol

- **Evaluator:** GPT-4o-mini (cheaper, sufficient for pass/fail judgments)
- **Re-scoring:** Run each configuration 3 times, report mean + range
- **Abstention handling:** Count "I don't know" responses as correct if the
  ground truth expects abstention; count as incorrect if the ground truth
  has a known answer

## What to Expect

Be honest: Ragamuffin isn't optimized for these benchmarks. They test automated
recall from chat histories, which is different from Ragamuffin's design center
(organizational knowledge management with human-readable vaults, fact lifecycles,
review queues, and git-backed versioning).

**Expected baseline (Config A):** 55–65%
- Strengths should be information extraction and single-hop retrieval
- Weaknesses will be temporal reasoning and multi-hop across sessions
- The fact system (Config B) may add 3–8 points if it surfaces structured
  relationships that pure vector search misses
- Tiered recall (Config C) won't change scores — it's an efficiency improvement,
  not a recall improvement. Don't include it in the comparison table unless
  latency/cost metrics are also reported.

**Factors that work against Ragamuffin:**
- No specialized temporal reasoning (dates, recency weighting, session ordering)
- No entity extraction or cross-session entity resolution
- No conversation-specific memory compaction or summarization
- Chunk boundaries are heading-based, not semantic-turn-based — conversations
  in bench datasets are not markdown documents

**Factors that might help:**
- The fact lifecycle system (`/v1/facts`) stores structured facts independently
  of chunks, which could help with multi-session fact recall
- The fact graph (`GET /v1/facts/{key}/graph?depth=N`) traverses relationship
  edges, which could surface linked facts a pure vector search wouldn't find

## Deliverables

1. **Harness script** — `benchmarks/run.py` in the ragamuffin repo
2. **Benchmark runner** — `benchmarks/run_all.sh` that runs all 4 configurations
   against both benchmarks and produces a summary table
3. **Results document** — `benchmarks/RESULTS.md` with scores, configuration
   details, and analysis
4. **Published results** — blog post or README section with the honest numbers

## Non-Goals

- Optimizing specifically for these benchmarks (no benchmark-specific prompt
  engineering, no specialized conversation chunkers, no temporal reasoning hacks)
- Building a general-purpose evaluation suite — these two benchmarks are sufficient
  for v0.7
- Comparing against closed-source services (Mem0 cloud, Zep cloud) — compare
  against their published numbers from their own evaluations, don't run them
