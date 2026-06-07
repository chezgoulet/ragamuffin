#!/usr/bin/env python3
"""
Ragamuffin benchmark harness.
Runs LongMemEval or LoCoMo against a local Ragamuffin instance.
"""

import argparse
import json
import os
import sys
import time
import urllib.parse

import requests

# ── Config ──────────────────────────────────────────────────────────────────

RAGAMUFFIN_URL = os.environ.get("RAGAMUFFIN_URL", "http://localhost:8000")
GPT4O_MINI_KEY = os.environ.get("OPENAI_API_KEY", "")
GPT4O_MINI_MODEL = "gpt-4o-mini"
EVALUATOR_MAX_TOKENS = 512

BENCH_DIR = os.path.dirname(os.path.abspath(__file__))
DATA_DIR = os.path.join(BENCH_DIR, "data")

# ── API helpers ─────────────────────────────────────────────────────────────


def api_post(path, json_body, description=None):
    url = f"{RAGAMUFFIN_URL}{path}"
    r = requests.post(url, json=json_body, timeout=30)
    if r.status_code >= 400:
        print(f"  [WARN] POST {path} returned {r.status_code}: {r.text[:200]}", file=sys.stderr)
    r.raise_for_status()
    return r.json()


def api_get(path, params=None):
    url = f"{RAGAMUFFIN_URL}{path}"
    r = requests.get(url, params=params, timeout=30)
    if r.status_code >= 400:
        print(f"  [WARN] GET {path} returned {r.status_code}: {r.text[:200]}", file=sys.stderr)
    r.raise_for_status()
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
    raise TimeoutError(f"Indexing did not complete within {timeout_sec}s")


def recall_query(query, top_k=10, detail="l2"):
    return api_post("/recall", {"query": query, "top_k": top_k, "detail": detail})


def list_facts(prefix=""):
    return api_get("/v1/facts", params={"prefix": prefix})


def get_chunk(chunk_id):
    return api_get(f"/v1/chunks/{chunk_id}")


def get_fact_graph(fact_key, depth=2):
    return api_get(f"/v1/facts/{urllib.parse.quote(fact_key, safe='')}/graph",
                   params={"depth": depth})


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


# ── Evaluator LLM ───────────────────────────────────────────────────────────


def call_evaluator_llm(prompt):
    """Call GPT-4o-mini with a prompt, return response text."""
    if not GPT4O_MINI_KEY:
        raise RuntimeError("OPENAI_API_KEY required for evaluator LLM")
    r = requests.post(
        "https://api.openai.com/v1/chat/completions",
        headers={
            "Authorization": f"Bearer {GPT4O_MINI_KEY}",
            "Content-Type": "application/json",
        },
        json={
            "model": GPT4O_MINI_MODEL,
            "messages": [{"role": "user", "content": prompt}],
            "max_tokens": EVALUATOR_MAX_TOKENS,
            "temperature": 0.0,
        },
        timeout=30,
    )
    r.raise_for_status()
    return r.json()["choices"][0]["message"]["content"]


# ── Recall → Answer pipeline ────────────────────────────────────────────────


def build_answer_prompt(question, context_chunks, facts=None, graph_info=None):
    """Format retrieved context into a prompt for the evaluator LLM."""
    lines = ["You are answering questions based on a conversation history."]
    lines.append("")
    lines.append("=== Retrieved Chunks ===")
    for i, c in enumerate(context_chunks):
        text = c.get("text", c.get("first_paragraph", ""))
        lines.append(f"[{i+1}] {text}")
    if facts:
        lines.append("")
        lines.append("=== Retrieved Facts ===")
        for f in facts:
            lines.append(f"- {f.get('key', '?')}: {f.get('value', '?')}")
    if graph_info:
        lines.append("")
        lines.append("=== Fact Relationships ===")
        for edge in graph_info.get("edges", []):
            lines.append(f"  {edge['source']} --{edge['relationship']}--> {edge['target']}")
    lines.append("")
    lines.append(f"Question: {question}")
    lines.append("")
    lines.append("Answer concisely. If the information is not present, say 'I don't know'.")
    return "\n".join(lines)


def answer_from_context(question, config, vault=""):
    """
    Retrieves context using the given config, then calls evaluator LLM.
    Returns (answer_text, context_summary).
    """
    config = config.lower()
    context_chunks = []
    facts = []
    graph_info = None

    if config in ("a", "c"):
        # Pure recall or tiered recall
        if config == "c":
            result = recall_query(question, top_k=10, detail="l0")
        else:
            result = recall_query(question, top_k=10, detail="l2")

        results_list = result.get("results", [])
        for r_item in results_list:
            if config == "c":
                # Tiered: get only chunk_id then fetch full
                cid = r_item.get("chunk_id", "")
                if cid:
                    chunk = get_chunk(cid)
                    context_chunks.append(chunk)
            else:
                context_chunks.append(r_item)

    elif config in ("b", "d"):
        # Recall + Facts
        result = recall_query(question, top_k=10, detail="l2")
        context_chunks = result.get("results", [])
        # Get facts for this vault
        facts_resp = list_facts(prefix=vault)
        facts = facts_resp.get("entries", [])
        if config == "d":
            # Full stack: also get fact graphs for top facts
            for f in facts[:3]:
                fk = f.get("key", "")
                if fk:
                    try:
                        graph_info = get_fact_graph(fk, depth=2)
                    except requests.HTTPError:
                        pass

    prompt = build_answer_prompt(question, context_chunks, facts, graph_info)
    answer = call_evaluator_llm(prompt)
    return answer, {"chunks": len(context_chunks), "facts": len(facts), "graph": graph_info is not None}


# ── Data loaders ────────────────────────────────────────────────────────────


def load_longmemeval():
    """
    Yield (conversation_id, turns, questions) for each conversation
    in the LongMemEval "S" (shorter) setting.
    """
    data_path = os.path.join(DATA_DIR, "LongMemEval", "data")
    s_path = os.path.join(data_path, "S")
    if not os.path.isdir(s_path):
        # Try alternative paths
        for p in [os.path.join(data_path, "setting_S"),
                  os.path.join(data_path, "short")]:
            if os.path.isdir(p):
                s_path = p
                break

    conv_files = sorted([
        os.path.join(s_path, f)
        for f in os.listdir(s_path)
        if f.endswith(".json") and not f.startswith(".")
    ])

    if not conv_files:
        # Flat JSON in data root
        conv_files = sorted([
            os.path.join(DATA_DIR, "LongMemEval", f)
            for f in os.listdir(os.path.join(DATA_DIR, "LongMemEval"))
            if f.endswith(".json") and not f.startswith(".")
        ])

    for cf in conv_files:
        with open(cf) as fh:
            data = json.load(fh)

        conv_id = data.get("conversation_id", os.path.splitext(os.path.basename(cf))[0])
        turns = data.get("turns", data.get("history", data.get("conversation", [])))
        questions = data.get("questions", data.get("qa_pairs", data.get("evaluation", [])))
        yield conv_id, turns, questions


def load_locomo():
    """
    Yield (conversation_id, turns, questions) for each conversation
    in the LoCoMo (Backboard) dataset. Excludes category 5 (adversarial).
    """
    data_path = os.path.join(DATA_DIR, "Backboard-Locomo-Benchmark")
    conv_path = os.path.join(data_path, "conversations")

    if not os.path.isdir(conv_path):
        # Try flat structure
        conv_path = data_path

    conv_dirs = sorted([
        d for d in os.listdir(conv_path)
        if os.path.isdir(os.path.join(conv_path, d)) and not d.startswith(".")
    ])

    for cd in conv_dirs:
        # Check category — skip adversarial (cat 5)
        cat_file = os.path.join(conv_path, cd, "category.json")
        if os.path.exists(cat_file):
            with open(cat_file) as fh:
                cat_data = json.load(fh)
            if isinstance(cat_data, dict) and cat_data.get("category") in (5, "5"):
                continue

        # Load turns
        turns = []
        turn_file = os.path.join(conv_path, cd, "turns.json")
        if os.path.exists(turn_file):
            with open(turn_file) as fh:
                turns = json.load(fh)

        # Load QA pairs
        questions = []
        qa_file = os.path.join(conv_path, cd, "qa.json")
        if os.path.exists(qa_file):
            with open(qa_file) as fh:
                questions = json.load(fh)

        if not turns and not questions:
            # Single file per conversation
            conv_file = os.path.join(conv_path, f"{cd}.json")
            if os.path.exists(conv_file):
                with open(conv_file) as fh:
                    data = json.load(fh)
                turns = data.get("turns", data.get("history", data.get("conversation", [])))
                questions = data.get("questions", data.get("qa_pairs", []))

        yield cd, turns, questions


# ── Ingest ──────────────────────────────────────────────────────────────────


def ingest_conversation(conv_id, turns, vault, auto_extract=False):
    """Ingest a conversation into Ragamuffin. Returns session_id."""
    if not turns:
        return None

    # Build content from first turn
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

    # Append remaining turns
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


# ── Scoring ────────────────────────────────────────────────────────────────


def score_longmemeval(answers, ground_truths):
    """Use LongMemEval's evaluation protocol with GPT-4o-mini."""
    correct = 0
    total = 0
    details = []
    for qid, (answer, gt) in enumerate(zip(answers, ground_truths)):
        total += 1
        # Ask evaluator to judge correctness
        eval_prompt = (
            f"Question has ground truth: {gt}\n"
            f"System answered: {answer}\n\n"
            "Is this answer correct? Reply with exactly 'CORRECT' or 'INCORRECT'."
        )
        verdict = call_evaluator_llm(eval_prompt).strip().upper()
        if "CORRECT" in verdict:
            correct += 1
            details.append((qid, True))
        else:
            details.append((qid, False))
    return correct / total if total > 0 else 0.0, details


def score_locomo(answers, ground_truths):
    """Token-level F1 with Porter stemming."""
    # Simplified: use word-overlap F1 for each answer
    import re
    from collections import Counter

    def tokenize(text):
        words = re.findall(r"[a-z0-9']+", text.lower())
        # Porter stemmer approximation — just remove common suffixes
        stemmed = []
        for w in words:
            for suffix in ["ing", "ed", "ly", "es", "s", "ion", "tion", "ment"]:
                if w.endswith(suffix) and len(w) > len(suffix) + 1:
                    w = w[:-len(suffix)]
                    break
            stemmed.append(w)
        return stemmed

    f1_scores = []
    for ans, gt in zip(answers, ground_truths):
        if isinstance(gt, dict):
            gt = gt.get("answer", gt.get("ground_truth", ""))
        a_tokens = tokenize(ans)
        g_tokens = tokenize(gt)
        if not a_tokens and not g_tokens:
            f1_scores.append(1.0)
            continue
        a_counts = Counter(a_tokens)
        g_counts = Counter(g_tokens)
        overlap = sum((a_counts & g_counts).values())
        precision = overlap / len(a_tokens) if a_tokens else 0.0
        recall = overlap / len(g_tokens) if g_tokens else 0.0
        f1 = 2 * precision * recall / (precision + recall) if (precision + recall) > 0 else 0.0
        f1_scores.append(f1)
    return sum(f1_scores) / len(f1_scores) if f1_scores else 0.0, f1_scores


# ── Main ────────────────────────────────────────────────────────────────────


def main():
    parser = argparse.ArgumentParser(description="Ragamuffin benchmark harness")
    parser.add_argument("--benchmark", required=True, choices=["longmemeval", "locomo"],
                        help="Benchmark to run")
    parser.add_argument("--config", required=True, choices=["a", "b", "c", "d"],
                        help="Configuration variant")
    parser.add_argument("--vault-prefix", default="bench-",
                        help="Prefix for vault names")
    parser.add_argument("--skip-ingest", action="store_true",
                        help="Skip ingestion (reuse existing data)")
    parser.add_argument("--max-convs", type=int, default=0,
                        help="Max conversations to process (0 = all)")
    parser.add_argument("--output", default="",
                        help="Path to write results JSON (optional)")
    args = parser.parse_args()

    if not GPT4O_MINI_KEY:
        print("ERROR: OPENAI_API_KEY is required", file=sys.stderr)
        sys.exit(1)

    # Load dataset
    print(f"Loading {args.benchmark}...")
    if args.benchmark == "longmemeval":
        conversations = list(load_longmemeval())
    else:
        conversations = list(load_locomo())

    print(f"  Found {len(conversations)} conversations")
    if args.max_convs > 0:
        conversations = conversations[:args.max_convs]
        print(f"  Limiting to {args.max_convs}")

    auto_extract = args.config in ("b", "d")

    all_answers = []
    all_ground_truths = []
    total_questions = 0
    conv_results = []

    for idx, (conv_id, turns, questions) in enumerate(conversations):
        vault = f"{args.vault_prefix}{conv_id}"
        print(f"\n[{idx+1}/{len(conversations)}] Conversation {conv_id} ({len(turns)} turns, {len(questions)} questions)")

        # Ingest
        if not args.skip_ingest:
            print(f"  Ingesting into vault '{vault}'...")
            ingest_conversation(conv_id, turns, vault, auto_extract=auto_extract)
            wait_for_indexing(vault=vault)
            if auto_extract:
                # Allow extraction pipeline to process
                time.sleep(3)
        else:
            print(f"  Skipping ingest (vault '{vault}' must already exist)")

        # Answer questions
        conv_answers = []
        conv_gts = []
        for q_idx, q_item in enumerate(questions):
            if isinstance(q_item, dict):
                question = q_item.get("question", q_item.get("query", ""))
                ground_truth = q_item.get("answer", q_item.get("ground_truth", ""))
            else:
                question = str(q_item)
                ground_truth = ""

            if not question:
                continue

            print(f"  [{q_idx+1}/{len(questions)}] Q: {question[:80]}...")
            answer, ctx = answer_from_context(question, args.config, vault=vault)
            conv_answers.append(answer)
            conv_gts.append(ground_truth)
            all_answers.append(answer)
            all_ground_truths.append(ground_truth)

        total_questions += len(conv_answers)

        # Score this conversation
        if args.benchmark == "longmemeval":
            score, details = score_longmemeval(conv_answers, conv_gts)
        else:
            score, details = score_locomo(conv_answers, conv_gts)

        conv_results.append({
            "conversation": conv_id,
            "questions": len(conv_answers),
            "score": round(score, 4),
        })
        print(f"  Score: {score:.3f} ({len(conv_answers)} questions)")

    # Overall results
    if args.benchmark == "longmemeval":
        overall_score, _ = score_longmemeval(all_answers, all_ground_truths)
    else:
        overall_score, _ = score_locomo(all_answers, all_ground_truths)

    print(f"\n{'='*50}")
    print(f"Benchmark:      {args.benchmark}")
    print(f"Configuration:  {args.config.upper()}")
    print(f"Conversations:  {len(conversations)}")
    print(f"Total questions: {total_questions}")
    print(f"Overall score:  {overall_score:.4f}")
    print(f"{'='*50}")

    # Write output
    output = {
        "benchmark": args.benchmark,
        "config": args.config,
        "auto_extract": auto_extract,
        "conversations": len(conversations),
        "total_questions": total_questions,
        "overall_score": overall_score,
        "per_conversation": conv_results,
    }

    print(json.dumps(output, indent=2))

    out_path = args.output
    if not out_path:
        out_path = f"results_{args.benchmark}_config{args.config.upper()}.json"
    with open(out_path, "w") as fh:
        json.dump(output, fh, indent=2)
    print(f"\nResults written to {out_path}")


if __name__ == "__main__":
    main()
