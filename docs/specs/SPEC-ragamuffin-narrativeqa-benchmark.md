# SPEC: NarrativeQA Benchmark for Ragamuffin

**Status:** Draft  
**Author:** dev  
**Date:** 2026-06-23  
**Issue:** #690  
**Depends on:** Ragamuffin v0.9.x, Qdrant, LLM judge (gpt-4o-mini)

## Motivation

Current benchmarks (LongMemEval, LoCoMo) test Ragamuffin on synthetic and
conversational long-context QA. NarrativeQA adds a complementary dimension:
**narrative-level reading comprehension** — asking questions about full novels,
plays, and film scripts that require understanding plot, character, and events
across 50K–200K+ tokens of continuous text.

This tests whether Ragamuffin can:
- Retain narrative-level context across very long documents
- Answer questions that require synthesizing information from multiple chapters
- Distinguish between similar characters and events in the same story
- Handle texts at the scale of full novels (>500K tokens in some cases)

## Dataset

- **Source:** [deepmind/narrativeqa](https://huggingface.co/datasets/deepmind/narrativeqa) on HuggingFace
- **Format:** Parquet files (train/test/validation)
- **Size:** ~1,572 stories, ~46,765 QA pairs
- **Stories:** 1,572 documents from Project Gutenberg (novels), plus play/film scripts
- **License:** Apache 2.0 (dataset). Story texts from Gutenberg are public domain.
  Film scripts excluded due to uncertain copyright status.

### Filtering

Only `gutenberg`-kind stories with full text available are ingested, to avoid
copyright concerns. Stories exceeding 500K tokens are skipped (Ragamuffin
practical ingest limit).

## Implementation

### Data Pipeline

```
HuggingFace parquet  ──►  benchmarks/ingest_narrativeqa.py  ──►  data/narrativeqa/
                                                                   stories/*.json
                                                                   questions.json
                                                                   metadata.json
```

The ingest script (`benchmarks/ingest_narrativeqa.py`):
1. Downloads parquet files from HuggingFace via HTTP
2. Parses with pyarrow/pandas
3. Filters to Gutenberg-only stories with full text
4. Chunks stories by 4,000-word windows (200-word overlap)
5. Saves per-story JSON files + consolidated questions.json

### Loader

`benchmarks/loaders/narrativeqa.py` implements the `DatasetLoader` interface
used by all benchmark loaders. It reads the parsed data directory and produces
`Conversation` objects with:
- System message: Wikipedia summary of the story
- User message: Full story text (as single blob — chunking happens during ingest)

### Benchmark Runner

The existing `run.py` gauntlet runner already supports `narrativeqa` as a
dataset option (`--datasets narrativeqa`). It:
1. Loads stories via the NarrativeQALoader
2. Ingests all stories into a shared vault
3. Runs all QA pairs through /ask for each config (A/B/C/D)
4. Scores against ground truth using the existing scoring pipeline

### Scoring

NarrativeQA has multiple valid answers per question (collected from multiple
human annotators). The scoring approach:
1. **Primary:** Check if the Ragamuffin answer contains any of the ground-truth
   answers as a substring (relaxed exact match)
2. **Fallback:** Token F1 against the primary answer (used for LoCoMo)
3. **Judge:** LLM-as-judge (gpt-4o-mini) for ambiguous answers — same judge used
   for LongMemEval

## Configs

Same 4-config ablation as existing benchmarks:

| Config | Ingest | Ask mode | What it tests |
|--------|--------|----------|---------------|
| A | No facts | rag | Baseline recall + synthesis |
| B | auto_extract | rag | Fact-enhanced retrieval |
| C | No facts | auto | Tiered recall mode |
| D | auto_extract | rag + facts | Full stack |

## Baseline Expectations

Since this is the first run, there is no baseline. Expected floor: ~30% (random
/ naive). Realistic target for Config D: ~40–55% accuracy based on published
NarrativeQA results (original paper reports 46–56% BLEU-4 for human-level, 54%
for the best BART-based system).

## Output

Results are written to `benchmarks/results/` as JSONL trace files, matching the
format of LongMemEval and LoCoMo traces (see `benchmarks/README.md` for schema).

## Success Criteria

- [ ] All ~1,572 Gutenberg stories parsed and saved to disk
- [ ] All ~46K QA pairs extracted and indexed by story
- [ ] Ingest completes without error against a Ragamuffin instance
- [ ] Benchmark runner produces structured results (per-story accuracy)
- [ ] Full run completes in under 4 hours

## Open Questions

1. **Vault strategy:** One shared vault vs per-story vaults? Shared is simpler
   and matches real-world usage, but may leak context between unrelated stories.
   Current plan: shared vault (matches LongMemEval pattern).

2. **Chunk sizing:** 4,000 words per chunk with 200-word overlap. Heavier stories
   (200K+ tokens) will produce 50+ chunks. Verify that Ragamuffin's recall
   handles cross-chunk questions well.

3. **Scoring sensitivity:** Exact-match with multiple valid answers may
   underestimate accuracy. LLM-as-judge adds cost but may give fairer scores.
   Start with relaxed exact match; add LLM judge as a refinement pass.

4. **46K questions is a lot.** The benchmark runner should use progressive
   saving (already implemented) and batch questions per story to amortize
   context loading.

## Future Work (v1.2+)

- Per-genre accuracy breakdown (novels vs plays vs short stories)
- Cross-story questions (synthesizing knowledge across multiple stories)
- Time-series analysis: does accuracy degrade across story length?
