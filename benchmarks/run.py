#!/usr/bin/env python3
"""
Ragamuffin benchmark harness v2 — resilient, /ask-based, checkpointed.

Usage:
    python3 benchmarks/run.py --benchmark longmemeval --config a --max-convs 5
    python3 benchmarks/run.py --benchmark locomo --config b --resume
    python3 benchmarks/run.py --benchmark longmemeval --config d --max-convs 0

Configs:
  a — Pure /ask (recall + synthesize)
  b — /ask + facts (auto_extract on ingest)
  c — Tiered /ask
  d — /ask + facts + fact graph (auto_extract on ingest)
"""

import argparse
import json
import os
import re
import sys
import time

import requests

# ── Config ──────────────────────────────────────────────────────────────────

RAGAMUFFIN_URL = os.environ.get("RAGAMUFFIN_URL", "http://localhost:8000")
LITELLM_URL = os.environ.get("LITELLM_URL", "http://localhost:4000")
LITELLM_API_KEY = os.environ.get("LITELLM_MASTER_KEY", "")

BENCH_DIR = os.path.dirname(os.path.abspath(__file__))
DATA_DIR = os.path.join(BENCH_DIR, "data")

PROGRESS_INTERVAL = 5   # update progress file every N questions

# ── API helpers ─────────────────────────────────────────────────────────────


def api_post(path, json_body, timeout=30):
    url = f"{RAGAMUFFIN_URL}{path}"
    r = requests.post(url, json=json_body, timeout=timeout)
    if r.status_code >= 400:
        msg = r.text[:200]
        raise requests.HTTPError(f"POST {path} {r.status_code}: {msg}", response=r)
    return r.json()


def api_get(path, params=None, timeout=30):
    url = f"{RAGAMUFFIN_URL}{path}"
    r = requests.get(url, params=params, timeout=timeout)
    if r.status_code >= 400:
        msg = r.text[:200]
        raise requests.HTTPError(f"GET {path} {r.status_code}: {msg}", response=r)
    return r.json()


def wait_for_indexing(vault="default", poll_sec=2, timeout_sec=300):
    """Block until Ragamuffin finishes indexing (status != 'indexing')."""
    deadline = time.time() + timeout_sec
    while time.time() < deadline:
        health = api_get("/health")
        idx = health.get("indexing", True)
        if not idx:
            return
        time.sleep(poll_sec)
    print(f"  [WARN] Indexing may not have completed within {timeout_sec}s", file=sys.stderr)


def ask_in_vault(vault, query, mode="rag", top_k=10, timeout=30):
    """POST /vault/{vault}/ask and return the answer text."""
    body = {"query": query, "mode": mode, "top_k": top_k}
    result = api_post(f"/vault/{vault}/ask", body, timeout=timeout)
    return result.get("answer", "")


def create_session(agent_id, content, vault, auto_extract=False):
    body = {
        "agent_id": agent_id,
        "content": content,
        "vault": vault,
        "auto_extract": auto_extract,
    }
    return api_post("/v1/sessions", body)


def append_turn(session_id, content, role="user", auto_extract=False):
    body = {"content": content, "role": role, "auto_extract": auto_extract}
    return api_post(f"/v1/sessions/{session_id}/turns", body)


# ── Retry wrapper ───────────────────────────────────────────────────────────


def answer_with_retry(question, vault, mode="rag", top_k=10, max_retries=5):
    """
    Answer a single question via /vault/{vault}/ask with exponential backoff.
    Never raises — returns "ANSWER_FAILED" after exhausting retries.
    """
    for attempt in range(1, max_retries + 1):
        try:
            return ask_in_vault(vault, question, mode=mode, top_k=top_k)
        except requests.HTTPError as e:
            status = e.response.status_code if e.response else 0
            if status == 429:
                wait = 15 * (2 ** (attempt - 1))
            elif status in (502, 503):
                wait = 10 * attempt
            else:
                wait = 5 * attempt
            print(f"  [WARN] Q attempt {attempt}/{max_retries} ({status}): {e}", file=sys.stderr)
        except (requests.Timeout, ConnectionError) as e:
            wait = 5 * attempt
            print(f"  [WARN] Q attempt {attempt}/{max_retries} (timeout): {e}", file=sys.stderr)
        time.sleep(wait)
    print(f"  [ERROR] Q failed after {max_retries} attempts, skipping", file=sys.stderr)
    return "ANSWER_FAILED"


# ── Data loaders ────────────────────────────────────────────────────────────


def load_longmemeval(data_path=None):
    """
    Yield (conversation_id, turns, questions) for each conversation
    in the LongMemEval "S" (shorter) setting.

    500 individual JSON files in data/LongMemEval/data/S/. Each file:
      - sessions: dict[session_id -> list[{role, content}]]
      - question, answer, question_type, question_id, answer_session_ids
    """
    if data_path is None:
        s_path = None
        base = os.path.join(DATA_DIR, "LongMemEval", "data")
        for candidate in [os.path.join(base, "S"),
                          os.path.join(base, "setting_S"),
                          os.path.join(base, "short")]:
            if os.path.isdir(candidate):
                s_path = candidate
                break
        if not s_path:
            # Try data root
            s_path = os.path.join(DATA_DIR, "LongMemEval")
    else:
        s_path = data_path

    conv_files = sorted([
        os.path.join(s_path, f)
        for f in os.listdir(s_path)
        if f.endswith(".json") and not f.startswith(".")
    ])

    if not conv_files:
        print(f"  [ERROR] No JSON files found in {s_path}", file=sys.stderr)
        return

    for cf in conv_files:
        with open(cf) as fh:
            data = json.load(fh)

        conv_id = data.get("question_id", data.get("conversation_id",
                          os.path.splitext(os.path.basename(cf))[0]))

        # Flatten sessions dict into ordered turns
        sessions = data.get("sessions", {})
        turns = []
        if isinstance(sessions, dict):
            for sk in sorted(sessions.keys()):
                session_turns = sessions[sk]
                if isinstance(session_turns, list):
                    for turn in session_turns:
                        if isinstance(turn, dict):
                            role = turn.get("role", turn.get("speaker", "user"))
                            content = turn.get("content", turn.get("message", turn.get("text", "")))
                        else:
                            role = "user"
                            content = str(turn)
                        turns.append({"role": role.lower(), "content": content})

        # One question per conversation
        question_text = data.get("question", "")
        answer_text = str(data.get("answer", ""))
        questions = [{
            "question": question_text,
            "answer": answer_text,
            "type": data.get("question_type", ""),
        }]

        if not turns or not question_text:
            continue
        yield conv_id, turns, questions


def load_locomo(data_path=None):
    """
    Yield (conversation_id, turns, questions) for each conversation
    in the LoCoMo (Backboard) dataset.

    Flat JSON array (locomo_dataset.json). Each entry has:
      - conversation: dict with session_N keys
      - qa: list of {question, answer, evidence, category}
      - sample_id
    """
    if data_path is None:
        data_path = os.path.join(DATA_DIR, "Backboard-Locomo-Benchmark")
    dataset_file = os.path.join(data_path, "locomo_dataset.json")

    if not os.path.exists(dataset_file):
        # Fallback: try conversations subdirectory format
        conv_path = os.path.join(data_path, "conversations")
        if not os.path.isdir(conv_path):
            conv_path = data_path

        conv_dirs = sorted([
            d for d in os.listdir(conv_path)
            if os.path.isdir(os.path.join(conv_path, d)) and not d.startswith(".")
        ])

        for cd in conv_dirs:
            cat_file = os.path.join(conv_path, cd, "category.json")
            if os.path.exists(cat_file):
                with open(cat_file) as fh:
                    cat_data = json.load(fh)
                if isinstance(cat_data, dict) and cat_data.get("category") in (5, "5"):
                    continue

            turns = []
            turn_file = os.path.join(conv_path, cd, "turns.json")
            if os.path.exists(turn_file):
                with open(turn_file) as fh:
                    turns = json.load(fh)

            questions = []
            qa_file = os.path.join(conv_path, cd, "qa.json")
            if os.path.exists(qa_file):
                with open(qa_file) as fh:
                    questions = json.load(fh)

            if not turns and not questions:
                conv_file = os.path.join(conv_path, f"{cd}.json")
                if os.path.exists(conv_file):
                    with open(conv_file) as fh:
                        data = json.load(fh)
                    turns = data.get("turns", data.get("history", []))
                    questions = data.get("questions", data.get("qa_pairs", []))

            yield cd, turns, questions
        return

    with open(dataset_file) as fh:
        dataset = json.load(fh)

    if isinstance(dataset, dict):
        dataset = dataset.get("data", dataset.get("conversations", [dataset]))

    for entry in dataset:
        conv_id = entry.get("sample_id", entry.get("conversation_id",
                            entry.get("id", "")))

        # Flatten conversation sessions
        turns = []
        speaker_order = []  # first unique speaker = user, second = assistant
        conv = entry.get("conversation", entry.get("sessions", {}))
        if isinstance(conv, dict):
            for sk in sorted(conv.keys()):
                session_turns = conv[sk]
                if isinstance(session_turns, list):
                    for turn in session_turns:
                        if isinstance(turn, dict):
                            speaker = turn.get("speaker", turn.get("role", "")).lower()
                            message = turn.get("message", turn.get("content", turn.get("text", "")))
                            # Map character names to user/assistant roles.
                            # First unique speaker = user, second = assistant.
                            known_speakers = {"speaker_a": "user", "speaker_b": "assistant",
                                             "user": "user", "human": "user",
                                             "assistant": "assistant", "ai": "assistant", "bot": "assistant"}
                            if speaker in known_speakers:
                                role = known_speakers[speaker]
                            else:
                                if speaker not in speaker_order:
                                    speaker_order.append(speaker)
                                role = "user" if speaker_order.index(speaker) == 0 else "assistant"
                            turns.append({"role": role, "content": message})
                        else:
                            turns.append({"role": "user", "content": str(turn)})

        # Load QA pairs — all categories valid
        raw_qa = entry.get("qa", entry.get("questions", entry.get("qa_pairs", [])))
        questions = []
        if isinstance(raw_qa, list):
            for q_item in raw_qa:
                if isinstance(q_item, dict):
                    questions.append({
                        "question": q_item.get("question", q_item.get("query", "")),
                        "answer": str(q_item.get("answer", q_item.get("ground_truth", ""))),
                        "category": q_item.get("category", 0),
                        "evidence": q_item.get("evidence", ""),
                    })
                else:
                    questions.append({
                        "question": str(q_item), "answer": "",
                        "category": 0, "evidence": "",
                    })

        if not conv_id or not turns:
            continue
        yield conv_id, turns, questions


# ── Ingest ──────────────────────────────────────────────────────────────────


def ingest_conversation(conv_id, turns, vault, auto_extract=False):
    """Ingest a conversation into Ragamuffin. Returns session_id or None."""
    if not turns:
        return None

    first_turn = turns[0]
    if isinstance(first_turn, dict):
        content = first_turn.get("content", first_turn.get("text", ""))
        role = first_turn.get("role", "user")
    else:
        content = str(first_turn)
        role = "user"

    full_content = f"{role.capitalize()}: {content}"
    session = create_session(f"bench-{conv_id}", full_content, vault,
                             auto_extract=auto_extract)
    session_id = session.get("session_id", session.get("id", ""))
    if not session_id:
        return None

    for turn in turns[1:]:
        if isinstance(turn, dict):
            content = turn.get("content", turn.get("text", ""))
            role = turn.get("role", "user")
        else:
            content = str(turn)
            role = "user"
        if content.strip():
            append_turn(session_id, f"{role.capitalize()}: {content}", role,
                        auto_extract=auto_extract)

    return session_id


def ingest_document(conv_id, turns, vault, auto_extract=False):
    """
    Ingest a conversation as a single document via /v1/documents.
    Used for LoCoMo: 500-700 turns as one markdown doc, not turn-by-turn
    via sessions. Avoids SQLite journal writes and inotify storms (#526).
    """
    lines = []
    for turn in turns:
        if isinstance(turn, dict):
            role = turn.get("role", "user").capitalize()
            content = turn.get("content", turn.get("text", ""))
        else:
            role = "User"
            content = str(turn)
        if content.strip():
            lines.append(f"{role}: {content}")
    full_text = "\n\n".join(lines)

    source = f"locomo/{conv_id}.md"
    body = {
        "content": full_text,
        "source": source,
        "vault": vault,
        "tags": ["locomo", conv_id],
    }
    if auto_extract:
        body["auto_extract"] = True

    return api_post("/v1/documents", body, timeout=120)


# ── Checkpoint ──────────────────────────────────────────────────────────────


def save_checkpoint(path, results):
    """Write incremental results to checkpoint file."""
    tmp = path + ".tmp"
    with open(tmp, "w") as fh:
        json.dump(results, fh, indent=2)
    os.replace(tmp, path)


def total_questions_estimate(conversations, current_idx):
    """Estimate total questions across remaining conversations."""
    total = 0
    for _, _, questions in conversations[current_idx:]:
        total += len(questions)
    return total


def write_progress(path, progress):
    """Write lightweight progress file for operator monitoring (#528)."""
    tmp = path + ".tmp"
    try:
        with open(tmp, "w") as fh:
            json.dump(progress, fh, indent=2)
        os.replace(tmp, path)
    except OSError:
        pass  # non-fatal


def load_checkpoint(path):
    """Load checkpoint. Returns (results dict, set of completed (conv_id, q_idx))."""
    if not os.path.exists(path):
        return None, set()
    with open(path) as fh:
        results = json.load(fh)
    completed = set()
    # Scan completed conversations
    for conv in results.get("per_conversation", []):
        cid = conv.get("conversation", "")
        for i, d in enumerate(conv.get("details", [])):
            if d.get("answer", "") or d.get("f1") is not None:
                completed.add((cid, i))
    # Also scan in-progress conversation (#527)
    in_progress = results.get("_in_progress")
    if in_progress:
        cid = in_progress.get("conversation", "")
        for i, d in enumerate(in_progress.get("details", [])):
            if d.get("answer", ""):
                completed.add((cid, i))
    return results, completed


# ── Scoring ─────────────────────────────────────────────────────────────────


def call_evaluator_llm(prompt):
    """Call LiteLLM proxy chat/completions with the evaluator prompt."""
    api_key = LITELLM_API_KEY or os.environ.get("OPENAI_API_KEY", "")
    if not api_key:
        raise RuntimeError(
            "No API key found for evaluator LLM. "
            "Set LITELLM_MASTER_KEY or OPENAI_API_KEY."
        )

    r = requests.post(
        f"{LITELLM_URL}/v1/chat/completions",
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        json={
            "model": os.environ.get("EVALUATOR_MODEL", "gpt-4o"),
            "messages": [{"role": "user", "content": prompt}],
            "max_tokens": 512,
            "temperature": 0.0,
        },
        timeout=30,
    )
    r.raise_for_status()
    return r.json()["choices"][0]["message"]["content"]


def score_longmemeval(answer, ground_truth):
    """Judge correctness via evaluator LLM. Returns 1.0 or 0.0."""
    if answer == "ANSWER_FAILED":
        return 0.0
    prompt = (
        f"Ground truth: {ground_truth}\n"
        f"System answer: {answer}\n\n"
        "Is this answer correct? Reply with exactly 'CORRECT' or 'INCORRECT'."
    )
    try:
        verdict = call_evaluator_llm(prompt).strip().upper()
        return 1.0 if "CORRECT" in verdict else 0.0
    except Exception as e:
        print(f"  [WARN] LLM judge failed: {e}", file=sys.stderr)
        return 0.0


def tokenize(text):
    """Lowercase, split on non-alpha, return list of tokens."""
    return re.findall(r"[a-z0-9]+", text.lower())


def token_f1(pred, ref):
    """Token-level F1 with Porter stemming approximation."""
    p_tokens = tokenize(pred)
    r_tokens = tokenize(ref)

    # Porter stemmer approximation: common suffixes
    p_set = set()
    r_set = set()
    for t in p_tokens:
        for suf in ["ing", "ed", "ly", "es", "s", "tion", "ment", "ness"]:
            if t.endswith(suf) and len(t) > len(suf) + 2:
                t = t[:-len(suf)]
                break
        p_set.add(t)
    for t in r_tokens:
        for suf in ["ing", "ed", "ly", "es", "s", "tion", "ment", "ness"]:
            if t.endswith(suf) and len(t) > len(suf) + 2:
                t = t[:-len(suf)]
                break
        r_set.add(t)

    if not p_set and not r_set:
        return 1.0
    if not p_set or not r_set:
        return 0.0

    intersection = p_set & r_set
    precision = len(intersection) / len(p_set)
    recall = len(intersection) / len(r_set)
    if precision + recall == 0:
        return 0.0
    return 2 * precision * recall / (precision + recall)


def score_locomo(answer, ground_truth):
    """Token-level F1 score. Returns float 0-1."""
    if answer == "ANSWER_FAILED":
        return 0.0
    return token_f1(answer, ground_truth)


# ── Main ────────────────────────────────────────────────────────────────────


def parse_args():
    parser = argparse.ArgumentParser(description="Ragamuffin benchmark harness")
    parser.add_argument("--benchmark", required=True,
                        choices=["longmemeval", "locomo"],
                        help="Benchmark dataset")
    parser.add_argument("--config", required=True,
                        choices=["a", "b", "c", "d"],
                        help="Configuration preset")
    parser.add_argument("--max-convs", type=int, default=0,
                        help="Max conversations (0 = all)")
    parser.add_argument("--conversation-limit", type=int, default=None,
                        help="Alias for --max-convs")
    parser.add_argument("--skip-ingest", action="store_true",
                        help="Skip ingestion, reuse existing vaults")
    parser.add_argument("--resume", action="store_true",
                        help="Resume from checkpoint")
    parser.add_argument("--output", default="",
                        help="Output path (default: auto-generated)")
    parser.add_argument("--vault-prefix", default="bench-",
                        help="Prefix for vault names")
    parser.add_argument("--path", default="",
                        help="Path to data file or directory (overrides default data dir)")
    return parser.parse_args()


def build_ask_mode(config):
    """Map config letter to /ask mode parameter."""
    if config in ("c", "d"):
        return "tiered"
    return "rag"


def main():
    args = parse_args()

    # --conversation-limit is an alias for --max-convs
    if args.conversation_limit is not None and args.max_convs == 0:
        args.max_convs = args.conversation_limit

    data_path = args.path if args.path else None

    if args.benchmark == "longmemeval":
        conversations = list(load_longmemeval(data_path))
    else:
        conversations = list(load_locomo(data_path))

    print(f"Loaded {args.benchmark}: {len(conversations)} conversations")
    if args.max_convs > 0:
        conversations = conversations[:args.max_convs]
        print(f"  Limited to {args.max_convs}")

    auto_extract = args.config in ("b", "d")
    # LoCoMo uses full-context ask mode (load entire document into LLM context)
    # LongMemEval uses the config-prescribed mode (rag/tiered)
    if args.benchmark == "locomo":
        ask_mode = "full"
    else:
        ask_mode = build_ask_mode(args.config)

    # Determine output path
    out_path = args.output or f"results_{args.benchmark}_config{args.config.upper()}.json"
    ckpt_path = out_path + ".checkpoint"

    # Load checkpoint if resuming
    completed_qs = set()
    results = None
    if args.resume:
        results, completed_qs = load_checkpoint(ckpt_path)
        if results:
            print(f"Resumed: {len(completed_qs)} questions already completed")
        else:
            print("No checkpoint found, starting fresh")

    if results is None:
        results = {
            "benchmark": args.benchmark,
            "config": args.config,
            "auto_extract": auto_extract,
            "ask_mode": ask_mode,
            "total_conversations": len(conversations),
            "total_questions": 0,
            "overall_score": 0.0,
            "per_conversation": [],
            "_started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }

    # Progress file path (#528)
    progress_path = f"/tmp/benchmark_progress_{args.benchmark}_{args.config.upper()}.json"

    all_answers = []
    all_ground_truths = []
    question_count = 0

    for idx, (conv_id, turns, questions) in enumerate(conversations):
        vault = f"{args.vault_prefix}{conv_id}"

        # Check if this conversation was already completed
        conv_done = any(
            c.get("conversation") == conv_id
            for c in results.get("per_conversation", [])
        ) if args.resume else False

        if conv_done:
            # Load existing answers/gts for re-scoring
            for c in results["per_conversation"]:
                if c.get("conversation") == conv_id:
                    for d in c.get("details", []):
                        all_answers.append(d.get("answer", "ANSWER_FAILED"))
                        all_ground_truths.append(d.get("ground_truth", ""))
                        question_count += 1
            print(f"[{idx+1}/{len(conversations)}] {conv_id} — skipped (already completed)")
            continue

        print(f"\n[{idx+1}/{len(conversations)}] {conv_id} ({len(turns)} turns, {len(questions)} questions)")

        # Ingest — different strategies for LoCoMo vs LongMemEval
        if not args.skip_ingest:
            print(f"  Ingesting into vault '{vault}'...")
            if args.benchmark == "locomo":
                # LoCoMo: single document per conversation, full-context ask mode (#526)
                ingest_document(conv_id, turns, vault, auto_extract=auto_extract)
            else:
                # LongMemEval: session-based ingest, RAG ask mode (unchanged)
                ingest_conversation(conv_id, turns, vault, auto_extract=auto_extract)
            wait_for_indexing(vault=vault)
            if auto_extract:
                time.sleep(3)
        else:
            print(f"  Skipping ingest (vault '{vault}' must already exist)")

        # Answer questions
        conv_answers = []
        conv_gts = []
        conv_details = []

        for q_idx, q_item in enumerate(questions):
            if isinstance(q_item, dict):
                question = q_item.get("question", q_item.get("query", ""))
                ground_truth = q_item.get("answer", q_item.get("ground_truth", ""))
            else:
                question = str(q_item)
                ground_truth = ""

            if not question:
                continue

            # Check if this specific question was already answered
            if (conv_id, q_idx) in completed_qs:
                print(f"  [{q_idx+1}/{len(questions)}] Q — skipped (checkpointed)")
                continue

            print(f"  [{q_idx+1}/{len(questions)}] Q: {question[:80]}...")
            answer = answer_with_retry(question, vault, mode=ask_mode)

            conv_answers.append(answer)
            conv_gts.append(ground_truth)
            all_answers.append(answer)
            all_ground_truths.append(ground_truth)
            question_count += 1

            # Score
            if args.benchmark == "longmemeval":
                score = score_longmemeval(answer, ground_truth)
            else:
                score = score_locomo(answer, ground_truth)

            conv_details.append({
                "question": question,
                "answer": answer,
                "ground_truth": ground_truth,
                "score": round(score, 4),
            })
            print(f"    Score: {score:.3f}")

            # Checkpoint after every question (#527): save results immediately
            # so nothing is lost if the process is killed between questions.
            results["total_questions"] = question_count
            results["_in_progress"] = {
                "conversation": conv_id,
                "details": conv_details,
            }
            save_checkpoint(ckpt_path, results)

            # Progress file every N questions (#528)
            if question_count % PROGRESS_INTERVAL == 0:
                running_scores = [d["score"] for d in conv_details if d.get("score") is not None]
                avg_score = sum(running_scores) / len(running_scores) if running_scores else 0.0
                total_qs = sum(len(c.get("details", [])) for c in results.get("per_conversation", []))
                total_qs += len(conv_details)
                progress = {
                    "benchmark": args.benchmark,
                    "config": args.config.upper(),
                    "started_at": results.get("_started_at", time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())),
                    "questions_completed": question_count,
                    "questions_total": total_questions_estimate(conversations, idx),
                    "avg_score": round(avg_score, 4),
                    "current_question": question[:100],
                    "last_updated": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                }
                write_progress(progress_path, progress)

        # Compute per-conversation score
        if conv_answers:
            if args.benchmark == "longmemeval":
                conv_score = sum(
                    score_longmemeval(a, gt) for a, gt in zip(conv_answers, conv_gts)
                ) / len(conv_answers)
            else:
                conv_score = sum(
                    score_locomo(a, gt) for a, gt in zip(conv_answers, conv_gts)
                ) / len(conv_answers)
        else:
            conv_score = 0.0

        conv_result = {
            "conversation": conv_id if conv_id else f"conv-{idx}",
            "questions": len(conv_answers),
            "score": round(conv_score, 4),
            "details": conv_details,
        }
        results["per_conversation"].append(conv_result)
        results.pop("_in_progress", None)
        print(f"  Conversation score: {conv_score:.3f} ({len(conv_answers)} questions)")

        # Checkpoint after each conversation (clears _in_progress)
        results["total_questions"] = question_count
        save_checkpoint(ckpt_path, results)

    # Final overall score
    if all_answers:
        if args.benchmark == "longmemeval":
            overall = sum(
                score_longmemeval(a, gt) for a, gt in zip(all_answers, all_ground_truths)
            ) / len(all_answers)
        else:
            overall = sum(
                score_locomo(a, gt) for a, gt in zip(all_answers, all_ground_truths)
            ) / len(all_answers)
    else:
        overall = 0.0

    results["total_questions"] = len(all_answers)
    results["overall_score"] = round(overall, 4)

    # Print results
    print(f"\n{'='*50}")
    print(f"Benchmark:       {args.benchmark}")
    print(f"Configuration:   {args.config.upper()}")
    print(f"Ask mode:        {ask_mode}")
    print(f"Auto-extract:    {auto_extract}")
    print(f"Conversations:   {len(conversations)}")
    print(f"Total questions: {len(all_answers)}")
    print(f"Overall score:   {overall:.4f}")
    print(f"{'='*50}\n")
    print(json.dumps(results, indent=2))

    # Write final output
    save_checkpoint(out_path, results)
    # Remove checkpoint file on success
    if os.path.exists(ckpt_path):
        os.remove(ckpt_path)
    print(f"\nResults written to {out_path}")


if __name__ == "__main__":
    main()
