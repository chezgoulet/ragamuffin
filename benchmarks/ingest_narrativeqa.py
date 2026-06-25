#!/usr/bin/env python3
"""
NarrativeQA Dataset — Fetch via HF Datasets Server, parse, and ingest.

Downloads via HuggingFace's datasets-server HTTP API (no pyarrow needed).
Processes rows on-the-fly to minimize memory. Filters to Gutenberg-only texts
with full text, chunks stories, and optionally ingests into Ragamuffin.

Usage:
    python3 benchmarks/ingest_narrativeqa.py                     # Download + parse only
    python3 benchmarks/ingest_narrativeqa.py --ingest            # Download + parse + ingest
    python3 benchmarks/ingest_narrativeqa.py --max-stories 10    # Quick smoke test
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.request
import urllib.error
from typing import Any, Dict, List, Optional, Tuple

# ── Constants ───────────────────────────────────────────────────────────────────

BENCH_DIR = os.path.dirname(os.path.abspath(__file__))
DATA_DIR = os.path.join(BENCH_DIR, "data", "narrativeqa")
STORIES_DIR = os.path.join(DATA_DIR, "stories")
QUESTIONS_FILE = os.path.join(DATA_DIR, "questions.json")
METADATA_FILE = os.path.join(DATA_DIR, "metadata.json")

# HF Datasets Server API
DATASETS_SERVER = "https://datasets-server.huggingface.co"
DATASET = "deepmind/narrativeqa"
CONFIG = "default"
SPLITS = ["train", "test", "validation"]
PAGE_SIZE = 20  # rows per API call (stories are 800KB+, keeping per-call under 16MB)

# Filtering
MAX_STORY_WORDS = 500_000  # Skip stories above this
CHUNK_WORDS = 4_000  # Target words per chunk
CHUNK_OVERLAP_WORDS = 200  # Overlap between chunks

# Ingest defaults
RAGAMUFFIN_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
INGEST_DELAY = float(os.environ.get("RAGAMUFFIN_INGEST_DELAY", "0.1"))


# ── Logging ─────────────────────────────────────────────────────────────────────


def log(msg: str):
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


# ── Core: fetch + parse on-the-fly ──────────────────────────────────────────────


def _extract_story_qa(row: Dict) -> Tuple[Optional[Dict], Optional[Dict]]:
    """Extract story + QA pair from one row.

    Returns (story_dict_or_None, qa_dict_or_None).
    Returns (None, None) if row should be filtered out.
    """
    doc = row.get("document", {})
    if isinstance(doc, str):
        try:
            doc = json.loads(doc)
        except json.JSONDecodeError:
            return None, None

    kind = doc.get("kind", "")
    story_text = doc.get("text", "")

    # Filter: only Gutenberg with full text
    if kind != "gutenberg" or not story_text or len(story_text.strip()) < 100:
        return None, None

    word_count = doc.get("word_count", 0)
    if isinstance(word_count, str):
        try:
            word_count = int(word_count)
        except ValueError:
            word_count = len(story_text.split())
    if word_count > MAX_STORY_WORDS:
        return None, None

    story_id = doc.get("id", "")
    summary = doc.get("summary", {})
    if isinstance(summary, str):
        try:
            summary = json.loads(summary)
        except json.JSONDecodeError:
            summary = {}

    story = {
        "id": story_id,
        "kind": kind,
        "url": doc.get("url", ""),
        "word_count": word_count,
        "text": story_text,
        "summary_text": summary.get("text", "") if isinstance(summary, dict) else "",
        "summary_url": summary.get("url", "") if isinstance(summary, dict) else "",
        "summary_title": summary.get("title", "") if isinstance(summary, dict) else "",
        "start": doc.get("start", ""),
        "end": doc.get("end", ""),
    }

    # Extract question
    q_raw = row.get("question", {})
    if isinstance(q_raw, str):
        try:
            q_raw = json.loads(q_raw)
        except json.JSONDecodeError:
            q_raw = {}
    q_text = q_raw.get("text", "") if isinstance(q_raw, dict) else ""

    # Extract answers
    answers_raw = row.get("answers", [])
    if isinstance(answers_raw, str):
        try:
            answers_raw = json.loads(answers_raw)
        except json.JSONDecodeError:
            answers_raw = []
    answers = []
    if isinstance(answers_raw, list):
        for a in answers_raw:
            if isinstance(a, dict):
                answers.append(a.get("text", ""))
            elif isinstance(a, str):
                answers.append(a)

    if not q_text or not answers:
        return story, None  # story but no valid question

    qa = {
        "text": q_text,
        "answers": answers,
        "story_id": story_id,
    }
    return story, qa


def _save_story(story: Dict):
    """Save one story to disk. First occurrence wins (dedup across splits)."""
    sid = story["id"]
    path = os.path.join(STORIES_DIR, f"{sid}.json")
    if not os.path.exists(path):
        with open(path, "w") as f:
            json.dump(story, f, indent=2)


def _append_question(qa: Dict):
    """Append one QA pair to questions.jsonl (append-only, dedup by story+text)."""
    qa_path = os.path.join(DATA_DIR, "questions.jsonl")
    dedup_path = os.path.join(DATA_DIR, "_questions_seen.txt")

    sid = qa["story_id"]
    q_text = qa["text"]

    # Check dedup via a simple seen file (one line per story+text)
    key = f"{sid}||{q_text}\n"
    if not os.path.exists(dedup_path):
        with open(dedup_path, "w") as f:
            f.write(key)
        with open(qa_path, "a") as f:
            f.write(json.dumps(qa) + "\n")
        return

    with open(dedup_path, "r") as f:
        if any(line == key for line in f):
            return  # already seen

    with open(dedup_path, "a") as f:
        f.write(key)
    with open(qa_path, "a") as f:
        f.write(json.dumps(qa) + "\n")


def _page_request(url: str, retries: int = 10) -> Optional[Dict]:
    """Make a paginated request to HF datasets server with rate-limit backoff.

    Retries on 429 with exponential backoff (up to ~60s wait).
    Returns parsed JSON or None on permanent failure.
    """
    for attempt in range(retries):
        try:
            req = urllib.request.Request(url)
            req.add_header("User-Agent", "RagamuffinBenchmark/1.0")
            resp = urllib.request.urlopen(req, timeout=30)
            return json.loads(resp.read())
        except urllib.error.HTTPError as e:
            if e.code == 429:
                # Rate limited — exponential backoff with jitter
                wait = min(2 ** attempt + (time.time() % 1), 60)
                log(f"    ⏳ Rate limited (429). Waiting {wait:.1f}s (attempt {attempt+1}/{retries})...")
                time.sleep(wait)
                continue
            log(f"    ✗ HTTP {e.code} at {url[-80:]}: {e.read().decode()[:150]}")
            return None
        except urllib.error.URLError as e:
            log(f"    ✗ Network error: {e.reason}")
            return None
        except json.JSONDecodeError as e:
            log(f"    ✗ JSON decode error: {e}")
            return None
        except Exception as e:
            log(f"    ✗ Error: {e}")
            return None
    log(f"    ✗ Exhausted retries for {url[-80:]}")
    return None


def process_split(split: str, splits_info: Dict[str, int]):
    """Fetch all rows for one split and process them on-the-fly."""
    offset = 0
    t0 = time.perf_counter()
    stories_saved = 0
    questions_saved = 0
    filtered_kind = 0
    filtered_no_text = 0
    filtered_long = 0
    duplicate_story = 0
    consecutive_empty = 0

    log(f"  Processing {split} split...")

    while True:
        url = (
            f"{DATASETS_SERVER}/rows"
            f"?dataset={DATASET}"
            f"&config={CONFIG}"
            f"&split={split}"
            f"&offset={offset}"
            f"&length={PAGE_SIZE}"
        )

        data = _page_request(url)
        if data is None:
            consecutive_empty += 1
            if consecutive_empty >= 3:
                log(f"    ✗ Too many failures, aborting {split}")
                break
            continue
        consecutive_empty = 0

        rows = data.get("rows", [])
        if not rows:
            break

        rows = data.get("rows", [])
        if not rows:
            break

        for r in rows:
            story, qa = _extract_story_qa(r["row"])
            if story is None and qa is None:
                # Count why it was filtered
                doc = r["row"].get("document", {})
                if isinstance(doc, str):
                    try:
                        doc = json.loads(doc)
                    except json.JSONDecodeError:
                        pass
                kind = doc.get("kind", "") if isinstance(doc, dict) else ""
                text = doc.get("text", "") if isinstance(doc, dict) else ""
                wc = doc.get("word_count", 0) if isinstance(doc, dict) else 0
                if isinstance(wc, str):
                    try:
                        wc = int(wc)
                    except ValueError:
                        pass
                if kind != "gutenberg":
                    filtered_kind += 1
                elif not text or len(str(text).strip()) < 100:
                    filtered_no_text += 1
                elif wc > MAX_STORY_WORDS:
                    filtered_long += 1
                else:
                    # Could be malformed row
                    filtered_no_text += 1
                continue

            if story:
                path = os.path.join(STORIES_DIR, f"{story['id']}.json")
                if os.path.exists(path):
                    duplicate_story += 1
                else:
                    _save_story(story)
                    stories_saved += 1

            if qa:
                _append_question(qa)
                questions_saved += 1

        offset += len(rows)
        elapsed = time.perf_counter() - t0
        rate = offset / elapsed if elapsed else 0
        log(f"    [{offset}/{splits_info[split]}]  stories: {stories_saved}  qs: {questions_saved}  "
            f"skip: {filtered_kind+filtered_no_text+filtered_long}  dup: {duplicate_story}  "
            f"{rate:.0f} rows/s")

        if len(rows) < PAGE_SIZE:
            break

    elapsed = time.perf_counter() - t0
    log(f"  ✓ {split}: {offset} rows, {stories_saved} stories, {questions_saved} qs "
        f"({elapsed:.1f}s)")
    return stories_saved, questions_saved


def get_split_sizes() -> Dict[str, int]:
    """Get row counts for each split."""
    sizes: Dict[str, int] = {}
    try:
        url = f"{DATASETS_SERVER}/size?dataset={DATASET}"
        req = urllib.request.Request(url)
        req.add_header("User-Agent", "RagamuffinBenchmark/1.0")
        resp = urllib.request.urlopen(req, timeout=15)
        data = json.loads(resp.read())
        for s in data["size"]["splits"]:
            sizes[s["split"]] = s["num_rows"]
    except Exception as e:
        log(f"  ⚠ Could not get split sizes: {e}")
        sizes = {"train": 14650, "test": 10557, "validation": 3461}
        log(f"  Using fallback sizes: {sizes}")
    return sizes


# ── Ingest into Ragamuffin ──────────────────────────────────────────────────────


def ingest_into_ragamuffin(
    max_stories: Optional[int] = None,
    vault: str = "narrativeqa_d",
    config_label: str = "D",
):
    """Ingest saved stories into a Ragamuffin vault."""
    log(f"\nIngesting into vault '{vault}' (config {config_label})...")
    t0 = time.perf_counter()

    story_files = sorted(
        f for f in os.listdir(STORIES_DIR) if f.endswith(".json")
    )
    if max_stories:
        story_files = story_files[:max_stories]

    total = len(story_files)
    if total == 0:
        log("  No stories to ingest.")
        return

    # Ensure vault exists
    try:
        _ragamuffin_request("POST", "/v1/ingest", {
            "content": "NarrativeQA benchmark dataset placeholder.",
            "source": "narrativeqa/metadata",
            "vault": vault,
            "tags": ["benchmark", "narrativeqa", "metadata"],
        })
    except Exception as e:
        log(f"  ⚠ Vault setup: {e}")

    ok = err = 0
    for idx, sf in enumerate(story_files):
        sid = sf.replace(".json", "")
        path = os.path.join(STORIES_DIR, sf)

        try:
            with open(path) as f:
                story = json.load(f)
        except (json.JSONDecodeError, OSError) as e:
            err += 1
            continue

        # 1. Ingest Wikipedia summary
        summary_text = story.get("summary_text", "")
        if summary_text:
            try:
                _ragamuffin_request("POST", "/v1/ingest", {
                    "content": summary_text,
                    "source": f"narrativeqa/{sid}/summary",
                    "vault": vault,
                    "tags": ["benchmark", "narrativeqa", sid, "summary"],
                })
                ok += 1
            except Exception as e:
                err += 1
                if err <= 5:
                    log(f"  ⚠ ingest summary [{sid}]: {str(e)[:80]}")

            if INGEST_DELAY > 0:
                time.sleep(INGEST_DELAY)

        # 2. Ingest story text in chunks
        story_text = story.get("text", "")
        words = story_text.split()
        if words:
            step = CHUNK_WORDS - CHUNK_OVERLAP_WORDS
            chunks = []
            for ci in range(0, len(words), step):
                chunk_words = words[ci:ci + CHUNK_WORDS]
                chunks.append(" ".join(chunk_words))
                if ci + CHUNK_WORDS >= len(words):
                    break

            for ci, chunk in enumerate(chunks):
                source = f"narrativeqa/{sid}/chunk-{ci}"
                tags = ["benchmark", "narrativeqa", sid, f"chunk-{ci}"]
                if config_label in ("B", "D"):
                    tags.append("auto_extract")

                try:
                    _ragamuffin_request("POST", "/v1/ingest", {
                        "content": chunk,
                        "source": source,
                        "vault": vault,
                        "tags": tags,
                    })
                    ok += 1
                except Exception as e:
                    err += 1
                    if err <= 5:
                        log(f"  ⚠ ingest chunk [{sid}/{ci}]: {str(e)[:80]}")

                if INGEST_DELAY > 0:
                    time.sleep(INGEST_DELAY)

        # Progress
        if (idx + 1) % 10 == 0 or idx == total - 1:
            pct = (idx + 1) / total * 100
            elapsed = time.perf_counter() - t0
            rate = (idx + 1) / elapsed if elapsed else 0
            remaining = (total - idx - 1) / max(rate, 0.001)
            log(f"  ingest: [{idx+1}/{total}] {pct:.0f}%  {rate:.1f}/s  ETA: {remaining:.0f}s  errs: {err}")

    elapsed = time.perf_counter() - t0
    log(f"\nIngest complete: {ok} ok, {err} err in {elapsed:.0f}s")


def _ragamuffin_request(
    method: str, path: str, body: Optional[Dict] = None, timeout: int = 120
):
    """Make a request to Ragamuffin API."""
    url = f"{RAGAMUFFIN_URL}{path}"
    data_bytes = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data_bytes, method=method)
    req.add_header("Content-Type", "application/json")
    req.add_header("User-Agent", "RagamuffinNarrativeQA/1.0")
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body_text = e.read().decode() if e.fp else ""
        raise Exception(f"HTTP {e.code}: {body_text[:200]}")


# ── Save consolidated questions ────────────────────────────────────────────────


def finalize_questions():
    """Convert questions.jsonl to questions.json for compat with loader."""
    qa_path = os.path.join(DATA_DIR, "questions.jsonl")
    if not os.path.exists(qa_path):
        log("  No questions.jsonl found.")
        return

    questions = []
    with open(qa_path) as f:
        for line in f:
            line = line.strip()
            if line:
                questions.append(json.loads(line))

    with open(QUESTIONS_FILE, "w") as f:
        json.dump(questions, f, indent=2)

    log(f"  Finalized {len(questions)} questions to {QUESTIONS_FILE}")


def save_metadata(stories_count: int, questions_count: int, elapsed: float):
    """Save dataset metadata."""
    metadata = {
        "total_stories": stories_count,
        "total_questions": questions_count,
        "chunk_words": CHUNK_WORDS,
        "chunk_overlap_words": CHUNK_OVERLAP_WORDS,
        "max_story_words": MAX_STORY_WORDS,
        "source": "hf-datasets-server",
        "parsed_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "elapsed_seconds": round(elapsed, 1),
    }
    os.makedirs(DATA_DIR, exist_ok=True)
    with open(METADATA_FILE, "w") as f:
        json.dump(metadata, f, indent=2)
    log(f"  Metadata saved to {METADATA_FILE}")


# ── CLI ─────────────────────────────────────────────────────────────────────────


def parse_args():
    parser = argparse.ArgumentParser(
        description="NarrativeQA: fetch via HF datasets server, parse, and ingest",
    )
    parser.add_argument("--ingest", action="store_true",
                        help="Ingest into Ragamuffin after parsing")
    parser.add_argument("--vault", default="narrativeqa_d",
                        help="Target Ragamuffin vault name (default: narrativeqa_d)")
    parser.add_argument("--config", default="D",
                        choices=["A", "B", "C", "D"],
                        help="Config label for tags (default: D)")
    parser.add_argument("--max-stories", type=int, default=None,
                        help="Max stories to process (for smoke testing)")
    parser.add_argument("--chunk-words", type=int, default=CHUNK_WORDS,
                        help=f"Words per chunk (default: {CHUNK_WORDS})")
    return parser.parse_args()


def main():
    args = parse_args()
    global CHUNK_WORDS
    CHUNK_WORDS = args.chunk_words

    log("╔══════════════════════════════════════════════════════╗")
    log("║  NarrativeQA — Fetch via HF + Parse + Ingest        ║")
    log("╚══════════════════════════════════════════════════════╝")
    log(f"  Data dir: {DATA_DIR}  |  Max stories: {args.max_stories or 'all'}")
    log(f"  Chunk size: {CHUNK_WORDS} words")
    log("")

    os.makedirs(STORIES_DIR, exist_ok=True)
    t_start = time.perf_counter()

    # Step 1: Fetch + parse all splits
    log("--- Step 1: Fetch from HF Datasets Server + Parse ---")
    sizes = get_split_sizes()
    total_stories = 0
    total_questions = 0

    for split in SPLITS:
        s, q = process_split(split, sizes)
        total_stories += s
        total_questions += q

    # Step 2: Finalize
    log("\n--- Step 2: Finalize ---")
    finalize_questions()
    elapsed = time.perf_counter() - t_start
    save_metadata(total_stories, total_questions, elapsed)

    log(f"\n  Total: {total_stories} stories, {total_questions} questions ({elapsed:.1f}s)")

    # Step 3: Ingest (optional)
    if args.ingest:
        log("\n--- Step 3: Ingest into Ragamuffin ---")
        ingest_into_ragamuffin(
            max_stories=args.max_stories,
            vault=args.vault,
            config_label=args.config,
        )

    log("\nDone.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:
        log(f"FATAL: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
