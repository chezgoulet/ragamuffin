"""NarrativeQA dataset loader.

Loads DeepMind's NarrativeQA long-form reading-comprehension benchmark
(https://huggingface.co/datasets/deepmind/narrativeqa) and adapts it to the
Ragamuffin benchmark harness.

NarrativeQA ships as HuggingFace parquet files. Each row is one question +
its source document:

    document : {id, kind, url, file_size, word_count, start, end,
                summary: {text, tokens, url, title}, text}
    question : {text, tokens}
    answers  : [{text, tokens}, ...]

``document.text`` holds the FULL story text (novel, play, or film script),
``document.kind`` is one of ``gutenberg`` (public-domain books) or
``movie`` (film scripts — under a separate licence). The issue (#690) asks us
to skip copyrighted film scripts, so the loader defaults to ``gutenberg`` only.

Ingestion strategy
------------------
Each story becomes its own Conversation and — by default — its own vault
(``nqa-<kind>-<doc_id[:8]>``). Novels are independent works; isolating them
avoids stuffing 100k+ token documents into one shared vault and keeps each
``/ask`` focused on a single book. A shared-vault mode is available via
``--nqa-shared-vault`` for ablations.

The runner ingests each story as a single blob (mirroring the LongMemEval /
LoCoMo loaders); Ragamuffin's indexer chunks internally by token window.
"""

from __future__ import annotations

import json
import logging
import os
import shutil
import urllib.request
from typing import Dict, List, Optional

from benchmarks.core.types import Conversation, IngestPlan, Question
from benchmarks.loaders.base import DatasetLoader

logger = logging.getLogger("ragamuffin.benchmark")

# HuggingFace dataset-viewer parquet listing endpoint.
HF_PARQUET_API = "https://huggingface.co/api/datasets/deepmind/narrativeqa/parquet/default"

# Heuristic word→token ratio for the token-budget filter.
WORDS_PER_TOKEN = 1.33

# Default ceiling on a story's word count. ~100k words ≈ 133k tokens, which
# keeps a single ingest within Ragamuffin's practical limits and /ask context.
DEFAULT_MAX_WORDS = 100_000

# Question-type heuristic buckets.
_TYPE_WHO = ("who", "whose", "which character", "what is the name of the")
_TYPE_WHERE = ("where", "in what place", "what city", "what country", "what town")
_TYPE_WHEN = ("when", "what year", "what time", "what date", "how long")
_TYPE_WHY = ("why", "what caused", "what reason")
_TYPE_HOW = ("how", "by what means", "in what way")


class NarrativeQALoader(DatasetLoader):
    """Loads NarrativeQA from local parquet files (downloading them if absent)."""

    def __init__(
        self,
        dataset_path: str,
        vault_prefix: str = "nqa",
        config_label: str = "D",
        kinds: Optional[List[str]] = None,
        splits: Optional[List[str]] = None,
        max_words: int = DEFAULT_MAX_WORDS,
        max_stories: int = 0,
        max_questions_per_story: int = 0,
        per_story_vaults: bool = True,
        auto_download: bool = True,
    ):
        self.dataset_path = dataset_path
        self.vault_prefix = vault_prefix
        self.config_label = config_label
        # Default to public-domain Gutenberg texts (skip copyrighted film scripts).
        self.kinds = [k.lower() for k in (kinds or ["gutenberg"])]
        self.splits = splits or ["train", "validation", "test"]
        self.max_words = max_words
        self.max_stories = max_stories
        self.max_questions_per_story = max_questions_per_story
        self.per_story_vaults = per_story_vaults
        self.auto_download = auto_download

        self._loaded_questions: Dict[str, Question] = {}
        self._returned_questions = False
        self._stats: Dict[str, int] = {}
        self._q_counter = 0  # global question index across all rows
        self._story_q_count: Dict[str, int] = {}  # per-story question cap tracking

    # ── DatasetLoader interface ────────────────────────────────────────────

    def name(self) -> str:
        return "narrativeqa"

    def load(self) -> List[Conversation]:
        """Discover parquet files, parse, filter, and build Conversations."""
        parquet_dir = self._ensure_parquet_dir()
        files = self._discover_parquet_files(parquet_dir)
        if not files:
            if self.auto_download:
                logger.warning("no parquet files found — attempt download")
                self.download()
                files = self._discover_parquet_files(parquet_dir)
            if not files:
                logger.error("no NarrativeQA parquet files at %s", parquet_dir)
                return []

        self._loaded_questions = {}
        self._q_counter = 0
        self._story_q_count = {}
        conversations: List[Conversation] = []
        skipped_kind = skipped_size = 0
        stories_loaded = 0

        for pf in files:
            rows = self._read_parquet(pf)
            for row in rows:
                doc = row.get("document") or {}
                kind = (doc.get("kind") or "").lower()
                if kind and self.kinds and kind not in self.kinds:
                    skipped_kind += 1
                    continue
                word_count = int(doc.get("word_count") or 0)
                if self.max_words and word_count > self.max_words:
                    skipped_size += 1
                    continue
                text = (doc.get("text") or "").strip()
                if not text:
                    continue

                doc_id = doc.get("id") or f"doc-{len(conversations)}"
                vault = self._vault_for(doc_id, kind)
                conv = Conversation(
                    id=doc_id,
                    messages=[{"role": "user", "content": text}],
                    vault=vault,
                    source=f"narrativeqa/{kind}/{doc_id}",
                )
                conversations.append(conv)

                for q in self._questions_for(row, doc_id):
                    if q.id not in self._loaded_questions:
                        self._loaded_questions[q.id] = q

                stories_loaded += 1
                if self.max_stories and stories_loaded >= self.max_stories:
                    break
            if self.max_stories and stories_loaded >= self.max_stories:
                break

        self._stats = {
            "stories_loaded": stories_loaded,
            "questions": len(self._loaded_questions),
            "skipped_kind": skipped_kind,
            "skipped_size": skipped_size,
            "kinds": len(self.kinds),
        }
        logger.info(
            "narrativeqa: loaded %d stories, %d questions (skipped kind=%d, size=%d, kinds=%s)",
            stories_loaded,
            len(self._loaded_questions),
            skipped_kind,
            skipped_size,
            self.kinds,
        )
        return conversations

    def ingest_strategy(
        self, conversation: Conversation, config_label: str
    ) -> IngestPlan:
        return IngestPlan(
            vault=conversation.vault,
            conversations=[conversation],
            auto_extract=(config_label in ("B", "D")),
            config_label=config_label,
        )

    def questions(self, conversation: Conversation) -> List[Question]:
        """Return all unique questions once, empty thereafter (matches other loaders)."""
        if self._returned_questions:
            return []
        self._returned_questions = True
        return list(self._loaded_questions.values())

    # ── Public helpers ─────────────────────────────────────────────────────

    def download(self) -> None:
        """Download NarrativeQA parquet files via the HF dataset-viewer API.

        Writes files to ``<dataset_path>/parquet/default/<split>/<i>.parquet``.
        """
        out_root = os.path.join(self.dataset_path, "parquet", "default")
        os.makedirs(out_root, exist_ok=True)
        try:
            import pyarrow  # noqa: F401  (ensure available)
        except ImportError:
            raise RuntimeError(
                "pyarrow is required to read NarrativeQA parquet files. "
                "Install with: pip install pyarrow"
            )

        listing = self._fetch_json(HF_PARQUET_API)
        if not isinstance(listing, dict):
            raise RuntimeError(f"unexpected parquet listing: {type(listing)}")
        for split in self.splits:
            urls = listing.get(split, [])
            if not urls:
                logger.warning("no parquet urls for split %s", split)
                continue
            split_dir = os.path.join(out_root, split)
            os.makedirs(split_dir, exist_ok=True)
            for i, url in enumerate(urls):
                dest = os.path.join(split_dir, f"{i}.parquet")
                if os.path.exists(dest):
                    continue
                logger.info("downloading %s/%d.parquet", split, i)
                self._download_file(url, dest)
        logger.info("NarrativeQA parquet downloaded to %s", out_root)

    def stats(self) -> Dict[str, int]:
        return dict(self._stats)

    # ── Internal ───────────────────────────────────────────────────────────

    def _ensure_parquet_dir(self) -> str:
        d = os.path.join(self.dataset_path, "parquet", "default")
        os.makedirs(d, exist_ok=True)
        return d

    def _discover_parquet_files(self, parquet_dir: str) -> List[str]:
        files: List[str] = []
        for split in self.splits:
            split_dir = os.path.join(parquet_dir, split)
            if not os.path.isdir(split_dir):
                continue
            for fn in sorted(os.listdir(split_dir)):
                if fn.endswith(".parquet"):
                    files.append(os.path.join(split_dir, fn))
        return files

    def _read_parquet(self, path: str) -> List[Dict]:
        try:
            import pyarrow.parquet as pq
        except ImportError:
            raise RuntimeError("pyarrow is required to read NarrativeQA parquet files")
        try:
            return pq.read_table(path).to_pylist()
        except Exception as e:  # pragma: no cover - defensive
            logger.warning("failed to read %s: %s", path, e)
            return []

    def _vault_for(self, doc_id: str, kind: str) -> str:
        if not self.per_story_vaults:
            return f"{self.vault_prefix}-shared"
        safe = "".join(c for c in doc_id[:8] if c.isalnum() or c in "-_")
        return f"{self.vault_prefix}-{kind or 'doc'}-{safe}"

    def _questions_for(self, row: Dict, doc_id: str) -> List[Question]:
        """Build the Question(s) for one parquet row.

        NarrativeQA stores ONE question per row; ``row['answers']`` are the
        reference answers (typically 2) for that question. We emit a single
        Question whose ground truth is the reference answers joined with ``|``
        (standard NarrativeQA scoring accepts any reference answer).
        """
        q = row.get("question") or {}
        qtext = (q.get("text") or "").strip()
        if not qtext:
            return []

        if self.max_questions_per_story:
            used = self._story_q_count.get(doc_id, 0)
            if used >= self.max_questions_per_story:
                return []
            self._story_q_count[doc_id] = used + 1

        answers = row.get("answers") or []
        gt_parts = [a.get("text", "").strip() for a in answers if a.get("text")]
        ground_truth = " | ".join(gt_parts) if gt_parts else ""

        self._q_counter += 1
        return [
            Question(
                id=f"nqa-{doc_id}-{self._q_counter:05d}",
                benchmark="narrativeqa",
                config_label=self.config_label,
                question_type=self._classify(qtext),
                text=qtext,
                ground_truth=ground_truth,
                conversation_id=doc_id,
            )
        ]

    @staticmethod
    def _classify(text: str) -> str:
        low = text.lower()
        if any(tok in low for tok in _TYPE_WHO):
            return "character"
        if any(tok in low for tok in _TYPE_WHERE):
            return "setting"
        if any(tok in low for tok in _TYPE_WHEN):
            return "temporal"
        if any(tok in low for tok in _TYPE_WHY):
            return "cause"
        if any(tok in low for tok in _TYPE_HOW):
            return "method"
        if low.startswith("what") or "what happened" in low:
            return "plot"
        return "comprehension"

    @staticmethod
    def _fetch_json(url: str) -> object:
        req = urllib.request.Request(url, headers={"User-Agent": "RagamuffinBenchmark/0.9"})
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.loads(resp.read().decode())

    @staticmethod
    def _download_file(url: str, dest: str) -> None:
        req = urllib.request.Request(url, headers={"User-Agent": "RagamuffinBenchmark/0.9"})
        with urllib.request.urlopen(req, timeout=120) as resp, open(dest, "wb") as fh:
            shutil.copyfileobj(resp, fh)
