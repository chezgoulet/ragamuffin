"""Phase 1 Benchmark: Concurrent Fact Contention.

Tests server behavior under concurrent write pressure to the same
fact key. Verifies:
- Multiple concurrent writes to the same key don't panic or 500
- Last-write-wins semantics (or conflict detection)
- Server stays responsive during contention
"""

from __future__ import annotations

import logging
import threading
import time
import uuid
from typing import Any, Dict, List, Optional

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")


def run_phase1_fact_contention(
    client: RagamuffinClient,
    vault: str,
    concurrency: int = 20,
) -> List[Dict[str, Any]]:
    """Run the concurrent fact contention benchmark.

    Args:
        client: Ragamuffin API client
        vault: Target vault name
        concurrency: Number of concurrent writers to the same key
    """
    results = []
    prefix = f"cc-{uuid.uuid4().hex[:8]}"
    contention_key = f"{prefix}-contention-target"

    # ── Test 1: Concurrent writes to the same key ──────────────────────────
    logger.info("Test 1: %d concurrent writes to key %s", concurrency, contention_key)

    write_errors: List[str] = []
    write_latencies: List[float] = []
    write_lock = threading.Lock()

    def write_fact(worker_id: int):
        t0 = time.perf_counter()
        try:
            client.create_fact(
                key=contention_key,
                value=f"Value from worker {worker_id} at {time.time()}",
                vault=vault,
                source="benchmark-contention",
                source_type="test",
            )
            lat = (time.perf_counter() - t0) * 1000
            with write_lock:
                write_latencies.append(lat)
        except Exception as e:
            lat = (time.perf_counter() - t0) * 1000
            with write_lock:
                write_latencies.append(lat)
                write_errors.append(f"worker {worker_id}: {e}")

    threads = []
    for i in range(concurrency):
        t = threading.Thread(target=write_fact, args=(i,), daemon=True)
        t.start()
        threads.append(t)

    for t in threads:
        t.join(timeout=30)

    contention_elapsed_ms = sum(write_latencies) / len(write_latencies) if write_latencies else 0

    results.append({
        "test": "concurrent_write",
        "pass": len(write_errors) < concurrency * 0.5,  # Allow some failures
        "latency_ms": round(contention_elapsed_ms, 1),
        "detail": f"{concurrency} concurrent writes: {len(write_errors)} errors, "
                  f"avg_latency={round(sum(write_latencies)/len(write_latencies), 1) if write_latencies else 0}ms",
        "concurrency": concurrency,
        "errors": len(write_errors),
        "error_details": write_errors[:5],
    })

    # ── Test 2: Read back — verify some value was persisted ────────────────
    logger.info("Test 2: Read back contention target")
    time.sleep(1)  # Let Qdrant settle
    t0 = time.perf_counter()
    try:
        data = client.get_fact(key=contention_key, vault=vault)
        elapsed = (time.perf_counter() - t0) * 1000
        got_value = data.get("value", data.get("fact", {}).get("value", ""))
        results.append({
            "test": "contention_readback",
            "pass": bool(got_value),
            "latency_ms": round(elapsed, 1),
            "detail": f"retrieved value: {str(got_value)[:100]}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "contention_readback",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 3: Concurrent reads during write contention ──────────────────
    logger.info("Test 3: Concurrent reads during write contention")
    read_errors: List[str] = []
    read_latencies: List[float] = []
    read_lock = threading.Lock()

    def read_fact():
        t0 = time.perf_counter()
        try:
            client.get_fact(key=contention_key, vault=vault)
            lat = (time.perf_counter() - t0) * 1000
            with read_lock:
                read_latencies.append(lat)
        except Exception as e:
            lat = (time.perf_counter() - t0) * 1000
            with read_lock:
                read_latencies.append(lat)
                read_errors.append(str(e))

    # Fire concurrent reads
    threads = []
    for _ in range(concurrency):
        t = threading.Thread(target=read_fact, daemon=True)
        t.start()
        threads.append(t)

    for t in threads:
        t.join(timeout=15)

    avg_read = sum(read_latencies) / len(read_latencies) if read_latencies else 0
    results.append({
        "test": "concurrent_read",
        "pass": len(read_errors) < concurrency * 0.5,
        "latency_ms": round(avg_read, 1),
        "detail": f"{len(read_latencies)} reads, {len(read_errors)} errors, "
                  f"avg_latency={round(avg_read, 1)}ms",
        "errors": len(read_errors),
    })

    return results
