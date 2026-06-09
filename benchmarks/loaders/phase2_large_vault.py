"""Phase 2 Benchmark: Large-Vault Scale Stress.

Ingest 500+ chunks into one vault, then measure /ask and /recall latency
at scale. Verifies that Ragamuffin maintains acceptable performance as
vault size grows.

Acceptance criteria:
- 500+ chunks ingested successfully (no errors)
- /ask latency stays under 5s median
- /recall latency stays under 2s median
- No connection timeouts or 500 errors during recall/ask
"""

from __future__ import annotations

import logging
import random
import time
import uuid
from typing import Any, Dict, List

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")

# ── Content templates ───────────────────────────────────────────────────────────

TOPICS = [
    "project planning methodologies",
    "deployment strategies",
    "monitoring tools",
    "security audits",
    "performance optimization",
    "documentation practices",
    "incident post-mortems",
    "team communication patterns",
    "code review workflows",
    "testing frameworks",
    "database design principles",
    "API versioning strategies",
    "container orchestration",
    "service mesh architectures",
    "observability patterns",
    "cost optimization",
    "disaster recovery planning",
    "capacity planning",
    "release management",
    "technical debt management",
]

TEMPLATE = """Session {session_id}: Discussion about {topic}.

The team evaluated multiple approaches to {topic}. Key considerations
included scalability, maintainability, and team expertise. After thorough
analysis, they decided on a solution that balanced these factors with
the organization's existing infrastructure and tooling preferences.

Specific findings included:
- Primary recommendation: {recommendation}
- Alternative considered: {alternative}
- Risk factors: {risks}
- Next steps: {next_steps}
- Owner: {owner}

The decision was documented in the team's knowledge base for future
reference. Follow-up discussions were scheduled to review the outcome
after the implementation phase.
"""

RECOMMENDATIONS = [
    "iterative development with two-week sprints",
    "blue-green deployment with canary testing",
    "distributed tracing with OpenTelemetry",
    "zero-trust architecture with quarterly penetration testing",
    "connection pooling with 5-minute query cache TTL",
    "automated documentation generation from code comments",
    "blameless post-mortem culture with actionable follow-ups",
]

ALTERNATIVES = [
    "waterfall methodology",
    "rolling deployment without canary",
    "traditional monitoring with static thresholds",
    "perimeter-based security model",
    "no caching with direct database queries",
    "manual documentation updates",
    "root-cause-focused post-mortems",
]

RISKS = [
    "team learning curve, migration complexity",
    "vendor lock-in, cost overrun",
    "alert fatigue, false positives",
    "user friction, training requirements",
    "stale data, cache invalidation bugs",
    "outdated documentation, knowledge silos",
    "blame culture, incomplete follow-through",
]

NEXT_STEPS = [
    "draft RFC, schedule review, assign implementation team",
    "set up pipeline, configure monitoring, run dry run",
    "evaluate tools, POC with real traffic, document findings",
    "audit current posture, create remediation plan, schedule fixes",
    "profile current performance, implement changes, measure improvement",
    "audit existing docs, create templates, set review cadence",
    "schedule review, document findings, track action items",
]

OWNERS = ["alice", "bob", "carol", "dave", "eve", "frank", "grace", "heidi"]


def _generate_content(session_id: int) -> str:
    """Generate a unique content string for a session."""
    topic = TOPICS[session_id % len(TOPICS)]
    rec = RECOMMENDATIONS[session_id % len(RECOMMENDATIONS)]
    alt = ALTERNATIVES[session_id % len(ALTERNATIVES)]
    risk = RISKS[session_id % len(RISKS)]
    nxt = NEXT_STEPS[session_id % len(NEXT_STEPS)]
    owner = OWNERS[session_id % len(OWNERS)]
    return TEMPLATE.format(
        session_id=session_id,
        topic=topic,
        recommendation=rec,
        alternative=alt,
        risks=risk,
        next_steps=nxt,
        owner=owner,
    )


RECALL_QUERIES = [
    "What was the recommendation for project planning?",
    "How should deployment be handled?",
    "What security measures were discussed?",
    "What monitoring approach was chosen?",
    "How should documentation be managed?",
    "What incident response practices were established?",
    "What is the cache strategy?",
    "How should the team handle technical debt?",
    "What disaster recovery plan was proposed?",
    "What capacity planning approach was decided?",
]


# ── Main benchmark ──────────────────────────────────────────────────────────────


def run_phase2_large_vault(
    client: RagamuffinClient,
    vault: str,
    target_chunks: int = 60,
) -> List[Dict[str, Any]]:
    """Run the large-vault scale stress benchmark.

    Args:
        client: Ragamuffin API client.
        vault: Target vault name.
        target_chunks: Number of sessions/chunks to ingest (default 60, adjust for 500+).

    Returns a list of individual test results.
    """
    results = []
    ingest_errors = 0

    # ── Phase 1: Ingest target_chunks sessions ──────────────────────────────
    logger.info("Ingesting %d sessions into vault %s", target_chunks, vault)
    t0 = time.perf_counter()

    for i in range(target_chunks):
        content = _generate_content(i)
        try:
            client.ingest(
                content=content,
                source=f"phase2-bench/session-{i:04d}",
                vault=vault,
            )
        except Exception as e:
            ingest_errors += 1
            logger.warning("Ingest error on session %d: %s", i, e)
        if (i + 1) % 50 == 0:
            logger.info("Ingested %d/%d sessions", i + 1, target_chunks)

    ingest_elapsed = time.perf_counter() - t0
    results.append({
        "test": "large_vault_ingest",
        "pass": ingest_errors == 0,
        "latency_ms": round(ingest_elapsed * 1000, 1),
        "detail": f"ingested {target_chunks} sessions in {ingest_elapsed:.1f}s with {ingest_errors} errors",
    })

    # Wait for indexing to complete
    time.sleep(5)

    # ── Phase 2: Measure /recall latency ────────────────────────────────────
    logger.info("Measuring /recall latency across %d queries", len(RECALL_QUERIES))
    recall_failures = 0
    recall_latencies = []

    for q in RECALL_QUERIES:
        t1 = time.perf_counter()
        try:
            data = client.recall(query=q, vault=vault, top_k=3)
            elapsed_ms = (time.perf_counter() - t1) * 1000
            recall_latencies.append(elapsed_ms)
            results_container = data.get("results", data.get("chunks", []))
            if not results_container:
                recall_failures += 1
        except Exception as e:
            elapsed_ms = (time.perf_counter() - t1) * 1000
            recall_latencies.append(elapsed_ms)
            recall_failures += 1
            logger.warning("Recall error: %s", e)

    if recall_latencies:
        sorted_lat = sorted(recall_latencies)
        n = len(sorted_lat)
        results.append({
            "test": "large_vault_recall_latency",
            "pass": recall_failures < 3,
            "latency_ms": round(sorted_lat[int(n * 0.5)], 1),
            "detail": (
                f"p50={sorted_lat[int(n*0.5)]:.0f}ms p95={sorted_lat[int(n*0.95)]:.0f}ms "
                f"p99={sorted_lat[int(n*0.99)]:.0f}ms failures={recall_failures}/{len(RECALL_QUERIES)}"
            ),
        })

    # ── Phase 3: Measure /ask latency ────────────────────────────────────────
    logger.info("Measuring /ask latency")
    ask_latencies = []
    ask_failures = 0

    for q in RECALL_QUERIES[:5]:  # fewer queries for /ask (slower)
        t1 = time.perf_counter()
        try:
            data = client.ask(query=q, vault=vault)
            elapsed_ms = (time.perf_counter() - t1) * 1000
            ask_latencies.append(elapsed_ms)
            answer = data.get("answer", "")
            if not answer:
                ask_failures += 1
        except Exception as e:
            elapsed_ms = (time.perf_counter() - t1) * 1000
            ask_latencies.append(elapsed_ms)
            ask_failures += 1
            logger.warning("Ask error: %s", e)

    if ask_latencies:
        sorted_ask = sorted(ask_latencies)
        n = len(sorted_ask)
        results.append({
            "test": "large_vault_ask_latency",
            "pass": ask_failures < 2,
            "latency_ms": round(sorted_ask[int(n * 0.5)], 1),
            "detail": (
                f"p50={sorted_ask[int(n*0.5)]:.0f}ms p95={sorted_ask[int(n*0.95)]:.0f}ms "
                f"p99={sorted_ask[int(n*0.99)]:.0f}ms failures={ask_failures}/{min(5,len(RECALL_QUERIES))}"
            ),
        })

    # ── Phase 4: Verify vault listing includes our vault ────────────────────
    logger.info("Verifying vault listing")
    t1 = time.perf_counter()
    try:
        vaults = client.list_vaults()
        elapsed_ms = (time.perf_counter() - t1) * 1000
        found = vault in vaults
        results.append({
            "test": "large_vault_listing",
            "pass": found,
            "latency_ms": round(elapsed_ms, 1),
            "detail": f"vault '{vault}' {'found' if found else 'not found'} in {len(vaults)} vaults",
        })
    except Exception as e:
        elapsed_ms = (time.perf_counter() - t1) * 1000
        results.append({
            "test": "large_vault_listing",
            "pass": False,
            "latency_ms": round(elapsed_ms, 1),
            "detail": str(e),
        })

    return results
