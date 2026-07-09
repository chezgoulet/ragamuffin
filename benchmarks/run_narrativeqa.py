#!/usr/bin/env python3
"""NarrativeQA benchmark runner for Ragamuffin.

Standalone entry point for the NarrativeQA long-form reading-comprehension
benchmark (#690). This wraps ``benchmarks.run`` machinery with NarrativeQA
defaults so the dataset can be run on its own (it is large: ~1,500 Gutenberg
stories → ~30k questions in full).

Ingestion strategy: one vault per story (``nqa-<kind>-<docid8>``), so each
``/ask`` is scoped to a single novel. Filtering defaults to public-domain
Gutenberg texts (skips copyrighted film scripts) and caps story size to keep
within Ragamuffin's ingest/context limits.

Usage:
    # Smoke test — 3 stories, all questions, Config D
    python3 benchmarks/run_narrativeqa.py --max-stories 3

    # Config A only, cap to 10 questions/story
    python3 benchmarks/run_narrativeqa.py --config A --max-questions 10

    # Full Gutenberg set (downloads data first if missing)
    python3 benchmarks/run_narrativeqa.py

    # Include movie scripts too, shared vault, custom max word count
    python3 benchmarks/run_narrativeqa.py --kinds gutenberg movie \
        --shared-vault --max-words 200000

Environment:
    RAGAMUFFIN_URL  (default http://localhost:8000)
    OPENAI_API_KEY / LITELLM_* for the scoring judge
"""

from __future__ import annotations

import argparse
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from benchmarks.configs import Config  # noqa: E402
from benchmarks.core.client import RagamuffinClient  # noqa: E402
from benchmarks.loaders.narrativeqa import NarrativeQALoader  # noqa: E402

DEFAULT_DATA = os.path.join(os.path.dirname(os.path.abspath(__file__)), "data", "NarrativeQA")


def parse_args():
    p = argparse.ArgumentParser(description="Ragamuffin × NarrativeQA benchmark")
    p.add_argument("--config", default="D", choices=["A", "B", "C", "D"],
                   help="Single config to run (default: D)")
    p.add_argument("--data", default=DEFAULT_DATA,
                   help="Path to NarrativeQA parquet directory")
    p.add_argument("--kinds", nargs="*", default=["gutenberg"],
                   help="Story kinds to include (gutenberg, movie)")
    p.add_argument("--max-stories", type=int, default=0,
                   help="Cap number of stories ingested (0 = all)")
    p.add_argument("--max-questions", type=int, default=0,
                   help="Cap questions per story (0 = all)")
    p.add_argument("--max-words", type=int, default=100_000,
                   help="Skip stories longer than this word count")
    p.add_argument("--shared-vault", action="store_true",
                   help="Ingest all stories into one shared vault (default: per-story)")
    p.add_argument("--no-download", action="store_true",
                   help="Do not auto-download parquet files if missing")
    p.add_argument("--clean", action="store_true",
                   help="Clear per-story vaults before ingest (destructive)")
    p.add_argument("--ingest-delay", type=float, default=0.0,
                   help="Delay (s) between ingest calls")
    return p.parse_args()


def main():
    args = parse_args()
    from benchmarks.run import (  # local import to reuse helpers
        ALL_CFG, RUN_ID, RESULTS_DIR, STATUS_FILE, log, log_header,
        ingest_all, run_qa, save_results, BASE_URL,
    )

    cfg = Config.parse(args.config)
    if cfg is None:
        print(f"bad config: {args.config}")
        return 2

    client = RagamuffinClient(base_url=BASE_URL, ingest_timeout=120, ask_timeout=30)
    if not client.health():
        log(f"FATAL: Ragamuffin unreachable at {BASE_URL}")
        return 1

    loader = NarrativeQALoader(
        dataset_path=args.data,
        config_label=cfg.value,
        kinds=args.kinds,
        max_stories=args.max_stories,
        max_questions_per_story=args.max_questions,
        max_words=args.max_words,
        per_story_vaults=not args.shared_vault,
        auto_download=not args.no_download,
    )

    log_header(f"NarrativeQA — Config {cfg.value} (kinds={args.kinds})")
    t0 = time.perf_counter()
    convs = loader.load()
    if not convs:
        log("No stories loaded. Run without --no-download, or check --data path.")
        return 1
    qs = loader.questions(convs[0])
    qdata = [(q.text, q.ground_truth, q.question_type, q.conversation_id) for q in qs]
    log(f"Loaded {len(convs)} stories, {len(qdata)} questions in {time.perf_counter()-t0:.1f}s")

    # Per-story vault resolver for ask routing.
    vault_map = {c.id: c.vault for c in convs}

    def resolver(text, gt, qt, conv_id):
        return vault_map.get(conv_id, f"nqa-{RUN_ID}")

    vault = f"nqa-bench-{RUN_ID}"
    if args.clean:
        for v in set(vault_map.values()):
            try:
                client.clear_vault(v)
                log(f"  cleared {v}")
            except Exception:
                pass

    # Phase 1: ingest
    ingest_all(client, convs, vault, "narrativeqa",
               ingest_delay=args.ingest_delay,
               use_conv_vault=not args.shared_vault)

    # Phase 2: ask (single config)
    results, acc, correct, total = run_qa(
        client, vault, qdata, cfg, "narrativeqa", "narrativeqa",
        vault_resolver=resolver,
    )
    save_results(results, cfg, "narrativeqa", correct, total)

    log("")
    log(f"NarrativeQA Config {cfg.value}: {correct}/{total} = {acc:.1%}")
    log(f"Status: {STATUS_FILE}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:  # pragma: no cover
        import traceback
        traceback.print_exc()
        sys.exit(1)
