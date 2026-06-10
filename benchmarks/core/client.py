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

    def clear_vault(self, name: str) -> Dict:
        """Clear all data in a vault. Requires confirmation."""
        data, status = self._request("POST", f"/v1/vaults/{name}/clear", body={"confirm": True})
        return data if isinstance(data, dict) else {"status": str(status)}

    def list_vaults(self) -> List[str]:
        """List available vault names."""
        data, _ = self._request("GET", "/vaults")
        if isinstance(data, dict):
            vaults = data.get("vaults", data.get("names", []))
            if isinstance(vaults, list):
                return [v["name"] if isinstance(v, dict) else v for v in vaults]
            return []
        if isinstance(data, list):
            return data
        return []

    def create_fact(
        self,
        key: str,
        value: str,
        vault: str,
        source: str = "benchmark",
        source_type: str = "conversation",
        tags: Optional[List[str]] = None,
    ) -> Dict[str, Any]:
        """Create a fact in a vault.

        Uses vault-prefixed POST /vault/{name}/v1/facts for
        correct routing to the per-vault facts collection.
        Accepts optional list of tag strings.
        """
        path = f"/vault/{urllib.parse.quote(vault)}/v1/facts"
        body = {
            "key": key,
            "value": value,
            "source": source or "benchmark",
            "source_type": source_type or "conversation",
        }
        if tags:
            body["tags"] = tags
        data, status = self._request("POST", path, body=body)
        return data if isinstance(data, dict) else {"status": str(status)}

    def get_fact(
        self,
        key: str,
        vault: str,
    ) -> Dict[str, Any]:
        """Get a single fact by key from a vault.

        Uses vault-prefixed GET /vault/{name}/v1/facts?key=...
        Returns the fact dict or raises PermanentError on 404.
        """
        path = f"/vault/{urllib.parse.quote(vault)}/v1/facts?key={urllib.parse.quote(key)}"
        data, status = self._request("GET", path)
        return data if isinstance(data, dict) else {}

    def list_facts(
        self,
        vault: str,
        prefix: Optional[str] = None,
        tag: Optional[str] = None,
        status: Optional[str] = None,
    ) -> Dict[str, Any]:
        """List facts in a vault with optional filters.

        Uses vault-prefixed GET /vault/{name}/v1/facts?prefix=...
        """
        params = []
        if prefix:
            params.append(f"prefix={urllib.parse.quote(prefix)}")
        if tag:
            params.append(f"tag={urllib.parse.quote(tag)}")
        if status:
            params.append(f"status={urllib.parse.quote(status)}")
        path = f"/vault/{urllib.parse.quote(vault)}/v1/facts"
        if params:
            path += "?" + "&".join(params)
        data, status_code = self._request("GET", path)
        return data if isinstance(data, dict) else {}

    def ingest(
        self,
        content: str,
        source: str,
        vault: str,
        tags: Optional[List[str]] = None,
    ) -> Dict[str, Any]:
        """Ingest content into a vault. Tags must be a list of strings."""
        body = {
            "content": content,
            "source": source,
            "vault": vault,
        }
        if tags:
            body["tags"] = tags
        data, status = self._request("POST", "/v1/ingest", body=body)
        return data if isinstance(data, dict) else {"status": str(status)}

    def recall(
        self,
        query: str,
        vault: str,
        top_k: int = 5,
        score_threshold: float = 0.0,
        detail: str = "l2",
    ) -> Dict[str, Any]:
        """Recall chunks matching a query from a vault."""
        path = f"/vault/{vault}/recall"
        body = {
            "query": query,
            "top_k": top_k,
            "score_threshold": score_threshold,
            "detail": detail,
        }
        data, _ = self._request("POST", path, body=body)
        return data if isinstance(data, dict) else {"results": []}

    def batch_recall(
        self,
        queries: List[str],
        vault: str,
        top_k: int = 3,
    ) -> Dict[str, Any]:
        """Submit multiple recall queries in a single batch request."""
        path = f"/vault/{vault}/v1/batch/recall"
        query_objects = [
            {"query": q, "vault": vault, "top_k": top_k}
            for q in queries
        ]
        body = {"queries": query_objects}
        data, _ = self._request("POST", path, body=body)
        return data if isinstance(data, dict) else {}

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
