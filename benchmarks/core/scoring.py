"""Scoring wrapper around the upstream evaluate_qa judge.

evaluate_qa.py is the authoritative LongMemEval judge. This module wraps
it for use in the benchmark harness.
"""

from __future__ import annotations

import importlib.util
import json
import logging
import os
import sys
from typing import Dict, List

from .types import Question, Result

logger = logging.getLogger("ragamuffin.benchmark")

# Path to upstream judge
_EVALUATE_QA = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
    "evaluate_qa.py",
)


def _load_litellm_judge():
    """Load the LiteLLM-based judge."""
    try:
        from .litellm_judge import evaluate as litellm_evaluate
        return litellm_evaluate
    except Exception as e:
        logger.warning("LiteLLM judge unavailable: %s", e)
        return None


def _load_upstream_judge():
    """Lazy-load the evaluate_qa judge module once."""
    if _load_upstream_judge.cached is not None:
        return _load_upstream_judge.cached

    if not os.path.exists(_EVALUATE_QA):
        _load_upstream_judge.cached = None
        return None

    spec = importlib.util.spec_from_file_location("evaluate_qa", _EVALUATE_QA)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["evaluate_qa"] = mod
    spec.loader.exec_module(mod)
    _load_upstream_judge.cached = mod
    return mod


_load_upstream_judge.cached = None  # type: ignore[attr-defined]


def score_answer(question: Question, answer: str) -> float:
    """Score a single answer against ground truth using an LLM judge.

    Uses LiteLLM first, falls back to upstream evaluate_qa, then
    exact string match on failure.
    Returns 0.0-1.0 where 1.0 is a perfect match.
    """
    # 1. Try LiteLLM judge (fastest, no deps needed)
    litellm_evaluate = _load_litellm_judge()
    if litellm_evaluate is not None:
        try:
            result = litellm_evaluate(
                question.text,
                answer,
                question.ground_truth,
            )
            return float(min(max(result, 0.0), 1.0))
        except Exception as e:
            logger.warning("LiteLLM judge failed: %s", e)

    # 2. Try upstream judge (requires openai, backoff, tqdm)
    judge = _load_upstream_judge()
    if judge is not None and hasattr(judge, "evaluate"):
        try:
            result = judge.evaluate(
                question.text,
                answer,
                question.ground_truth,
            )
            if isinstance(result, (int, float)):
                return float(min(max(result, 0.0), 1.0))
            if isinstance(result, dict):
                return float(min(max(result.get("score", 0.0), 0.0), 1.0))
            if isinstance(result, bool):
                return 1.0 if result else 0.0
        except Exception as e:
            logger.warning("upstream judge evaluate failed: %s", e)

    # 3. Fallback: exact string match
    return 1.0 if answer.strip().lower() == question.ground_truth.strip().lower() else 0.0


def score_batch(results: List[Result]) -> Dict:
    """Score a batch of results, returning per-type aggregates.

    Returns:
    {
        "total": N,
        "correct": M,
        "accuracy": 0.XX,
        "by_type": {
            "temporal-reasoning": {"total": 4, "correct": 3, "accuracy": 0.75},
            ...
        },
        "by_config": {
            "D": {"total": 10, "correct": 8, "accuracy": 0.8},
            ...
        }
    }
    """
    total = len(results)
    correct = sum(1 for r in results if r.correct)
    by_type: Dict[str, Dict] = {}
    by_config: Dict[str, Dict] = {}

    for r in results:
        t = r.question.question_type
        c = r.question.config_label
        for d, key in [(by_type, t), (by_config, c)]:
            if key not in d:
                d[key] = {"total": 0, "correct": 0}
            d[key]["total"] += 1
            if r.correct:
                d[key]["correct"] += 1

    for d in [by_type, by_config]:
        for key in d:
            t = d[key]["total"]
            d[key]["accuracy"] = round(d[key]["correct"] / t, 4) if t else 0.0

    return {
        "total": total,
        "correct": correct,
        "accuracy": round(correct / total, 4) if total else 0.0,
        "by_type": by_type,
        "by_config": by_config,
    }


def compare_to_baseline(results: List[Result], baseline_path: str) -> Dict:
    """Compare results to a baseline JSON file.

    Returns delta report showing which scores improved, regressed, or stayed.
    """
    if not os.path.exists(baseline_path):
        return {"error": f"baseline not found: {baseline_path}"}

    with open(baseline_path) as f:
        baseline = json.load(f)

    current = score_batch(results)

    delta = current["accuracy"] - baseline.get("accuracy", 0)
    return {
        "baseline_accuracy": baseline.get("accuracy", 0),
        "current_accuracy": current["accuracy"],
        "delta": round(delta, 4),
        "regression": delta < -0.01,
        "improvement": delta > 0.01,
        "stable": -0.01 <= delta <= 0.01,
        "by_type_deltas": {
            t: current.get("by_type", {}).get(t, {}).get("accuracy", 0)
            - baseline.get("by_type", {}).get(t, {}).get("accuracy", 0)
            for t in set(list(current.get("by_type", {})) + list(baseline.get("by_type", {})))
        },
    }
