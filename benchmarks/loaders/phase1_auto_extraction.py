"""Phase 1 Benchmark: Auto-Extraction from Ingestion.

Tests the automatic fact extraction pipeline by ingesting conversations
with RAGAMUFFIN_EXTRACT_ENABLED and verifying facts appear.
"""

from __future__ import annotations

import logging
import time
import uuid
from typing import Any, Dict, List

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")


def run_phase1_auto_extraction(
    client: RagamuffinClient,
    vault: str,
) -> List[Dict[str, Any]]:
    """Run the auto-extraction benchmark.

    Ingests conversations and checks if facts were auto-extracted.
    Note: auto-extraction is configured server-side via
    RAGAMUFFIN_EXTRACT_ENABLED. The benchmark detects whether it's
    enabled and reports accordingly — no test failure if disabled,
    but an informational note.
    """
    results = []
    prefix = f"ae-{uuid.uuid4().hex[:8]}"

    # ── Ingest a fact-rich conversation ──────────────────────────────────────
    logger.info("Ingesting conversation for auto-extraction check")
    conversation = """user: I'm a software engineer working on distributed systems.
user: My team uses Go and Python primarily.
user: We're building a microservice architecture with Kafka for message passing.
user: I've been in the industry for 8 years, specializing in backend systems.
assistant: That's a strong background. Distributed systems with Kafka is a solid choice for microservices. Go and Python complement each other well for high-throughput and quick prototyping.
user: I'm looking for a new role where I can work on database internals.
user: I have experience with PostgreSQL and Redis.
assistant: Database internals is a fascinating specialty. Your Kafka experience with distributed consensus and your Go proficiency maps directly to database engine work.
user: I'm particularly interested in storage engines and query optimization.
assistant: Storage engines (LSM trees, B-trees) and query optimization are deep areas. Your systems background gives you a great foundation for this.
user: What companies are known for strong database engineering teams?
assistant: Cockroach Labs (CockroachDB), PlanetScale (Vitess), SingleStore, and the PostgreSQL core team are all excellent. Also check out DuckDB Labs for embedded analytics."""

    t0 = time.perf_counter()
    try:
        client.ingest(content=conversation, source=f"{prefix}-engineer-chat", vault=vault)
        ingest_elapsed = (time.perf_counter() - t0) * 1000
        logger.info("Ingest took %.0fms", ingest_elapsed)
    except Exception as e:
        ingest_elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "ingest_for_extraction",
            "pass": False,
            "latency_ms": round(ingest_elapsed, 1),
            "detail": f"ingest failed: {e}",
        })
        return results

    # ── Check if facts were auto-extracted ───────────────────────────────────
    logger.info("Checking for auto-extracted facts")
    time.sleep(5)  # Allow extraction goroutine to complete

    t0 = time.perf_counter()
    try:
        data = client.list_facts(vault=vault, prefix="")
        elapsed = (time.perf_counter() - t0) * 1000
        facts = data.get("facts", data.get("results", data.get("data", [])))
        fact_count = len(facts) if isinstance(facts, list) else 0

        # Also check via prefix search for our conversation
        data2 = client.list_facts(vault=vault, prefix=f"{prefix}")
        facts2 = data2.get("facts", data2.get("results", data2.get("data", [])))
        prefixed_count = len(facts2) if isinstance(facts2, list) else 0

        results.append({
            "test": "auto_extraction_detection",
            "pass": True,  # informational — not a hard pass/fail
            "latency_ms": round(elapsed, 1),
            "detail": f"total facts in vault: {fact_count}, prefixed: {prefixed_count}",
            "extraction_enabled": fact_count > 0,
            "fact_count": fact_count,
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "auto_extraction_detection",
            "pass": True,  # informational
            "latency_ms": round(elapsed, 1),
            "detail": f"check failed: {e}",
            "extraction_enabled": False,
            "fact_count": 0,
        })

    # ── Ask a question that needs the extracted knowledge ────────────────────
    logger.info("Asking extraction-dependent question")
    t0 = time.perf_counter()
    try:
        resp = client.ask("What programming languages does the user know and how many years of experience do they have?", vault)
        answer = resp.get("answer", resp.get("response", str(resp)))
        elapsed = (time.perf_counter() - t0) * 1000

        has_go = "go" in answer.lower()
        has_python = "python" in answer.lower()
        has_years = "8" in answer or "eight" in answer.lower()
        passed = has_go and has_python

        results.append({
            "test": "extraction_synthesis",
            "pass": passed,
            "latency_ms": round(elapsed, 1),
            "detail": f"answer mentions Go={has_go}, Python={has_python}, years={has_years}",
            "answer_preview": answer.strip()[:200],
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "extraction_synthesis",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    return results
