#!/usr/bin/env python3
"""
Q&A-only runner for LongMemEval (vault already populated).
Writes status to /tmp/qa_v4_status.txt with fsync for reliable monitoring.
Saves results after each config.
"""

import json, os, sys, time, traceback
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from benchmarks.configs import Config
from benchmarks.core.types import Question, Result
from benchmarks.core.client import RagamuffinClient
from benchmarks.core.scoring import score_answer

BASE_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
RESULTS_DIR = "benchmarks/results"
ALL_CFG = [Config.A, Config.B, Config.C, Config.D]
STATUS_FILE = "/tmp/qa_v4_status.txt"

def status(msg):
    """Write to status file with fsync - reliable for external monitoring."""
    ts = time.strftime("%H:%M:%S")
    line = f"[{ts}] {msg}\n"
    with open(STATUS_FILE, "a") as f:
        f.write(line)
        f.flush()
        os.fsync(f.fileno())
    # Also print to stdout (may or may not appear, but try)
    print(line, end="", flush=True)

def load_questions():
    """Load questions from LongMemEval S dataset without re-ingesting."""
    status("Loading LongMemEval questions...")
    t0 = time.perf_counter()
    from benchmarks.loaders.longmemeval import LongMemEvalLoader
    loader = LongMemEvalLoader(dataset_path="benchmarks/data/LongMemEval", config_label="D")
    convs = loader.load()
    # Get all unique questions from first conversation
    first = convs[0] if convs else None
    if not first:
        raise RuntimeError("No conversations loaded")
    qs = loader.questions(first)
    qdata = [(q.text, q.ground_truth, q.question_type) for q in qs]
    status(f"Loaded {len(convs)} convs, {len(qdata)} unique Qs ({time.perf_counter()-t0:.1f}s)")
    return qdata, convs

def run_config(client, cfg, vault, qdata, label):
    """Run one config against one dataset."""
    cfg_name = f"{label}_config_{cfg.value}"
    status(f"\n{'='*50}")
    status(f"Starting {cfg_name} (mode={cfg.ask_mode})")
    status(f"{'='*50}")

    correct = total = 0
    results = []
    t0 = time.perf_counter()

    for i, (text, gt, qt) in enumerate(qdata):
        qid = f"{label}-{i:04d}"
        start = time.perf_counter()
        answer = ""
        retries = 0
        error = None

        try:
            resp = client.ask(text, vault, mode=cfg.ask_mode)
            answer = resp.get("answer", resp.get("response", ""))
        except Exception as e:
            error = str(e)

        elapsed_ms = (time.perf_counter() - start) * 1000

        q_obj = Question(id=qid, benchmark="longmemeval", config_label=cfg.value, question_type=qt, text=text, ground_truth=str(gt), conversation_id=vault)
        s = score_answer(q_obj, answer)
        is_correct = s >= 0.5
        if is_correct:
            correct += 1
        total += 1

        results.append({
            "question_id": qid,
            "question": text[:100],
            "ground_truth": str(gt)[:100],
            "answer": answer[:500] if answer else "",
            "score": s,
            "correct": is_correct,
            "latency_ms": round(elapsed_ms, 1),
            "error": error,
        })

        # Progress every 25 Qs
        if (i + 1) % 25 == 0 or (i + 1) == len(qdata):
            elapsed = time.perf_counter() - t0
            rate = (i + 1) / elapsed if elapsed else 0
            pct = (i + 1) / len(qdata) * 100
            status(f"  [{i+1}/{len(qdata)}] {pct:.0f}% acc={correct/total:.1%} ({rate:.1f}/s)")

    # Save results
    accuracy = correct / total if total > 0 else 0
    run_id = f"{cfg_name}_{int(time.time())}"
    out_dir = os.path.join(RESULTS_DIR, run_id)
    os.makedirs(out_dir, exist_ok=True)

    with open(os.path.join(out_dir, "accuracy.json"), "w") as f:
        json.dump({
            "benchmark": label,
            "config": cfg.value,
            "mode": cfg.ask_mode,
            "correct": correct,
            "total": total,
            "accuracy": accuracy,
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }, f, indent=2)

    with open(os.path.join(out_dir, "trace.jsonl"), "w") as f:
        for r in results:
            f.write(json.dumps(r) + "\n")

    elapsed = time.perf_counter() - t0
    status(f"DONE {cfg_name}: {correct}/{total} = {accuracy:.1%} ({elapsed:.0f}s)")
    return accuracy, correct, total

def main():
    status("=" * 60)
    status("Q&A Runner v4 — LongMemEval (vault lme-v4)")
    status(f"Start: {time.strftime('%Y-%m-%d %H:%M:%S')}")

    client = RagamuffinClient(base_url=BASE_URL)
    if not client.health():
        status("FATAL: Server unreachable")
        return 1
    status(f"Server OK at {BASE_URL}")

    os.makedirs(RESULTS_DIR, exist_ok=True)

    vault = "lme-v4"
    qdata, _ = load_questions()
    status(f"Vault {vault} ready ({len(qdata)} questions)")

    scores = {}
    for cfg in ALL_CFG:
        try:
            acc, correct, total = run_config(client, cfg, vault, qdata, "longmemeval")
            scores[cfg.value] = {"accuracy": acc, "correct": correct, "total": total}
        except Exception as e:
            status(f"CRASHED config {cfg.value}: {e}")
            traceback.print_exc()
            scores[cfg.value] = {"accuracy": 0, "correct": 0, "total": 0}

    # Summary
    baseline_d = 0.533
    status(f"\n{'='*60}")
    status("LONGMEMEVAL SUMMARY")
    for cv in ["A", "B", "C", "D"]:
        s = scores.get(cv, {})
        ds = f" (baseline={baseline_d})" if cv == "D" else ""
        status(f"  Config {cv}: {s.get('accuracy', 0):.1%} ({s.get('correct', 0)}/{s.get('total', 0)}){ds}")

    # Save summary
    with open(os.path.join(RESULTS_DIR, "longmemeval_v4_summary.json"), "w") as f:
        json.dump(scores, f, indent=2)

    status("=== DONE ===")
    return 0

if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:
        status(f"FATAL: {e}")
        traceback.print_exc()
        sys.exit(1)
