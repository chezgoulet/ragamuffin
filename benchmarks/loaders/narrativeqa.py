"""NarrativeQA dataset loader.

Loads the DeepMind NarrativeQA dataset from HuggingFace parquet files.
Stories are chunked and ingested into Ragamuffin vaults. Only public domain
(Gutenberg) stories are used. Film scripts are excluded due to copyright.

Dataset structure:
    - Each row = (document, question, answers)
    - Multiple rows can reference the same document
    - Document text can be very large (up to 800K chars / 200K+ tokens)

Memory strategy:
    - Stream parquet files one row-group at a time
    - Deduplicate documents by ID
    - Write intermediate JSONL to disk, then load into the Conversation format

Expected structure:
    <data_dir>/train-*.parquet
    <data_dir>/test-*.parquet
    <data_dir>/validation-*.parquet
"""

from __future__ import annotations

import json
import logging
import os
import re
import time
from pathlib import Path
from typing import Dict, List, Optional, Set, Tuple

from benchmarks.core.types import Conversation, IngestPlan, Question
from benchmarks.loaders.base import DatasetLoader

logger = logging.getLogger("ragamuffin.benchmark")

# ── Constants ───────────────────────────────────────────────────────────────────

# Approximate char-to-token ratio for English text
CHARS_PER_TOKEN = 4

# Target chunk size in tokens (conservative for Ragamuffin)
TARGET_CHUNK_TOKENS = 4000
TARGET_CHUNK_CHARS = TARGET_CHUNK_TOKENS * CHARS_PER_TOKEN

# Maximum document token count to ingest (skip novels longer than this)
MAX_DOC_TOKENS = 150_000
MAX_DOC_CHARS = MAX_DOC_TOKENS * CHARS_PER_TOKEN

# Source of the HF dataset
NARRATIVEQA_HF_PATH = "deepmind/narrativeqa"

# Valid document kinds (only public domain)
VALID_KINDS = {"gutenberg"}

# Number of parquet shards per split
SHARDS_PER_SPLIT = 24  # train


class NarrativeQALoader(DatasetLoader):
    """Loads the NarrativeQA dataset from local parquet files.

    The dataset must be downloaded first via download_datasets.sh or manually
    into <benchmarks/data>/narrativeqa/.

    Design:
      - Each story is a "conversation" (single long message)
      - Stories are chunked into ~4K token segments for ingest
      - All stories share one vault, with story-ID tags for filtering
      - Questions are grouped by story; only Gutenberg (public domain) used
    """

    def __init__(
        self,
        dataset_path: str,
        vault_prefix: str = "nqa",
        config_label: str = "D",
        max_stories: int = 0,  # 0 = all
        chunk_chars: int = TARGET_CHUNK_CHARS,
        max_doc_chars: int = MAX_DOC_CHARS,
        rebuild_cache: bool = False,
    ):
        self.dataset_path = dataset_path
        self.vault_prefix = vault_prefix
        self.config_label = config_label
        self.max_stories = max_stories
        self.chunk_chars = chunk_chars
        self.max_doc_chars = max_doc_chars
        self.rebuild_cache = rebuild_cache
        self._loaded_questions: Dict[str, Question] = {}
        self._doc_questions: Dict[str, List[Question]] = {}  # doc_id -> [Question]
        self._story_count = 0

    def name(self) -> str:
        return "narrativeqa"

    def load(self) -> List[Conversation]:
        """Load all Gutenberg stories as conversations.

        Returns list of Conversation objects, one per story.
        Questions are deduplicated and grouped by document.
        """
        cache_file = os.path.join(self.dataset_path, "_stories_cache.jsonl")
        stories = []

        # Try loading from cache first
        if not self.rebuild_cache and os.path.exists(cache_file):
            stories = self._load_cache(cache_file)

        if not stories:
            # Need to rebuild
            stories = self._build_stories()
            self._save_cache(cache_file, stories)

        # Build question index
        self._build_question_index(stories)

        # Create conversations
        conversations = self._stories_to_conversations(stories)

        self._story_count = len(conversations)
        logger.info(
            "loaded %d stories, %d unique questions (vault=%s-nqa-v1)",
            len(conversations),
            len(self._loaded_questions),
            self.vault_prefix,
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
        """Return questions for a specific story conversation.

        Uses conversation.id (story doc_id) to look up questions.
        Only returns questions once (first story that matches).
        """
        # Extract doc_id from conversation source
        doc_id = conversation.source.replace("narrativeqa/", "").split("/")[0]
        qs = self._doc_questions.get(doc_id, [])
        return qs

    # ── Internal: Loading from parquet ─────────────────────────────────────────

    def _build_stories(self) -> List[Dict]:
        """Build story list from parquet files.

        Reads each parquet file row-group by row-group to stay within
        memory limits. Deduplicates by document ID.
        """
        import pyarrow.parquet as pq

        seen_docs: Dict[str, Dict] = {}
        split_dirs = ["train", "test", "validation"]
        file_count = 0

        for split in split_dirs:
            split_dir = os.path.join(self.dataset_path, split)
            if not os.path.isdir(split_dir):
                # Try flat files
                continue
            parquet_files = sorted(Path(split_dir).glob("*.parquet"))
            if not parquet_files:
                continue
            logger.info("Processing %d parquet files from %s/", len(parquet_files), split)
            for fpath in parquet_files:
                n = self._process_parquet_file(str(fpath), seen_docs)
                file_count += 1
                if self.max_stories and len(seen_docs) >= self.max_stories:
                    break
            if self.max_stories and len(seen_docs) >= self.max_stories:
                break

        if not seen_docs and file_count == 0:
            # Try flat files at dataset_path root
            parquet_files = sorted(Path(self.dataset_path).glob("*.parquet"))
            if parquet_files:
                logger.info("Processing %d flat parquet files", len(parquet_files))
                for fpath in parquet_files:
                    self._process_parquet_file(str(fpath), seen_docs)
                    if self.max_stories and len(seen_docs) >= self.max_stories:
                        break

        stories = list(seen_docs.values())
        logger.info(
            "Built %d stories from %d parquet files (%d skipped non-gutenberg)",
            len(stories),
            file_count,
            len(seen_docs) - sum(1 for s in seen_docs.values() if s["kind"] in VALID_KINDS),
        )
        return stories

    def _process_parquet_file(
        self, fpath: str, seen_docs: Dict[str, Dict]
    ) -> int:
        """Process one parquet file. Returns number of rows read."""
        import pyarrow.parquet as pq

        pf = pq.ParquetFile(fpath)
        rows_total = 0

        for rg_idx in range(pf.metadata.num_row_groups):
            table = pf.read_row_group(rg_idx)
            for i in range(len(table)):
                try:
                    doc_raw = table.column("document")[i].as_py()
                    q_raw = table.column("question")[i].as_py()
                    a_raw = table.column("answers")[i].as_py()
                except Exception:
                    continue

                if not isinstance(doc_raw, dict) or not isinstance(q_raw, dict):
                    continue

                doc_id = str(doc_raw.get("id", ""))
                if not doc_id:
                    continue

                kind = str(doc_raw.get("kind", ""))
                rows_total += 1

                # Filter: only Gutenberg
                if kind not in VALID_KINDS:
                    continue

                # New document?
                if doc_id not in seen_docs:
                    text = doc_raw.get("text") or ""
                    if len(text) > self.max_doc_chars:
                        logger.debug(
                            "Skipping doc %s: %d chars exceeds limit",
                            doc_id, len(text),
                        )
                        continue

                    story = {
                        "id": doc_id,
                        "kind": kind,
                        "text": text,
                        "word_count": doc_raw.get("word_count", 0),
                        "summary": "",
                    }
                    summary_raw = doc_raw.get("summary")
                    if isinstance(summary_raw, dict):
                        story["summary"] = summary_raw.get("text", "") or ""
                    seen_docs[doc_id] = story

                # Attach question
                q_text = q_raw.get("text", "") or ""
                answers_raw = a_raw or []
                answer_texts = [
                    a.get("text", "") for a in answers_raw if isinstance(a, dict)
                ]
                if q_text and answer_texts:
                    seen_docs[doc_id].setdefault("questions", []).append({
                        "text": q_text,
                        "answers": answer_texts,
                    })

                if self.max_stories and len(seen_docs) >= self.max_stories:
                    break

            if self.max_stories and len(seen_docs) >= self.max_stories:
                break

        return rows_total

    def _build_question_index(self, stories: List[Dict]):
        """Build question lookup from stories."""
        seen_qids: Set[str] = set()
        self._doc_questions = {}
        qidx = 0

        for story in stories:
            doc_id = story["id"]
            story_questions = []
            for q in story.get("questions", []):
                q_text = q["text"]
                # Create a deterministic question ID
                qid = f"nqa-{doc_id[:8]}-{qidx:04d}"
                if qid in seen_qids:
                    continue
                seen_qids.add(qid)
                answer_text = q["answers"][0] if q["answers"] else ""

                question_obj = Question(
                    id=qid,
                    benchmark="narrativeqa",
                    config_label=self.config_label,
                    question_type=self._infer_type(q_text),
                    text=q_text,
                    ground_truth=answer_text,
                    conversation_id=doc_id,
                )
                story_questions.append(question_obj)
                self._loaded_questions[qid] = question_obj
                qidx += 1

            self._doc_questions[doc_id] = story_questions

    def _stories_to_conversations(self, stories: List[Dict]) -> List[Conversation]:
        """Convert story dicts to Conversation objects.

        Each story is a single conversation with chunked messages.
        """
        shared_vault = f"{self.vault_prefix}-nqa-v1"
        conversations = []

        for story in stories:
            doc_id = story["id"]
            text = story["text"]
            summary = story.get("summary", "")

            chunks = self._chunk_text(text)

            # Create messages: first message = summary + chunk text
            messages = []
            for ci, chunk in enumerate(chunks):
                if ci == 0 and summary:
                    content = f"[Story Summary]\n{summary}\n\n[Story Text - Part 1]\n{chunk}"
                else:
                    content = f"[Story Text - Part {ci+1}]\n{chunk}"

                messages.append({
                    "role": "system" if ci == 0 else "assistant",
                    "content": content,
                })

            conversations.append(
                Conversation(
                    id=doc_id,
                    messages=messages,
                    vault=shared_vault,
                    source=f"narrativeqa/{doc_id}",
                )
            )

        return conversations

    @staticmethod
    def _chunk_text(text: str, chunk_chars: int = TARGET_CHUNK_CHARS) -> List[str]:
        """Split text into chunks at paragraph boundaries.

        Tries to split on double newlines first, then single newlines,
        then falls back to character count.
        """
        if len(text) <= chunk_chars:
            return [text]

        chunks = []
        while text:
            if len(text) <= chunk_chars:
                chunks.append(text)
                break

            # Try to find a paragraph break within the chunk window
            chunk = text[:chunk_chars]

            # Walk backwards to find a paragraph break
            split_at = -1
            for sep in ["\n\n", "\n", ". ", "! ", "? "]:
                pos = chunk.rfind(sep)
                if pos > chunk_chars // 2:  # At least halfway through
                    split_at = pos + len(sep)
                    break

            if split_at <= 0:
                split_at = chunk_chars  # hard cut

            chunks.append(text[:split_at])
            text = text[split_at:]

        return chunks

    @staticmethod
    def _infer_type(q_text: str) -> str:
        """Infer question type from the question text."""
        ql = q_text.lower()
        if any(w in ql for w in ["who", "whom", "whose"]):
            return "character"
        if any(w in ql for w in ["where", "which place", "what location"]):
            return "location"
        if any(w in ql for w in ["when", "how long", "what year", "what time", "how old"]):
            return "temporal"
        if any(w in ql for w in ["why", "what reason", "what cause", "what purpose"]):
            return "reasoning"
        if any(w in ql for w in ["how many", "how much", "what number", "what is the name"]):
            return "factual"
        if any(w in ql for w in ["what", "which", "describe", "explain"]):
            return "description"
        if any(w in ql for w in ["did", "does", "was", "were", "had", "has", "have",
                                  "is", "are", "will"]):
            return "binary"
        return "unknown"

    # ── Caching ────────────────────────────────────────────────────────────────

    def _save_cache(self, path: str, stories: List[Dict]):
        """Save stories to JSONL cache for faster reload."""
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            for story in stories:
                # Remove text from cache to keep it small — only cache metadata
                # Text will be reloaded on next load
                cache_entry = {k: v for k, v in story.items() if k != "text"}
                cache_entry["_text_len"] = len(story.get("text", ""))
                f.write(json.dumps(cache_entry) + "\n")
        logger.info("Saved %d story metadata to cache: %s", len(stories), path)

    def _load_cache(self, path: str) -> List[Dict]:
        """Load story metadata from JSONL cache.

        Returns empty list if cache is stale or missing.
        """
        if not os.path.exists(path):
            return []
        stories = []
        try:
            with open(path) as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    story = json.loads(line)
                    # Text needs to be reloaded from parquet if we want full text
                    story["text"] = ""
                    stories.append(story)
            logger.info("Loaded %d stories from cache", len(stories))
        except (json.JSONDecodeError, OSError) as e:
            logger.warning("Cache read failed: %s", e)
            return []
        return stories
