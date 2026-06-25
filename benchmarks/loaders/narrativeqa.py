"""NarrativeQA dataset loader for the Ragamuffin benchmark gauntlet.

Loads pre-parsed stories and questions from the local data directory
(output of ingest_narrativeqa.py). Stories are loaded as conversations
with story text in chunks, each tagged with story metadata.

Usage (via run.py):
    python3 benchmarks/run.py --datasets narrativeqa

Direct usage:
    from benchmarks.loaders.narrativeqa import NarrativeQALoader
    loader = NarrativeQALoader()
    conversations = loader.load()
    questions = loader.questions(conversations[0])
"""

from __future__ import annotations

import json
import logging
import os
from typing import Dict, List, Optional

from benchmarks.core.types import Conversation, IngestPlan, Question
from benchmarks.loaders.base import DatasetLoader

logger = logging.getLogger("ragamuffin.benchmark")

# Default paths relative to benchmarks/
BENCH_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_DATA_DIR = os.path.join(BENCH_DIR, "data", "narrativeqa")
DEFAULT_STORIES_DIR = os.path.join(DEFAULT_DATA_DIR, "stories")
DEFAULT_QUESTIONS_FILE = os.path.join(DEFAULT_DATA_DIR, "questions.json")


class NarrativeQALoader(DatasetLoader):
    """Loads pre-parsed NarrativeQA stories and questions.

    Uses the output of ingest_narrativeqa.py — expects stories/ and
    questions.json in the data directory.

    All stories share a single vault so the retriever can find relevant
    content across the full story corpus. Each story is loaded as one
    "conversation" with the story text and summary as messages.
    """

    def __init__(
        self,
        data_dir: str = DEFAULT_DATA_DIR,
        vault: str = "narrativeqa",
        config_label: str = "D",
        max_stories: Optional[int] = None,
    ):
        self.data_dir = data_dir
        self.stories_dir = os.path.join(data_dir, "stories")
        self.questions_file = os.path.join(data_dir, "questions.json")
        self.vault = vault
        self.config_label = config_label
        self.max_stories = max_stories

    def name(self) -> str:
        return "narrativeqa"

    def load(self) -> List[Conversation]:
        """Load all stories from the parsed data directory.

        Each story becomes one Conversation with:
        - A "system" message containing the Wikipedia summary
        - A "user" message containing the story text (full, as a single blob)

        All conversations share a single vault.
        """
        if not os.path.isdir(self.stories_dir):
            logger.error(
                "NarrativeQA stories directory not found at %s. "
                "Run benchmarks/ingest_narrativeqa.py first.",
                self.stories_dir,
            )
            return []

        story_files = sorted(os.listdir(self.stories_dir))
        if not story_files:
            logger.warning("No story files found in %s", self.stories_dir)
            return []

        if self.max_stories:
            story_files = story_files[: self.max_stories]

        conversations: List[Conversation] = []

        for sf in story_files:
            if not sf.endswith(".json"):
                continue

            sid = sf.replace(".json", "")
            sf_path = os.path.join(self.stories_dir, sf)

            try:
                with open(sf_path) as f:
                    story = json.load(f)
            except (json.JSONDecodeError, OSError) as e:
                logger.warning("Skipping story %s: %s", sid, e)
                continue

            # Build messages: summary + story text
            messages: List[Dict] = []

            summary_text = story.get("summary_text", "")
            if summary_text:
                messages.append({
                    "role": "system",
                    "content": f"[Wikipedia Summary — {story.get('summary_title', sid)}]\n{summary_text}",
                })

            story_text = story.get("text", "")
            if story_text:
                word_count = story.get("word_count", 0)
                messages.append({
                    "role": "user",
                    "content": f"[Story: {sid} ({word_count} words)]\n{story_text}",
                })

            if not messages:
                logger.warning("Skipping story %s: no content", sid)
                continue

            conversations.append(
                Conversation(
                    id=sid,
                    messages=messages,
                    vault=self.vault,
                    source=f"narrativeqa/{sid}",
                )
            )

        logger.info(
            "Loaded %d stories into vault '%s'",
            len(conversations),
            self.vault,
        )
        return conversations

    def ingest_strategy(
        self,
        conversation: Conversation,
        config_label: str,
    ) -> IngestPlan:
        """Return the ingest plan for a single story/conversation."""
        return IngestPlan(
            vault=conversation.vault,
            conversations=[conversation],
            auto_extract=(config_label in ("B", "D")),
            config_label=config_label,
        )

    def questions(self, conversation: Conversation) -> List[Question]:
        """Return all questions for a story, loaded from questions.json.

        Questions are loaded once and cached. Uses story_id from the
        conversation to filter relevant questions. Returns empty list
        for conversations that don't have questions (shouldn't happen
        in normal use).
        """
        if not hasattr(self, "_questions_cache"):
            self._questions_cache = self._load_all_questions()

        story_id = conversation.id
        story_questions = self._questions_cache.get(story_id, [])
        return story_questions

    def _load_all_questions(self) -> Dict[str, List[Question]]:
        """Load all questions from questions.json, indexed by story_id."""
        if not os.path.exists(self.questions_file):
            logger.warning("Questions file not found: %s", self.questions_file)
            return {}

        questions_by_story: Dict[str, List[Question]] = {}
        try:
            with open(self.questions_file) as f:
                raw = json.load(f)
        except (json.JSONDecodeError, OSError) as e:
            logger.warning("Failed to load questions: %s", e)
            return {}

        for idx, q_entry in enumerate(raw):
            story_id = q_entry.get("story_id", "")
            if not story_id:
                continue

            q_text = q_entry.get("text", "")
            answers = q_entry.get("answers", [])
            if not q_text or not answers:
                continue

            # Use first answer as ground truth (primary answer per NarrativeQA)
            ground_truth = answers[0]

            qid = f"nqa-{story_id[:12]}-{idx:04d}"

            if story_id not in questions_by_story:
                questions_by_story[story_id] = []

            questions_by_story[story_id].append(
                Question(
                    id=qid,
                    benchmark="narrativeqa",
                    config_label=self.config_label,
                    question_type="reading-comprehension",
                    text=q_text,
                    ground_truth=ground_truth,
                    conversation_id=story_id,
                )
            )

        total = sum(len(qs) for qs in questions_by_story.values())
        logger.info(
            "Loaded %d questions across %d stories",
            total,
            len(questions_by_story),
        )
        return questions_by_story

    def ingest_tags(self, conversation: Conversation, config_label: str) -> Dict[str, str]:
        """Return base tags for ingested NarrativeQA content."""
        return {
            "benchmark": self.name(),
            "story": conversation.id,
            "config": config_label,
        }
