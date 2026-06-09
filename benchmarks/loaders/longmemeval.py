"""LongMemEval dataset loader.

Loads conversations from the LongMemEval S/ directory structure:
  S/{session_id}/conversation.json — list of turns
  S/{session_id}/questions.json   — list of question objects with ground truth
"""

from __future__ import annotations

import json
import logging
import os
from typing import Dict, List, Optional

from benchmarks.core.types import Conversation, IngestPlan, Question
from benchmarks.loaders.base import DatasetLoader

logger = logging.getLogger("ragamuffin.benchmark")


class LongMemEvalLoader(DatasetLoader):
    """Loads the LongMemEval dataset from a local directory.

    Expected structure:
        <dataset_path>/S/<session_id>/conversation.json
        <dataset_path>/S/<session_id>/questions.json
    """

    def __init__(
        self,
        dataset_path: str,
        vault_prefix: str = "lme",
        config_label: str = "D",
    ):
        self.dataset_path = dataset_path
        self.vault_prefix = vault_prefix
        self.config_label = config_label

    # ── DatasetLoader interface ──────────────────────────────────────────────

    def name(self) -> str:
        return "longmemeval"

    def load(self) -> List[Conversation]:
        """Discover all sessions and load their conversations."""
        sessions = self._discover_sessions()
        conversations = []

        for session_id in sessions:
            conv_path = os.path.join(self.dataset_path, session_id, "conversation.json")
            if not os.path.exists(conv_path):
                logger.warning("missing conversation.json for session %s", session_id)
                continue
            try:
                with open(conv_path) as f:
                    data = json.load(f)
            except (json.JSONDecodeError, OSError) as e:
                logger.warning("skipping session %s: %s", session_id, e)
                continue

            vault = self._vault_name(session_id)
            messages = self._normalize_messages(data)
            conversations.append(
                Conversation(
                    id=session_id,
                    messages=messages,
                    vault=vault,
                    source=f"longmemeval/{session_id}",
                )
            )

        logger.info("loaded %d conversations from LongMemEval", len(conversations))
        return conversations

    def ingest_strategy(
        self,
        conversation: Conversation,
        config_label: str,
    ) -> IngestPlan:
        return IngestPlan(
            vault=conversation.vault,
            conversations=[conversation],
            auto_extract=(config_label in ("B", "D")),
            config_label=config_label,
        )

    def questions(self, conversation: Conversation) -> List[Question]:
        """Load questions for a single session."""
        session_id = conversation.id
        q_path = os.path.join(self.dataset_path, session_id, "questions.json")
        if not os.path.exists(q_path):
            return []

        with open(q_path) as f:
            data = json.load(f)

        # Handle both list-of-objects and single-object formats
        raw_questions = data if isinstance(data, list) else [data]

        return [
            Question(
                id=f"lme-{session_id}-{i}",
                benchmark="longmemeval",
                config_label=self.config_label,
                question_type=self._infer_type(q),
                text=q.get("question", q.get("text", "")),
                ground_truth=q.get("answer", q.get("ground_truth", "")),
                conversation_id=session_id,
            )
            for i, q in enumerate(raw_questions)
            if q.get("question", q.get("text", ""))
        ]

    # ── Internal ──────────────────────────────────────────────────────────────

    def _discover_sessions(self) -> List[str]:
        """Return sorted list of session IDs from the S/ directory."""
        sessions_dir = os.path.join(self.dataset_path, "S")
        if not os.path.isdir(sessions_dir):
            logger.error("LongMemEval S/ directory not found at %s", sessions_dir)
            return []
        entries = sorted(os.listdir(sessions_dir))
        return [e for e in entries if os.path.isdir(os.path.join(sessions_dir, e))]

    def _vault_name(self, session_id: str) -> str:
        return f"{self.vault_prefix}-{session_id}"

    def _normalize_messages(self, data) -> List[Dict]:
        """Normalize conversation data into a list of message dicts.

        LongMemEval conversations come in two formats:
        - flat list of {"role": ..., "content": ...}
        - nested {"messages": [...]}
        """
        if isinstance(data, list):
            return data
        if isinstance(data, dict):
            for key in ("messages", "conversation", "turns"):
                if key in data and isinstance(data[key], list):
                    return data[key]
            # Single-turn format
            if "role" in data:
                return [data]
        logger.warning("unrecognized conversation format for %s", data.get("id", "unknown"))
        return []

    def _infer_type(self, q: Dict) -> str:
        """Infer question type from the question object."""
        known = q.get("type", q.get("category", ""))
        if known:
            return known.lower().replace(" ", "-")
        # Fall back to heuristic from question text
        text = q.get("question", q.get("text", "")).lower()
        if any(w in text for w in ["before", "after", "how many", "how long", "when"]):
            return "temporal-reasoning"
        if any(w in text for w in ["what did", "what was", "who", "which"]):
            return "fact-retrieval"
        return "unknown"
