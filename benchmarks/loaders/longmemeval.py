"""LongMemEval dataset loader.

Loads conversations from the LongMemEval S/ directory structure.
All sessions share a single vault so the retriever sees the full
cross-session context for each question.

Expected structure:
    <dataset_path>/S/<session_id>/conversation.json
    <dataset_path>/S/<session_id>/questions.json
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
    """Loads the LongMemEval dataset from a local S/ directory.

    All conversations are ingested into vault="lme-v1" for shared context.
    Questions are deduplicated by question_id across sessions.
    Only the first conversation processed returns questions.
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

    def name(self) -> str:
        return "longmemeval"

    def load(self) -> List[Conversation]:
        """Discover all sessions and load their conversations into one vault."""
        self._loaded_questions: Dict[str, Question] = {}
        sessions = self._discover_sessions()
        if not sessions:
            return []

        # Shared vault across all configs — config differences are in ask_mode
        shared_vault = f"{self.vault_prefix}-v1"
        conversations = []

        for session_id in sessions:
            conv_path = os.path.join(self.dataset_path, "S", session_id, "conversation.json")
            if not os.path.exists(conv_path):
                logger.warning("missing conversation.json for session %s", session_id)
                continue
            try:
                with open(conv_path) as f:
                    data = json.load(f)
            except (json.JSONDecodeError, OSError) as e:
                logger.warning("skipping session %s: %s", session_id, e)
                continue

            messages = self._normalize_messages(data)
            conversations.append(
                Conversation(
                    id=session_id,
                    messages=messages,
                    vault=shared_vault,
                    source=f"longmemeval/{session_id}",
                )
            )

            # Collect questions deduped by question_id
            qs = self._load_session_questions(session_id)
            for q in qs:
                if q.id not in self._loaded_questions:
                    self._loaded_questions[q.id] = q

        logger.info(
            "loaded %d conversations, %d unique questions (vault=%s)",
            len(conversations),
            len(self._loaded_questions),
            shared_vault,
        )
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
        """Return all unique questions for the first conversation, empty for rest."""
        if not hasattr(self, "_returned_questions"):
            self._returned_questions = False
        if self._returned_questions:
            return []
        self._returned_questions = True
        return list(self._loaded_questions.values())

    # ── Internal ──────────────────────────────────────────────────────────────

    def _discover_sessions(self) -> List[str]:
        sessions_dir = os.path.join(self.dataset_path, "S")
        if not os.path.isdir(sessions_dir):
            logger.error("LongMemEval S/ directory not found at %s", sessions_dir)
            return []
        entries = sorted(os.listdir(sessions_dir))
        return [e for e in entries if os.path.isdir(os.path.join(sessions_dir, e))]

    def _load_session_questions(self, session_id: str) -> List[Question]:
        q_path = os.path.join(self.dataset_path, "S", session_id, "questions.json")
        if not os.path.exists(q_path):
            return []
        with open(q_path) as f:
            raw = json.load(f)
        raw_questions = raw if isinstance(raw, list) else [raw]
        out = []
        seen_ids = set()
        for q in raw_questions:
            qid = q.get("question_id", "")
            if not qid or qid in seen_ids:
                continue
            seen_ids.add(qid)
            text = q.get("question", q.get("text", ""))
            if not text:
                continue
            out.append(
                Question(
                    id=f"lme-{qid}",
                    benchmark="longmemeval",
                    config_label=self.config_label,
                    question_type=self._infer_type(q),
                    text=text,
                    ground_truth=str(q.get("answer", q.get("ground_truth", ""))),
                    conversation_id=session_id,
                )
            )
        return out

    @staticmethod
    def _normalize_messages(data) -> List[Dict]:
        if isinstance(data, list):
            return data
        if isinstance(data, dict):
            for key in ("messages", "conversation", "turns"):
                if key in data and isinstance(data[key], list):
                    return data[key]
            if "role" in data:
                return [data]
        logger.warning("unrecognized conversation format")
        return []

    @staticmethod
    def _infer_type(q: Dict) -> str:
        known = q.get("type", q.get("category", ""))
        if known:
            return str(known).lower().replace(" ", "-")
        text = q.get("question", q.get("text", "")).lower()
        if any(w in text for w in ["before", "after", "how many", "how long", "when"]):
            return "temporal-reasoning"
        if any(w in text for w in ["what did", "what was", "who", "which"]):
            return "fact-retrieval"
        return "unknown"
