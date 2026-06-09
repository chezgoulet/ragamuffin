"""Retry with exponential backoff and error classification."""

from __future__ import annotations

import logging
import time
from typing import Any, Callable, Optional, Tuple

logger = logging.getLogger("ragamuffin.benchmark")


class RetryableError(Exception):
    """A transient error that may succeed on retry."""


class PermanentError(Exception):
    """A non-retryable error (e.g. 400 Bad Request)."""


class classify:
    """Error classification helpers."""

    TRANSIENT_STATUSES = {429, 502, 503, 504}

    @staticmethod
    def from_response(status_code: int, body: str = "") -> Exception:
        """Classify an HTTP response status code into retryable or permanent error."""
        if status_code in classify.TRANSIENT_STATUSES:
            return RetryableError(
                f"HTTP {status_code}: {body[:200]}" if body else f"HTTP {status_code}"
            )
        if status_code in {400, 401, 403, 404}:
            return PermanentError(
                f"HTTP {status_code}: {body[:200]}" if body else f"HTTP {status_code}"
            )
        return RetryableError(
            f"HTTP {status_code}: {body[:200]}" if body else f"HTTP {status_code}"
        )

    @staticmethod
    def from_exception(exc: Exception) -> Exception:
        """Classify a Python exception into retryable or permanent."""
        msg = str(exc)
        # Connection errors are transient
        if any(x in msg.lower() for x in ["connection", "timeout", "eof", "resolve"]):
            return RetryableError(msg)
        # Everything else is permanent
        return PermanentError(msg)


def retry(
    fn: Callable[..., Any],
    max_retries: int = 3,
    base_delay: float = 1.0,
    max_delay: float = 30.0,
    backoff_factor: float = 2.0,
    is_retryable: Optional[Callable[[Exception], bool]] = None,
) -> Tuple[Any, int]:
    """Call fn with exponential backoff retry.

    Returns (result, retry_count). Raises the last exception on permanent
    failure or after exhausting retries.
    """
    last_exc: Optional[Exception] = None
    retries = 0
    delay = base_delay

    while retries <= max_retries:
        try:
            result = fn()
            return result, retries
        except Exception as exc:
            last_exc = exc

            # Permanent errors fail immediately
            if isinstance(exc, PermanentError):
                raise

            # Custom retryability check
            if is_retryable and not is_retryable(exc):
                raise

            retries += 1
            if retries > max_retries:
                break

            logger.warning(
                "retry %d/%d after %.1fs: %s",
                retries,
                max_retries,
                delay,
                exc,
            )
            time.sleep(delay)
            delay = min(delay * backoff_factor, max_delay)

    raise last_exc  # type: ignore[misc]
