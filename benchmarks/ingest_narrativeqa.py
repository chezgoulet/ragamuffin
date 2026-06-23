#!/usr/bin/env python3
"""
NarrativeQA Dataset — Download, parse, and ingest into Ragamuffin.

Usage:
    python3 benchmarks/ingest_narrativeqa.py                     # Download + parse only
    python3 benchmarks/ingest_narrativeqa.py --ingest            # Download + parse + ingest
    python3 benchmarks/ingest_narrativeqa.py --ingest --vault narrativeqa_d
    python3 benchmarks/ingest_narrativeqa.py --max-stories 10    # Quick smoke test
    python3 benchmarks/ingest_narrativeqa.py --skip-download     # Re-parse cached parquet

Downloads NarrativeQA parquet files from HuggingFace, extracts stories + QA
pairs, filters to Gutenberg/public-domain texts with full text, chunks stories
by word-count windows, and optionally ingests into a Ragamuffin vault.

Output:
    benchmarks/data/narrativeqa/
        stories/             # One JSON file per story (parsed + cleaned)
        questions.json       # All QA pairs indexed by story_id
        metadata.json        # Dataset summary
        parquet/             # Downloaded parquet files (cached)
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
PARQUET_DIR = os.path.join(DATA_DIR, "parquet")
STORIES_DIR = os.path.join(DATA_DIR, "stories")
QUESTIONS_FILE = os.path.join(DATA_DIR, "questions.json")
METADATA_FILE = os.path.join(DATA_DIR, "metadata.json")

# HuggingFace parquet paths — dynamically discovered from API
HF_API = "https://huggingface.co/api/datasets/deepmind/narrativeqa/parquet"
HF_BASE = "https://huggingface.co/datasets/deepmind/narrativeqa/parquet/default"

# Filtering
MAX_STORY_TOKENS = 500_000  # Skip stories above this
CHUNK_WORDS = 4_000  # Target words per chunk
CHUNK_OVERLAP_WORDS = 200  # Overlap between chunks

# Ingest defaults
RAGAMUFFIN_URL = os.environ.get("RAGAMUFFIN_URL", "http://ragamuffin:8000")
INGEST_DELAY = float(os.environ.get("RAGAMUFFIN_INGEST_DELAY", "0.1"))


# ── Logging ─────────────────────────────────────────────────────────────────────


def log(msg: str):
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


# ── Download ─────────────────────────────────────────────────────────────────────


def _discover_parquet_urls() -> Dict[str, List[str]]:
    """Discover all parquet file URLs across all splits from the HF API.

    Returns dict of split_name -> [url, ...].
    """
    try:
        req = urllib.request.Request(HF_API)
        req.add_header("User-Agent", "RagamuffinBenchmark/1.0")
        resp = urllib.request.urlopen(req, timeout=15)
        manifest = json.loads(resp.read())
    except Exception as e:
        log(f"  ⚠ Could not discover splits from API: {e}")
        log(f"  Falling back to default splits")
        # Fallback: well-known splits
        return {
            "train": [f"{HF_BASE}/train/{i}.parquet" for i in range(8)],
            "test": [f"{HF_BASE}/test/{i}.parquet" for i in range(6)],
            "validation": [f"{HF_BASE}/validation/{i}.parquet" for i in range(2)],
        }

    # Parse the manifest — it's nested under the "default" config
    config = manifest.get("default", manifest)
    splits: Dict[str, List[str]] = {}
    for split_name, urls in config.items():
        # urls from the API are HuggingFace API URLs, not raw download URLs.
        # Convert them to raw download: /api/datasets/... -> /datasets/...
        raw_urls = []
        for u in urls:
            # Replace /api/datasets/ with /datasets/ for raw parquet download
            raw = u.replace("/api/datasets/", "/datasets/")
            raw_urls.append(raw)
        splits[split_name] = raw_urls

    log(f"  Discovered splits: {', '.join(f'{k}: {len(v)} files' for k, v in splits.items())}")
    return splits


def download_parquet_files():
    """Download NarrativeQA parquet files from HuggingFace across all splits."""
    os.makedirs(PARQUET_DIR, exist_ok=True)

    splits = _discover_parquet_urls()
    total_files = sum(len(urls) for urls in splits.values())
    downloaded = 0
    skipped = 0

    for split_name, urls in splits.items():
        split_dir = os.path.join(PARQUET_DIR, split_name)
        os.makedirs(split_dir, exist_ok=True)

        log(f"\n  Split: {split_name} ({len(urls)} files)")
        for i, url in enumerate(urls):
            dest = os.path.join(split_dir, f"{i}.parquet")

            if os.path.exists(dest) and os.path.getsize(dest) > 1000:
                size_mb = os.path.getsize(dest) / (1024 * 1024)
                log(f"    ✓ {split_name}/{i}.parquet cached ({size_mb:.1f} MB)")
                skipped += 1
                continue

            log(f"    Downloading {split_name}/{i}.parquet...")
            t0 = time.perf_counter()
            try:
                urllib.request.urlretrieve(url, dest)
                elapsed = time.perf_counter() - t0
                size_mb = os.path.getsize(dest) / (1024 * 1024)
                log(f"    ✓ {split_name}/{i}.parquet: {size_mb:.1f} MB ({elapsed:.1f}s)")
                downloaded += 1

                # Rate-limit to be gentle to HF
                if downloaded % 3 == 0:
                    time.sleep(0.5)

            except urllib.error.HTTPError as e:
                log(f"    ✗ {split_name}/{i}.parquet: HTTP {e.code}")
            except urllib.error.URLError as e:
                log(f"    ✗ {split_name}/{i}.parquet: {e.reason}")

    log(f"\n  Download complete: {downloaded} downloaded, {skipped} cached ({total_files} total)")


# ── Lightweight Parquet Reader ──────────────────────────────────────────────────


def _read_parquet_table(path: str) -> List[Dict[str, Any]]:
    """Read a parquet file and return rows as dicts.

    Tries pyarrow first (fast), then pandas, then fails gracefully.
    """
    # Try pyarrow first (fastest, most memory-efficient)
    try:
        import pyarrow.parquet as pq
        table = pq.read_table(path)
        return table.to_pylist()
    except ImportError:
        pass

    # Try pandas (slower but works)
    try:
        import pandas as pd
        df = pd.read_parquet(path)
        return df.to_dict("records")
    except ImportError:
        pass

    raise ImportError(
        "Cannot read parquet files. Install pyarrow or pandas:\n"
        "  pip install pyarrow\n"
        "  pip install pandas\n"
        "Or download and run the benchmark on a system with these installed."
    )


# ── Parse ────────────────────────────────────────────────────────────────────────


def parse_stories() -> Tuple[List[Dict], Dict[str, List[Dict]]]:
    """Parse all parquet files and extract stories + questions.

    Reads parquet files from each split subdirectory under PARQUET_DIR.
    Expected structure:
        parquet/train/0.parquet ...
        parquet/test/0.parquet ...
        parquet/validation/0.parquet ...

    Returns (stories_list, questions_by_story).
    Each story is a dict with: id, kind, url, word_count, text, summary_text,
    start, end.

    Questions are deduplicated by question text within each story.
    """
    log("Parsing parquet files...")
    t0 = time.perf_counter()

    stories: Dict[str, Dict] = {}
    questions: Dict[str, List[Dict]] = {}
    total_rows = 0
    filtered_kind = 0
    filtered_no_text = 0
    filtered_too_long = 0
    parquet_count = 0

    if not os.path.isdir(PARQUET_DIR):
        log(f"  ✗ Parquet directory not found: {PARQUET_DIR}")
        log(f"  Run without --skip-download first, or manually download the dataset.")
        return [], {}

    # Discover all parquet files across all splits
    split_dirs = sorted([d for d in os.listdir(PARQUET_DIR)
                         if os.path.isdir(os.path.join(PARQUET_DIR, d))])
    if not split_dirs:
        # No split dirs — maybe files are flat (legacy layout)
        flat_files = sorted([f for f in os.listdir(PARQUET_DIR) if f.endswith(".parquet")])
        if flat_files:
            split_dirs = [""]  # read from root

    for split in split_dirs:
        split_path = os.path.join(PARQUET_DIR, split) if split else PARQUET_DIR
        parquet_files = sorted([f for f in os.listdir(split_path) if f.endswith(".parquet")])
        if not parquet_files:
            continue

        log(f"  Split: {split or 'flat'} ({len(parquet_files)} files)")
        for pf in parquet_files:
            path = os.path.join(split_path, pf)

            rows = _read_parquet_table(path)
            total_rows += len(rows)
            parquet_count += 1

            # Process every 1000 rows for progress
            for ri, row in enumerate(rows):
                doc = row.get("document", {})
                if isinstance(doc, str):
                    try:
                        doc = json.loads(doc)
                    except json.JSONDecodeError:
                        continue

                story_id = doc.get("id", "")
                kind = doc.get("kind", "")
                story_text = doc.get("text", "")

                # Filter: only Gutenberg with full text, under token limit
                # (Film scripts excluded due to copyright)
                if kind != "gutenberg":
                    filtered_kind += 1
                    continue
                if not story_text or len(story_text.strip()) < 100:
                    filtered_no_text += 1
                    continue
                word_count = doc.get("word_count", 0)
                if isinstance(word_count, str):
                    try:
                        word_count = int(word_count)
                    except ValueError:
                        word_count = len(story_text.split())
                if word_count > MAX_STORY_TOKENS:
                    filtered_too_long += 1
                    continue

                if story_id not in stories:
                    summary = doc.get("summary", {})
                    if isinstance(summary, str):
                        try:
                            summary = json.loads(summary)
                        except json.JSONDecodeError:
                            summary = {}
                    stories[story_id] = {
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
                    continue

                # Deduplicate by question text within story
                if story_id not in questions:
                    questions[story_id] = []
                seen_qs = {q["text"] for q in questions[story_id]}
                if q_text not in seen_qs:
                    questions[story_id].append({
                        "text": q_text,
                        "answers": answers,
                        "story_id": story_id,
                    })

            if (parquet_count % 2 == 0) or parquet_count == 1:
                log(f"    progress: {len(stories)} stories, "
                    f"{sum(len(qs) for qs in questions.values())} questions")

    elapsed = time.perf_counter() - t0
    log(f"\nParsing complete:")
    log(f"  Parquet files parsed: {parquet_count}")
    log(f"  Total rows: {total_rows}")
    log(f"  Filtered (not Gutenberg): {filtered_kind}")
    log(f"  Filtered (no text): {filtered_no_text}")
    log(f"  Filtered (too long >{MAX_STORY_TOKENS} tokens): {filtered_too_long}")
    log(f"  Stories kept: {len(stories)}")
    log(f"  Questions kept: {sum(len(qs) for qs in questions.values())}")
    log(f"  Elapsed: {elapsed:.1f}s")

    return list(stories.values()), questions


def chunk_story_text(story: Dict) -> List[str]:
    """Split story text into word-count chunks with overlap."""
    text = story.get("text", "")
    words = text.split()
    if len(words) <= CHUNK_WORDS:
        return [text]

    chunks = []
    step = CHUNK_WORDS - CHUNK_OVERLAP_WORDS
    for i in range(0, len(words), step):
        chunk_words = words[i:i + CHUNK_WORDS]
        chunks.append(" ".join(chunk_words))
        if i + CHUNK_WORDS >= len(words):
            break
    return chunks


def save_parsed_data(stories: List[Dict], questions: Dict[str, List[Dict]]):
    """Save parsed stories and questions to disk."""
    os.makedirs(STORIES_DIR, exist_ok=True)

    # Save each story as individual JSON for modular loading
    for story in stories:
        sid = story["id"]
        path = os.path.join(STORIES_DIR, f"{sid}.json")
        with open(path, "w") as f:
            json.dump(story, f, indent=2)

    # Save all questions
    flat_questions = []
    for sid, qs in questions.items():
        for q in qs:
            flat_questions.append(q)
    with open(QUESTIONS_FILE, "w") as f:
        json.dump(flat_questions, f, indent=2)

    # Save metadata
    metadata = {
        "total_stories": len(stories),
        "total_questions": len(flat_questions),
        "stories_with_questions": len(questions),
        "chunk_words": CHUNK_WORDS,
        "chunk_overlap_words": CHUNK_OVERLAP_WORDS,
        "max_story_tokens": MAX_STORY_TOKENS,
        "parsed_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    with open(METADATA_FILE, "w") as f:
        json.dump(metadata, f, indent=2)

    log(f"\nSaved {len(stories)} stories to {STORIES_DIR}/")
    log(f"Saved {len(flat_questions)} questions to {QUESTIONS_FILE}")
    log(f"Saved metadata to {METADATA_FILE}")


# ── Ingest into Ragamuffin ──────────────────────────────────────────────────────


def ingest_into_ragamuffin(
    stories: List[Dict],
    questions: Dict[str, List[Dict]],
    vault: str,
    config_label: str = "D",
    max_stories: Optional[int] = None,
):
    """Ingest narrative stories + summaries into a Ragamuffin vault."""
    import urllib.request as req_lib
    import urllib.parse

    log(f"\nIngesting into vault '{vault}' (config {config_label})...")
    t0 = time.perf_counter()

    # Ensure vault exists by ingesting a small placeholder first
    try:
        _ragamuffin_request("POST", "/v1/ingest", {
            "content": "NarrativeQA benchmark dataset placeholder.",
            "source": "narrativeqa/metadata",
            "vault": vault,
            "tags": ["benchmark", "narrativeqa", "metadata"],
        })
    except Exception as e:
        log(f"  ⚠ Vault setup: {e}")

    stories_to_ingest = stories[:max_stories] if max_stories else stories
    total = len(stories_to_ingest)
    ok = err = 0

    for idx, story in enumerate(stories_to_ingest):
        sid = story["id"]
        word_count = story.get("word_count", 0)

        # 1. Ingest the Wikipedia summary (compact, high-signal)
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

        # 2. Ingest the story text in chunks
        chunks = chunk_story_text(story)
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
        if (idx + 1) % 10 == 0 or idx == 0 or idx == total - 1:
            pct = (idx + 1) / total * 100
            elapsed = time.perf_counter() - t0
            rate = (idx + 1) / elapsed if elapsed else 0
            remaining = (total - idx - 1) / max(rate, 0.001)
            log(f"  ingest: [{idx+1}/{total}] {pct:.0f}%  {rate:.1f}/s  ETA: {remaining:.0f}s  errs: {err}")

    elapsed = time.perf_counter() - t0
    log(f"\nIngest complete: {ok} ok, {err} err in {elapsed:.0f}s")


def _ragamuffin_request(method: str, path: str, body: Optional[Dict] = None, timeout: int = 30):
    """Make a request to Ragamuffin API."""
    import urllib.request
    import urllib.error
    import json as json_mod

    url = f"{RAGAMUFFIN_URL}{path}"
    data_bytes = json_mod.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data_bytes, method=method)
    req.add_header("Content-Type", "application/json")
    req.add_header("User-Agent", "RagamuffinNarrativeQA/1.0")

    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        return json_mod.loads(resp.read())
    except urllib.error.HTTPError as e:
        body_text = e.read().decode() if e.fp else ""
        raise Exception(f"HTTP {e.code}: {body_text[:200]}")


# ── CLI ─────────────────────────────────────────────────────────────────────────


def parse_args():
    parser = argparse.ArgumentParser(
        description="NarrativeQA: download, parse, and ingest",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--skip-download", action="store_true",
                        help="Skip downloading parquet files")
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

    log("╔══════════════════════════════════════════════╗")
    log("║  NarrativeQA — Download + Parse + Ingest    ║")
    log("╚══════════════════════════════════════════════╝")
    log(f"  Data dir: {DATA_DIR}")
    log(f"  Max stories: {args.max_stories or 'all'}")
    log(f"  Chunk size: {CHUNK_WORDS} words")
    log("")

    # Step 1: Download
    if not args.skip_download:
        log("--- Step 1: Download parquet files ---")
        download_parquet_files()
    else:
        log("--- Step 1: Skipped (--skip-download) ---")
    log("")

    # Step 2: Parse
    log("--- Step 2: Parse stories + questions ---")
    stories, questions = parse_stories()
    save_parsed_data(stories, questions)
    log("")

    # Step 3: Ingest (optional)
    if args.ingest:
        log("--- Step 3: Ingest into Ragamuffin ---")
        ingest_into_ragamuffin(
            stories, questions,
            vault=args.vault,
            config_label=args.config,
            max_stories=args.max_stories,
        )
    else:
        log("--- Step 3: Skipped (pass --ingest to enable) ---")
        log(f"  To ingest: python3 benchmarks/ingest_narrativeqa.py --ingest --vault {args.vault}")

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
