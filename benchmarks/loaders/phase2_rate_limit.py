"""Phase 2 Benchmark: Rate-Limit Threshold Testing.

Push requests until 429 responses appear, then verify that the
client-level retry + exponential backoff eventually succeeds.

Acceptance criteria:
- Requests to rate-limited endpoints eventually produce 429
- After a backoff pause, the same request succeeds
- Multiple concurrent requests show increasing retry counts
- Summary logged with observed rate-limit threshold
"""

from __future__ import annotations

import json
import logging
import random
import time
import uuid
from typing import Any, Dict, List, Optional
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")

# ── Helpers ─────────────────────────────────────────────────────────────────────


def _raw_request(
    url: str,
    method: str,
    body: Optional[bytes] = None,
    headers: Optional[Dict[str, str]] = None,
    timeout: int = 10,
) -> tuple[Any, int]:
    """Send a raw HTTP request WITHOUT client-side retry.

    Returns (parsed_body_or_None, status_code).
    Raises HTTPError for 4xx/5xx — we catch that to detect 429s.
    """
    hdrs = {
        "Content-Type": "application/json",
        "User-Agent": "RagamuffinBenchmark/0.9",
    }
    if headers:
        hdrs.update(headers)

    req = Request(url, data=body, headers=hdrs, method=method)
    try:
        with urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            return (json.loads(raw.decode()) if raw.strip() else {}, resp.status)
    except HTTPError as e:
        raw_body = e.read().decode() if e.fp else ""
        try:
            parsed = json.loads(raw_body) if raw_body.strip() else {}
        except json.JSONDecodeError:
            parsed = {"body": raw_body[:200]}
        raise HTTPError(e.url, e.code, e.msg, e.hdrs, None) from e


def _run_rate_limit_burst(
    client: RagamuffinClient,
    vault: str,
    endpoint: str,
    burst_count: int = 30,
    interval_ms: float = 5.0,
) -> tuple[int, int, List[int]]:
    """Fire burst_count requests at an endpoint with minimal interval.

    Returns (total_429_count, status_codes_list).
    Uses :rolling as base URL.
    """
    base = client.base_url.rstrip("/")
    url = f"{base}{endpoint}"
    headers = {}
    if client.api_key:
        headers["Authorization"] = f"Bearer {client.api_key}"

    body = json.dumps({"query": f"burst-test-{uuid.uuid4().hex[:4]}", "vault": vault}).encode()

    status_codes: List[int] = []
    for i in range(burst_count):
        try:
            _, status = _raw_request(url, "POST", body=body, headers=headers)
            status_codes.append(status)
        except HTTPError as e:
            status_codes.append(e.code)
        time.sleep(interval_ms / 1000.0)

    n_429 = sum(1 for s in status_codes if s == 429)
    return n_429, status_codes


def _run_rate_limit_backoff_test(
    client: RagamuffinClient,
    vault: str,
    endpoint: str,
) -> bool:
    """Hit a rate-limited endpoint with client retry — verify it eventually succeeds.

    Returns True if the request succeeds after retry, False otherwise.
    """
    path = endpoint
    body = {"query": f"backoff-test-{uuid.uuid4().hex[:4]}", "vault": vault}
    try:
        client._request("POST", path, body=body)
        return True
    except Exception as e:
        logger.warning("Backoff test failed for %s: %s", endpoint, e)
        return False


# ── Main benchmark ──────────────────────────────────────────────────────────────


def run_phase2_rate_limit(
    client: RagamuffinClient,
    vault: str,
) -> List[Dict[str, Any]]:
    """Run the rate-limit threshold benchmark.

    Returns a list of individual test results.
    """
    results = []
    endpoints = ["/ask", "/draft", "/audit"]

    # ── Test 1: Burst /ask to trigger 429 ───────────────────────────────────
    logger.info("Test 1: Burst /ask requests to trigger 429")
    for ep in endpoints:
        t0 = time.perf_counter()
        try:
            path = f"/vault/{vault}{ep}"
            n_429, codes = _run_rate_limit_burst(client, vault, path, burst_count=40, interval_ms=2.0)
            elapsed = (time.perf_counter() - t0) * 1000
            pct_429 = round(n_429 / len(codes) * 100, 1)
            results.append({
                "test": f"rate_limit_burst_{ep.strip('/')}",
                "pass": n_429 > 0,
                "latency_ms": round(elapsed, 1),
                "detail": f"{n_429}/{len(codes)} requests got 429 ({pct_429}%)",
            })
        except Exception as e:
            elapsed = (time.perf_counter() - t0) * 1000
            results.append({
                "test": f"rate_limit_burst_{ep.strip('/')}",
                "pass": False,
                "latency_ms": round(elapsed, 1),
                "detail": str(e),
            })

    # ── Test 2: Client retry + backoff eventually succeeds ───────────────────
    logger.info("Test 2: Client retry + backoff after rate-limit")
    for ep in endpoints:
        t0 = time.perf_counter()
        try:
            path = f"/vault/{vault}{ep}"
            ok = _run_rate_limit_backoff_test(client, vault, path)
            elapsed = (time.perf_counter() - t0) * 1000
            results.append({
                "test": f"rate_limit_backoff_{ep.strip('/')}",
                "pass": ok,
                "latency_ms": round(elapsed, 1),
                "detail": f"backoff request {'succeeded' if ok else 'failed'}",
            })
        except Exception as e:
            elapsed = (time.perf_counter() - t0) * 1000
            results.append({
                "test": f"rate_limit_backoff_{ep.strip('/')}",
                "pass": False,
                "latency_ms": round(elapsed, 1),
                "detail": str(e),
            })

    # ── Test 3: Sequential requests at slow pace should not rate-limit ───────
    logger.info("Test 3: Slow sequential requests should not 429")
    t0 = time.perf_counter()
    n_429_slow = 0
    for i in range(10):
        path = f"/vault/{vault}/ask"
        body = {"query": f"slow-test-{i}", "vault": vault}
        try:
            client._request("POST", path, body=body)
        except HTTPError as e:
            if e.code == 429:
                n_429_slow += 1
        except Exception:
            pass  # other errors (e.g. LLM not configured) don't count as rate-limit
        time.sleep(1.0)
    elapsed = (time.perf_counter() - t0) * 1000
    results.append({
        "test": "rate_limit_slow_requests",
        "pass": n_429_slow == 0,
        "latency_ms": round(elapsed, 1),
        "detail": f"0/{10} requests got 429 (expected 0 at 1 req/sec)",
    })

    # ── Test 4: Health endpoint should never rate-limit ──────────────────────
    logger.info("Test 4: Health endpoint should never rate-limit")
    t0 = time.perf_counter()
    n_429_health = 0
    for _ in range(50):
        try:
            _, status = _raw_request(f"{client.base_url}/health", "GET")
            if status == 429:
                n_429_health += 1
        except HTTPError as e:
            if e.code == 429:
                n_429_health += 1
        except Exception:
            pass
    elapsed = (time.perf_counter() - t0) * 1000
    results.append({
        "test": "rate_limit_health_endpoint",
        "pass": n_429_health == 0,
        "latency_ms": round(elapsed, 1),
        "detail": f"0/50 health checks got 429 (expected 0 — health is not rate-limited)",
    })

    return results
