#!/usr/bin/env python3
"""Benchmark gauntlet runner — v1.0 accuracy sprint.

Direct file I/O for reliable logging in background mode.
Run: RAGAMUFFIN_URL=http://ragamuffin:8000 python3 -u benchmarks/gauntlet.py
"""

from __future__ import annotations

import json
import os
import sys
import time
from typing import List, Dict

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from benchmarks.configs import Config
from benchmarks.core.client import RagamuffinClient
from benchmarks.core.scoring import score_batch, score_answer
from benchmarks.core.trace import TraceWriter, trace_path
from benchmarks.core.types import Conversation, Question, Result
from benchmarks.loaders.longmemeval import LongMemEvalLoader
from benchmarks.loaders.locomo import LoCoMoLoader

BASE_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
LONGMEMEVAL_DATA = "benchmarks/data/LongMemEval"
LOCOMO_DATA = "benchmarks/data/Backboard-Locomo-Benchmark"
RESULTS_DIR = "benchmarks/results"
LOG_FILE = "/tmp/gauntlet.log"

HISTORICAL_BASELINE = {
    "longmemeval": {"D": {"accuracy": 0.533}},
    "locomo": {"D": {"accuracy": 0.467}},
}


def log(msg: str):
    """Write timestamped line to log file, immediately flushed and fsynced."""
    ts = time.strftime("%H:%M:%S")
    line = f"{ts} {msg}\n"
    with open(LOG_FILE, "a") as f:
        f.write(line)
        f.flush()
        os.fsync(f.fileno())


def bulk_ingest(client: RagamuffinClient, conversations: List[Conversation], vault: str, label: str):
    log(f"Bulk-ingesting {len(conversations)} conversations into {vault}...")
    ingested = 0
    errors = 0
    t0 = time.perf_counter()

    for i, conv in enumerate(conversations):
        parts = []
        for msg in conv.messages:
            content = msg.get("content", "")
            if not content:
                continue
            speaker = msg.get("speaker", msg.get("role", "User").capitalize())
            parts.append(f"[{speaker}]: {content}")
        if not parts:
            continue

        content = "\n\n".join(parts)
        try:
            client.ingest(
                content=content,
                source=conv.source or conv.id,
                vault=vault,
                tags=["benchmark", label, conv.id],
            )
            ingested += 1
        except Exception as e:
            errors += 1
            if errors <= 3:
                log(f"  ingest error for {conv.id}: {e}")

        if (i + 1) % 500 == 0:
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed
            eta = (len(conversations) - i - 1) / rate
            log(f"  ingested {i+1}/{len(conversations)} ({rate:.1f}/s, eta {eta:.0f}s, {errors} errs)")

    elapsed = time.perf_counter() - t0
    rate = ingested / elapsed if elapsed else 0
    log(f"Ingested {ingested} docs into {vault} in {elapsed:.0f}s ({rate:.1f}/s, {errors} errors)")


def run_questions(client: RagamuffinClient, questions: List[Question], vault: str, mode: str) -> List[Result]:
    log(f"Running {len(questions)} Qs (mode={mode}) against {vault}")
    results: List[Result] = []
    t0 = time.perf_counter()
    correct_total = 0

    for i, q in enumerate(questions):
        start = time.perf_counter()
        try:
            resp = client.ask(q.text, vault, mode=mode)
            answer = resp.get("answer", resp.get("response", ""))
            elapsed_ms = (time.perf_counter() - start) * 1000
            error = None
        except Exception as e:
            answer = ""
            elapsed_ms = (time.perf_counter() - start) * 1000
            error = str(e)

        score = score_answer(q, answer)
        correct = score >= 0.5

        results.append(Result(
            question=q, answer=answer[:500] if answer else "",
            correct=correct, latency_ms=elapsed_ms, retries=0,
            error=error, score=score,
        ))
        if correct:
            correct_total += 1

        if (i + 1) % 100 == 0 or (i + 1) == len(questions):
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed if elapsed > 0 else 0
            remaining = (len(questions) - i - 1) / rate if rate > 0 else 0
            # Sample first/last question info
            q_sample = questions[min(i, 4)]
            gt_short = q_sample.ground_truth[:60] if q_sample.ground_truth else "?"
            log(f"  [{i+1}/{len(questions)}] acc={correct_total/(i+1):.1%} ({rate:.1f}/s, eta={remaining:.0f}s) "
                f"eg: {q_sample.text[:60]}... -> {gt_short}")

    return results


def save_results(benchmark: str, config_label: str, results: List[Result], scoring: Dict):
    run_id = f"{benchmark}_config_{config_label}_{int(time.time())}"
    out_dir = os.path.join(RESULTS_DIR, run_id)
    os.makedirs(out_dir, exist_ok=True)

    with open(os.path.join(out_dir, "accuracy.json"), "w") as f:
        json.dump(scoring, f, indent=2)
    trace = [r.to_trace() for r in results]
    with open(os.path.join(out_dir, "trace.jsonl"), "w") as f:
        for t in trace:
            f.write(json.dumps(t) + "\n")
    log(f"Results saved to {out_dir}")


def build_report(all_results, commit_sha):
    parts = [
        f"## v1.0 Benchmark Results — {commit_sha}",
        "",
        f"**Run**: {time.strftime('%Y-%m-%d %H:%M UTC')}",
        f"**Server**: `{BASE_URL}`",
        f"**Commit**: `{commit_sha}`",
        "",
        "### LongMemEval",
        "",
        "| Config | Accuracy | Correct/Total | vs v0.9.0-rc.1 Baseline |",
        "|--------|----------|--------------|------------------------|",
    ]

    for cfg in ["A", "B", "C", "D"]:
        res = all_results.get("longmemeval", {}).get(cfg, [])
        s = score_batch(res) if res else {"accuracy": 0.0, "total": 0, "correct": 0}
        hist = HISTORICAL_BASELINE.get("longmemeval", {}).get(cfg, {}).get("accuracy")
        if cfg == "D" and hist is not None:
            d = s["accuracy"] - hist
            ds = f"{'+' if d >= 0 else ''}{d:.1%}"
        else:
            ds = "—"
        parts.append(f"| {cfg} | {s['accuracy']:.1%} | {s['correct']}/{s['total']} | {ds} |")

    last = all_results.get("longmemeval", {}).get("D", [])
    if last:
        s = score_batch(last)
        parts.append(""); parts.append("| Question Type | Accuracy |"); parts.append("|--------------|----------|")
        for qt, st in sorted(s.get("by_type", {}).items()):
            parts.append(f"| {qt} | {st['accuracy']:.1%} ({st['correct']}/{st['total']}) |")

    parts.append(""); parts.append("### LoCoMo")
    parts.append(""); parts.append("| Config | Accuracy | Correct/Total | vs v0.9.0-rc.1 Baseline |")
    parts.append("|--------|----------|--------------|------------------------|")

    for cfg in ["A", "B", "C", "D"]:
        res = all_results.get("locomo", {}).get(cfg, [])
        s = score_batch(res) if res else {"accuracy": 0.0, "total": 0, "correct": 0}
        hist = HISTORICAL_BASELINE.get("locomo", {}).get(cfg, {}).get("accuracy")
        if cfg == "D" and hist is not None:
            d = s["accuracy"] - hist
            ds = f"{'+' if d >= 0 else ''}{d:.1%}"
        else:
            ds = "—"
        parts.append(f"| {cfg} | {s['accuracy']:.1%} | {s['correct']}/{s['total']} | {ds} |")

    last = all_results.get("locomo", {}).get("D", [])
    if last:
        s = score_batch(last)
        parts.append(""); parts.append("| Question Type | Accuracy |"); parts.append("|--------------|----------|")
        for qt, st in sorted(s.get("by_type", {}).items()):
            parts.append(f"| {qt} | {st['accuracy']:.1%} ({st['correct']}/{st['total']}) |")

    parts.append(""); parts.append("### Notable Changes")
    parts.append(""); parts.append("*To be filled in after analysis.*")
    return "\n".join(parts)


def post_to_github(all_results):
    from benchmarks.core.github import post_issue_comment

    sha = os.popen("cd /home/node/.openclaw/workspace/ragamuffin && git log -1 --oneline 2>/dev/null").read().strip()
    if not sha:
        sha = "testing (3b06ef6)"

    report = build_report(all_results, sha)
    rpath = os.path.join(RESULTS_DIR, "issue_691_report.md")
    with open(rpath, "w") as f:
        f.write(report)
    log(f"Report written to {rpath}")

    try:
        post_issue_comment(repo="chezgoulet/ragamuffin", issue=691, body=report)
        log("Posted to issue #691")
    except Exception as e:
        log(f"GitHub post failed: {e}; report above")


def main():
    # Clear old log
    open(LOG_FILE, "w").close()

    log("=" * 60)
    log("Ragamuffin v1.0 Benchmark Gauntlet")
    log("=" * 60)

    client = RagamuffinClient(base_url=BASE_URL)
    if not client.health():
        log(f"FATAL: Ragamuffin unreachable at {BASE_URL}")
        return 1
    log("Health OK")

    os.makedirs(RESULTS_DIR, exist_ok=True)
    all_results: Dict = {}
    configs = [Config.A, Config.B, Config.C, Config.D]

    # ═══════════════ PHASE 1: LongMemEval ═══════════════════════════════
    log("\n### PHASE 1: LongMemEval ###")

    loader = LongMemEvalLoader(dataset_path=LONGMEMEVAL_DATA, config_label="D")
    convs = loader.load()
    if not convs:
        log("FATAL: No LongMemEval convs")
        return 1
    qs = loader.questions(convs[0]) if convs else []
    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    log(f"Loaded {len(convs)} conversations, {len(qdata)} questions")

    vault = "lme-v1b"
    bulk_ingest(client, convs, vault, "longmemeval")

    all_results["longmemeval"] = {}
    for cfg in configs:
        log(f"\n--- LongMemEval Config {cfg.value} (mode={cfg.ask_mode}) ---")
        questions = [
            Question(id=f"lme-{i}", benchmark="longmemeval", config_label=cfg.value,
                     question_type=qt, text=t, ground_truth=gt, conversation_id=vault)
            for i, (t, gt, qt) in enumerate(qdata)
        ]
        results = run_questions(client, questions, vault, cfg.ask_mode)
        all_results["longmemeval"][cfg.value] = results
        s = score_batch(results)
        log(f"  Config {cfg.value}: {s['correct']}/{s['total']} = {s['accuracy']:.1%}")
        save_results("longmemeval", cfg.value, results, s)
        time.sleep(2)

    # ═══════════════ PHASE 2: LoCoMo ════════════════════════════════════
    log("\n### PHASE 2: LoCoMo ###")

    loco_loader = LoCoMoLoader(dataset_path=LOCOMO_DATA, config_label="D")
    convs = loco_loader.load()
    if not convs:
        log("FATAL: No LoCoMo convs")
        return 1
    qs = loco_loader.questions(convs[0]) if convs else []
    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    log(f"Loaded {len(convs)} conversations, {len(qdata)} questions")

    vault = "locomo-v1b"
    bulk_ingest(client, convs, vault, "locomo")

    all_results["locomo"] = {}
    for cfg in configs:
        log(f"\n--- LoCoMo Config {cfg.value} (mode={cfg.ask_mode}) ---")
        questions = [
            Question(id=f"locomo-{i}", benchmark="locomo", config_label=cfg.value,
                     question_type=qt, text=t, ground_truth=gt, conversation_id=vault)
            for i, (t, gt, qt) in enumerate(qdata)
        ]
        results = run_questions(client, questions, vault, cfg.ask_mode)
        all_results["locomo"][cfg.value] = results
        s = score_batch(results)
        log(f"  Config {cfg.value}: {s['correct']}/{s['total']} = {s['accuracy']:.1%}")
        save_results("locomo", cfg.value, results, s)
        time.sleep(2)

    # ═══════════════ REPORT ═════════════════════════════════════════════
    log("\n" + "=" * 60)
    log("GAUNTLET COMPLETE")
    log("=" * 60)
    post_to_github(all_results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
