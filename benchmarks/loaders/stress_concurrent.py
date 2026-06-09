"""Concurrent load generator stress test.

Sends N simultaneous /ask requests and measures throughput and latency.
"""

from __future__ import annotations

import logging
import os
import threading
import time
from dataclasses import dataclass
from typing import Dict, List, Optional

from benchmarks.core.client import RagamuffinClient
from benchmarks.core.types import StressResult

logger = logging.getLogger("ragamuffin.benchmark")

CONCURRENT_TEST_QUERY = os.environ.get(
    "RAGAMUFFIN_STRESS_QUERY",
    "Summarize the key information in this vault.",
)


@dataclass
class _Result:
    latency_ms: float
    success: bool
    error: Optional[str] = None


class ConcurrentStressTest:
    """Fire N requests concurrently and measure latency/throughput."""

    def __init__(
        self,
        client: RagamuffinClient,
        concurrency: int = 10,
        total_requests: int = 100,
        vault: str = "default",
        query: str = CONCURRENT_TEST_QUERY,
    ):
        self.client = client
        self.concurrency = concurrency
        self.total_requests = total_requests
        self.vault = vault
        self.query = query

    def name(self) -> str:
        return "concurrent"

    def run(self) -> StressResult:
        """Run the concurrent stress test."""
        results: List[_Result] = []
        results_lock = threading.Lock()
        completed = threading.Event()
        start_time = time.time()

        def worker():
            while True:
                with results_lock:
                    if len(results) >= self.total_requests:
                        completed.set()
                        return
                t0 = time.perf_counter()
                try:
                    self.client.ask(self.query, self.vault)
                    elapsed = (time.perf_counter() - t0) * 1000
                    with results_lock:
                        results.append(_Result(latency_ms=elapsed, success=True))
                except Exception as e:
                    elapsed = (time.perf_counter() - t0) * 1000
                    with results_lock:
                        results.append(
                            _Result(latency_ms=elapsed, success=False, error=str(e))
                        )

        # Start worker threads
        threads = []
        for _ in range(self.concurrency):
            t = threading.Thread(target=worker, daemon=True)
            t.start()
            threads.append(t)

        # Wait for completion
        completed.wait(timeout=300)

        elapsed = time.time() - start_time
        latencies = sorted(r.latency_ms for r in results)
        successes = [r for r in results if r.success]
        errors = [r for r in results if not r.success]

        n = len(latencies)
        p50 = latencies[int(n * 0.5)] if n else 0
        p95 = latencies[int(n * 0.95)] if n else 0
        p99 = latencies[int(n * 0.99)] if n else 0

        return StressResult(
            name=self.name(),
            total_requests=len(results),
            success_count=len(successes),
            error_count=len(errors),
            latency_p50=p50,
            latency_p95=p95,
            latency_p99=p99,
            throughput_rps=len(results) / elapsed if elapsed > 0 else 0,
            errors=[{"error": e.error} for e in errors[:50]],  # cap error detail
        )
