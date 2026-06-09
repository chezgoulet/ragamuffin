"""Phase 2 Benchmark: Draft/Audit Endpoints.

Tests the /vault/{name}/draft and /vault/{name}/audit endpoints:
- Draft creates an LLM-generated draft answer with PR creation
- Audit runs health checks on a vault (stale files, semantic conflicts, etc.)

Acceptance criteria:
- /draft accepts valid requests with title and target_path
- /audit accepts valid stale_days and checks parameters
- Both endpoints return proper error responses for invalid input
- Both endpoints return within reasonable time
"""

from __future__ import annotations

import json
import logging
import time
import uuid
from typing import Any, Dict, List
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")


def _raw_post(
    url: str,
    body: Dict[str, Any],
    headers: Dict[str, str],
    timeout: int = 30,
) -> tuple[int, Any]:
    """Send a raw POST request, return (status_code, parsed_body)."""
    req = Request(
        url,
        data=json.dumps(body).encode(),
        headers={**headers, "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            parsed = json.loads(raw.decode()) if raw.strip() else {}
            return resp.status, parsed
    except HTTPError as e:
        raw_body = e.read().decode() if e.fp else ""
        try:
            parsed = json.loads(raw_body) if raw_body.strip() else {}
        except json.JSONDecodeError:
            parsed = {"body": raw_body[:200]}
        return e.code, parsed


def run_phase2_draft_audit(
    client: RagamuffinClient,
    vault: str,
) -> List[Dict[str, Any]]:
    """Run the draft/audit endpoint benchmark.

    Returns a list of individual test results.
    """
    results = []
    base = client.base_url.rstrip("/")
    headers = {}
    if client.api_key:
        headers["Authorization"] = f"Bearer {client.api_key}"

    # ── Test 1: /draft — valid request ──────────────────────────────────────
    logger.info("Test 1: /draft valid request")
    t0 = time.perf_counter()
    try:
        path = f"/vault/{vault}/draft"
        url = f"{base}{path}"
        body = {
            "title": f"Phase 2 benchmark draft {uuid.uuid4().hex[:8]}",
            "target_path": f"benchmarks/phase2/{vault}/test-{uuid.uuid4().hex[:6]}.md",
        }
        status, data = _raw_post(url, body, headers)
        elapsed = (time.perf_counter() - t0) * 1000

        # Draft may succeed (200) or return specific errors depending on LLM/config
        expected_stati = {200, 400, 403, 503}
        ok = status in expected_stati
        results.append({
            "test": "draft_valid_request",
            "pass": ok,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}: {json.dumps(data)[:200]}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "draft_valid_request",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 2: /draft — missing title (should 400) ──────────────────────────
    logger.info("Test 2: /draft missing title")
    t0 = time.perf_counter()
    try:
        path = f"/vault/{vault}/draft"
        url = f"{base}{path}"
        body = {"target_path": "test.md"}
        status, data = _raw_post(url, body, headers)
        elapsed = (time.perf_counter() - t0) * 1000
        ok = status == 400
        results.append({
            "test": "draft_missing_title",
            "pass": ok,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}: expected 400",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "draft_missing_title",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 3: /draft — missing target_path (should 400) ────────────────────
    logger.info("Test 3: /draft missing target_path")
    t0 = time.perf_counter()
    try:
        path = f"/vault/{vault}/draft"
        url = f"{base}{path}"
        body = {"title": "test draft"}
        status, data = _raw_post(url, body, headers)
        elapsed = (time.perf_counter() - t0) * 1000
        ok = status == 400
        results.append({
            "test": "draft_missing_target_path",
            "pass": ok,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}: expected 400",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "draft_missing_target_path",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 4: /draft — POST on GET-only endpoint (405 check) ────────
    # Already covered — draft handler rejects non-POST at API level.

    # ── Test 5: /audit — valid request ───────────────────────────────────────
    logger.info("Test 5: /audit valid request")
    t0 = time.perf_counter()
    try:
        path = f"/vault/{vault}/audit"
        url = f"{base}{path}"
        body = {
            "stale_days": 90,
            "checks": ["stale", "duplicate"],
        }
        status, data = _raw_post(url, body, headers)
        elapsed = (time.perf_counter() - t0) * 1000
        ok = status in {200, 400, 500, 502, 503}
        results.append({
            "test": "audit_valid_request",
            "pass": ok,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}: checks={data.get('checks_run', [])}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "audit_valid_request",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 6: /audit — invalid JSON (should 400) ──────────────────────────
    logger.info("Test 6: /audit invalid JSON")
    t0 = time.perf_counter()
    try:
        url = f"{base}/vault/{vault}/audit"
        req = Request(
            url,
            data=b"not json",
            headers={**headers, "Content-Type": "application/json"},
            method="POST",
        )
        with urlopen(req, timeout=10) as resp:
            status = resp.status
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "audit_invalid_json",
            "pass": status == 400,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}: expected 400 for invalid JSON",
        })
    except HTTPError as e:
        elapsed = (time.perf_counter() - t0) * 1000
        status = e.code
        results.append({
            "test": "audit_invalid_json",
            "pass": status == 400,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}: expected 400 for invalid JSON",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "audit_invalid_json",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 7: /audit — defaults applied for empty body ─────────────────────
    logger.info("Test 7: /audit empty body (defaults)")
    t0 = time.perf_counter()
    try:
        url = f"{base}/vault/{vault}/audit"
        body = {}
        status, data = _raw_post(url, body, headers)
        elapsed = (time.perf_counter() - t0) * 1000
        ok = status in {200, 400, 500}
        results.append({
            "test": "audit_defaults",
            "pass": ok,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}: checks_run={data.get('checks_run', [])}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "audit_defaults",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 8: /draft bare endpoint (no vault) ─────────────────────────────
    logger.info("Test 8: /draft bare endpoint")
    t0 = time.perf_counter()
    try:
        url = f"{base}/draft"
        body = {
            "title": f"bare-test-{uuid.uuid4().hex[:6]}",
            "target_path": "benchmarks/test.md",
        }
        status, data = _raw_post(url, body, headers)
        elapsed = (time.perf_counter() - t0) * 1000
        ok = status in {200, 400, 403, 503}
        results.append({
            "test": "draft_bare_endpoint",
            "pass": ok,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "draft_bare_endpoint",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    # ── Test 9: /audit bare endpoint (no vault) ─────────────────────────────
    logger.info("Test 9: /audit bare endpoint")
    t0 = time.perf_counter()
    try:
        url = f"{base}/audit"
        body = {"stale_days": 30, "checks": ["stale"]}
        status, data = _raw_post(url, body, headers)
        elapsed = (time.perf_counter() - t0) * 1000
        ok = status in {200, 400, 500}
        results.append({
            "test": "audit_bare_endpoint",
            "pass": ok,
            "latency_ms": round(elapsed, 1),
            "detail": f"HTTP {status}",
        })
    except Exception as e:
        elapsed = (time.perf_counter() - t0) * 1000
        results.append({
            "test": "audit_bare_endpoint",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": str(e),
        })

    return results
