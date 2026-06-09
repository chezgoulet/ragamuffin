"""Garbage input stress test.

Sends malformed payloads to Ragamuffin to verify graceful error handling.
Tests: empty sessions, unicode bombs, null bytes, extreme sizes.
"""

from __future__ import annotations

import logging
import time
from typing import List

from benchmarks.core.client import RagamuffinClient
from benchmarks.core.types import StressProfile, StressResult

logger = logging.getLogger("ragamuffin.benchmark")


class MalformedInputStressTest(StressProfile):
    """Send malformed/garbage inputs and verify graceful handling."""

    def __init__(self, client: RagamuffinClient, vault: str = "default"):
        self.client = client
        self.vault = vault

    def name(self) -> str:
        return "malformed-input"

    def run(self) -> StressResult:
        """Run the malformed input tests."""
        results: List[dict] = []
        errors: List[dict] = []

        test_cases = [
            ("empty string", "", "empty"),
            ("null byte", "hello\x00world", "malformed"),
            ("unicode bomb", "\u0000\uFFFF" * 1000, "malformed"),
            ("extreme unicode", "\U0001F4A9" * 10000, "malformed"),
            ("control chars", "\x00\x01\x02\x1F\x7F", "malformed"),
            ("very long line", "A" * 100000, "overflow"),
            ("only whitespace", "   \t\n\r   ", "empty"),
            ("html injection", "<script>alert(1)</script>", "malformed"),
        ]

        for name, content, category in test_cases:
            t0 = time.perf_counter()
            try:
                self.client.ingest(
                    content=content,
                    source=f"stress/malformed/{name}",
                    vault=self.vault,
                )
                elapsed = (time.perf_counter() - t0) * 1000
                results.append({"name": name, "latency_ms": elapsed, "error": None})
                logger.info("malformed test %q: ingested (%.0fms)", name, elapsed)
            except Exception as e:
                elapsed = (time.perf_counter() - t0) * 1000
                results.append(
                    {"name": name, "latency_ms": elapsed, "error": str(e)}
                )
                errors.append({"test": name, "category": category, "error": str(e)})
                logger.info("malformed test %q: rejected (%s)", name, e)

        # Ask / recall with garbage queries
        ask_tests = [
            ("empty query", ""),
            ("null byte query", "hello\x00world"),
            ("unicode query", "\U0001F4A9" * 100),
            ("very long query", "x" * 50000),
        ]

        ask_latencies = []
        for name, query in ask_tests:
            t0 = time.perf_counter()
            try:
                self.client.ask(query, self.vault)
                ask_latencies.append((time.perf_counter() - t0) * 1000)
                logger.info("malformed ask %q: answered (%.0fms)", name, ask_latencies[-1])
            except Exception as e:
                ask_latencies.append((time.perf_counter() - t0) * 1000)
                errors.append({"test": name, "category": "ask", "error": str(e)})
                logger.info("malformed ask %q: rejected (%s)", name, e)

        return StressResult(
            name=self.name(),
            total_requests=len(results) + len(ask_tests),
            success_count=len(results) + len(ask_tests) - len(errors),
            error_count=len(errors),
            latency_p50=_percentile(ask_latencies, 50),
            latency_p95=_percentile(ask_latencies, 95),
            latency_p99=_percentile(ask_latencies, 99),
            throughput_rps=0,
            errors=errors[:50],
        )


def _percentile(values: List[float], p: int) -> float:
    """Compute the pth percentile from a sorted list."""
    if not values:
        return 0
    sorted_vals = sorted(values)
    idx = int(len(sorted_vals) * p / 100)
    return sorted_vals[min(idx, len(sorted_vals) - 1)]
