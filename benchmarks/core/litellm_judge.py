"""LLM-based judge using LiteLLM proxy.

Replaces the upstream evaluate_qa.py judge (which requires openai/backoff/tqdm)
with direct LiteLLM calls through the existing proxy.

Interface: evaluate(question, answer, ground_truth) -> float (0.0 or 1.0)
"""

from __future__ import annotations

import json
import os
import urllib.request
from typing import Union

LITELLM_URL = os.environ.get("LITELLM_URL", "http://172.21.0.2:4000")
LITELLM_API_KEY = os.environ.get("LITELLM_API_KEY", "")
MODEL = os.environ.get("LITELLM_JUDGE_MODEL", "gpt-4o")

SYSTEM_PROMPT = (
    "You are a strict answer verifier. "
    "Compare the predicted answer to the ground truth. "
    "Reply with ONLY a single number: 1.0 if the predicted answer "
    "matches the ground truth (allowing paraphrasing), 0.0 otherwise."
)


def evaluate(
    question: str,
    answer: str,
    ground_truth: Union[str, int, float],
) -> float:
    """Score a predicted answer against ground truth via LLM judge.

    Returns 1.0 for a match, 0.0 otherwise.
    Falls back to exact string match on error or timeout.
    """
    if not answer or not answer.strip():
        return 0.0

    user_prompt = (
        f"Question: {question}\n"
        f"Ground truth: {ground_truth}\n"
        f"Predicted answer: {answer}\n"
        f"Score:"
    )

    body = json.dumps({
        "model": MODEL,
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": user_prompt},
        ],
        "max_tokens": 5,
        "temperature": 0,
    }).encode()

    req = urllib.request.Request(
        f"{LITELLM_URL}/v1/chat/completions",
        data=body,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {LITELLM_API_KEY}",
        },
        method="POST",
    )

    try:
        resp = urllib.request.urlopen(req, timeout=10)
        data = json.loads(resp.read())
        score_str = data["choices"][0]["message"]["content"].strip()
        score = float(score_str)
        return min(max(score, 0.0), 1.0)
    except Exception:
        # Fallback: exact match
        gt = str(ground_truth).strip().lower()
        ans = answer.strip().lower()
        return 1.0 if ans == gt else 0.0
