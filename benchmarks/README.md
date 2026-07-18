# Ragamuffin Benchmarks

Benchmark harness for evaluating Ragamuffin's long-term memory capabilities
using two established long-context QA datasets.

## Benchmarks

### LongMemEval

~500 questions across 5 ability dimensions:
- **Extraction** — retrieving specific facts from long contexts
- **Multi-session reasoning** — synthesizing across multiple conversations
- **Temporal reasoning** — understanding event ordering and timing
- **Knowledge updates** — tracking fact revisions and contradictions
- **Abstention** — knowing when information isn't present

Setting S (single-session variant). Dataset downloaded via
`download_datasets.sh` places per-conversation files in `data/longmemeval/`.

### LoCoMo

~1,986 QA pairs covering Categories 1-4, scored by Token F1. Measures
fact-level retrieval accuracy across long conversation transcripts.

Dataset downloaded via `download_datasets.sh` into `data/locomo/`.

### NarrativeQA

~1,572 public-domain stories (Project Gutenberg novels & plays) and ~30k
abstractive QA pairs from DeepMind's long-form reading-comprehension dataset.
Tests whether Ragamuffin can answer genuine comprehension questions about
full novels (*Moby Dick*, *A Tale of Two Cities*, *1984*, *The Hobbit*).

- **Ingestion:** one vault per story (`nqa-gutenberg-<docid8>`) by default;
  film scripts (`kind=movie`) are skipped to avoid copyrighted texts.
- **Filtering:** `--max-words` caps story size (default 100k ≈ 133k tokens)
  to stay within ingest/context limits.
- **Scoring:** LLM-judge (GPT-4o) over reference answers joined with `|`,
  exact-match fallback. Per-type breakdown: character/setting/temporal/cause/
  method/plot/comprehension.
- **Data:** parquet files downloaded from HuggingFace via
  `download_datasets.sh` (or auto-downloaded on first run; needs `pyarrow`).

```bash
# Smoke test — 3 stories, Config D
python3 benchmarks/run_narrativeqa.py --max-stories 3
# Full Gutenberg set via the unified runner
python3 benchmarks/run.py --datasets narrativeqa --config d
```

The harness runs each benchmark against four Ragamuffin configurations:

| Config | Name | What it tests |
|---|---|---|
| **A** | Baseline | Pure `/ask` (recall + synthesize). No fact extraction or graph traversal. |
| **B** | Recall + Facts | `/ask` + `auto_extract: true` on ingest. Facts stored alongside chunks in Qdrant. |
| **C** | Tiered Recall | Efficiency variant using `mode: auto` on recall. Falls back from RAG to full-document when confidence is low. Score ≈ Config A. |
| **D** | Full Stack | `/ask` + facts + fact graph traversal (`/v1/facts/{key}/graph`) + query rewrite (HyDE) + listwise rerank. Most comprehensive — uses relationships for multi-hop reasoning and the full retrieval pipeline. |

Config D is the primary target. A–C exist as ablation controls to measure the
contribution of each pipeline stage.

Config D sends `rewrite: "hyde"` and `rerank: true` on every `/ask` call, so
the server must have `RAGAMUFFIN_RETRIEVAL_REWRITE` (LLM available) and
`RAGAMUFFIN_RERANK` enabled for these stages to take effect. When the server
has them disabled, the flags are safe no-ops and Config D degrades to plain
recall + synthesis.

## Requirements

- Python 3.10+
- `pip install -r requirements.txt` (requests library)
- Running Ragamuffin instance on `http://localhost:8000` (or set `RAGAMUFFIN_URL`)
- Evaluator LLM access: set `OPENAI_API_KEY` in `.env`
- Datasets downloaded via `bash benchmarks/download_datasets.sh`

## Usage

### Config D (Primary Target)

Config D is the full-stack configuration that uses all Ragamuffin features.
Run it for the most comprehensive evaluation.

```bash
# LongMemEval — 5 conversations for quick validation
python3 benchmarks/run.py --benchmark longmemeval --config d --max-convs 5

# Full dataset (all conversations)
python3 benchmarks/run.py --benchmark longmemeval --config d --max-convs 0

# LoCoMo — 5 conversations
python3 benchmarks/run.py --benchmark locomo --config d --max-convs 5
```

### Full Sweep

```bash
# All configs, both datasets
bash benchmarks/run_all.sh

# Quick smoke test — Config A, LongMemEval only
bash benchmarks/run_all.sh --quick
```

### Resuming

If a run is interrupted, resume from the last checkpoint:

```bash
python3 benchmarks/run.py --benchmark longmemeval --config d --resume
```

Checkpoint files are written to `data/<benchmark>/<config>_progress.json`.
Each question is saved as it completes, so resume is safe at any point.

### Other Configs

```bash
python3 benchmarks/run.py --benchmark longmemeval --config a --max-convs 5
python3 benchmarks/run.py --benchmark longmemeval --config b --max-convs 5
python3 benchmarks/run.py --benchmark locomo --config c --max-convs 5
```

## Output Structure

Each run produces artifacts in two places:

### Trace Directory: `results/<run_timestamp>/`

Every run creates a `results/` directory with one JSONL trace file per
dataset+config combination:

```
results/
└── 2026-06-08T14-30-00/
    ├── longmemeval_A.jsonl
    ├── longmemeval_B.jsonl
    ├── longmemeval_C.jsonl
    ├── longmemeval_D.jsonl
    ├── locomo_A.jsonl
    ├── locomo_B.jsonl
    ├── locomo_C.jsonl
    ├── locomo_D.jsonl
    └── run_metadata.json
```

`run_metadata.json` captures the run parameters:

```json
{
  "run_id": "2026-06-08T14-30-00",
  "started_at": "2026-06-08T14:30:00Z",
  "completion_time": "2026-06-08T16:15:00Z",
  "ragamuffin_version": "v0.9.0-rc.1",
  "configs": ["A", "B", "C", "D"],
  "benchmarks": ["longmemeval", "locomo"],
  "max_convs": 0,
  "total_questions": 0,
  "results_summary": {},
  "errors": []
}
```

### Trace Format (JSONL)

Each line in a trace file represents one question result:

```json
{
  "question_id": "lme_042",
  "benchmark": "longmemeval",
  "config": "D",
  "question_type": "extraction",
  "question": "What URL did the team decide on for the production database?",
  "ground_truth": "postgres://prod.internal:5432/primary",
  "ragamuffin_answer": "The team selected postgres://prod.internal:5432/primary",
  "correct": true,
  "latency_ms": 2847,
  "retries": 0,
  "error": null,
  "sources": [
    "session_042/summary.md",
    "session_042/turn_015.md"
  ],
  "vault": "longmemeval_d"
}
```

| Field | Type | Description |
|---|---|---|
| `question_id` | string | Unique question identifier from the dataset |
| `benchmark` | string | `longmemeval` or `locomo` |
| `config` | string | Config label (A, B, C, or D) |
| `question_type` | string | Ability dimension (extraction, multi_session, temporal, knowledge_update, abstention) |
| `question` | string | The question text sent to Ragamuffin |
| `ground_truth` | string | Expected answer from the dataset |
| `ragamuffin_answer` | string | What Ragamuffin returned |
| `correct` | boolean | Whether the answer matched ground truth (via judge or exact match) |
| `latency_ms` | int | End-to-end latency in milliseconds |
| `retries` | int | Number of retries (429/502/timeout) before success or failure |
| `error` | string or null | Error message if the question failed entirely; null on success |
| `sources` | array of strings | Source files/chunks Ragamuffin cited in its answer |
| `vault` | string | The vault name used for this run |

### Aggregated Results: `benchmarks/RESULTS.md`

After a full sweep, aggregated accuracy scores are written to
`benchmarks/RESULTS.md`. Each run appends a section with:
- Overall accuracy per config+dataset combination
- Per-ability breakdown for LongMemEval (extraction, multi-session, etc.)
- Token F1 scores for LoCoMo
- Latency statistics (p50, p95, p99)

Compare against published SOTA scores from the respective papers.

Use `benchmarks/core/trace.py` to load and analyze trace files programmatically:

```python
from benchmarks.core.trace import load_trace

results = load_trace("results/2026-06-08T14-30-00/longmemeval_D.jsonl")
for r in results:
    print(r.question_id, r.correct, r.latency_ms)
```

## Interpreting Results

### LongMemEval Scores

Scores are reported as **accuracy** — the fraction of questions answered
correctly, broken down by ability dimension:

| Dimension | What it measures | Target (Config D) |
|---|---|---|
| Extraction | Finding specific facts in long contexts | ≥ 0.80 |
| Multi-session reasoning | Synthesizing across multiple conversations | ≥ 0.65 |
| Temporal reasoning | Understanding event ordering and timing | ≥ 0.72 |
| Knowledge updates | Tracking fact revisions and contradictions | ≥ 0.68 |
| Abstention | Knowing when information isn't present | ≥ 0.60 |

**Overall accuracy** should be ≥ 0.70 for a production release.

### LoCoMo Scores

LoCoMo uses **Token F1** — token-level precision/recall on the intersection
of ground-truth and predicted answers. Scores range 0.0–1.0, with 1.0 being
a perfect token-level match. Target (Config D): ≥ 0.65.

### Latency

Latency is reported in milliseconds as p50, p95, and p99 across all questions
in a run. High p95/p99 spread suggests timeout or retry issues. Target p50 for
Config D: < 5000ms.

## Baseline Comparison

`benchmarks/baseline.json` stores the reference accuracy for regression
detection. Its structure:

```json
{
  "overall_accuracy": 0.72,
  "per_type_accuracy": {
    "extraction": 0.81,
    "multi_session": 0.68,
    "temporal": 0.74,
    "knowledge_update": 0.70,
    "abstention": 0.65
  },
  "baseline_updated_at": "2026-06-01T12:00:00Z",
  "baseline_commit": "a1b2c3d4e5f6..."
}
```

### How the baseline is used

1. **Benchmark CI** (`build.yml`) runs on merge to `main` (benchmark suite can be triggered manually)
2. Results are compared against `baseline.json` for each dimension
3. **Regressions** (> 3 percentage point drop in any dimension) flag the run for
   review and can block the release PR
4. **Improvements** update the baseline for the next release

### Updating the baseline

The baseline is updated when a release version is cut (merge to `main`):

1. Run the full benchmark suite: `bash benchmarks/run_all.sh`
2. Verify results in `benchmarks/RESULTS.md` — look for regressions or anomalies
3. Update `baseline.json` with the new accuracy values
4. Commit and tag with the release version (e.g. `v0.9.0`)

The baseline is not updated per-PR. It's a release-level reference point.

### Interpreting baseline drift

| Delta | Meaning |
|---|---|
| +0.03 to +0.10 | Improvement — new features are helping recall |
| -0.03 to +0.03 | Noise — within normal variance across runs |
| -0.03 to -0.10 | Minor regression — investigate. May be acceptable if tradeoff is clear |
| < -0.10 | Significant regression — likely blocks release. Investigate before promoting |

## Vault Strategy

By default, all benchmark conversations for a given config share a single
vault (named after the benchmark and config, e.g. `locomo_b`). This matches
real-world deployment where an agent's knowledge accumulates in one vault.

**Planned:** A `--separate-vaults` flag will create one vault per conversation
(per `agent::<conversation_id>` convention) to isolate sessions more cleanly.
This is not yet implemented — see PR #571 for status. When writing downstream
tooling, consider both the shared-vault and per-conversation-vault patterns.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `RAGAMUFFIN_URL` | `http://localhost:8000` | Ragamuffin API endpoint |
| `LITELLM_URL` | `http://localhost:4000` | LiteLLM proxy URL |
| `LITELLM_MASTER_KEY` | `""` | LiteLLM API key |
| `OPENAI_API_KEY` | — | Evaluator LLM key (required) |

For Config D's full retrieval pipeline, the **Ragamuffin server** (not the
harness) must also set:

| Server variable | Effect |
|---|---|
| `RAGAMUFFIN_RETRIEVAL_REWRITE` | Default query-rewrite mode; Config D overrides per-request with `hyde` |
| `RAGAMUFFIN_RERANK` | Enables listwise LLM rerank; required for Config D's `rerank: true` to apply |

## Architecture

The harness (`run.py`) is a v2 design with three key improvements over v1:

1. **Resilience** — exponential-backoff retry with `answer_with_retry()` for
   429, 502, and timeout errors. Never raises — marks failed questions as
   `ANSWER_FAILED` and continues.
2. **/ask-based** — uses the `/vault/{name}/ask` endpoint (synthesized answer)
   instead of raw `/recall`. Tests the full pipeline: embedding → search →
   LLM synthesis.
3. **Checkpointed** — saves per-question progress to a JSON file. Resuming
   with `--resume` skips already-answered questions. Progress file at
   `data/<benchmark>/<config>_progress.json`.
