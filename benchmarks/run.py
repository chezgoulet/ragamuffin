#!/usr/bin/env python3
"""
Ragamuffin benchmark harness — CLI entry point.

Runs accuracy benchmarks, stress tests, or classifies failures from traces.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import time
from typing import Dict, List, Optional

# Ensure benchmarks package is importable
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from benchmarks.configs import Config  # noqa: E402
from benchmarks.core.checkpoint import CheckpointManager  # noqa: E402
from benchmarks.core.client import RagamuffinClient  # noqa: E402
from benchmarks.core.scoring import compare_to_baseline, score_batch, score_answer  # noqa: E402
from benchmarks.core.trace import TraceWriter, load_trace, trace_path  # noqa: E402
from benchmarks.core.types import Conversation, IngestPlan, Result  # noqa: E402
from benchmarks.loaders.longmemeval import LongMemEvalLoader  # noqa: E402
from benchmarks.loaders.stress_concurrent import ConcurrentStressTest  # noqa: E402
from benchmarks.loaders.stress_large_vault import LargeVaultStressTest  # noqa: E402
from benchmarks.loaders.stress_malformed import MalformedInputStressTest  # noqa: E402

logger = logging.getLogger("ragamuffin.benchmark")


# ── CLI ─────────────────────────────────────────────────────────────────────────


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Ragamuffin benchmark harness",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python3 benchmarks/run.py --benchmark longmemeval --config d --max-convs 5
  python3 benchmarks/run.py --benchmark longmemeval --config d --resume
  python3 benchmarks/run.py --stress concurrent --concurrency 10 --requests 100
  python3 benchmarks/run.py --benchmark longmemeval --config d --stress concurrent
  python3 benchmarks/run.py --classify trace_D_20260609.jsonl --dry-run
        """,
    )

    # Target
    parser.add_argument(
        "--base-url",
        default=os.environ.get("RAGAMUFFIN_URL", "http://localhost:8000"),
        help="Ragamuffin server URL (default: $RAGAMUFFIN_URL or http://localhost:8000)",
    )
    parser.add_argument(
        "--api-key",
        default=os.environ.get("RAGAMUFFIN_API_KEY", ""),
        help="API key for authenticated endpoints",
    )

    # Benchmark mode
    parser.add_argument("--benchmark", choices=["longmemeval", "locomo"], help="Benchmark dataset to run")
    parser.add_argument("--dataset", default="datasets/longmemeval", help="Path to benchmark dataset")
    parser.add_argument(
        "--config",
        default="D",
        help="Benchmark config label: A (baseline), B (recall+facts), C (tiered), D (full stack, default)",
    )
    parser.add_argument("--max-convs", type=int, default=0, help="Max conversations to process (0=all)")
    parser.add_argument("--resume", action="store_true", help="Resume from last checkpoint")
    parser.add_argument(
        "--checkpoint-interval",
        type=int,
        default=50,
        help="Save checkpoint every N questions (default: 50)",
    )

    # Stress mode
    parser.add_argument("--stress", choices=["concurrent", "large-vault", "malformed"], help="Stress test to run")
    parser.add_argument("--concurrency", type=int, default=10, help="Concurrent requests (stress: concurrent)")
    parser.add_argument("--requests", type=int, default=100, help="Total requests (stress: concurrent)")
    parser.add_argument("--target-sessions", type=int, default=50, help="Target session count (stress: large-vault)")

    # Classify mode
    parser.add_argument("--classify", metavar="TRACE_FILE", help="Classify failures from a trace file")
    parser.add_argument("--dry-run", action="store_true", help="Show what would be filed without filing")

    # Output
    parser.add_argument("--output", help="Output directory for results (default: .benchmark_results)")
    parser.add_argument("--baseline", default="benchmarks/baseline.json", help="Baseline JSON for regression detection")
    parser.add_argument("--no-baseline", action="store_true", help="Skip baseline comparison")
    parser.add_argument("--verbose", "-v", action="store_true", help="Enable debug logging")

    return parser


# ── Benchmark runner ────────────────────────────────────────────────────────────


def run_benchmark(args: argparse.Namespace) -> int:
    """Run an accuracy benchmark."""
    config = Config.parse(args.config)
    if config is None:
        logger.error("invalid config: %s (use A, B, C, or D)", args.config)
        return 1

    client = RagamuffinClient(
        base_url=args.base_url,
        api_key=args.api_key,
    )

    if not client.health():
        logger.error("Ragamuffin is not reachable at %s", args.base_url)
        return 1

    # Load dataset
    if args.benchmark == "longmemeval":
        loader = LongMemEvalLoader(
            dataset_path=args.dataset,
            config_label=config.value,
        )
    elif args.benchmark == "locomo":
        logger.error("LoCoMo loader is a stub — not yet implemented")
        return 1
    else:
        logger.error("unknown benchmark: %s", args.benchmark)
        return 1

    conversations = loader.load()
    if not conversations:
        logger.error("no conversations loaded from %s", args.dataset)
        return 1

    if args.max_convs > 0:
        conversations = conversations[:args.max_convs]

    logger.info(
        "loaded %d conversations for config %s",
        len(conversations),
        config.value,
    )

    # Setup checkpoint
    run_id = f"{args.benchmark}_config_{config.value}_{int(time.time())}"
    checkpoint = CheckpointManager(run_id, interval=args.checkpoint_interval)
    checkpoint.init()

    # Load existing results if resuming
    all_results: List[Result] = []
    if args.resume:
        saved = checkpoint.load()
        if saved:
            all_results = saved
            logger.info("resumed with %d completed results", len(all_results))

    # Setup trace
    trace_writer = TraceWriter(trace_path(run_id))
    completed_ids = {r.question.id for r in all_results}

    # Process each conversation
    for conv_idx, conv in enumerate(conversations):
        plan = loader.ingest_strategy(conv, config.value)
        questions = loader.questions(conv)

        if not questions:
            logger.warning("no questions for conversation %s, skipping", conv.id)
            continue

        # Filter to unanswered questions
        pending = [q for q in questions if q.id not in completed_ids]
        if not pending:
            logger.info("conversation %s: all %d questions already answered", conv.id, len(questions))
            continue

        # Ingest conversation
        logger.info(
            "[%d/%d] ingesting conversation %s (%d messages, %d questions)",
            conv_idx + 1,
            len(conversations),
            conv.id,
            len(conv.messages),
            len(pending),
        )

        if not _ingest_conversation(client, conv, plan):
            logger.error("ingestion failed for %s, skipping questions", conv.id)
            continue

        # Ask each question
        for q_idx, question in enumerate(pending):
            t0 = time.perf_counter()
            try:
                resp = client.ask(question.text, plan.vault, mode=config.ask_mode)
                answer = resp.get("answer", resp.get("response", ""))
            except Exception as e:
                answer = ""
                elapsed_ms = (time.perf_counter() - t0) * 1000
                result = Result(
                    question=question,
                    answer="",
                    correct=False,
                    latency_ms=elapsed_ms,
                    retries=0,
                    error=str(e),
                )
                all_results.append(result)
                trace_writer.write(result)
                logger.warning(
                    "question %s: error: %s",
                    question.id,
                    e,
                )
                continue

            elapsed_ms = (time.perf_counter() - t0) * 1000

            # Score the answer
            score = score_answer(question, answer)
            correct = score >= 0.5

            result = Result(
                question=question,
                answer=answer[:500],
                correct=correct,
                latency_ms=elapsed_ms,
                retries=0,
                sources=resp.get("sources", resp.get("chunks", [])),
                score=score,
            )
            all_results.append(result)
            trace_writer.write(result)
            completed_ids.add(question.id)
            trace_writer.flush()

            logger.info(
                "  [%d/%d] %s: %s (%.0fms, score=%.2f)",
                q_idx + 1,
                len(pending),
                question.id,
                "✓" if correct else "✗",
                elapsed_ms,
                score,
            )

            # Save checkpoint
            if checkpoint.should_save(len(all_results)):
                checkpoint.save(all_results)

    # Final checkpoint
    checkpoint.save(all_results)
    trace_writer.close()

    # Score and report
    scoring = score_batch(all_results)
    logger.info("=" * 60)
    logger.info("Results: %d correct / %d total = %.1f%%", scoring["correct"], scoring["total"], scoring["accuracy"] * 100)
    logger.info("By type: %s", json.dumps(scoring["by_type"], indent=2))
    logger.info("=" * 60)

    # Baseline comparison
    if not args.no_baseline:
        comparison = compare_to_baseline(all_results, args.baseline)
        if "error" in comparison:
            logger.warning("baseline comparison: %s", comparison["error"])
        else:
            delta_str = f"+{comparison['delta']:.2%}" if comparison["delta"] >= 0 else f"{comparison['delta']:.2%}"
            logger.info("Baseline delta: %s", delta_str)
            if comparison["regression"]:
                logger.warning("REGRESSION detected vs baseline!")

    # Write accuracy report
    output_path = args.output or os.path.join(".benchmark_results", run_id)
    os.makedirs(output_path, exist_ok=True)
    report_path = os.path.join(output_path, "accuracy.json")
    with open(report_path, "w") as f:
        json.dump(scoring, f, indent=2)
    logger.info("accuracy report written to %s", report_path)

    return 0


# ── Stress runner ───────────────────────────────────────────────────────────────


def run_stress(args: argparse.Namespace) -> int:
    """Run a stress test."""
    client = RagamuffinClient(
        base_url=args.base_url,
        api_key=args.api_key,
    )

    if args.stress == "concurrent":
        test = ConcurrentStressTest(
            client=client,
            concurrency=args.concurrency,
            total_requests=args.requests,
        )
    elif args.stress == "large-vault":
        test = LargeVaultStressTest(
            client=client,
            target_sessions=args.target_sessions,
        )
    elif args.stress == "malformed":
        test = MalformedInputStressTest(client=client)
    else:
        logger.error("unknown stress test: %s", args.stress)
        return 1

    logger.info("running stress test: %s", test.name())
    result = test.run()

    # Report
    logger.info("=" * 60)
    logger.info("Stress test: %s", result.name)
    logger.info("  Requests: %d total, %d success, %d errors",
                result.total_requests, result.success_count, result.error_count)
    logger.info("  Latency: p50=%.0fms p95=%.0fms p99=%.0fms",
                result.latency_p50, result.latency_p95, result.latency_p99)
    logger.info("  Throughput: %.1f req/s", result.throughput_rps)
    if result.errors:
        logger.info("  Errors (%d):", len(result.errors))
        for e in result.errors[:5]:
            logger.info("    - %s", e)
    logger.info("=" * 60)

    # Write report
    output_path = args.output or os.path.join(
        ".benchmark_results",
        f"stress_{result.name}_{int(time.time())}",
    )
    os.makedirs(output_path, exist_ok=True)
    report_path = os.path.join(output_path, "stress.json")
    with open(report_path, "w") as f:
        json.dump(result.to_dict(), f, indent=2)
    logger.info("stress report written to %s", report_path)

    return 0


# ── Classify runner ─────────────────────────────────────────────────────────────


def run_classify(args: argparse.Namespace) -> int:
    """Classify failures from a trace file."""
    trace_file = args.classify
    if not os.path.exists(trace_file):
        logger.error("trace file not found: %s", trace_file)
        return 1

    results = load_trace(trace_file)
    if not results:
        logger.error("no results loaded from %s", trace_file)
        return 1

    failures = [r for r in results if not r.correct]
    logger.info("Loaded %d results, %d failures", len(results), len(failures))

    # Classify by error type
    by_type: Dict[str, int] = {}
    for f in failures:
        error_type = f.error or "incorrect_answer"
        by_type[error_type] = by_type.get(error_type, 0) + 1

    logger.info("Failure breakdown:")
    for error_type, count in sorted(by_type.items(), key=lambda x: -x[1]):
        logger.info("  %s: %d", error_type, count)

    # File GitHub issues (unless dry-run)
    if not args.dry_run:
        try:
            from benchmarks.classify_failures import file_issues  # noqa: F811

            filed = file_issues(failures, trace_file, dry_run=False)
            logger.info("Filed %d GitHub issues", len(filed))
        except ImportError:
            logger.warning("classify_failures.file_issues not found, skipping issue filing")
            return 0
        except Exception as e:
            logger.error("failed to file issues: %s", e)
            return 1
    else:
        logger.info("Dry run: would file issues for %d failure patterns", len(by_type))

    output_path = args.output or os.path.join(".benchmark_results", f"classify_{int(time.time())}")
    os.makedirs(output_path, exist_ok=True)
    classify_path = os.path.join(output_path, "failures.json")
    with open(classify_path, "w") as f:
        json.dump({"total_failures": len(failures), "by_type": by_type}, f, indent=2)
    logger.info("classification written to %s", classify_path)

    return 0


# ── Helpers ─────────────────────────────────────────────────────────────────────


def _ingest_conversation(client: RagamuffinClient, conv: Conversation, plan: IngestPlan) -> bool:
    """Ingest all messages from a conversation into the target vault."""
    try:
        for msg in conv.messages:
            role = msg.get("role", "user")
            content = msg.get("content", "")
            if not content:
                continue
            client.ingest(
                content=content,
                source=f"{conv.id}/{role}",
                vault=plan.vault,
                tags={
                    "benchmark": "longmemeval",
                    "conversation": conv.id,
                    "role": role,
                },
            )
        return True
    except Exception as e:
        logger.error("ingest failed for %s: %s", conv.id, e)
        return False


# ── Entry point ─────────────────────────────────────────────────────────────────


def main() -> int:
    args = build_parser().parse_args()

    # Setup logging
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%H:%M:%S",
    )

    if args.classify:
        return run_classify(args)
    if args.stress:
        return run_stress(args)
    if args.benchmark:
        return run_benchmark(args)

    # No mode specified
    logger.error("no mode specified. Use --benchmark, --stress, or --classify.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
