#!/usr/bin/env python3
"""
Phase 1 Benchmark Suite Runner — 6 benchmarks for Ragamuffin v0.9.

Usage:
    PYTHONPATH=/tmp/pylibs:$PYTHONPATH python3 benchmarks/run_phase1.py \\
        [--base-url http://ragamuffin:8000] \\
        [--vault bench-phase1-XXXX] \\
        [--vault-b bench-phase1-b-XXXX] \\
        [--benchmark cross-session|fact-lifecycle|auto-extraction|vault-isolation|batch-recall|fact-contention|all] \\
        [--output results.json]
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import time
import uuid
from typing import Any, Dict, List

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from benchmarks.core.client import RagamuffinClient
from benchmarks.loaders.phase1_cross_session_recall import run_phase1_cross_session_recall
from benchmarks.loaders.phase1_fact_lifecycle import run_phase1_fact_lifecycle
from benchmarks.loaders.phase1_auto_extraction import run_phase1_auto_extraction
from benchmarks.loaders.phase1_vault_isolation import run_phase1_vault_isolation
from benchmarks.loaders.phase1_batch_recall import run_phase1_batch_recall
from benchmarks.loaders.phase1_fact_contention import run_phase1_fact_contention

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger("phase1")


# ── Helpers ─────────────────────────────────────────────────────────────────────


def create_vault(client: RagamuffinClient, vault: str) -> bool:
    """Create a vault by ingesting a simple marker file."""
    try:
        client.ingest(
            content="Phase 1 benchmark vault marker.",
            source=f"phase1-vault-init",
            vault=vault,
        )
        logger.info("Vault %s provisioned", vault)
        return True
    except Exception as e:
        logger.error("Failed to provision vault %s: %s", vault, e)
        return False


def summarize_results(results: Dict[str, Any]) -> Dict[str, Any]:
    """Compute pass rate and summary from raw results."""
    total = 0
    passed = 0
    failures = []

    for benchmark_name, tests in results.items():
        if not isinstance(tests, list):
            continue
        for test in tests:
            total += 1
            if test.get("pass", False):
                passed += 1
            else:
                failures.append(f"  [{benchmark_name}] {test.get('test', '?')}: {test.get('detail', '?')}")

    return {
        "total_tests": total,
        "passed": passed,
        "failed": total - passed,
        "pass_rate": round(passed / total * 100, 1) if total > 0 else 0.0,
        "failures": failures,
    }


# ── Main ───────────────────────────────────────────────────────────────────────


def main():
    parser = argparse.ArgumentParser(description="Phase 1 Benchmark Suite")
    parser.add_argument("--base-url", default=os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000"))
    parser.add_argument("--vault", default=f"bench-phase1-{uuid.uuid4().hex[:6]}")
    parser.add_argument("--vault-b", default=f"bench-phase1-b-{uuid.uuid4().hex[:6]}")
    parser.add_argument("--benchmark", choices=[
        "cross-session", "fact-lifecycle", "auto-extraction",
        "vault-isolation", "batch-recall", "fact-contention", "all"
    ], default="all")
    parser.add_argument("--output", default="")
    parser.add_argument("--no-provision", action="store_true", help="Skip vault provisioning (use existing)")
    args = parser.parse_args()

    client = RagamuffinClient(base_url=args.base_url)

    # ── Health check ────────────────────────────────────────────────────────
    if not client.health():
        logger.error("Ragamuffin not reachable at %s", args.base_url)
        sys.exit(1)

    vault = args.vault
    vault_b = args.vault_b

    # ── Provision vaults ────────────────────────────────────────────────────
    if not args.no_provision:
        create_vault(client, vault)
        create_vault(client, vault_b)
        time.sleep(2)  # Let indexing complete

    # ── Run benchmarks ──────────────────────────────────────────────────────
    results: Dict[str, Any] = {}
    benchmarks_to_run = ["cross-session", "fact-lifecycle", "auto-extraction",
                         "vault-isolation", "batch-recall", "fact-contention"] if args.benchmark == "all" else [args.benchmark]

    for bm in benchmarks_to_run:
        logger.info("=== Running benchmark: %s ===", bm)
        t0 = time.time()
        try:
            if bm == "cross-session":
                res = run_phase1_cross_session_recall(client, vault)
            elif bm == "fact-lifecycle":
                res = run_phase1_fact_lifecycle(client, vault)
            elif bm == "auto-extraction":
                res = run_phase1_auto_extraction(client, vault)
            elif bm == "vault-isolation":
                res = run_phase1_vault_isolation(client, vault, vault_b)
            elif bm == "batch-recall":
                res = run_phase1_batch_recall(client, vault)
            elif bm == "fact-contention":
                res = run_phase1_fact_contention(client, vault)
            else:
                logger.warning("Unknown benchmark: %s", bm)
                continue

            elapsed = time.time() - t0
            passed = sum(1 for r in res if r.get("pass", False))
            total = len(res)
            logger.info("Benchmark %s: %d/%d passed (%.1fs)", bm, passed, total, elapsed)
            results[bm] = res

        except Exception as e:
            logger.error("Benchmark %s crashed: %s", bm, e)
            results[bm] = [{"test": "runner", "pass": False, "detail": f"crash: {e}"}]

    # ── Summary ─────────────────────────────────────────────────────────────
    summary = summarize_results(results)
    logger.info("=" * 60)
    logger.info("Phase 1 Summary: %d/%d passed (%.1f%%)",
                summary["passed"], summary["total_tests"], summary["pass_rate"])
    if summary["failures"]:
        logger.warning("Failures:")
        for f in summary["failures"]:
            logger.warning(f)

    # ── Output ──────────────────────────────────────────────────────────────
    output = {
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "base_url": args.base_url,
        "run_id": f"phase1_{time.strftime('%Y%m%dT%H%M%S', time.gmtime())}",
        "results": {k: v for k, v in results.items()},
        "summary": summary,
    }

    out_path = args.output or f"benchmarks/results/v0.10.0-rc.1/phase1_{time.strftime('%Y%m%dT%H%M%S', time.gmtime())}.json"
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w") as f:
        json.dump(output, f, indent=2, default=str)
    logger.info("Results written to %s", out_path)

    # Print concise summary to stdout for CI
    print(f"\nPHASE1_SUMMARY={json.dumps(summary)}")

    sys.exit(0 if summary["failed"] == 0 else 1)


if __name__ == "__main__":
    main()
