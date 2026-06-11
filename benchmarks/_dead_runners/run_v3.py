#!/usr/bin/env python3
"""
v1.0 Benchmark runner — v3.
Writes directly to fd 1 (already redirected to log file via shell).
"""

from __future__ import annotations

import json
import os
import sys
import time
from typing import Dict, List

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from benchmarks.configs import Config
from benchmarks.core.client import RagamuffinClient
from benchmarks.core.scoring import score_batch, score_answer
from benchmarks.core.types import Question, Result
from benchmarks.loaders.longmemeval import LongMemEvalLoader
from benchmarks.loaders.locomo import LoCoMoLoader

BASE_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
RESULTS_DIR = "benchmarks/results"
HISTORICAL = {"longmemeval": {"D": 0.533}, "locomo": {"D": 0.467}}
ALL_CFG = [Config.A, Config.B, Config.C, Config.D]
FD = 1  # Write to stdout (redirected to log file via shell)


def log(msg: str):
    """Write timestamped line directly to fd 1."""
    ts = time.strftime("%H:%M:%S")
    line = f"{ts} {msg}\n"
    os.write(FD, line.encode())


def ingest_batch(client, conversations, vault, label):
    log(f"Ingesting {len(conversations)} conversations into {vault}...")
    ok = err = 0
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
            if err <= 5:
                log(f"  ingest err [{conv.id}]: {e}")

        if (i + 1) % 4000 == 0:
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed if elapsed else 0
            remaining = (len(conversations) - i - 1) / max(rate, 0.001)
            log(f"  [{i+1}/{len(conversations)}] {rate:.0f}/s, eta={remaining:.0f}s, {err} errs, ok={ok}")

    elapsed = time.perf_counter() - t0
    log(f"Ingested {ok}/{len(conversations)} into {vault} in {elapsed:.0f}s, {err} errs")


def ask_batch(client, questions, vault, mode):
    log(f"Q&A: {len(questions)} Qs, mode={mode}, vault={vault}")
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
            if i < 5:
                log(f"  ask err [{q.id}]: {e}")

        s = score_answer(q, answer)
        correct_flag = s >= 0.5
        if correct_flag:
            correct += 1

        results.append(Result(
            question=q, answer=answer[:500] if answer else "",
            correct=correct_flag, latency_ms=elapsed_ms,
            retries=0, score=s,
        ))

        if (i + 1) % 100 == 0 or (i + 1) == len(questions):
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed if elapsed else 0
            remaining = (len(questions) - i - 1) / max(rate, 0.001)
            log(f"  [{i+1}/{len(questions)}] acc={correct/(i+1):.1%} ({rate:.1f}/s, eta={remaining:.0f}s)")

    return results


def save(benchmark, cfg, results):
    run_id = f"{benchmark}_config_{cfg.value}_{int(time.time())}"
    out_dir = os.path.join(RESULTS_DIR, run_id)
    os.makedirs(out_dir, exist_ok=True)

    scoring = score_batch(results)
    with open(os.path.join(out_dir, "accuracy.json"), "w") as f:
        json.dump(scoring, f, indent=2)
    with open(os.path.join(out_dir, "trace.jsonl"), "w") as f:
        for r in results:
            f.write(json.dumps(r.to_trace()) + "\n")

    log(f"SAVED: {benchmark} config {cfg.value} = {scoring['correct']}/{scoring['total']} ({scoring['accuracy']:.1%})")
    return scoring


def main():
    log("=" * 60)
    log("v1.0 Benchmark Runner (fd1 logging)")
    log("=" * 60)
    log(f"Start: {time.strftime('%Y-%m-%d %H:%M:%S')}")

    client = RagamuffinClient(base_url=BASE_URL)
    if not client.health():
        log("FATAL: Server unreachable")
        return 1
    log(f"Server OK at {BASE_URL}")

    os.makedirs(RESULTS_DIR, exist_ok=True)
    all_scores = {"longmemeval": {}, "locomo": {}}

    # ═══════════════ LONGMEMEVAL ═══════════════════════════════════════
    log("\n--- PHASE 1: LongMemEval ---")

    t0 = time.perf_counter()
    loader = LongMemEvalLoader(dataset_path="benchmarks/data/LongMemEval", config_label="D")
    convs = loader.load()
    qs = loader.questions(convs[0]) if convs else []
    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    log(f"Loaded {len(convs)} convs, {len(qdata)} Qs ({time.perf_counter()-t0:.1f}s)")

    vault = "lme-v4"
    ingest_batch(client, convs, vault, "longmemeval")

    for cfg in ALL_CFG:
        log(f"\n--- LME Config {cfg.value} (mode={cfg.ask_mode}) ---")
        questions = [
            Question(id=f"lme-{i}", benchmark="longmemeval", config_label=cfg.value,
                     question_type=qt, text=t, ground_truth=gt, conversation_id=vault)
            for i, (t, gt, qt) in enumerate(qdata)
        ]
        try:
            results = ask_batch(client, questions, vault, cfg.ask_mode)
            all_scores["longmemeval"][cfg.value] = save("longmemeval", cfg, results)
        except Exception as e:
            log(f"FAILED LME {cfg.value}: {e}")

    # ═══════════════ LOCOMO ════════════════════════════════════════════
    log("\n--- PHASE 2: LoCoMo ---")

    loader = LoCoMoLoader(dataset_path="benchmarks/data/Backboard-Locomo-Benchmark", config_label="D")
    convs = loader.load()
    qs = loader.questions(convs[0]) if convs else []
    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    log(f"Loaded {len(convs)} convs, {len(qdata)} Qs")

    vault = "locomo-v4"
    ingest_batch(client, convs, vault, "locomo")

    for cfg in ALL_CFG:
        log(f"\n--- LoCoMo Config {cfg.value} (mode={cfg.ask_mode}) ---")
        questions = [
            Question(id=f"locomo-{i}", benchmark="locomo", config_label=cfg.value,
                     question_type=qt, text=t, ground_truth=gt, conversation_id=vault)
            for i, (t, gt, qt) in enumerate(qdata)
        ]
        try:
            results = ask_batch(client, questions, vault, cfg.ask_mode)
            all_scores["locomo"][cfg.value] = save("locomo", cfg, results)
        except Exception as e:
            log(f"FAILED LoCoMo {cfg.value}: {e}")

    # ═══════════════ REPORT ════════════════════════════════════════════
    log("\n--- SUMMARY ---")
    for bench in ["longmemeval", "locomo"]:
        log(f"{bench}:")
        for cfg_label in ["A", "B", "C", "D"]:
            s = all_scores.get(bench, {}).get(cfg_label, {})
            hist = HISTORICAL.get(bench, {}).get(cfg_label)
            ds = f" (vs {s.get('accuracy', 0) - hist:.1%})" if cfg_label == "D" and hist else ""
            log(f"  Config {cfg_label}: {s.get('accuracy', 0):.1%} ({s.get('correct', 0)}/{s.get('total', 0)}){ds}")

    log("\n=== DONE ===")
    return 0


if __name__ == "__main__":
    sys.exit(main())
