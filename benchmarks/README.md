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

## Configs

The harness runs each benchmark against four Ragamuffin configurations:

| Config | Name | What it tests |
|---|---|---|
| **A** | Baseline | Pure `/ask` (recall + synthesize). No fact extraction or graph traversal. |
| **B** | Recall + Facts | `/ask` + `auto_extract: true` on ingest. Facts stored alongside chunks in Qdrant. |
| **C** | Tiered Recall | Efficiency variant using `mode: auto` on recall. Falls back from RAG to full-document when confidence is low. Score ≈ Config A. |
| **D** | Full Stack | `/ask` + facts + fact graph traversal (`/v1/facts/{key}/graph`). Most comprehensive — uses relationships for multi-hop reasoning. |

Config D is the primary target. A–C exist as ablation controls to measure the
contribution of each pipeline stage.

## Requirements

- Python 3.10+
- `pip install -r requirements.txt` (requests library)
- Running Ragamuffin instance on `http://localhost:8000` (or set `RAGAMUFFIN_URL`)
- Evaluator LLM access: set `OPENAI_API_KEY` in `.env`
- Datasets downloaded via `bash benchmarks/download_datasets.sh`

## Usage

```bash
# Full sweep — all configs, both datasets
bash benchmarks/run_all.sh

# Quick smoke test — Config A, LongMemEval only
bash benchmarks/run_all.sh --quick

# Single benchmark run
python3 benchmarks/run.py --benchmark longmemeval --config d --max-convs 5

# Resume from checkpoint
python3 benchmarks/run.py --benchmark locomo --config b --resume

# Full dataset (all conversations)
python3 benchmarks/run.py --benchmark longmemeval --config d --max-convs 0
```

## Vault Strategy

By default, all benchmark conversations for a given config share a single
vault (named after the benchmark and config, e.g. `locomo_b`). This matches
real-world deployment where an agent's knowledge accumulates in one vault.

**Planned:** A `--separate-vaults` flag will create one vault per conversation
(per `agent::<conversation_id>` convention) to isolate sessions more cleanly.
This is not yet implemented — see PR #571 for status. When writing downstream
tooling, consider both the shared-vault and per-conversation-vault patterns.

## Results

Results are written to `benchmarks/RESULTS.md`. Each run appends a section
with accuracy/Token F1 scores per config and a per-ability breakdown for
LongMemEval. Compare against published SOTA scores from the respective papers.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `RAGAMUFFIN_URL` | `http://localhost:8000` | Ragamuffin API endpoint |
| `LITELLM_URL` | `http://localhost:4000` | LiteLLM proxy URL |
| `LITELLM_MASTER_KEY` | `""` | LiteLLM API key |
| `OPENAI_API_KEY` | — | Evaluator LLM key (required) |

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
