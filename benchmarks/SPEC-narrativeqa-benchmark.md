# SPEC — NarrativeQA Benchmark for Ragamuffin

**Status:** Draft  
**Author:** dev  
**Date:** 2026-06-14  
**Issue:** [#690](https://github.com/chezgoulet/ragamuffin/issues/690)

## Objective

Add NarrativeQA (DeepMind's long-form reading comprehension dataset) to the
Ragamuffin benchmark gauntlet. This tests whether Ragamuffin can answer
genuine comprehension questions about full-length novels, plays, and film
scripts — a fundamentally harder problem than the synthetic conversation
recall tested by LongMemEval and LoCoMo.

## Dataset

- **Source:** [deepmind/narrativeqa](https://huggingface.co/datasets/deepmind/narrativeqa) on HuggingFace
- **Contents:** 1,572 stories, 46,765 QA pairs
- **Format:** Parquet files (train/test/validation splits)
- **Story sources:** Project Gutenberg (public domain novels), movie scripts
- **Stories include:** Moby Dick, A Tale of Two Cities, 1984, The Hobbit, and
  hundreds more — from short stories to full novels (200K+ tokens)

### Data Fields

| Field | Type | Description |
|---|---|---|
| `document.id` | string | Unique story identifier |
| `document.kind` | string | `"gutenberg"` or `"movie"` |
| `document.url` | string | Source URL |
| `document.word_count` | int | Approximate token count |
| `document.text` | string | Full story text (may be empty for some splits) |
| `document.summary.text` | string | Wikipedia summary |
| `question.text` | string | The question |
| `answers` | list[dict] | Ground-truth answers (each has `text` + `tokens`) |

## Strategy

### Story Filtering

Only **Gutenberg stories with full text available** will be ingested. Movie
scripts and stories without document.text are excluded to avoid copyright
concerns and ensure the story text is actually present.

Stories exceeding **500K tokens** are skipped (Ragamuffin practical ingest
limit).

### Vault Strategy

One vault per config per benchmark run, named `narrativeqa_{config}`. Each
story occupies a vault sub-path via story-summary and story-text source IDs
with story-level metadata tags. This keeps the vault count manageable (4
vaults for 4 configs, not 1,572 individual vaults) while providing enough
context isolation for per-story question answering.

### Chunking Strategy

Stories are chunked by word-count windows (default: ~4,000 words per chunk)
with overlap. Each chunk gets metadata tags identifying its story, chapter
position, and source. The benchmark queries `/ask` against the vault;
Ragamuffin's retriever finds the relevant chunks across the full story.

### Question Answering

Each question is sent to `/vault/{name}/ask` with `mode=rag`. The ground
truth is the first answer from the `answers` list (the dataset's primary
answer, verified by the original authors).

### Scoring

NarrativeQA answers are open-ended (not multiple choice). Scoring uses:

1. **LiteLLM judge** (same as LongMemEval/LoCoMo) — an LLM evaluator that
   judges semantic equivalence between the predicted and ground-truth answer.
2. **Fallback:** Token F1 (character-level precision/recall on the
   intersection of predicted and ground-truth tokens) when the judge is
   unavailable.

## Benchmark Configurations

All four standard configs (A, B, C, D) apply:

| Config | Ingest Mode | Ask Mode | Facts |
|--------|------------|----------|-------|
| A | Baseline (no auto_extract) | `rag` | No |
| B | auto_extract=true | `rag` | Yes |
| C | Baseline | `auto` | No |
| D | Full stack | `rag` | Yes + graph |

## Files

| File | Purpose |
|------|---------|
| `benchmarks/ingest_narrativeqa.py` | Download + parse + ingest script |
| `benchmarks/loaders/narrativeqa.py` | DatasetLoader for the benchmark runner |
| `benchmarks/run.py` | Updated to support `--datasets narrativeqa` |
| `benchmarks/download_datasets.sh` | Updated with NarrativeQA download step |

## Success Criteria

- NarrativeQA stories are ingested into Ragamuffin vaults
- Benchmark runner produces structured results (accuracy per story,
  per question type)
- Scores are credible enough to publish on the website
- The whole pipeline runs in under 4 hours (46K questions)
