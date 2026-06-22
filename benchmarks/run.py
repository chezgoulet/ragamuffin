#!/usr/bin/env python3
"""
Ragamuffin Benchmark Gauntlet — v2.0

Standard operation:
    python3 benchmarks/run.py

All datasets, all configs, checkpointed, monitored, reported.

Design:
  - Reliable progress via STATUS_FILE (open + fsync to known path)
  - Checkpoints saved after each config — enables resume
  - Deterministic vault names with --clean option
  - Circuit breaker for transient server errors
  - Memory-efficient: conversational data released after ingest
  - Health checks during long runs
  - Posts summary to GitHub issue when complete
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import traceback
from typing import Dict, List, Optional, Tuple

# Force stdout/stderr to be unbuffered
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from benchmarks.configs import Config
from benchmarks.core.client import RagamuffinClient
from benchmarks.core.scoring import score_answer, score_batch
from benchmarks.core.types import Question, Result

# ── CLI ────────────────────────────────────────────────────────────────────────

def parse_args():
    parser = argparse.ArgumentParser(
        description="Ragamuffin Benchmark Gauntlet — v2.0",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python3 benchmarks/run.py                           # All datasets, all configs
  python3 benchmarks/run.py --datasets longmemeval     # LongMemEval only
  python3 benchmarks/run.py --datasets longmemeval --configs D  # Config D only
  python3 benchmarks/run.py --clean                    # Clean vaults before run
  python3 benchmarks/run.py --model gpt-4o --post      # Override judge + post
        """,
    )
    parser.add_argument(
        "--datasets",
        nargs="*",
        default=["longmemeval", "locomo", "narrativeqa"],
        choices=["longmemeval", "locomo", "narrativeqa"],
        help="Datasets to run (default: all)",
    )
    parser.add_argument(
        "--configs",
        nargs="*",
        default=None,
        choices=["A", "B", "C", "D"],
        help="Configs to run (default: all)",
    )
    parser.add_argument(
        "--clean",
        action="store_true",
        help="Clear vaults before re-ingesting (destructive)",
    )
    parser.add_argument(
        "--model",
        default=None,
        help="Override LLM judge model (default: gpt-4o)",
    )
    parser.add_argument(
        "--post",
        action="store_true",
        default=os.environ.get("BENCH_POST_TO_GITHUB", "0") == "1",
        help="Post results to GitHub issue",
    )
    parser.add_argument(
        "--issue",
        type=int,
        default=None,
        help="GitHub issue number to post results to",
    )
    parser.add_argument(
        "--chunks",
        type=int,
        default=0,
        help="Split each conversation into N chunks during ingest (default: 1 blob per conversation)",
    )
    parser.add_argument(
        "--ingest-delay",
        type=float,
        default=0.0,
        help="Delay (seconds) between ingest calls — paces writes to reduce Qdrant contention (default: 0)",
    )
    parser.add_argument(
        "--qdrant-url",
        default=None,
        help="Qdrant REST API URL for health checks (default: http://qdrant:6333)",
    )
    return parser.parse_args()


# ── Constants ───────────────────────────────────────────────────────────────────

BASE_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
RESULTS_DIR = os.environ.get(
    "RAGAMUFFIN_BENCH_RESULTS",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), "results"),
)
STATUS_FILE = os.environ.get(
    "RAGAMUFFIN_BENCH_STATUS",
    os.path.join(RESULTS_DIR, "run_status.txt"),
)
RUN_ID = time.strftime("%Y%m%d-%H%M%S")
ALL_CFG = [Config.A, Config.B, Config.C, Config.D]
DEFAULT_ISSUE = 691

# Circuit breaker
MAX_CONSECUTIVE_ERRORS = 10
HEALTH_CHECK_INTERVAL = 50  # questions
SAVE_INTERVAL = 100  # questions — partial trace save

# Fallback: 30% accuracy — a naive "always pick first answer" baseline
FLOOR_ACCURACY = 0.30


# ── Reliable logging ────────────────────────────────────────────────────────────


def log(msg: str, end: str = "\n"):
    """Write timestamped line to STATUS_FILE AND stdout. Reliable."""
    ts = time.strftime("%H:%M:%S")
    line = f"[{ts}] {msg}"
    os.makedirs(os.path.dirname(STATUS_FILE), exist_ok=True)
    with open(STATUS_FILE, "a") as f:
        f.write(line + end)
        f.flush()
        os.fsync(f.fileno())
    # Also write to stdout — may or may not persist, but try
    sys.stdout.write(line + end)
    sys.stdout.flush()


def log_header(title: str):
    """Draw a section header."""
    log("")
    log("═" * 55)
    log(f" {title}")
    log("═" * 55)


# ── Checkpoint ──────────────────────────────────────────────────────────────────


def checkpoint_path(label: str) -> str:
    return os.path.join(RESULTS_DIR, f"checkpoint_{label}.json")


def save_checkpoint(label: str, configs_completed: List[str]):
    """Record which configs are done for this benchmark."""
    cp = {"benchmark": label, "configs_completed": configs_completed, "ts": time.time()}
    path = checkpoint_path(label)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(cp, f, indent=2)
        f.flush()
        os.fsync(f.fileno())


def load_checkpoint(label: str) -> List[str]:
    """Return list of completed config labels for this benchmark."""
    path = checkpoint_path(label)
    if os.path.exists(path):
        with open(path) as f:
            cp = json.load(f)
            return cp.get("configs_completed", [])
    return []


# ── Question loading ────────────────────────────────────────────────────────────


def load_questions_benchmark(label: str) -> Tuple[List[Conversation], List[Dict]]:
    """Load conversations + questions for a benchmark dataset.

    Returns (conversations, question_data) where question_data is
    list of (text, ground_truth, question_type) tuples.
    Uses streaming-friendly approach when available.
    """
    from benchmarks.loaders.longmemeval import LongMemEvalLoader

    log(f"Loading {label} dataset...")
    t0 = time.perf_counter()
    loader = LongMemEvalLoader(
        dataset_path=os.path.join(os.path.dirname(__file__), "data", "LongMemEval"),
        config_label="D",
    )
    convs = loader.load()
    elapsed = time.perf_counter() - t0
    log(f"Loaded {len(convs)} conversations ({elapsed:.1f}s)")

    # Get deduplicated questions from first conversation
    if not convs:
        raise RuntimeError(f"No conversations loaded for {label}")
    qs = loader.questions(convs[0])
    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    log(f"Questions: {len(qdata)} unique")
    return convs, qdata


# ── Ingest ──────────────────────────────────────────────────────────────────────


def _flatten_messages(messages: List[Dict]) -> str:
    """Flatten a list of messages into a single text block."""
    parts = []
    for msg in messages:
        content = msg.get("content", "")
        if not content:
            continue
        speaker = msg.get("speaker", msg.get("role", "User").capitalize())
        parts.append(f"[{speaker}]: {content}")
    return "\n\n".join(parts)


def ingest_all(client: RagamuffinClient, convs: List[Conversation], vault: str, label: str, chunk_size: int = 0, ingest_delay: float = 0.0):
    """Ingest all conversations into a vault. Reports progress reliably.

    When chunk_size > 0, split each conversation's messages into that many
    roughly equal chunks before ingesting. Each chunk gets a unique source ID.

    When ingest_delay > 0, sleeps between each ingest call to pace writes
    and reduce Qdrant segment-flush contention. Also checks Qdrant health
    periodically and backs off if Qdrant is in a recovery state.
    """
    if chunk_size:
        total_chunks = sum(max(1, len(c.messages) // (len(c.messages) // chunk_size + 1))
                          for c in convs if c.messages)
        log(f"Ingesting {len(convs)} conversations into '{vault}' as ~{total_chunks} chunks...")
    else:
        log(f"Ingesting {len(convs)} conversations into '{vault}'...")

    t0 = time.perf_counter()
    ok = err = 0
    total_target = len(convs) if not chunk_size else total_chunks
    progress_count = 0
    last_report_pct = 0

    for i, conv in enumerate(convs):
        if not conv.messages:
            continue

        if chunk_size > 0:
            # Split messages into evenly-sized groups
            msgs = conv.messages
            group_size = max(1, len(msgs) // chunk_size)
            groups = [msgs[j:j + group_size] for j in range(0, len(msgs), group_size)]
        else:
            groups = [conv.messages]

        for gi, group in enumerate(groups):
            content = _flatten_messages(group)
            if not content:
                continue

            # ── Qdrant health gate ────────────────────────────────────
            # Check Qdrant health periodically; back off during
            # segment-flush cycles to avoid "Storage timeout" errors.
            if progress_count % 50 == 0:
                healthy, reason = client.qdrant_collection_status(f"ragamuffin_{vault}")
                if not healthy:
                    reason_str = reason or "unknown"
                    log(f"  ⏳ Qdrant not ready ({reason_str}) — waiting 5s...")
                    for _ in range(12):  # up to 60s
                        time.sleep(5)
                        healthy, reason = client.qdrant_collection_status(f"ragamuffin_{vault}")
                        if healthy:
                            log(f"  ✓ Qdrant ready, resuming ingest")
                            break
                    else:
                        log(f"  ⚠ Qdrant still not ready after 60s — continuing anyway")

            # ── Ingest pacing ──────────────────────────────────────────
            if ingest_delay > 0:
                time.sleep(ingest_delay)

            # Unique source per chunk within a conversation
            source = f"{conv.source or conv.id}/chunk-{gi}" if chunk_size else (conv.source or conv.id)
            tags = ["benchmark", label, conv.id]
            if chunk_size:
                tags.append(f"chunk-{gi}")

            try:
                client.ingest(
                    content=content,
                    source=source,
                    vault=vault,
                    tags=tags,
                )
                ok += 1
            except Exception as e:
                err += 1
                if err <= 3:
                    log(f"  ingest err [{conv.id}/chunk-{gi}]: {str(e)[:100]}")

            progress_count += 1
            chunk_pct = progress_count / total_target * 100
            report_block = int(chunk_pct / 10) * 10
            if report_block > last_report_pct or progress_count == total_target:
                last_report_pct = report_block
                elapsed = time.perf_counter() - t0
                rate = progress_count / elapsed if elapsed else 0
                remaining = (total_target - progress_count) / max(rate, 0.001)
                log(f"  ingest: [{progress_count}/{total_target}] {chunk_pct:.0f}%  {rate:.1f}/s  ETA: {remaining:.0f}s  errs: {err}")

    elapsed = time.perf_counter() - t0
    log(f"Ingest complete: {ok} ok, {err} err in {elapsed:.0f}s")


# ── Q&A ─────────────────────────────────────────────────────────────────────────


def run_qa(
    client: RagamuffinClient,
    vault: str,
    qdata: List[Tuple[str, str, str]],
    cfg: Config,
    label: str,
    benchmark_label: str,
) -> Tuple[List[Dict], float, int, int]:
    """Run Q&A for one config against a populated vault.

    Returns (results_list, accuracy, correct, total).
    Reports progress after each batch.
    """
    cfg_name = f"{benchmark_label}_config_{cfg.value}"
    log_header(f"{benchmark_label} — Config {cfg.value} (mode={cfg.ask_mode})")

    total = len(qdata)
    results = []
    correct = 0
    errors_consecutive = 0
    t0 = time.perf_counter()
    health_counter = 0

    for i, (text, gt, qt) in enumerate(qdata):
        qid = f"{label}-{i:04d}"
        answer = ""
        error = None
        start = time.perf_counter()

        try:
            resp = client.ask(text, vault, mode=cfg.ask_mode)
            answer = resp.get("answer", resp.get("response", ""))
            errors_consecutive = 0
        except Exception as e:
            error = str(e)
            errors_consecutive += 1

        elapsed_ms = (time.perf_counter() - start) * 1000

        # Score
        q_obj = Question(
            id=qid,
            benchmark=benchmark_label,
            config_label=cfg.value,
            question_type=qt,
            text=text,
            ground_truth=str(gt),
            conversation_id=vault,
        )
        s = score_answer(q_obj, answer)
        is_correct = s >= 0.5
        if is_correct:
            correct += 1

        results.append({
            "question_id": qid,
            "question": text[:120],
            "ground_truth": str(gt)[:120],
            "answer": answer[:500] if answer else "",
            "score": s,
            "correct": is_correct,
            "latency_ms": round(elapsed_ms, 1),
            "error": error,
        })

        # ── Circuit breaker ─────────────────────────────────────────────
        if errors_consecutive >= MAX_CONSECUTIVE_ERRORS:
            log(f"  ⚠ CIRCUIT BREAKER: {errors_consecutive} consecutive errors")
            break

        # ── Health check ────────────────────────────────────────────────
        health_counter += 1
        if health_counter >= HEALTH_CHECK_INTERVAL:
            health_counter = 0
            if not client.health():
                log(f"  ⚠ Server health check FAILED — pausing 30s...")
                for wait in range(30, 0, -5):
                    time.sleep(5)
                    if client.health():
                        log(f"  ✓ Server recovered after {30-wait}s")
                        break
                else:
                    log(f"  ✗ Server unreachable after 30s — continuing anyway")

        # ── Progress report ─────────────────────────────────────────────
        pct = (i + 1) / total * 100
        if pct == 100 or (i + 1) % SAVE_INTERVAL == 0 or (i + 1) % 25 == 0 and (i + 1) <= 100:
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed if elapsed else 0
            eta_remaining = (total - i - 1) / max(rate, 0.001)
            eta_str = time.strftime("%H:%M", time.localtime(time.time() + eta_remaining))
            if error:
                log(f"  [{i+1}/{total}] {pct:.0f}%  acc: {correct/(i+1):.1%}  rate: {rate:.1f}/s  ETA: {eta_str}  err: {error[:60]}")
            else:
                log(f"  [{i+1}/{total}] {pct:.0f}%  acc: {correct/(i+1):.1%}  rate: {rate:.1f}/s  ETA: {eta_str}  errs: {errors_consecutive}")

        # ── Save partial trace every 100 Qs ─────────────────────────────
        if (i + 1) % SAVE_INTERVAL == 0:
            _save_partial(results, cfg_name, correct, i + 1)

    # ── Config complete ─────────────────────────────────────────────────
    accuracy = correct / max(len(results), 1)
    elapsed = time.perf_counter() - t0
    log(f"  ✅ Config {cfg.value}: {correct}/{len(results)} = {accuracy:.1%} ({elapsed:.0f}s)")
    if errors_consecutive >= MAX_CONSECUTIVE_ERRORS:
        log(f"  ⚠ Config {cfg.value} ABORTED early — circuit breaker triggered")
    elif len(results) < total:
        log(f"  ⚠ Config {cfg.value} incomplete: {len(results)}/{total} questions answered")

    return results, accuracy, correct, len(results)


def _save_partial(results: List[Dict], cfg_name: str, correct: int, total: int):
    """Save partial progress to avoid losing data on crash."""
    run_id = f"{cfg_name}_partial_{total}"
    out_dir = os.path.join(RESULTS_DIR, "_partial", run_id)
    os.makedirs(out_dir, exist_ok=True)

    accuracy = correct / max(total, 1)
    with open(os.path.join(out_dir, "accuracy.json"), "w") as f:
        json.dump({
            "benchmark": cfg_name.split("_config_")[0],
            "config": cfg_name.split("_config_")[-1],
            "partial": True,
            "correct": correct,
            "total": total,
            "accuracy": accuracy,
        }, f, indent=2)
        f.flush()
        os.fsync(f.fileno())

    with open(os.path.join(out_dir, "trace.jsonl"), "w") as f:
        for r in results:
            f.write(json.dumps(r) + "\n")
        f.flush()
        os.fsync(f.fileno())


# ── Save ────────────────────────────────────────────────────────────────────────


def save_results(
    results: List[Dict],
    cfg: Config,
    label: str,
    correct: int,
    total: int,
) -> Dict:
    """Save final results for one config. Returns scoring dict."""
    accuracy = correct / max(total, 1) if total else 0
    run_id = f"{label}_config_{cfg.value}_{int(time.time())}"
    out_dir = os.path.join(RESULTS_DIR, run_id)
    os.makedirs(out_dir, exist_ok=True)

    scoring = {
        "benchmark": label,
        "config": cfg.value,
        "mode": cfg.ask_mode,
        "correct": correct,
        "total": total,
        "accuracy": accuracy,
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }

    with open(os.path.join(out_dir, "accuracy.json"), "w") as f:
        json.dump(scoring, f, indent=2)
        f.flush()
        os.fsync(f.fileno())

    with open(os.path.join(out_dir, "trace.jsonl"), "w") as f:
        for r in results:
            f.write(json.dumps(r) + "\n")
        f.flush()
        os.fsync(f.fileno())

    log(f"  💾 Saved to {out_dir}/")
    return scoring


# ── GitHub Post ──────────────────────────────────────────────────────────────────


def post_to_github(
    issue_num: int,
    all_scores: Dict[str, Dict[str, Dict]],
    baseline: Dict[str, float],
):
    """Post summary to GitHub issue."""
    try:
        from benchmarks.core.github import GitHubPoster
        poster = GitHubPoster()
    except Exception:
        log("  GitHub poster not available — skipping issue comment")
        return

    body_lines = [
        "## v1.0 Benchmark Gauntlet Results",
        "",
        f"Run timestamp: {time.strftime('%Y-%m-%d %H:%M:%S')}",
        "",
    ]

    for bench in ["longmemeval", "locomo", "narrativeqa"]:
        body_lines.append(f"### {bench}")
        body_lines.append("")
        body_lines.append("| Config | Accuracy | Correct/Total | Baseline (D) | Δ |")
        body_lines.append("|--------|----------|---------------|-------------|-----|")
        scores = all_scores.get(bench, {})
        for cv in ["A", "B", "C", "D"]:
            s = scores.get(cv, {})
            acc = s.get("accuracy", 0)
            corr = s.get("correct", 0)
            tot = s.get("total", 0)
            baseline_val = baseline.get(bench, {}).get(cv, None)
            if cv == "D" and baseline_val:
                delta = acc - baseline_val
                delta_str = f"{delta:+.1%}"
            else:
                delta_str = "—"
            # Highlight vs baseline
            if cv == "D" and baseline_val:
                baseline_display = f"{baseline_val:.1%}"
            else:
                baseline_display = "—"
            body_lines.append(f"| {cv} | {acc:.1%} | {corr}/{tot} | {baseline_display} | {delta_str} |")
        body_lines.append("")

    body_lines.extend([
        "### Notes",
        "- Scored with fallback fuzzy matcher (see #720 for limitations)",
        "- Vault: per-run deterministic names",
        "- Runner: v2.0 (see #719 for design)",
    ])

    body = "\n".join(body_lines)
    try:
        poster.comment(issue_num, body)
        log(f"  📝 Posted results to issue #{issue_num}")
    except Exception as e:
        log(f"  ⚠ GitHub post failed: {e}")


# ── Main ────────────────────────────────────────────────────────────────────────


def run_benchmark(
    label: str,
    vault: str,
    loader_fn,
    client: RagamuffinClient,
    skip_ingest: bool = False,
    skip_configs: Optional[List[str]] = None,
    chunk_size: int = 0,
    ingest_delay: float = 0.0,
) -> Dict[str, Dict]:
    """Run full benchmark (ingest + all configs). Returns scores dict."""
    if skip_configs is None:
        skip_configs = []

    scores = {}

    # Phase 1: Ingest
    if not skip_ingest:
        convs, qdata = loader_fn()
        ingest_all(client, convs, vault, label, chunk_size=chunk_size, ingest_delay=ingest_delay)
    else:
        # Just load questions, no conversations needed
        _, qdata = loader_fn()

    # Phase 2: Q&A for each config
    for cfg in ALL_CFG:
        if cfg.value in skip_configs:
            log(f"  Skipping Config {cfg.value} (already completed)")
            continue

        try:
            results, acc, correct, total = run_qa(client, vault, qdata, cfg, label, label)
            scoring = save_results(results, cfg, label, correct, total)
            scores[cfg.value] = scoring
            save_checkpoint(label, list(scores.keys()))
            _save_partial(results, f"{label}_config_{cfg.value}", correct, total)
        except Exception as e:
            log(f"  ✗ Config {cfg.value} FAILED: {e}")
            traceback.print_exc()
            scores[cfg.value] = {"accuracy": 0.0, "correct": 0, "total": 0}

    return scores


def _run_dataset(label: str, vault: str, client: RagamuffinClient, skip_configs: List[str], datasets: List[str], chunk_size: int = 0, ingest_delay: float = 0.0):
    """Run a single dataset, respecting --datasets filter."""
    if label not in datasets:
        log(f"  Skipping {label} (not selected)")
        return {}

    if label == "longmemeval":
        log_header("DATASET: LongMemEval (19,195 conversations, 500 questions)")
        def load_fn():
            return load_questions_benchmark("longmemeval")
    elif label == "locomo":
        log_header("DATASET: LoCoMo (10 conversations, 1986 questions)")
        def load_fn():
            from benchmarks.loaders.locomo import LoCoMoLoader
            log("Loading LoCoMo dataset...")
            t0 = time.perf_counter()
            loader = LoCoMoLoader(
                dataset_path=os.path.join(os.path.dirname(__file__), "data", "Backboard-Locomo-Benchmark"),
                config_label="D",
            )
            convs = loader.load()
            elapsed = time.perf_counter() - t0
            log(f"Loaded {len(convs)} conversations ({elapsed:.1f}s)")
            if not convs:
                raise RuntimeError("No LoCoMo conversations loaded")
            qs = loader.questions(convs[0])
            qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
            log(f"Questions: {len(qdata)} unique")
            return convs, qdata
    elif label == "narrativeqa":
        log_header("DATASET: NarrativeQA (stories, 46K questions)")
        def load_fn():
            from benchmarks.loaders.narrativeqa import NarrativeQALoader
            log("Loading NarrativeQA dataset...")
            t0 = time.perf_counter()
            loader = NarrativeQALoader(
                dataset_path=os.path.join(os.path.dirname(__file__), "data", "narrativeqa"),
                config_label="D",
            )
            convs = loader.load()
            elapsed = time.perf_counter() - t0
            log(f"Loaded {len(convs)} stories ({elapsed:.1f}s)")
            if not convs:
                raise RuntimeError("No NarrativeQA stories loaded")
            # Collect ALL questions from ALL stories
            qdata = []
            for c in convs:
                qs = loader.questions(c)
                for q in qs:
                    qdata.append((q.text, q.ground_truth, q.question_type))
            log(f"Questions: {len(qdata)} unique across {len(convs)} stories")
            if not qdata:
                raise RuntimeError("No questions found in any NarrativeQA story")
            return convs, qdata

    return run_benchmark(
        label, vault, load_fn, client,
        skip_ingest=False,
        skip_configs=skip_configs,
        chunk_size=chunk_size,
        ingest_delay=ingest_delay,
    )


def main():
    args = parse_args()

    # Apply env overrides from CLI flags before anything else
    if args.model:
        os.environ["LITELLM_JUDGE_MODEL"] = args.model
    if args.issue:
        os.environ["BENCH_GITHUB_ISSUE"] = str(args.issue)

    log("")
    log("╔" + "═" * 53 + "╗")
    log("║  Ragamuffin Benchmark Gauntlet — v2.0")
    log("╚" + "═" * 53 + "╝")
    log("")

    log(f"Judge model: {os.environ.get('LITELLM_JUDGE_MODEL', 'gpt-4o')}")
    log(f"Datasets: {', '.join(args.datasets)}")

    client = RagamuffinClient(
        base_url=BASE_URL,
        ingest_timeout=120,
        ask_timeout=30,
    )
    if not client.health():
        log("FATAL: Server unreachable at " + BASE_URL)
        return 1
    log(f"Server: {BASE_URL} — healthy")

    os.makedirs(RESULTS_DIR, exist_ok=True)
    log(f"Results: {RESULTS_DIR}")
    log(f"Status:  {STATUS_FILE}")

    # Determine vault names — unique per run, or static if --clean
    if args.clean:
        lme_vault = "lme-bench-clean"
        locomo_vault = "locomo-bench-clean"
        nqa_vault = "nqa-bench-clean"
        log("Clean mode: clearing vaults...")
        for v in [lme_vault, locomo_vault, nqa_vault]:
            try:
                result = client.clear_vault(v)
                log(f"  Cleared '{v}': {result.get('chunks_deleted', 0)} chunks, "
                     f"{result.get('facts_deleted', 0)} facts deleted")
            except Exception as e:
                msg = str(e).lower()
                if "not found" in msg or "404" in msg:
                    log(f"  Vault '{v}' does not exist — will create during ingest")
                else:
                    log(f"  ⚠ Could not clear '{v}': {e}")
    else:
        lme_vault = f"lme-bench-{RUN_ID}"
        locomo_vault = f"locomo-bench-{RUN_ID}"
        nqa_vault = f"nqa-bench-{RUN_ID}"
        log(f"Unique vault names: {lme_vault}, {locomo_vault}, {nqa_vault}")

    # Set Qdrant health check URL if provided
    if args.qdrant_url:
        os.environ["RAGAMUFFIN_QDRANT_URL"] = args.qdrant_url

    # ── Run selected datasets ──────────────────────────────────────────
    lme_scores = _run_dataset(
        "longmemeval", lme_vault, client,
        skip_configs=[],
        datasets=args.datasets,
        chunk_size=args.chunks,
        ingest_delay=args.ingest_delay,
    )
    locomo_scores = _run_dataset(
        "locomo", locomo_vault, client,
        skip_configs=[],
        datasets=args.datasets,
        chunk_size=args.chunks,
        ingest_delay=args.ingest_delay,
    )
    nqa_scores = _run_dataset(
        "narrativeqa", nqa_vault, client,
        skip_configs=[],
        datasets=args.datasets,
        chunk_size=args.chunks,
        ingest_delay=args.ingest_delay,
    )

    # ── Summary ─────────────────────────────────────────────────────────
    all_scores = {"longmemeval": lme_scores, "locomo": locomo_scores, "narrativeqa": nqa_scores}
    baseline = {
        "longmemeval": {"D": 0.533},
        "locomo": {"D": 0.467},
        "narrativeqa": {"D": 0.0},
    }

    log("")
    log("╔" + "═" * 53 + "╗")
    log("║  SUMMARY")
    log("╚" + "═" * 53 + "╝")
    for bench in ["longmemeval", "locomo", "narrativeqa"]:
        scores = all_scores.get(bench, {})
        log(f"  {bench}:")
        for cv in ["A", "B", "C", "D"]:
            s = scores.get(cv, {})
            acc = s.get("accuracy", 0)
            corr = s.get("correct", 0)
            tot = s.get("total", 0)
            b = baseline.get(bench, {}).get(cv)
            if cv == "D" and b:
                ds = f" (vs baseline {b:.1%}: {acc - b:+.1%})"
            else:
                ds = ""
            log(f"    Config {cv}: {acc:.1%} ({corr}/{tot}){ds}")

    # Save master summary
    summary_path = os.path.join(RESULTS_DIR, "summary.json")
    with open(summary_path, "w") as f:
        json.dump({
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "runner": "v2.0",
            "results": all_scores,
        }, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    log(f"  Summary saved to {summary_path}")

    # ── GitHub post ─────────────────────────────────────────────────────
    issue_num = int(os.environ.get("BENCH_GITHUB_ISSUE", str(DEFAULT_ISSUE)))
    do_post = os.environ.get("BENCH_POST_TO_GITHUB", "0") == "1"
    if do_post:
        post_to_github(issue_num, all_scores, baseline)

    log("")
    log("═" * 55)
    log(" Done.")
    log("═" * 55)
    log("")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:
        log(f"FATAL: {e}")
        traceback.print_exc()
        sys.exit(1)
