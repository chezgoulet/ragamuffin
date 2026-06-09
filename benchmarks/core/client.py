"""Ragamuffin REST API client for benchmarks.

Zero external deps — uses urllib.request from stdlib.
"""

from __future__ import annotations

import json
import logging
import os
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Dict, List, Optional

from .retry import RetryableError, PermanentError, classify, retry

logger = logging.getLogger("ragamuffin.benchmark")

# ── Defaults ────────────────────────────────────────────────────────────────────

DEFAULT_BASE_URL = os.environ.get("RAGAMUFFIN_URL", "http://localhost:8000")
HTTP_TIMEOUT = int(os.environ.get("RAGAMUFFIN_HTTP_TIMEOUT", "30"))
MAX_RETRIES = int(os.environ.get("RAGAMUFFIN_MAX_RETRIES", "3"))


# ── Client ──────────────────────────────────────────────────────────────────────


class RagamuffinClient:
    """HTTP client for the Ragamuffin API.

    Handles retry with exponential backoff on 429/502/503.
    Raises RetryableError or PermanentError on failure.
    """

    def __init__(
        self,
        base_url: str = DEFAULT_BASE_URL,
        timeout: int = HTTP_TIMEOUT,
        max_retries: int = MAX_RETRIES,
        api_key: Optional[str] = None,
    ):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.max_retries = max_retries
        self.api_key = api_key or os.environ.get("RAGAMUFFIN_API_KEY", "")
        self.litellm_key = os.environ.get("LITELLM_API_KEY", "")

    # ── Public API ────────────────────────────────────────────────────────────

    def health(self) -> bool:
        """Check if Ragamuffin is reachable."""
        try:
            data, _ = self._request("GET", "/health")
            return data is not None
        except Exception:
            return False

    def list_vaults(self) -> List[str]:
        """List available vaults."""
        data, _ = self._request("GET", "/vaults")
        if isinstance(data, dict):
            vaults = data.get("vaults", data.get("names", []))
            if not vaults and "vaults" not in data:
                # /vaults returns array in some versions
                vaults = []
            return list(vaults)
        if isinstance(data, list):
            return data
        return []

    def ingest(
        self,
        content: str,
        source: str,
        vault: str,
        tags: Optional[Dict[str, str]] = None,
    ) -> Dict[str, Any]:
        """Ingest content into a vault."""
        body = {
            "content": content,
            "source": source,
            "vault": vault,
            "tags": tags or {},
        }
        data, status = self._request("POST", "/v1/ingest", body=body)
        return data if isinstance(data, dict) else {"status": str(status)}

    def ask(
        self,
        query: str,
        vault: str,
        mode: str = "rag",
    ) -> Dict[str, Any]:
        """Ask a question against a vault."""
        path = f"/vault/{vault}/ask"
        body = {"query": query, "mode": mode}
        data, _ = self._request("POST", path, body=body)
        return data if isinstance(data, dict) else {"answer": str(data)}

    # ── Internal ──────────────────────────────────────────────────────────────

    def _request(
        self,
        method: str,
        path: str,
        body: Optional[Dict] = None,
    ) -> tuple[Any, int]:
        """Make an HTTP request with retry logic.

        Returns (parsed_json_body, status_code).
        """

        def do_request() -> tuple[Any, int]:
            url = self.base_url + path
            headers = {
                "Content-Type": "application/json",
                "User-Agent": "RagamuffinBenchmark/0.9",
            }
            if self.api_key:
                headers["Authorization"] = f"Bearer {self.api_key}"

            data_bytes = json.dumps(body).encode() if body else None
            req = urllib.request.Request(
                url,
                data=data_bytes,
                headers=headers,
                method=method,
            )

            try:
                resp = urllib.request.urlopen(req, timeout=self.timeout)
                status = resp.status
                raw = resp.read()
                result = json.loads(raw.decode()) if raw.strip() else {}
                return result, status
            except urllib.error.HTTPError as e:
                raw_body = e.read().decode() if e.fp else ""
                status = e.code
                exc = classify.from_response(status, raw_body)

                # Check for LiteLLM auth failures
                if status == 401 and "litellm" in raw_body.lower() and not self.litellm_key:
                    raise PermanentError(
                        "LiteLLM returned 401. Set LITELLM_API_KEY env var."
                    ) from e

                raise exc from e
            except urllib.error.URLError as e:
                raise classify.from_exception(e) from e
            except OSError as e:
                raise classify.from_exception(e) from e

        result, retries_used = retry(
            do_request,
            max_retries=self.max_retries,
        )
        return result
