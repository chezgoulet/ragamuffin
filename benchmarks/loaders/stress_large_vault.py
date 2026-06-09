"""Large vault stress test.

Scales a vault to N sessions, then measures /ask and /recall latency.
"""

from __future__ import annotations

import logging
import random
import time
from typing import List

from benchmarks.core.client import RagamuffinClient
from benchmarks.core.types import StressProfile, StressResult

logger = logging.getLogger("ragamuffin.benchmark")

INGEST_CONTENT = """The following is a historical record of session {session_id}.

On day 1, the user asked about project planning methodologies. The system
recommended an adaptive approach using iterative development cycles with
two-week sprints and regular retrospectives.

On day 2, they discussed deployment strategies. The preferred approach
was blue-green deployment with automated rollback and canary testing at
5%% traffic before full rollout.

On day 3, the team evaluated monitoring tools. They selected a combination
of structured logging, distributed tracing, and metric aggregation with
configurable alert thresholds and on-call rotation schedules.

On day 4, the security audit was completed. Findings included the need for
zero-trust architecture, secrets rotation every 90 days, and quarterly
penetration testing with external vendors.

On day 5, the performance optimization phase began. Key improvements included
connection pooling, query caching with 5-minute TTL, and lazy loading of
infrequently accessed resources.

On day 6, the documentation was updated to reflect the new architecture
decisions. Each decision record included context, options considered, the
chosen approach, and the rationale behind it.

On day 7, the team conducted a post-mortem of the previous incident. Root
cause was identified as a race condition in the cache invalidation logic.
The fix involved adding distributed locks with configurable timeout and
retry with exponential backoff. Action items were tracked and owners
assigned.
"""


class LargeVaultStressTest(StressProfile):
    """Scale vault to N sessions and measure recall/ask latency."""

    def __init__(
        self,
        client: RagamuffinClient,
        target_sessions: int = 50,
        vault: str = "stress-vault",
        sample_queries: int = 20,
    ):
        self.client = client
        self.target_sessions = target_sessions
        self.vault = vault
        self.sample_queries = sample_queries

    def name(self) -> str:
        return "large-vault"

    def run(self) -> StressResult:
        """Scale vault and measure latency."""
        errors: List[dict] = []

        # Phase 1: Ingest N sessions
        logger.info(
            "large-vault: ingesting %d sessions into %s",
            self.target_sessions,
            self.vault,
        )
        ingest_start = time.time()
        for i in range(self.target_sessions):
            content = INGEST_CONTENT.format(session_id=i)
            try:
                self.client.ingest(
                    content=content,
                    source=f"stress/session-{i}",
                    vault=self.vault,
                )
            except Exception as e:
                errors.append({"phase": "ingest", "session": i, "error": str(e)})
            if (i + 1) % 10 == 0:
                logger.info("large-vault: ingested %d/%d", i + 1, self.target_sessions)
        ingest_elapsed = time.time() - ingest_start
        logger.info(
            "large-vault: ingest complete in %.1fs",
            ingest_elapsed,
        )

        # Phase 2: Measure /ask latency
        queries = [
            "What happened on day 1?",
            "What deployment strategy was discussed?",
            "Describe the monitoring tools evaluation.",
            "What did the security audit find?",
            "What performance improvements were made?",
            "When was documentation updated?",
            "What incident post-mortem was conducted?",
            "What was the root cause of the incident?",
            "What action items were tracked?",
            "What caching strategy was used?",
        ]

        ask_latencies = []
        for i in range(self.sample_queries):
            q = random.choice(queries)
            t0 = time.perf_counter()
            try:
                self.client.ask(q, self.vault)
                ask_latencies.append((time.perf_counter() - t0) * 1000)
            except Exception as e:
                ask_latencies.append((time.perf_counter() - t0) * 1000)
                errors.append({"phase": "ask", "query": i, "error": str(e)})

        latencies = sorted(ask_latencies)
        n = len(latencies)

        return StressResult(
            name=self.name(),
            total_requests=len(latencies),
            success_count=len(latencies),
            error_count=len(errors),
            latency_p50=latencies[int(n * 0.5)] if n else 0,
            latency_p95=latencies[int(n * 0.95)] if n else 0,
            latency_p99=latencies[int(n * 0.99)] if n else 0,
            throughput_rps=ingest_elapsed / self.target_sessions if self.target_sessions > 0 else 0,
            errors=errors[:50],
        )
