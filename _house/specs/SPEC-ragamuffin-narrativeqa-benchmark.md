# SPEC-ragamuffin-narrativeqa-benchmark

**Status:** Draft  
**Author:** dev  
**Issue:** chezgoulet/ragamuffin#690  
**Created:** 2026-06-22  

## Objective

Add NarrativeQA (DeepMind's long-form reading comprehension dataset) to the
Ragamuffin benchmark gauntlet. This tests whether Ragamuffin can answer genuine
comprehension questions about full novels — Moby Dick, A Tale of Two Cities,
1984, The Hobbit, and 1,569 more stories — after ingesting their full text.

## Dataset

- **Source:** [deepmind/narrativeqa](https://huggingface.co/datasets/deepmind/narrativeqa) on HuggingFace
- **Contents:** 1,572 stories, 46,765 question-answer pairs
- **Format:** Parquet files (train=24 shards, test=8 shards, validation=3 shards)
- **Split:** train (32,727 QA), test (10,419 QA), validation (3,619 QA)
- **License:** Apache 2.0 (dataset), CC BY-SA 4.0 (NarrativeQA annotations).
  Story texts are either public domain (Gutenberg) or licensed for research use
  (film scripts).
- **Stories include:** Novels and plays from Project Gutenberg (public domain),
  plus film scripts from various sources.
- **Memory profile:** ~35 GB raw parquet (all splits). Each novel text can be
  200K+ tokens (800K+ chars). Only Gutenberg stories are used in production
  runs to avoid film-script copyright concerns.

## Architecture

```
benchmarks/
├── data/narrativeqa/
│   ├── train/          # train-00000-of-00024.parquet through ...-00023
│   ├── test/           # test-00000-of-00008.parquet through ...-00007
│   └── validation/     # validation-00000-of-00003.parquet through ...-00002
├── loaders/
│   └── narrativeqa.py  # NarrativeQALoader (DatasetLoader subclass)
├── run_narrativeqa.py  # Dedicated runner for per-story Q&A
├── run.py              # Main gauntlet runner (integration)
└── download_datasets.sh
```

### Loader Design (`NarrativeQALoader`)

The loader extends the `DatasetLoader` ABC from `benchmarks/loaders/base.py`.

**Data flow:**
1. Reads parquet files one row-group at a time (memory-safe, ~10MB per read)
2. Filters to Gutenberg stories only (public domain; film scripts excluded)
3. Deduplicates stories by document ID (multiple questions per story)
4. Chunks long texts into ~4K token segments (~16K chars) at paragraph boundaries
5. Saves story metadata to JSONL cache for faster reloads

**Conversation format:**
- Each story becomes one `Conversation` with chunked messages
- First chunk includes the story summary + text; subsequent chunks are text-only
- All stories share one vault (`nqa-nqa-v1`), tagged by story document ID
- Questions reference their story via `conversation_id`

### Runner Design

**Main runner (`run.py` integration):**
- Added `narrativeqa` to the dataset dispatch
- Uses shared-vault ingest (all stories in one vault)
- Questions come from the first-loaded story (for now — see limitations)

**Dedicated runner (`run_narrativeqa.py`):**
- Ingests all stories into a run-specific vault
- Asks each question with story context
- Per-question checkpointing (JSON) for resume on crash
- Circuit breaker after 50 consecutive errors
- Per-type accuracy breakdown
- Results saved as `trace.jsonl` + `summary.json`

## Scoring

Uses the existing `score_answer` LLM-as-judge from `benchmarks/core/scoring.py`.
Each answer is scored against ground truth on a 0.0–1.0 scale (≥0.5 = correct).

**Question type inference:**
| Type | Heuristic | Example |
|---|---|---|
| character | who/whom/whose | "Who is Miss Delmer?" |
| location | where/which place | "Where does the story begin?" |
| temporal | when/how long/what year | "When does the battle occur?" |
| reasoning | why/what reason/cause | "Why does she leave?" |
| factual | how many/what number | "How many children does she have?" |
| description | what/which/describe | "What does the house look like?" |
| binary | did/was/had/is/are | "Did Frodo destroy the ring?" |
| unknown | — | fallback |

## Baselines

- **Human performance:** ~83–87% accuracy (reported in NarrativeQA paper)
- **BM25 baseline:** ~32–36%
- **Ragamuffin initial target (Config D):** ~40-50% (TBD)

## Configuration

All four Ragamuffin configs are tested:

| Config | RAG | Facts | Description |
|--------|-----|-------|-------------|
| A | Baseline | No | Pure recall, no fact extraction |
| B | Recall+Facts | Yes | Fact extraction on |
| C | Tiered | Tiered | Tiered recall strategy |
| D | Full Stack | Yes | Full pipeline |

## Memory & Performance Considerations

- **Memory:** Each parquet row-group read uses ~10-15MB. The full vault may
  exceed available memory (660MB available) — ingest is done story-by-story
  and individual novel texts are capped at 150K tokens.
- **Time:** ~35GB total parquet size. Ingest+QA on all stories takes
  approximately 4-8 hours depending on chunk count and server latency.
- **Checkpoints:** Saved after every question. Resume with `--resume`.

## Limitations (Current)

1. **Film scripts excluded.** Only Gutenberg (public domain) stories are used
   to avoid copyright concerns. This reduces the usable dataset from 1,572
   to approximately ~1,000 stories.
2. **Shared vault.** All stories share a single vault. This means questions
   about one story may retrieve chunks from another. This is a harder test
   (cross-story contamination resistance) but may underreport true accuracy.
3. **Chunk boundaries.** Story text is chunked at paragraph boundaries, which
   may split in the middle of relevant passages.
4. **Question deduplication.** Multiple questions about the same story are
   all asked independently — no follow-up context is maintained.

## Future Work

- Per-story vault isolation (separate vault per story for cleaner signal)
- Long-form answer evaluation (currently uses first answer token only)
- Gutenberg-only download script to skip film scripts
- Stream compression for large novel texts

## Related

- Ragamuffin Benchmark Gauntlet: see `benchmarks/README.md`
- Issue: chezgoulet/ragamuffin#690
- Dataset paper: Kociský et al. "The NarrativeQA Reading Comprehension Challenge" (2018)
