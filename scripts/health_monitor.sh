#!/bin/bash
# Health monitoring cron (#816)
# Runs every 60s: curl -sf /health || telegram-notify
# Usage: HEALTH_TELEGRAM_BOT=bot123:abc HEALTH_TELEGRAM_CHAT=-12345 ./health_monitor.sh
#        HEALTH_WEBHOOK_URL=https://hooks.example.com/alert ./health_monitor.sh

set -euo pipefail

HOST="${HEALTH_HOST:-localhost}"
PORT="${HEALTH_PORT:-8000}"
BASE="http://${HOST}:${PORT}"

RESP=$(curl -sf "$BASE/health" 2>&1) || {
  MSG="Ragamuffin health check FAILED at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

  # Telegram alert
  if [ -n "${HEALTH_TELEGRAM_BOT:-}" ] && [ -n "${HEALTH_TELEGRAM_CHAT:-}" ]; then
    curl -sf -X POST "https://api.telegram.org/bot${HEALTH_TELEGRAM_BOT}/sendMessage" \
      -H "Content-Type: application/json" \
      -d "{\"chat_id\":\"${HEALTH_TELEGRAM_CHAT}\",\"text\":\"${MSG}\"}" > /dev/null 2>&1 || true
  fi

  # Webhook alert
  if [ -n "${HEALTH_WEBHOOK_URL:-}" ]; then
    curl -sf -X POST "${HEALTH_WEBHOOK_URL}" \
      -H "Content-Type: application/json" \
      -d "{\"text\":\"${MSG}\",\"source\":\"ragamuffin-health\"}" > /dev/null 2>&1 || true
  fi

  echo "$MSG"
  exit 1
}

echo "Healthy: $(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','unknown'))" 2>/dev/null)"
exit 0
