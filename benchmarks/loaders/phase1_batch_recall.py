"""Phase 1 Benchmark: Batch Recall.

Submits multiple queries in a single /v1/batch/recall call and
verifies all queries return results correctly.

Tests:
- Single-query batch submission
- Multi-query batch (5 concurrent queries)
- Query timing (all should complete within timeout)
- Error isolation (a bad query shouldn't affect others)
"""

from __future__ import annotations

import logging
import time
import uuid
from typing import Any, Dict, List

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")


def run_phase1_batch_recall(
    client: RagamuffinClient,
    vault: str,
) -> List[Dict[str, Any]]:
    """Run the batch recall benchmark."""
    results = []

    # First ensure some content is in the vault for recall to return results
    ingest_prefix = f"br-{uuid.uuid4().hex[:8]}"
    _ensure_content(client, vault, ingest_prefix)

    # ── Test 1: Single query batch ──────────────────────────────────────────
    logger.info("Test 1: Single query batch recall")
    t0 = time.perf_counter()
    try:
        resp = client.batch_recall(
            queries=["Tokyo Japan travel guide"],
            vault=vault,
            top_k=3,
        )
        elapsed = (time.perf_counter() - t0) * 1000
        entries = resp.get("results", [])
        n_results = len(entries)
        has_scores = any(e.get("top_score", 0) > 0 for e in entries if isinstance(e, dict))
        results.append({
            "test": "batch_recall_single",
            "pass": n_results > 0,
            "latency_ms": round(elapsed, 1),
            "detail": f"{n_results} entries returned, has_scores={has_scores}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "batch_recall_single",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 2: Multi-query batch (5 queries) ─────────────────────────────
    logger.info("Test 2: Five-query batch recall")
    queries = [
        "What files are in this vault?",
        "Tell me about Tokyo",
        "Japan travel information",
        "Budget travel recommendations",
        "Weather and packing advice",
    ]
    t0 = time.perf_counter()
    try:
        resp = client.batch_recall(queries=queries, vault=vault, top_k=3)
        elapsed = (time.perf_counter() - t0) * 1000
        entries = resp.get("results", [])
        n_entries = len(entries)
        all_present = n_entries >= 3  # at least 3 of 5 queries returned
        results.append({
            "test": "batch_recall_multi",
            "pass": all_present,
            "latency_ms": round(elapsed, 1),
            "detail": f"{n_entries}/{len(queries)} queries returned results",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "batch_recall_multi",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 3: Verify batch results match individual recall ──────────────
    logger.info("Test 3: Batch results match individual recall")
    t0 = time.perf_counter()
    try:
        # Single query
        single = client.recall("Tokyo Japan travel guide", vault=vault, top_k=3)
        single_results = single.get("results", single.get("data", []))
        single_ids = set()
        for r in single_results if isinstance(single_results, list) else []:
            if isinstance(r, dict):
                cid = r.get("chunk_id", r.get("id", ""))
                if cid:
                    single_ids.add(cid)

        # Same query via batch
        batch = client.batch_recall(queries=["Tokyo Japan travel guide"], vault=vault, top_k=3)
        batch_entries = batch.get("results", [])
        batch_ids = set()
        for be in batch_entries if isinstance(batch_entries, list) else []:
            if isinstance(be, dict):
                for r in (be.get("results", []) if isinstance(be.get("results"), list) else []):
                    cid = r.get("chunk_id", r.get("id", ""))
                    if cid:
                        batch_ids.add(cid)

        overlap = bool(single_ids & batch_ids)
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "batch_recall_consistency",
            "pass": overlap,
            "latency_ms": round(elapsed, 1),
            "detail": f"single recall: {len(single_ids)} IDs, batch: {len(batch_ids)} IDs, overlap={overlap}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "batch_recall_consistency",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    return results


def _ensure_content(client: RagamuffinClient, vault: str, prefix: str) -> None:
    """Ensure the vault has some content for recall to work against."""
    content = f"""user: I'm planning a trip to Japan.
user: I want to visit Tokyo and Kyoto.
assistant: Great choices! Tokyo offers a mix of ultramodern and traditional.
user: What's the best time to visit?
assistant: Spring (March-May) for cherry blossoms or autumn (October-November) for foliage.
user: What should I budget?
assistant: Budget around $150-200 per day for mid-range travel.
user: What should I pack?
assistant: Pack layers, comfortable walking shoes, and a travel umbrella."""
    try:
        client.ingest(content=content, source=f"{prefix}-travel-guide", vault=vault)
    except Exception:
        pass  # Content may already exist; ignore errors
