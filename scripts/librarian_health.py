#!/usr/bin/env python3
"""Librarian health check (#795): alert if no facts written in 24 hours.

Queries Ragamuffin for the most recently created fact. If the latest fact
is older than ALERT_HOURS (default 24), fires a Telegram alert.

Usage:
  RAGAMUFFIN_ENDPOINT=http://ragamuffin:8000 ./librarian_health.py
  RAGAMUFFIN_ENDPOINT=http://ragamuffin:8000 \
    TELEGRAM_BOT_TOKEN=bot123:abc TELEGRAM_CHAT_ID=-12345 \
    ./librarian_health.py

Exit codes:
  0 = healthy (facts written recently)
  1 = alert (no recent writes, or API error)
"""

import json
import os
import sys
import time
from datetime import datetime, timezone
from urllib.request import Request, urlopen

ENDPOINT = os.environ.get("RAGAMUFFIN_ENDPOINT", "").rstrip("/")
ALERT_HOURS = int(os.environ.get("LIBRARIAN_ALERT_HOURS", "24"))
TELEGRAM_BOT_TOKEN = os.environ.get("TELEGRAM_BOT_TOKEN", "")
TELEGRAM_CHAT_ID = os.environ.get("TELEGRAM_CHAT_ID", "")


def fetch(url: str) -> dict:
    """GET a JSON endpoint, return parsed response."""
    req = Request(url, headers={"Accept": "application/json"})
    with urlopen(req, timeout=15) as resp:
        return json.loads(resp.read())


def send_telegram(message: str) -> None:
    """Send an alert via Telegram."""
    if not TELEGRAM_BOT_TOKEN or not TELEGRAM_CHAT_ID:
        print(f"[librarian] Telegram not configured. Would alert: {message}")
        return
    url = (
        f"https://api.telegram.org/bot{TELEGRAM_BOT_TOKEN}"
        f"/sendMessage"
    )
    payload = json.dumps({
        "chat_id": TELEGRAM_CHAT_ID,
        "text": message,
        "parse_mode": "markdown",
    }).encode()
    req = Request(url, data=payload, headers={"Content-Type": "application/json"})
    with urlopen(req, timeout=15) as resp:
        result = json.loads(resp.read())
        if not result.get("ok"):
            print(f"[librarian] Telegram send failed: {result}")


def main() -> int:
    if not ENDPOINT:
        print("[librarian] RAGAMUFFIN_ENDPOINT not set", file=sys.stderr)
        return 1

    now = datetime.now(timezone.utc)

    # Query facts collection for the most recently updated fact
    try:
        data = fetch(f"{ENDPOINT}/v1/facts?limit=5")
    except Exception as e:
        msg = f"[librarian] API error: {e}"
        print(msg, file=sys.stderr)
        send_telegram(f"⚠️ *Librarian Alert* — Ragamuffin API unreachable\n```\n{e}\n```")
        return 1

    entries = data.get("entries", [])
    if not entries:
        msg = "[librarian] No facts found in vault — possible data loss or empty vault"
        print(msg)
        send_telegram(f"⚠️ *Librarian Alert* — No facts in vault\nNo facts found. Possible data loss or empty vault.")
        return 1

    # Find the most recent fact by updated_at
    newest = max(
        entries,
        key=lambda e: e.get("updated_at", e.get("created_at", "")),
    )
    last_ts = newest.get("updated_at") or newest.get("created_at", "")
    if not last_ts:
        print("[librarian] No timestamp on newest fact — cannot determine freshness")
        return 0  # don't alert for missing metadata

    try:
        last_time = datetime.fromisoformat(last_ts.replace("Z", "+00:00"))
    except ValueError:
        print(f"[librarian] Cannot parse timestamp: {last_ts}")
        return 0

    hours_since = (now - last_time).total_seconds() / 3600

    if hours_since < ALERT_HOURS:
        print(
            f"[librarian] Healthy — last fact written "
            f"{hours_since:.1f}h ago ({newest.get('key', 'unknown')})"
        )
        return 0

    # Alert: too long since last write
    msg = (
        f"⚠️ *Librarian Alert*\n"
        f"No facts written in {hours_since:.0f} hours "
        f"(threshold: {ALERT_HOURS}h)\n"
        f"Last fact: `{newest.get('key', 'unknown')}` "
        f"at `{last_ts}`"
    )
    print(msg)
    send_telegram(msg)
    return 1


if __name__ == "__main__":
    sys.exit(main())
