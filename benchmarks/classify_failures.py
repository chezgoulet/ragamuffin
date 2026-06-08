#!/usr/bin/env python3
"""
Workflow D — Benchmark Failure Classification.

Reads trace data, compare results, and benchmark results from a failed
benchmark gauntlet run and creates actionable GitHub issues.

Usage:
    python3 benchmarks/classify_failures.py \\
        --traces /tmp/gauntlet_traces.jsonl \\
        --compare /tmp/compare_result.json \\
        --results /tmp/gauntlet_results.json \\
        --repo chezgoulet/ragamuffin \\
        --run-id 12345678
"""

import argparse
import json
import os
import sys
from collections import defaultdict


def load_traces(path):
    traces = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                traces.append(json.loads(line))
    return traces


def classify(traces, compare, results, repo, run_id):
    issues = []

    # ── Pattern 1: Per-type regression ────────────────────────────────────
    regressions = compare.get("regressions", {})
    for qtype, data in regressions.items():
        body = (
            f"## Benchmark Regression: {qtype}\n\n"
            f"**Baseline accuracy:** {data.get('baseline', 0):.1%}\n"
            f"**Current accuracy:** {data.get('current', 0):.1%}\n"
            f"**Drop:** {data.get('diff_pp', 0):.1f} percentage points\n\n"
            f"### Affected questions\n\n"
        )
        sample_count = 0
        for t in traces:
            if str(t.get("question_type", "")) == qtype and sample_count < 3:
                body += f"```json\n{json.dumps(t, indent=2)}\n```\n\n"
                sample_count += 1

        body += f"\n[CI Run #{run_id}](https://github.com/{repo}/actions/runs/{run_id})\n"
        issues.append({
            "title": f"Benchmark regression: {qtype} accuracy dropped {data.get('diff_pp', 0):.1f}pp",
            "body": body,
            "labels": ["benchmark"],
        })

    # ── Pattern 2: Ingest failures ────────────────────────────────────────
    total_convs = results.get("total_conversations", 0)
    conv_results = results.get("per_conversation", [])
    ingested = len(conv_results)
    ingest_failures = total_convs - ingested
    if total_convs > 0 and (ingest_failures / total_convs) > 0.01:
        issues.append({
            "title": f"Ingest reliability: {ingest_failures}/{total_convs} conversations failed to ingest",
            "body": (
                f"## Ingest Failures\n\n"
                f"**Total conversations:** {total_convs}\n"
                f"**Ingested:** {ingested}\n"
                f"**Failed:** {ingest_failures} ({ingest_failures/total_convs:.1%})\n\n"
                "Threshold of 1% exceeded. Check CI logs for rate limits, encoding errors, and timeouts.\n\n"
                f"[CI Run #{run_id}](https://github.com/{repo}/actions/runs/{run_id})\n"
            ),
            "labels": ["bug"],
        })

    # ── Pattern 3: Vault contamination ────────────────────────────────────
    # Same question type errors clustering in specific vaults
    vault_errors = defaultdict(lambda: defaultdict(int))
    for t in traces:
        qt = t.get("question_type", "unknown")
        if t.get("error"):
            vault_errors[qt]["errors"] += 1
    for qt, counts in vault_errors.items():
        if counts.get("errors", 0) > 3:
            issues.append({
                "title": f"Vault contamination suspected: {qt} errors cluster",
                "body": (
                    f"## Vault Contamination\n\n"
                    f"**Question type:** {qt}\n"
                    f"**Error count:** {counts.get('errors', 0)}\n"
                    f"**Sample:**\n"
                ),
                "labels": ["bug"],
            })

    # ── Pattern 4: Timeout rate ───────────────────────────────────────────
    timeout_count = sum(1 for t in traces if t.get("latency_ms", 0) >= 30000)
    total_questions = len(traces)
    if total_questions > 0 and (timeout_count / total_questions) > 0.05:
        issues.append({
            "title": f"Capacity/timeout: {timeout_count}/{total_questions} requests exceeded 30s",
            "body": (
                f"## High Timeout Rate\n\n"
                f"**Total questions:** {total_questions}\n"
                f"**Timed out (>30s):** {timeout_count} ({timeout_count/total_questions:.1%})\n\n"
                "Threshold of 5% exceeded. May indicate capacity issues.\n\n"
                f"[CI Run #{run_id}](https://github.com/{repo}/actions/runs/{run_id})\n"
            ),
            "labels": ["bug"],
        })

    # ── Pattern 5: Synthesis failure ──────────────────────────────────────
    # System answered incorrectly despite retrieving correct chunks
    # (Heuristic: answer contains "ANSWER_FAILED" or "cannot answer" or "not provided")
    synthesis_failures = []
    for t in traces:
        answer = t.get("ragamuffin_answer", "").lower()
        if not t.get("error") and any(phrase in answer for phrase in [
            "answer_failed", "cannot answer", "not provided", "no context",
            "i don't know", "i'm not sure", "unable to answer",
        ]):
            synthesis_failures.append(t)

    if len(synthesis_failures) > 3:
        samples = synthesis_failures[:3]
        sample_text = "\n\n".join(
            f"```json\n{json.dumps(s, indent=2)}\n```" for s in samples
        )
        issues.append({
            "title": f"Synthesis failure: {len(synthesis_failures)} questions answered 'cannot answer' despite retrieval",
            "body": (
                f"## Synthesis Failures\n\n"
                f"**Affected questions:** {len(synthesis_failures)}\n\n"
                f"The system retrieved chunks but the LLM refused to answer. "
                f"This suggests a synthesis prompt issue or context-window problem.\n\n"
                f"### Samples\n\n{sample_text}\n\n"
                f"[CI Run #{run_id}](https://github.com/{repo}/actions/runs/{run_id})\n"
            ),
            "labels": ["bug"],
        })

    # ── Pattern 6: New error modes ─────────────────────────────────────────
    # Track distinct error strings
    error_strings = defaultdict(int)
    for t in traces:
        err = t.get("error", "")
        if err:
            # Normalize by truncating at first colon
            err_key = err.split(":")[0]
            error_strings[err_key] += 1

    # Check for new errors
    known_errors = {"POST /ask", "POST /vault", "ConnectionError"}
    for err_key, count in error_strings.items():
        if err_key not in known_errors and count > 1:
            issues.append({
                "title": f"New error mode: '{err_key}' appears {count} times",
                "body": (
                    f"## New Error Mode Detected\n\n"
                    f"**Error pattern:** {err_key}\n"
                    f"**Count:** {count} occurrences\n\n"
                    f"This error pattern was not seen in previous runs.\n\n"
                    f"[CI Run #{run_id}](https://github.com/{repo}/actions/runs/{run_id})\n"
                ),
                "labels": ["bug"],
            })

    return issues


def create_github_issue(title, body, labels, repo, token):
    """Create a GitHub issue via API."""
    import urllib.request

    url = f"https://api.github.com/repos/{repo}/issues"
    data = json.dumps({"title": title, "body": body, "labels": labels}).encode()
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Authorization", f"token {token}")
    req.add_header("Content-Type", "application/json")

    try:
        resp = urllib.request.urlopen(req)
        result = json.loads(resp.read())
        print(f"  Created issue #{result['number']}: {result['html_url']}")
        return result
    except urllib.error.HTTPError as e:
        print(f"  Failed to create issue: {e.code} {e.read().decode()[:200]}", file=sys.stderr)
        return None


def main():
    parser = argparse.ArgumentParser(description="Classify benchmark failures")
    parser.add_argument("--traces", required=True, help="Path to traces JSONL")
    parser.add_argument("--compare", required=True, help="Path to compare_result.json")
    parser.add_argument("--results", required=True, help="Path to gauntlet_results.json")
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY", "chezgoulet/ragamuffin"))
    parser.add_argument("--run-id", default=os.environ.get("GITHUB_RUN_ID", "0"))
    parser.add_argument("--dry-run", action="store_true", help="Print issues without creating them")
    args = parser.parse_args()

    traces = load_traces(args.traces)

    with open(args.compare) as f:
        compare = json.load(f)

    with open(args.results) as f:
        results = json.load(f)

    issues = classify(traces, compare, results, args.repo, args.run_id)

    if not issues:
        print("No issues to create.")
        return

    print(f"Classified {len(issues)} issues:\n")

    if args.dry_run:
        for issue in issues:
            print(f"  [{', '.join(issue['labels'])}] {issue['title']}")
        return

    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        print("GITHUB_TOKEN not set, skipping issue creation", file=sys.stderr)
        return

    for issue in issues:
        create_github_issue(
            title=issue["title"],
            body=issue["body"],
            labels=issue["labels"],
            repo=args.repo,
            token=token,
        )


if __name__ == "__main__":
    main()
