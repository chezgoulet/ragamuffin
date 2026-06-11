"""Post comments to GitHub Issues using the GitHub API."""

from __future__ import annotations

import json
import logging
import os
import urllib.request
import urllib.error

logger = logging.getLogger("ragamuffin.benchmark")

API_BASE = "https://api.github.com"


def post_issue_comment(repo: str, issue: int, body: str) -> dict:
    """Post a comment to a GitHub issue."""
    token = os.environ.get("AGENT_GITHUB_TOKEN") or os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if not token:
        raise ValueError("No GitHub token found. Set AGENT_GITHUB_TOKEN, GITHUB_TOKEN, or GH_TOKEN.")

    url = f"{API_BASE}/repos/{repo}/issues/{issue}/comments"
    req = urllib.request.Request(
        url,
        data=json.dumps({"body": body}).encode(),
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "User-Agent": "RagamuffinBenchmark",
        },
        method="POST",
    )

    try:
        resp = urllib.request.urlopen(req, timeout=30)
        data = json.loads(resp.read().decode())
        logger.info("Comment posted: %s", data.get("html_url", url))
        return data
    except urllib.error.HTTPError as e:
        raw = e.read().decode() if e.fp else ""
        raise RuntimeError(f"GitHub API error {e.code}: {raw[:500]}") from e
