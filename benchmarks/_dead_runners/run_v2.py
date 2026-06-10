#!/usr/bin/env python3
"""Two-pass benchmark runner for v1.0 accuracy comparison to #691.

Pass 1: Ingest ALL conversations into a shared vault.
Pass 2: Ask ALL questions against the populated vault.

Logs to stderr (reliable with nohup > file 2>&1).
"""

from __future__ import annotations

import json
import logging
import os
import sys
import time
from typing import Dict, List

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from benchmarks.configs import Config
from benchmarks.core.client import RagamuffinClient
from benchmarks.core.scoring import score_batch, score_answer
from benchmarks.core.trace import TraceWriter, trace_path
from benchmarks.core.types import Question, Result
from benchmarks.loaders.longmemeval import LongMemEvalLoader
from benchmarks.loaders.locomo import LoCoMoLoader

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("v1.0-bench")

BASE_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
DATA_ROOT = "benchmarks/data"

HISTORICAL_BASELINE = {"longmemeval": {"D": 0.533}, "locomo": {"D": 0.467}}
ALL_CONFIGS = [Config.A, Config.B, Config.C, Config.D]


def ingest_all(client, conversations, vault, label):
    """Ingest all conversations (combined messages) into vault."""
    logger.info("Ingesting %d conversations into %s...", len(conversations), vault)
    ok = 0
    err = 0
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

        try:
            client.ingest(
                content="\n\n".join(parts),
                source=conv.source or conv.id,
                vault=vault,
                tags=["benchmark", label, conv.id],
            )
            ok += 1
        except Exception as e:
            err += 1
            if err <= 3:
                logger.warning("ingest error [%s]: %s", conv.id, e)

        if (i + 1) % 2000 == 0:
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed
            eta = (len(conversations) - i - 1) / max(rate, 0.001)
            logger.info("  ingested %d/%d (%.0f/s, eta %.0fs, %d errs)",
                        i + 1, len(conversations), rate, eta, err)

    elapsed = time.perf_counter() - t0
    logger.info("Ingested %d docs in %.0fs (%.0f/s, %d errs)",
                ok, elapsed, ok / elapsed if elapsed else 0, err)


def ask_questions(client, questions, vault, mode):
    """Ask all questions against the vault."""
    logger.info("Asking %d questions (mode=%s)...", len(questions), mode)
    results = []
    correct = 0
    t0 = time.perf_counter()

    for i, q in enumerate(questions):
        start = time.perf_counter()
        try:
            resp = client.ask(q.text, vault, mode=mode)
            answer = resp.get("answer", resp.get("response", ""))
            elapsed_ms = (time.perf_counter() - start) * 1000
        except Exception as e:
            answer = ""
            elapsed_ms = (time.perf_counter() - start) * 1000
            logger.debug("ask error [%s]: %s", q.id, e)

        score = score_answer(q, answer)
        is_correct = score >= 0.5
        if is_correct:
            correct += 1

        results.append(Result(
            question=q, answer=answer[:500] if answer else "",
            correct=is_correct, latency_ms=elapsed_ms,
            retries=0, score=score,
        ))

        if (i + 1) % 100 == 0 or (i + 1) == len(questions):
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed if elapsed > 0 else 0
            logger.info("  [%d/%d] acc=%.1f%% (%.1f/s)", i + 1, len(questions),
                        correct / (i + 1) * 100, rate)

    return results


def save_results(benchmark, cfg, results):
    run_id = f"{benchmark}_config_{cfg.value}_{int(time.time())}"
    out_dir = os.path.join("benchmarks/results", run_id)
    os.makedirs(out_dir, exist_ok=True)

    scoring = score_batch(results)
    with open(os.path.join(out_dir, "accuracy.json"), "w") as f:
        json.dump(scoring, f, indent=2)

    with open(os.path.join(out_dir, "trace.jsonl"), "w") as f:
        for r in results:
            f.write(json.dumps(r.to_trace()) + "\n")

    logger.info("Results: %s/benchmarks/results/%s", os.getcwd(), run_id)
    return scoring


def build_report(all_scores):
    """Build standard markdown report for issue #691."""
    lines = [
        "## v1.0 Benchmark Results",
        "",
        f"**Run**: {time.strftime('%Y-%m-%d %H:%M UTC')}",
        f"**Server**: `{BASE_URL}`",
        "",
        "### LongMemEval",
        "",
        "| Config | Accuracy | Correct/Total | vs v0.9.0-rc.1 Baseline |",
        "|--------|----------|--------------|------------------------|",
    ]
    for cfg_label in ["A", "B", "C", "D"]:
        s = all_scores.get("longmemeval", {}).get(cfg_label, {}).get("scoring", {})
        hist = HISTORICAL_BASELINE.get("longmemeval", {}).get(cfg_label)
        if cfg_label == "D" and hist:
            d = s.get("accuracy", 0) - hist
            ds = f"{'+' if d >= 0 else ''}{d:.1%}"
        else:
            ds = "—"
        lines.append(f"| {cfg_label} | {s.get('accuracy', 0):.1%} | {s.get('correct', 0)}/{s.get('total', 0)} | {ds} |")

    # LME by_type from Config D
    s = all_scores.get("longmemeval", {}).get("D", {}).get("scoring", {})
    bt = s.get("by_type", {})
    if bt:
        lines.append("")
        lines.append("| Question Type | Accuracy |")
        lines.append("|--------------|----------|")
        for qt, st in sorted(bt.items()):
            lines.append(f"| {qt} | {st['accuracy']:.1%} ({st['correct']}/{st['total']}) |")

    lines.append("")
    lines.append("### LoCoMo")
    lines.append("")
    lines.append("| Config | Accuracy | Correct/Total | vs v0.9.0-rc.1 Baseline |")
    lines.append("|--------|----------|--------------|------------------------|")
    for cfg_label in ["A", "B", "C", "D"]:
        s = all_scores.get("locomo", {}).get(cfg_label, {}).get("scoring", {})
        hist = HISTORICAL_BASELINE.get("locomo", {}).get(cfg_label)
        if cfg_label == "D" and hist:
            d = s.get("accuracy", 0) - hist
            ds = f"{'+' if d >= 0 else ''}{d:.1%}"
        else:
            ds = "—"
        lines.append(f"| {cfg_label} | {s.get('accuracy', 0):.1%} | {s.get('correct', 0)}/{s.get('total', 0)} | {ds} |")

    s = all_scores.get("locomo", {}).get("D", {}).get("scoring", {})
    bt = s.get("by_type", {})
    if bt:
        lines.append("")
        lines.append("| Question Type | Accuracy |")
        lines.append("|--------------|----------|")
        for qt, st in sorted(bt.items()):
            lines.append(f"| {qt} | {st['accuracy']:.1%} ({st['correct']}/{st['total']}) |")

    lines.append("")
    lines.append("### Notable Changes")
    lines.append("")
    lines.append("*Comparative analysis to be added after review.*")
    return "\n".join(lines)


def post_comment(all_scores):
    """Post results to GitHub issue #691."""
    from benchmarks.core.github import post_issue_comment

    report = build_report(all_scores)
    rpath = "benchmarks/results/issue_691_report.md"
    with open(rpath, "w") as f:
        f.write(report)
    logger.info("Report saved to %s", rpath)

    try:
        post_issue_comment(repo="chezgoulet/ragamuffin", issue=691, body=report)
        logger.info("Posted to issue #691")
    except Exception as e:
        logger.warning("GitHub post failed: %s", e)
        for line in report.split("\n"):
            logger.info(line)


def main():
    logger.info("=" * 60)
    logger.info("v1.0 Benchmark: Two-Pass Runner")
    logger.info("=" * 60)

    client = RagamuffinClient(base_url=BASE_URL)
    if not client.health():
        logger.error("Server unreachable at %s", BASE_URL)
        return 1

    os.makedirs("benchmarks/results", exist_ok=True)
    all_scores: Dict = {}

    # ═══════════════ LONGMEMEVAL ═══════════════════════════════════════
    logger.info("\n### LongMemEval ###")

    loader = LongMemEvalLoader(dataset_path=os.path.join(DATA_ROOT, "LongMemEval"), config_label="D")
    convs = loader.load()
    if not convs:
        logger.error("No LongMemEval convs loaded")
        return 1
    qs = loader.questions(convs[0]) if convs else []
    logger.info("Loaded %d convs, %d questions", len(convs), len(qs))

    vault = "lme-v2"
    ingest_all(client, convs, vault, "longmemeval")

    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    all_scores["longmemeval"] = {}

    for cfg in ALL_CONFIGS:
        logger.info("\n--- LongMemEval Config %s (mode=%s) ---", cfg.value, cfg.ask_mode)
        questions = [
            Question(id=f"lme-{i}", benchmark="longmemeval", config_label=cfg.value,
                     question_type=qt, text=t, ground_truth=gt, conversation_id=vault)
            for i, (t, gt, qt) in enumerate(qdata)
        ]
        results = ask_questions(client, questions, vault, cfg.ask_mode)
        scoring = save_results("longmemeval", cfg, results)
        all_scores["longmemeval"][cfg.value] = {"scoring": scoring, "results": results}
        logger.info("  Config %s: %d/%d = %.1f%%", cfg.value, scoring["correct"], scoring["total"], scoring["accuracy"] * 100)
        time.sleep(3)

    # ═══════════════ LOCOMO ════════════════════════════════════════════
    logger.info("\n### LoCoMo ###")

    loco_loader = LoCoMoLoader(dataset_path=os.path.join(DATA_ROOT, "Backboard-Locomo-Benchmark"), config_label="D")
    convs = loco_loader.load()
    if not convs:
        logger.error("No LoCoMo convs loaded")
        return 1
    qs = loco_loader.questions(convs[0]) if convs else []
    logger.info("Loaded %d convs, %d questions", len(convs), len(qs))

    vault = "locomo-v2"
    ingest_all(client, convs, vault, "locomo")

    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    all_scores["locomo"] = {}

    for cfg in ALL_CONFIGS:
        logger.info("\n--- LoCoMo Config %s (mode=%s) ---", cfg.value, cfg.ask_mode)
        questions = [
            Question(id=f"locomo-{i}", benchmark="locomo", config_label=cfg.value,
                     question_type=qt, text=t, ground_truth=gt, conversation_id=vault)
            for i, (t, gt, qt) in enumerate(qdata)
        ]
        results = ask_questions(client, questions, vault, cfg.ask_mode)
        scoring = save_results("locomo", cfg, results)
        all_scores["locomo"][cfg.value] = {"scoring": scoring, "results": results}
        logger.info("  Config %s: %d/%d = %.1f%%", cfg.value, scoring["correct"], scoring["total"], scoring["accuracy"] * 100)
        time.sleep(3)

    # ═══════════════ REPORT ════════════════════════════════════════════
    logger.info("\n" + "=" * 60)
    logger.info("ALL BENCHMARKS COMPLETE")
    logger.info("=" * 60)
    post_comment(all_scores)
    return 0


if __name__ == "__main__":
    sys.exit(main())
