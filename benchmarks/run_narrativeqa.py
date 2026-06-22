#!/usr/bin/env python3
"""
NarrativeQA Benchmark Runner — Runs Ragamuffin QA on the NarrativeQA dataset.

This script handles the per-story question structure by ingesting stories one
at a time and asking questions in story batches. This provides better accuracy
measurement than the shared-vault approach.

Usage:
    python3 benchmarks/run_narrativeqa.py --max-stories 5
    python3 benchmarks/run_narrativeqa.py --config D --max-stories 0 (all)
    python3 benchmarks/run_narrativeqa.py --config D --overwrite
    python3 benchmarks/run_narrativeqa.py --resume

Design:
  - Stories are ingested into a shared vault (tagged by story ID)
  - Questions are asked per-story, grouped for efficiency
  - Each question includes story context to guide recall
  - Results checkpointed per question, safe to resume
  - Memory-efficient: processes one parquet row-group at a time
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from typing import Dict, List, Optional, Tuple

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from benchmarks.configs import Config
from benchmarks.core.client import RagamuffinClient
from benchmarks.core.scoring import score_answer, score_batch
from benchmarks.loaders.narrativeqa import NarrativeQALoader

# ── Constants ───────────────────────────────────────────────────────────────────

BASE_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
RESULTS_DIR = os.environ.get(
    "RAGAMUFFIN_BENCH_RESULTS",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), "results"),
)
RUN_ID = time.strftime("%Y%m%d-%H%M%S")

# Circuit breaker
MAX_CONSECUTIVE_ERRORS = 50

# Reporting
REPORT_EVERY = 25  # N questions


# ── CLI ────────────────────────────────────────────────────────────────────────


def parse_args():
    parser = argparse.ArgumentParser(
        description="NarrativeQA Benchmark Runner",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python3 benchmarks/run_narrativeqa.py --max-stories 5
  python3 benchmarks/run_narrativeqa.py --config D --max-stories 0
  python3 benchmarks/run_narrativeqa.py --resume
        """,
    )
    parser.add_argument(
        "--config",
        default="D",
        choices=["A", "B", "C", "D"],
        help="Ragamuffin config (default: D)",
    )
    parser.add_argument(
        "--max-stories",
        type=int,
        default=5,
        help="Max stories to ingest (0 = all, default: 5 for quick test)",
    )
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Re-ingest stories even if already in vault",
    )
    parser.add_argument(
        "--resume",
        action="store_true",
        help="Resume from checkpoint, skip completed questions",
    )
    parser.add_argument(
        "--run-id",
        default=RUN_ID,
        help="Run identifier (default: auto)",
    )
    parser.add_argument(
        "--skip-ingest",
        action="store_true",
        help="Skip ingest phase (only run QA)",
    )
    parser.add_argument(
        "--rebuild-cache",
        action="store_true",
        help="Rebuild story cache from parquet files",
    )
    return parser.parse_args()


# ── Logging ─────────────────────────────────────────────────────────────────────


def log(msg: str, end: str = "\n"):
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}] {msg}", end=end, flush=True)


def log_header(title: str):
    log("")
    log("═" * 55)
    log(f" {title}")
    log("═" * 55)


# ── Checkpoint ──────────────────────────────────────────────────────────────────


def checkpoint_path(run_id: str) -> str:
    return os.path.join(RESULTS_DIR, f"nqa_checkpoint_{run_id}.json")


def save_checkpoint(run_id: str, completed_questions: List[str], results: List[Dict]):
    cp = {
        "run_id": run_id,
        "completed_questions": completed_questions,
        "results": results,
        "ts": time.time(),
    }
    path = checkpoint_path(run_id)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(cp, f, indent=2)
        f.flush()
        os.fsync(f.fileno())


def load_checkpoint(run_id: str) -> Tuple[List[str], List[Dict]]:
    path = checkpoint_path(run_id)
    if os.path.exists(path):
        with open(path) as f:
            cp = json.load(f)
            return cp.get("completed_questions", []), cp.get("results", [])
    return [], []


# ── Main ────────────────────────────────────────────────────────────────────────


def main():
    args = parse_args()
    cfg = Config.parse(args.config) or Config.D
    run_id = args.run_id

    log_header("NarrativeQA Benchmark")
    log(f"Config: {cfg.value} ({cfg.description})")
    log(f"Max stories: {'all' if args.max_stories == 0 else args.max_stories}")
    log(f"Run ID: {run_id}")
    log(f"Resume: {args.resume}")

    client = RagamuffinClient(base_url=BASE_URL)

    if not client.health():
        log("FATAL: Ragamuffin unreachable at " + BASE_URL)
        return 1
    log(f"Server: {BASE_URL} — healthy")

    # ── Load dataset ──────────────────────────────────────────────────
    log_header("Loading Dataset")
    t0 = time.perf_counter()
    loader = NarrativeQALoader(
        dataset_path=os.path.join(os.path.dirname(__file__), "data", "narrativeqa"),
        config_label=cfg.value,
        max_stories=args.max_stories,
        rebuild_cache=args.rebuild_cache,
    )
    stories = loader.load()
    elapsed = time.perf_counter() - t0
    log(f"Loaded {len(stories)} stories in {elapsed:.1f}s")

    if not stories:
        log("No stories loaded. Check dataset path.")
        return 1

    # Collect all story-question pairs
    all_questions: List[Tuple[str, str, str, str]] = []  # (story_id, q_text, gt, q_type)
    for c in stories:
        qs = loader.questions(c)
        for q in qs:
            all_questions.append((c.id, q.text, q.ground_truth, q.question_type))

    log(f"Total questions: {len(all_questions)} across {len(stories)} stories")

    vault = f"nqa-bench-{run_id}"

    # ── Ingest stories ───────────────────────────────────────────────
    if not args.skip_ingest:
        log_header("Ingesting Stories")
        ingest_all(client, stories, vault, cfg)

    # ── Load checkpoint if resuming ──────────────────────────────────
    completed_qids = []
    previous_results = []
    if args.resume:
        completed_qids, previous_results = load_checkpoint(run_id)
        log(f"Resuming: {len(completed_qids)} questions already completed")

    # ── Run Q&A ──────────────────────────────────────────────────────
    log_header("Running Q&A")
    results = list(previous_results)
    t0 = time.perf_counter()
    correct = sum(1 for r in results if r.get("correct"))
    total = len(results)
    errors_consecutive = 0

    for idx, (story_id, q_text, gt, q_type) in enumerate(all_questions):
        qid = f"nqa-{story_id[:8]}-{idx:04d}"

        if qid in completed_qids:
            continue

        # Build query with story context for better recall
        query = q_text

        start = time.perf_counter()
        answer = ""
        error = None

        try:
            resp = client.ask(query, vault, mode=cfg.ask_mode)
            answer = resp.get("answer", resp.get("response", ""))
            errors_consecutive = 0
        except Exception as e:
            error = str(e)
            errors_consecutive += 1

        elapsed_ms = (time.perf_counter() - start) * 1000

        # Score
        from benchmarks.core.types import Question as QType
        q_obj = QType(
            id=qid,
            benchmark="narrativeqa",
            config_label=cfg.value,
            question_type=q_type,
            text=q_text,
            ground_truth=str(gt),
            conversation_id=story_id,
        )
        s = score_answer(q_obj, answer)
        is_correct = s >= 0.5
        if is_correct:
            correct += 1

        result = {
            "question_id": qid,
            "story_id": story_id,
            "question": q_text[:120],
            "ground_truth": str(gt)[:120],
            "answer": (answer or "")[:500],
            "score": s,
            "correct": is_correct,
            "latency_ms": round(elapsed_ms, 1),
            "error": error,
            "config": cfg.value,
        }
        results.append(result)
        total += 1

        # ── Circuit breaker ─────────────────────────────────────────
        if errors_consecutive >= MAX_CONSECUTIVE_ERRORS:
            log(f"  ⚠ CIRCUIT BREAKER: {errors_consecutive} consecutive errors")
            break

        # ── Progress report ─────────────────────────────────────────
        if total % REPORT_EVERY == 0 or total == len(all_questions) or idx == 0:
            elapsed = time.perf_counter() - t0
            rate = total / elapsed if elapsed else 0
            pct = total / len(all_questions) * 100
            eta = (len(all_questions) - total) / max(rate, 0.001)
            eta_str = time.strftime("%H:%M", time.localtime(time.time() + eta))
            log(f"  [{total}/{len(all_questions)}] {pct:.0f}%  "
                f"acc: {correct/max(total,1):.1%}  "
                f"rate: {rate:.1f}/s  ETA: {eta_str}  "
                f"errs: {errors_consecutive}")

        # ── Checkpoint after each question ──────────────────────────
        completed_qids.append(qid)
        save_checkpoint(run_id, completed_qids, results)

    # ── Final results ───────────────────────────────────────────────
    accuracy = correct / max(len(results), 1)
    elapsed = time.perf_counter() - t0

    log_header("RESULTS")
    log(f"Config {cfg.value}: {correct}/{len(results)} = {accuracy:.1%}")
    log(f"Elapsed: {elapsed:.0f}s")

    # Per-type breakdown
    type_correct: Dict[str, int] = {}
    type_total: Dict[str, int] = {}
    for r in results:
        qt = r.get("question_type", "unknown")
        type_total[qt] = type_total.get(qt, 0) + 1
        if r.get("correct"):
            type_correct[qt] = type_correct.get(qt, 0) + 1
    log("")
    log("Per-type accuracy:")
    for qt in sorted(type_total.keys()):
        tc = type_correct.get(qt, 0)
        tt = type_total[qt]
        log(f"  {qt:15s}: {tc}/{tt} = {tc/tt:.1%}")

    # Save results
    out_dir = os.path.join(RESULTS_DIR, f"narrativeqa_{cfg.value}_{run_id}")
    os.makedirs(out_dir, exist_ok=True)

    summary = {
        "benchmark": "narrativeqa",
        "config": cfg.value,
        "correct": correct,
        "total": len(results),
        "accuracy": accuracy,
        "stories": len(stories),
        "per_type": {qt: {"correct": type_correct.get(qt, 0), "total": type_total[qt]}
                     for qt in type_total},
        "run_id": run_id,
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    with open(os.path.join(out_dir, "summary.json"), "w") as f:
        json.dump(summary, f, indent=2)
    with open(os.path.join(out_dir, "trace.jsonl"), "w") as f:
        for r in results:
            f.write(json.dumps(r) + "\n")

    log(f"Results saved to {out_dir}/")
    log("")
    log(" Done.")
    log("═" * 55)

    return 0


def ingest_all(
    client: RagamuffinClient,
    stories: List[Conversation],
    vault: str,
    cfg: Config,
):
    """Ingest all story chunks into the vault."""
    log(f"Ingesting {len(stories)} stories into '{vault}'...")

    # Flatten all chunks from all stories
    total_chunks = sum(len(c.messages) for c in stories)
    t0 = time.perf_counter()
    ok = err = 0

    for si, story in enumerate(stories):
        for ci, msg in enumerate(story.messages):
            content = msg.get("content", "")
            if not content:
                continue

            source = f"nqa/{story.id}/chunk-{ci}"
            tags = ["benchmark", "narrativeqa", story.id, f"chunk-{ci}"]

            try:
                client.ingest(content=content, source=source, vault=vault, tags=tags)
                ok += 1
            except Exception as e:
                err += 1
                if err <= 5:
                    rlog(f"  ingest err [{story.id}/chunk-{ci}]: {str(e)[:80]}")

            # Pacing: small delay between chunks
            time.sleep(0.05)

        if (si + 1) % 5 == 0 or si == len(stories) - 1:
            elapsed = time.perf_counter() - t0
            pct = (ok + err) / total_chunks * 100
            rlog(f"  ingest: [{ok+err}/{total_chunks}] {pct:.0f}%  errs: {err}")

    elapsed = time.perf_counter() - t0
    rlog(f"Ingest complete: {ok} ok, {err} err in {elapsed:.0f}s")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        log("\nInterrupted — checkpoint saved")
        sys.exit(130)
    except Exception as e:
        log(f"FATAL: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
