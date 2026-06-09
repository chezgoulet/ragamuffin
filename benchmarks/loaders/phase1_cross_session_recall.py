"""Phase 1 Benchmark: Multi-session ingestion + cross-session recall.

Ingests a sequence of conversation turns across sessions, then asks
questions that require temporal reasoning and knowledge synthesis
across the ingested sessions.

Tests:
- Ingestion of multiple session turns into a vault
- Temporal / cross-session synthesis via /ask
- Correctness of answers requiring multi-turn context
"""

from __future__ import annotations

import json
import logging
import time
import uuid
from typing import Any, Dict, List, Optional

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")

# ── Scenarios ───────────────────────────────────────────────────────────────────


class LongConversationScenario:
    """A multi-session conversation with questions that cross sessions."""

    def __init__(self, vault: str):
        self.vault = vault
        # Three sessions of a planning conversation
        self.sessions = [
            {
                "content": """user: I'm planning a trip to Japan in March.
user: I'll be there for two weeks.
user: My budget is roughly $5000 total.
assistant: That's a great trip! A two-week Japan trip on $5000 is doable if you plan carefully. Where are you thinking of going?
user: I want to visit Tokyo, Kyoto, and Osaka at minimum.
assistant: Excellent choices. The Golden Route (Tokyo-Kyoto-Osaka) is perfect for first-timers. March is cherry blossom season in many parts of Japan.""",
                "source": "session-1-planning",
            },
            {
                "content": """user: Where should I stay in Tokyo?
assistant: Shinjuku is great for first-timers — good transport connections and lots to do. Shibuya is trendier but more expensive.
user: What about Kyoto?
assistant: Stay near Kyoto Station or in Higashiyama for easy access to temples. Gion is beautiful but pricey.
user: I need to book a hotel, my budget for accommodation is $1500 total.
assistant: For 14 nights at $1500 that's about $107/night. Hostels or business hotels like APA will work. In Tokyo aim for ~$80/night to save for pricier Kyoto lodging.""",
                "source": "session-2-accommodation",
            },
            {
                "content": """user: What should I do for cherry blossoms in Tokyo?
assistant: Ueno Park, Shinjuku Gyoen, and Chidorigafuchi are top spots.
user: And in Kyoto?
assistant: Philosopher's Path, Maruyama Park, and Arashiyama are beautiful.
user: I love hiking. Are there good mountain trails in Japan?
assistant: Absolutely! Mount Takao near Tokyo, the Kumano Kodo pilgrimage trails, and the Nakasendo Trail are all excellent. You could also day-hike in Kamikochi if you venture toward the Alps.
user: I'd love to see Mount Fuji up close.
assistant: The Fuji Five Lakes area at the base is accessible without climbing. Lake Kawaguchi has great views. The actual climb is only July-August, so in March you'd just see it from the surrounding area.""",
                "source": "session-3-activities",
            },
        ]

        # Questions that need cross-session synthesis
        self.questions = [
            {
                "id": "cs-budget-accommodation",
                "question": "What is the user's total budget and how much are they allocating for accommodation?",
                "ground_truth": "$5000 total, $1500 for accommodation (about $107/night)",
                "reasoning": "Cross-reference session 1 (total budget) with session 2 (accommodation budget)",
                "type": "cross-session",
            },
            {
                "id": "cs-duration-cities",
                "question": "Which four cities or regions does the user plan to visit and for how long overall?",
                "ground_truth": "Tokyo, Kyoto, Osaka — two weeks total",
                "reasoning": "Session 1 mentions destinations and duration",
                "type": "cross-session",
            },
            {
                "id": "cs-seasonal-activities",
                "question": "What seasonal attraction is available in March and what hiking-related option involves Mount Fuji?",
                "ground_truth": "Cherry blossom season in March; Fuji Five Lakes area near Mount Fuji",
                "reasoning": "Session 1 mentions cherry blossom season, session 3 mentions Fuji Five Lakes",
                "type": "cross-session",
            },
            {
                "id": "cs-accommodation-tips",
                "question": "What accommodation type does the assistant recommend for Tokyo and what nightly rate should the user target?",
                "ground_truth": "Stay in Shinjuku; aim for ~$80/night (hostels or business hotels like APA)",
                "reasoning": "Session 2 discusses accommodation budget and recommendations",
                "type": "single-session",
            },
        ]

    def ingest_all(self, client: RagamuffinClient) -> None:
        """Ingest all sessions into the vault."""
        for session in self.sessions:
            logger.info("Ingesting session: %s", session["source"])
            client.ingest(
                content=session["content"],
                source=session["source"],
                vault=self.vault,
            )

    def score_answer(self, question: str, ground_truth: str, answer: str) -> Dict[str, Any]:
        """Score an answer by checking key terms from ground truth appear in answer."""
        gt_lower = ground_truth.lower()
        answer_lower = answer.lower() if answer else ""

        # Extract key terms: split on word boundaries, remove stopwords,
        # keep meaningful words > 3 chars or numeric values with $/numbers
        import re
        stopwords = {"the", "and", "for", "are", "but", "not", "you", "all", "can", "had", "her",
                     "was", "one", "our", "out", "has", "have", "been", "its", "more", "than",
                     "that", "this", "with", "what", "your", "from", "they", "will", "about",
                     "would", "there", "their", "when", "also", "how", "some", "very", "just"}

        # Extract meaningful terms from ground truth
        terms = set()
        for token in re.findall(r"[\w$]+|\$?\d+(?:[.,]?\d+)?/night|\$?\d+(?:[.,]?\d+)?", gt_lower):
            if token not in stopwords and len(token) > 2:
                terms.add(token)

        # Check each term — count how many appear in answer
        matched = sum(1 for t in terms if t in answer_lower)

        score = matched / len(terms) if terms else 0.0
        return {
            "score": round(score, 2),
            "matched_terms": matched,
            "total_terms": len(terms),
            "correct": score >= 0.4,
        }


# ── Runner ──────────────────────────────────────────────────────────────────────


def run_phase1_cross_session_recall(
    client: RagamuffinClient,
    vault: str,
) -> List[Dict[str, Any]]:
    """Run the cross-session recall benchmark."""
    results = []

    # Create vault via ingest
    scenario = LongConversationScenario(vault)
    scenario.ingest_all(client)

    for q in scenario.questions:
        logger.info("Asking: %s (%s)", q["id"], q["type"])
        t0 = time.perf_counter()

        try:
            resp = client.ask(q["question"], vault, mode="rag")
            answer = resp.get("answer", resp.get("response", str(resp)))
            elapsed = (time.perf_counter() - t0) * 1000
        except Exception as e:
            logger.error("Question %s failed: %s", q["id"], e)
            elapsed = (time.perf_counter() - t0) * 1000
            answer = ""
            results.append({
                "id": q["id"],
                "question": q["question"],
                "expected": q["ground_truth"],
                "answer": "",
                "error": str(e),
                "latency_ms": round(elapsed, 1),
                "type": q["type"],
                "score": 0.0,
                "pass": False,
            })
            continue

        score_info = scenario.score_answer(q["question"], q["ground_truth"], answer)
        passed = score_info["correct"]

        results.append({
            "id": q["id"],
            "question": q["question"],
            "expected": q["ground_truth"],
            "answer": answer.strip()[:200],
            "latency_ms": round(elapsed, 1),
            "type": q["type"],
            "score": score_info["score"],
            "matched_terms": score_info["matched_terms"],
            "total_terms": score_info["total_terms"],
            "pass": passed,
        })

    return results
