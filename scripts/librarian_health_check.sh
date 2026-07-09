#!/usr/bin/env bash
#
# librarian_health_check.sh — Librarian health check (#795)
#
# Queries Ragamuffin's GET /v1/facts/freshness endpoint and reports whether the
# librarian (the cron that writes completed kanban-card knowledge as facts) has
# written anything recently. Intended to run as a no_agent=True cron job on the
# same hourly cadence as the librarian cron.
#
# Behaviour:
#   - Healthy  → prints nothing, exits 0 (silent when healthy).
#   - Stale    → prints an alert to stdout, exits 1 (integrate with @passeurbot /
#                Telegram delivery, which forwards non-empty stdout to the channel).
#   - Error    → prints the error, exits 2 (so infra alerts on the check itself).
#
# Environment:
#   RAGAMUFFIN_URL      Base URL of the Ragamuffin server (default http://localhost:8000)
#   RAGAMUFFIN_VAULT    Optional vault name for /vault/{name}/v1/facts/freshness
#   FRESHNESS_THRESHOLD Optional override in seconds (server default is 86400 = 24h)
#   RAGAMUFFIN_API_KEY  Optional bearer/API key for authenticated deployments
#
# Examples:
#   ./librarian_health_check.sh
#   RAGAMUFFIN_URL=https://ragamuffin.internal:8000 ./librarian_health_check.sh
#   RAGAMUFFIN_VAULT=house ./librarian_health_check.sh

set -uo pipefail

RAGAMUFFIN_URL="${RAGAMUFFIN_URL:-http://localhost:8000}"
RAGAMUFFIN_VAULT="${RAGAMUFFIN_VAULT:-}"
RAGAMUFFIN_API_KEY="${RAGAMUFFIN_API_KEY:-}"

if [[ -n "$RAGAMUFFIN_VAULT" ]]; then
  URL="${RAGAMUFFIN_URL%/}/vault/${RAGAMUFFIN_VAULT}/v1/facts/freshness"
else
  URL="${RAGAMUFFIN_URL%/}/v1/facts/freshness"
fi

# Optional threshold override via query param (server clamps to config default
# if unset, so only pass when explicitly provided).
if [[ -n "${FRESHNESS_THRESHOLD:-}" ]]; then
  URL="${URL}?threshold_seconds=${FRESHNESS_THRESHOLD}"
fi

AUTH_HEADER=()
if [[ -n "$RAGAMUFFIN_API_KEY" ]]; then
  AUTH_HEADER=(-H "Authorization: Bearer ${RAGAMUFFIN_API_KEY}")
fi

# Query the endpoint. Use curl with a timeout; fail loudly on transport errors.
RESP=$(curl -sS --max-time 15 \
  -H "Accept: application/json" \
  "${AUTH_HEADER[@]}" \
  "$URL" 2>/dev/null)
CURL_EXIT=$?

if [[ $CURL_EXIT -ne 0 || -z "$RESP" ]]; then
  echo "LIBRARIAN HEALTH CHECK FAILED: unable to reach ${URL} (curl exit ${CURL_EXIT})"
  exit 2
fi

# Parse JSON fields. Requires `jq`; fall back to grep/sed if unavailable.
if command -v jq >/dev/null 2>&1; then
  parse() { jq -r --arg k "$1" '.[$k] | tostring' <<<"$RESP"; }
else
  parse() { echo "$RESP" | grep -o "\"$1\"[ ]*:[ ]*\"\?[0-9a-zA-Z:.\-]*\"\?" | head -1 | sed -E "s/.*:[ ]*\"?([0-9a-zA-Z:.\-]*)\"?/\1/"; }
fi

STALE=$(parse stale)
LAST_WRITE_AT=$(parse last_write_at)
AGE=$(parse age_seconds)
THRESHOLD=$(parse threshold_seconds)
COLLECTION=$(parse collection)
FACT_COUNT=$(parse fact_count)

# Defensive: if we couldn't parse the response shape, treat as error.
if [[ -z "$STALE" ]]; then
  echo "LIBRARIAN HEALTH CHECK FAILED: malformed response from ${URL}: ${RESP}"
  exit 2
fi

if [[ "$STALE" == "true" ]]; then
  if [[ "$AGE" == "-1" || -z "$LAST_WRITE_AT" ]]; then
    MSG="LIBRARIAN ALERT: no facts have ever been written to ${COLLECTION:-ragamuffin_facts} (librarian never ran or writes are failing)."
  else
    MSG="LIBRARIAN ALERT: no fact written in the last ${THRESHOLD:-86400}s. Last write: ${LAST_WRITE_AT} (age ${AGE}s, threshold ${THRESHOLD:-86400}s, collection ${COLLECTION:-ragamuffin_facts})."
  fi
  echo "$MSG"
  exit 1
fi

# Healthy — silent.
exit 0
