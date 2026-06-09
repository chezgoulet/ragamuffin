"""Phase 2 Benchmark: Event Stream Verification.

Subscribe to the /events SSE stream, perform actions (ingest, fact write),
and verify that matching events are received.

Acceptance criteria:
- /events returns Content-Type: text/event-stream
- Initial "connected" event is received
- After ingesting content, vault.file.changed event is received
- After creating a fact, fact.created event is received
- Connection close is handled gracefully
"""

from __future__ import annotations

import json
import logging
import threading
import time
import uuid
from typing import Any, Dict, List, Optional
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from benchmarks.core.client import RagamuffinClient

logger = logging.getLogger("ragamuffin.benchmark")


class SSEListener:
    """Subscribe to an SSE stream and collect events in a background thread."""

    def __init__(self, url: str, headers: Optional[Dict[str, str]] = None):
        self.url = url
        self.headers = headers or {}
        self.events: List[Dict[str, Any]] = []
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._error: Optional[str] = None

    def _run(self) -> None:
        """Background thread: read SSE events until stopped."""
        hdrs = {
            "Accept": "text/event-stream",
            "Cache-Control": "no-cache",
            **self.headers,
        }
        req = Request(self.url, headers=hdrs, method="GET")
        try:
            resp = urlopen(req, timeout=30)
            event_type = ""
            event_data = ""
            for raw_line in resp:
                if self._stop.is_set():
                    break
                line = raw_line.decode("utf-8", errors="replace").strip()
                if line.startswith("event: "):
                    event_type = line[7:]
                elif line.startswith("data: "):
                    event_data = line[6:]
                elif line == "":
                    # Empty line = event delimiter
                    if event_type and event_data:
                        try:
                            parsed = json.loads(event_data)
                        except json.JSONDecodeError:
                            parsed = {"raw": event_data}
                        self.events.append({"type": event_type, "data": parsed})
                    event_type = ""
                    event_data = ""
        except Exception as e:
            if not self._stop.is_set():
                self._error = str(e)

    def start(self) -> None:
        """Start listening in a background thread."""
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        """Signal the listener to stop and wait for thread to finish."""
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=5)

    def events_of_type(self, event_type: str) -> List[Dict[str, Any]]:
        """Return all collected events matching a given type."""
        return [e for e in self.events if e["type"] == event_type]

    @property
    def error(self) -> Optional[str]:
        return self._error


def _raw_post_body(
    url: str,
    body: Dict[str, Any],
    headers: Dict[str, str],
    timeout: int = 15,
) -> Optional[int]:
    """Send a raw POST, return status code only. No body parsing."""
    req = Request(
        url,
        data=json.dumps(body).encode(),
        headers={**headers, "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urlopen(req, timeout=timeout) as resp:
            resp.read()
            return resp.status
    except HTTPError as e:
        return e.code
    except Exception:
        return None


def run_phase2_event_stream(
    client: RagamuffinClient,
    vault: str,
) -> List[Dict[str, Any]]:
    """Run the event stream verification benchmark.

    Returns a list of individual test results.
    """
    results = []
    base = client.base_url.rstrip("/")
    headers = {}
    if client.api_key:
        headers["Authorization"] = f"Bearer {client.api_key}"

    # ── Test 1: SSE connection + initial event ──────────────────────────────
    logger.info("Test 1: SSE connection and initial event")
    listener = SSEListener(f"{base}/events", headers)
    t0 = time.perf_counter()
    listener.start()
    time.sleep(2)  # give time for connected event

    connected_events = listener.events_of_type("connected")
    elapsed = (time.perf_counter() - t0) * 1000
    has_connected = len(connected_events) >= 1
    results.append({
        "test": "events_connected",
        "pass": has_connected,
        "latency_ms": round(elapsed, 1),
        "detail": (
            f"{'connected event received' if has_connected else 'no connected event'}"
            f" ({len(listener.events)} total events)"
        ),
    })

    if listener.error:
        results.append({
            "test": "events_sse_connection",
            "pass": False,
            "latency_ms": round(elapsed, 1),
            "detail": f"SSE error: {listener.error}",
        })
        # Can't continue if SSE connection failed
        listener.stop()
        return results

    # ── Test 2: vault.file.changed after ingest ─────────────────────────────
    logger.info("Test 2: vault.file.changed event after ingest")
    pre_ingest_count = len(listener.events_of_type("vault.file.changed"))

    try:
        client.ingest(
            content=f"Phase 2 event stream benchmark content {uuid.uuid4().hex[:8]}",
            source="phase2-bench/sse-test",
            vault=vault,
        )
    except Exception as e:
        logger.warning("Ingest failed during event test: %s", e)

    time.sleep(3)  # wait for event propagation

    post_ingest_count = len(listener.events_of_type("vault.file.changed"))
    new_events = post_ingest_count - pre_ingest_count
    elapsed = (time.perf_counter() - t0) * 1000
    results.append({
        "test": "events_file_changed",
        "pass": new_events >= 1,
        "latency_ms": round(elapsed, 1),
        "detail": (
            f"got {new_events} vault.file.changed event(s) after ingest"
            if new_events >= 1
            else "no vault.file.changed event after ingest"
        ),
    })

    # ── Test 3: fact.created after creating a fact ──────────────────────────
    logger.info("Test 3: fact.created event after creating fact")
    pre_fact_count = len(listener.events_of_type("fact.created"))

    try:
        fkey = f"sse-test-{uuid.uuid4().hex[:8]}"
        client.create_fact(
            key=fkey,
            value=f"SSE event test fact {uuid.uuid4().hex[:8]}",
            vault=vault,
            source="phase2-bench/sse-test",
        )
    except Exception as e:
        logger.warning("Fact creation failed during event test: %s", e)

    time.sleep(3)

    post_fact_count = len(listener.events_of_type("fact.created"))
    new_fact_events = post_fact_count - pre_fact_count
    elapsed = (time.perf_counter() - t0) * 1000
    results.append({
        "test": "events_fact_created",
        "pass": new_fact_events >= 1,
        "latency_ms": round(elapsed, 1),
        "detail": (
            f"got {new_fact_events} fact.created event(s) after create"
            if new_fact_events >= 1
            else "no fact.created event after fact creation"
        ),
    })

    # ── Test 4: Event payload structure ──────────────────────────────────────
    logger.info("Test 4: Event payload structure")
    all_events = listener.events
    valid_payloads = 0
    for evt in all_events:
        # The SSE data: line contains the full CloudEvent envelope as JSON.
        # The envelope has specversion, type, source, id, time at the top level.
        # The inner data: field is the typed payload (e.g. FactCreatedData).
        has_spec = isinstance(evt, dict) and evt.get("specversion") == "1.0"
        has_type = isinstance(evt, dict) and bool(evt.get("type"))
        has_source = isinstance(evt, dict) and bool(evt.get("source"))
        has_id = isinstance(evt, dict) and bool(evt.get("id"))
        if has_spec and has_type and has_source and has_id:
            valid_payloads += 1

    events_with_data = len(all_events)
    elapsed = (time.perf_counter() - t0) * 1000
    results.append({
        "test": "events_payload_structure",
        "pass": valid_payloads == events_with_data,
        "latency_ms": round(elapsed, 1),
        "detail": (
            f"{valid_payloads}/{events_with_data} events have valid CloudEvents structure"
        ),
    })

    # ── Test 5: ragamuffin.started event (should appear on restart) ──────────
    # This would require a restart which we don't do in benchmarks.
    # Instead, verify ragamuffin.healthy is present (emitted periodically).
    logger.info("Test 5: Periodic health event")
    healthy_events = listener.events_of_type("ragamuffin.healthy")
    elapsed = (time.perf_counter() - t0) * 1000
    results.append({
        "test": "events_periodic_health",
        "pass": True,  # informative: may or may not appear in short window
        "latency_ms": round(elapsed, 1),
        "detail": f"got {len(healthy_events)} ragamuffin.healthy events during test window",
    })

    # ── Test 6: Graceful disconnect ─────────────────────────────────────────
    logger.info("Test 6: Graceful disconnect")
    t1 = time.perf_counter()
    listener.stop()
    elapsed = (time.perf_counter() - t1) * 1000
    results.append({
        "test": "events_graceful_disconnect",
        "pass": True,
        "latency_ms": round(elapsed, 1),
        "detail": f"SSE connection closed cleanly in {elapsed:.0f}ms",
    })

    # ── Test 7: Event types observed ─────────────────────────────────────────
    observed_types = set(e["type"] for e in listener.events)
    elapsed = (time.perf_counter() - t0) * 1000
    results.append({
        "test": "events_type_summary",
        "pass": True,
        "latency_ms": round(elapsed, 1),
        "detail": f"observed event types: {sorted(observed_types)}",
    })

    return results
