# SPEC-ragamuffin-narrativeqa-benchmark

**Status:** Implemented (v1.1 sprint — issue #690)
**Owner:** dev-chezgoulet
**Depends on:** benchmark harness (`benchmarks/run.py`, `benchmarks/loaders/*`), `RagamuffinClient`

## Objective

Add NarrativeQA — DeepMind's long-form reading-comprehension dataset — to the
Ragamuffin benchmark gauntlet. Ingest real narrative text (novels, plays)
into Ragamuffin vaults and answer the dataset's abstractive question–answer
pairs through `/ask`, scoring against the gold reference answers.

This complements the synthetic LongMemEval / LoCoMo conversations with genuine
novel-length comprehension questions (e.g. *Moby Dick*, *A Tale of Two Cities*,
*1984*, *The Hobbit*).

## Dataset

- **Source:** `deepmind/narrativeqa` on HuggingFace (parquet files via the
  dataset-viewer API: `https://huggingface.co/api/datasets/deepmind/narrativeqa/parquet/default`)
- **Contents:** 1,572 stories, 46,765 QA pairs
- **Format:** Parquet, one row per (story, question, reference-answers)
  - `document`: `{id, kind, url, file_size, word_count, start, end, summary, text}`
  - `question`: `{text, tokens}`
  - `answers`: `[{text, tokens}, ...]` (reference answers; typically 2)
- **License:** NarrativeQA CC BY-SA 4.0; story texts are public domain
  (Project Gutenberg) + licensed (film scripts). **Film scripts are skipped
  by default** — see Filtering below.

## Implementation

### Files

| File | Role |
|---|---|
| `benchmarks/loaders/narrativeqa.py` | `NarrativeQALoader` — parses parquet, filters, builds Conversations/Questions |
| `benchmarks/run.py` | Wired `--datasets narrativeqa`; per-story vault ingest + ask routing |
| `benchmarks/run_narrativeqa.py` | Standalone runner (smoke-test-friendly flags) |
| `benchmarks/download_datasets.sh` | Adds NarrativeQA parquet download step |
| `benchmarks/requirements.txt` | Adds `pyarrow` (required to read parquet) |
| `benchmarks/README.md` | Documents the new dataset + flags |

### Ingestion strategy

- **One vault per story** (`nqa-<kind>-<docid8>`) by default. Novels are
  independent works; isolating them keeps each `/ask` scoped to a single book
  and avoids stuffing 100k+ token documents into one shared vault.
- A `--shared-vault` mode (`run_narrativeqa.py`) collapses all stories into one
  vault for ablations.
- Stories are ingested as a single blob; Ragamuffin's indexer chunks
  internally by token window (matches LongMemEval/LoCoMo loaders).

### Filtering

- `kinds=["gutenberg"]` by default → **skips copyrighted film scripts** (`kind=="movie"`).
- `max_words=100_000` (≈133k tokens) cap → **skips oversized novels** that
  exceed Ragamuffin's practical ingest/context limits (the token-budget open
  question from the issue). Override with `--max-words`.
- Config B/D apply `auto_extract: true` on ingest (via `IngestPlan`).

### Scoring

- Per-question ground truth = reference answers joined with `|` (standard
  NarrativeQA accepts any reference answer).
- Scored by the existing LLM-judge (`core/litellm_judge.py`), with exact-match
  fallback when the judge is unavailable. Answers `correct` if judge score ≥ 0.5.
- Question types (heuristic): `character`, `setting`, `temporal`, `cause`,
  `method`, `plot`, `comprehension` — surfaced in per-type aggregates.

### Wiring

- `run.py --datasets narrativeqa [--config D]` runs the full pipeline.
- `run.py`'s `_run_dataset` builds a `conversation_id → vault` map and passes a
  `vault_resolver` to `run_qa` so each question is answered against its own
  story's vault. `ingest_all` gains a `use_conv_vault` flag for per-story ingest.

## Open questions — resolved

1. **Token budget.** Default `max_words=100_000` (~133k tokens) skips the
   largest novels; per-story vaults keep `/ask` context focused. Multi-hop
   reasoning across chapters is handled by Ragamuffin's RAG retrieval, not by
   manual chunk-by-chapter calls.
2. **Scoring method.** LLM-as-judge (GPT-4o, same harness as LongMemEval),
   exact-match fallback. Reference answers joined with `|`.
3. **Which stories qualify.** Default `gutenberg` only — excludes copyrighted
   film scripts. Override `--kinds gutenberg movie` to include scripts.

## Success criteria

- [x] NarrativeQA stories ingested into Ragamuffin vaults (per-story)
- [x] Benchmark runner produces structured results (accuracy per config, per question type)
- [x] Scores runnable end-to-end via `run.py` / `run_narrativeqa.py`
- [ ] Scores credible enough to publish on the website (requires a live
      Ragamuffin instance + judge LLM — run by CI on merge to `main`)
- [ ] Full 46k-question run under 4h (parallelize per story; pending live run)

## Usage

```bash
# Smoke test — 3 stories, all questions, Config D
python3 benchmarks/run_narrativeqa.py --max-stories 3

# Full Gutenberg set (auto-downloads parquet)
python3 benchmarks/run_narrativeqa.py

# Via the unified runner, all configs
python3 benchmarks/run.py --datasets narrativeqa
```
