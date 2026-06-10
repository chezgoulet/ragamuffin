"""LoCoMo dataset loader.

Loads conversations from the Backboard-Locomo-Benchmark dataset JSON.

Expected dataset file:
  <dataset_path>/locomo_dataset.json  — list of conversation objects
"""

from __future__ import annotations

import json
import logging
import os
from typing import Dict, List, Optional

from benchmarks.core.types import Conversation, IngestPlan, Question
from benchmarks.loaders.base import DatasetLoader

logger = logging.getLogger("ragamuffin.benchmark")

CATEGORY_MAP = {
    1: "single-hop",
    2: "temporal-reasoning",
    3: "multi-hop",
    4: "open-domain",
}


class LoCoMoLoader(DatasetLoader):
    """Loads the LoCoMo dataset from a local JSON file.

    All sessions are ingested into a single shared vault.
    Each conversation's QA pairs become the benchmark questions.
    """

    def __init__(
        self,
        dataset_path: str,
        vault_prefix: str = "locomo",
        config_label: str = "D",
    ):
        self.dataset_path = dataset_path
        self.vault_prefix = vault_prefix
        self.config_label = config_label

    def name(self) -> str:
        return "locomo"

    def load(self) -> List[Conversation]:
        """Load all conversations from the LoCoMo dataset.

        Each conversation in locomo_dataset.json has multiple sessions
        spanning months of dialogue between two speakers.

        All conversations share vault="locomo-v1" so the retriever sees
        the full cross-session context.
        """
        dataset_file = os.path.join(self.dataset_path, "locomo_dataset.json")
        if not os.path.exists(dataset_file):
            logger.error("LoCoMo dataset not found at %s", dataset_file)
            return []

        with open(dataset_file) as f:
            raw = json.load(f)

        if not isinstance(raw, list):
            logger.error("expected list of conversations, got %s", type(raw).__name__)
            return []

        shared_vault = f"{self.vault_prefix}-v1"
        self._loaded_questions: Dict[str, Question] = {}
        conversations: List[Conversation] = []

        for entry in raw:
            sample_id = entry.get("sample_id", f"conv-{len(conversations)}")
            conv_data = entry.get("conversation", {})
            qa_list = entry.get("qa", [])

            # Build messages: flatten all session messages in chronological order
            messages = self._build_messages(conv_data)

            if not messages:
                logger.warning("no messages for %s, skipping", sample_id)
                continue

            conversations.append(
                Conversation(
                    id=sample_id,
                    messages=messages,
                    vault=shared_vault,
                    source=f"locomo/{sample_id}",
                )
            )

            # Collect questions deduped by sample_id + question text
            for q_idx, qa in enumerate(qa_list):
                qid = f"locomo-{sample_id}-{q_idx}"
                if qid in self._loaded_questions:
                    continue
                qtext = qa.get("question", "")
                if not qtext:
                    continue
                cat = qa.get("category", 0)
                self._loaded_questions[qid] = Question(
                    id=qid,
                    benchmark="locomo",
                    config_label=self.config_label,
                    question_type=CATEGORY_MAP.get(cat, f"category-{cat}"),
                    text=qtext,
                    ground_truth=str(qa.get("answer", "")),
                    conversation_id=sample_id,
                )

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
        """Return ALL unique questions for the first conversation, none for rest."""
        if not hasattr(self, "_returned_questions"):
            self._returned_questions = False
        if self._returned_questions:
            return []
        self._returned_questions = True
        return list(self._loaded_questions.values())

    # ── Internal ──────────────────────────────────────────────────────────────

    def _build_messages(self, conv_data: Dict) -> List[Dict]:
        """Extract all session messages in chronological order.

        Session keys are session_1, session_2, ... session_N.
        Each session has {"speaker": str, "text": str, ...}.
        """
        messages: List[Dict] = []
        i = 1
        while True:
            session_key = f"session_{i}"
            session_msgs = conv_data.get(session_key)
            if session_msgs is None:
                break
            if not isinstance(session_msgs, list):
                i += 1
                continue
            for msg in session_msgs:
                speaker = msg.get("speaker", "user")
                text = msg.get("text", "")
                if not text:
                    continue
                # Map speaker names to roles
                role = "user" if speaker in ("user", "Human") else "assistant"
                messages.append({
                    "role": role,
                    "content": text,
                    "speaker": speaker,
                })
            i += 1

        return messages
