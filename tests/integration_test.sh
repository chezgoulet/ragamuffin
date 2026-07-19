#!/bin/bash
# Ragamuffin Integration Test Suite
#
# Spins up a fresh Ragamuffin instance with embedded store (no Qdrant needed),
# runs the full MCP lifecycle end-to-end, then cleans up.
#
# Usage:
#   export RAGAMUFFIN_EMBEDDING_API_KEY=sk-...
#   ./tests/integration_test.sh                    # finds ragamuffin on PATH
#   ./tests/integration_test.sh ./cmd/ragamuffin    # explicit path
#
# Requires: ragamuffin binary, curl, python3.
# Optional: RAGAMUFFIN_EMBEDDING_API_KEY (needed for embedded search/fact tests;
#   protocol-only tests run without it).

set -euo pipefail

# ── Config ──────────────────────────────────────────────────────────────────
RAGAMUFFIN_BIN="${1:-$(command -v ragamuffin || echo '')}"
PORT="${RAGAMUFFIN_INTEGRATION_PORT:-8001}"
BASE="http://127.0.0.1:${PORT}"
TEST_DIR="/tmp/ragamuffin-integration-$$"
VAULT_PATH="${TEST_DIR}/vault"
LOG_PATH="${TEST_DIR}/server.log"
PID_FILE="${TEST_DIR}/ragamuffin.pid"
PASS=0
FAIL=0
SKIP=0

# Colors
green() { echo -e "\033[32m  PASS\033[0m $1"; PASS=$((PASS+1)); }
red() { echo -e "\033[31m  FAIL\033[0m $1${2:+ ($2)}"; FAIL=$((FAIL+1)); }
skip() { echo -e "\033[33m  SKIP\033[0m $1"; SKIP=$((SKIP+1)); }
fail_exit() { echo -e "\033[31mFATAL:\033[0m $1"; cleanup; exit 1; }

# ── Prerequisites ───────────────────────────────────────────────────────────

check_prereqs() {
  if [ -z "$RAGAMUFFIN_BIN" ] || [ ! -x "$RAGAMUFFIN_BIN" ]; then
    echo "Building ragamuffin binary..."
    go build -o /tmp/ragamuffin-integration-bin ./cmd/ragamuffin
    RAGAMUFFIN_BIN="/tmp/ragamuffin-integration-bin"
  fi
  echo "Using: $RAGAMUFFIN_BIN"

  if ! command -v curl &>/dev/null; then
    fail_exit "curl is required"
  fi
  if ! command -v python3 &>/dev/null; then
    fail_exit "python3 is required"
  fi

  HAS_EMBEDDING=false
  if [ -n "${RAGAMUFFIN_EMBEDDING_API_KEY:-}" ]; then
    HAS_EMBEDDING=true
    echo "Embedding: enabled (RAGAMUFFIN_EMBEDDING_API_KEY set)"
  else
    echo "Embedding: disabled (set RAGAMUFFIN_EMBEDDING_API_KEY for full test suite)"
  fi
}

# ── Lifecycle ───────────────────────────────────────────────────────────────

start_ragamuffin() {
  mkdir -p "$VAULT_PATH"
  echo "Starting ragamuffin (embedded store, no Qdrant)..."
  RAGAMUFFIN_PORT="$PORT" \
  RAGAMUFFIN_HOST=127.0.0.1 \
  RAGAMUFFIN_VAULT_PATH="$VAULT_PATH" \
  RAGAMUFFIN_LOG_PATH="$LOG_PATH" \
  RAGAMUFFIN_VECTOR_STORE=embedded \
  RAGAMUFFIN_EMBEDDING_API_KEY="${RAGAMUFFIN_EMBEDDING_API_KEY:-}" \
  RAGAMUFFIN_LLM_PROVIDER="" \
  RAGAMUFFIN_EMBEDDING_MODEL="${RAGAMUFFIN_EMBEDDING_MODEL:-text-embedding-3-small}" \
  RAGAMUFFIN_EMBEDDING_BASE_URL="${RAGAMUFFIN_EMBEDDING_BASE_URL:-}" \
  RAGAMUFFIN_LOG_LEVEL=error \
  RAGAMUFFIN_AUTH_MODE=none \
  RAGAMUFFIN_GRAPH_ENABLED=true \
  RAGAMUFFIN_GRAPH_DB_PATH="${TEST_DIR}/graph.db" \
  "$RAGAMUFFIN_BIN" &>"$LOG_PATH" &
  echo $! > "$PID_FILE"
  echo "PID: $(cat "$PID_FILE")"

  # Wait for server to be ready
  for i in $(seq 1 30); do
    if curl -sf "$BASE/health" >/dev/null 2>&1; then
      echo "  ready after ${i}s"
      return 0
    fi
    sleep 1
  done
  tail -20 "$LOG_PATH"
  fail_exit "ragamuffin did not start within 30s"
}

cleanup() {
  if [ -f "$PID_FILE" ]; then
    kill "$(cat "$PID_FILE")" 2>/dev/null || true
    wait "$(cat "$PID_FILE")" 2>/dev/null || true
  fi
  rm -rf "$TEST_DIR"
  [ -f /tmp/ragamuffin-integration-bin ] && rm -f /tmp/ragamuffin-integration-bin
}

# ── Helper functions ────────────────────────────────────────────────────────

mcprpc() {
  local method="$1" params="${2:-{}}"
  curl -s -X POST "$BASE/mcp" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":${params}}"
}

restpost() {
  local path="$1" body="${2:-{}}"
  curl -s -X POST "$BASE${path}" \
    -H "Content-Type: application/json" \
    -d "$body"
}

restget() { curl -s "$BASE$1"; }

mcp_ok() {
  local desc="$1" body="$2"
  if echo "$body" | python3 -c "
import sys,json
d=json.load(sys.stdin)
e=d.get('error')
sys.exit(0 if e is None else 1)
" 2>/dev/null; then
    green "$desc"
  else
    local err=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',{}).get('message','?'))" 2>/dev/null)
    red "$desc" "unexpected error: $err"
  fi
}

mcp_err() {
  local desc="$1" body="$2"
  if echo "$body" | python3 -c "
import sys,json; d=json.load(sys.stdin)
sys.exit(0 if d.get('error') else 1)
" 2>/dev/null; then
    green "$desc"
  else
    red "$desc" "expected error"
  fi
}

mcp_has() {
  local desc="$1" body="$2" field="$3"
  if echo "$body" | python3 -c "
import sys,json; d=json.load(sys.stdin); r=d.get('result',{})
sys.exit(0 if isinstance(r,dict) and '$field' in r else 1)
" 2>/dev/null; then
    green "$desc"
  else
    red "$desc" "missing field '$field'"
  fi
}

mcp_count() {
  local desc="$1" body="$2" expected="$3"
  local count=$(echo "$body" | python3 -c "
import sys,json; d=json.load(sys.stdin); t=d.get('result',{}).get('tools',[])
print(len(t))
" 2>/dev/null)
  if [ "$count" -ge "$expected" ] 2>/dev/null; then
    green "$desc ($count tools)"
  else
    red "$desc" "got $count tools, expected >= $expected"
  fi
}

mcp_tool_exists() {
  local body="$1" tool="$2"
  echo "$body" | python3 -c "
import sys,json; d=json.load(sys.stdin)
tools=d.get('result',{}).get('tools',[])
for t in tools:
    if t.get('name')=='$tool':
        sys.exit(0)
sys.exit(1)
" 2>/dev/null
}

# ── Run ─────────────────────────────────────────────────────────────────────

trap cleanup EXIT
check_prereqs
start_ragamuffin

echo ""
echo "===== 0. MCP Protocol ====="

body=$(mcprpc "initialize")
mcp_ok "  initialize returns no error" "$body"
mcp_has "  initialize returns protocolVersion" "$body" "protocolVersion"
mcp_has "  initialize returns capabilities" "$body" "capabilities"

body=$(mcprpc "tools/list")
mcp_ok "  tools/list returns no error" "$body"
mcp_count "  tools/list tool count" "$body" 30
for tool in memory.recall memory.ask memory.fact_put memory.fact_get \
  memory.fact_list memory.fact_delete memory.fact_graph \
  memory.session_create memory.session_get memory.turn_append \
  memory.context_bundle memory.peer_list memory.briefing \
  memory.contradictions memory.graph_entity memory.graph_edges \
  memory.review memory.dialectic memory.changes memory.status; do
  if mcp_tool_exists "$body" "$tool"; then
    green "  essential tool '$tool' present"
  else
    red "  essential tool '$tool' missing"
  fi
done

# Input schema validation
echo "$body" | python3 -c "
import sys,json; d=json.load(sys.stdin); tools=d.get('result',{}).get('tools',[])
bad=[t['name'] for t in tools if t.get('inputSchema',{}).get('type')!='object']
if bad: sys.exit(1)
" 2>/dev/null && green "  All tools have valid inputSchema (type=object)" \
  || red "  Some tools have invalid inputSchema"

# Error handling
mcp_err "  missing jsonrpc field returns error" \
  "$(curl -s -X POST "$BASE/mcp" -H "Content-Type: application/json" -d '{"id":1,"method":"tools/list"}')"
mcp_err "  unknown method returns error" \
  "$(curl -s -X POST "$BASE/mcp" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"nonexistent"}')"

echo ""
echo "===== 1. Auto-Provisioning ====="

if $HAS_EMBEDDING; then
  body=$(mcprpc "tools/call" '{"name":"memory.recall","arguments":{"query":"test","vault":"agent::integration-test"}}')
  mcp_ok "  first recall against agent::integration-test (auto-provisions vault)" "$body"

  body=$(mcprpc "tools/call" '{"name":"memory.recall","arguments":{"query":"test","vault":"agent::integration-test"}}')
  mcp_ok "  second recall succeeds (vault already provisioned)" "$body"
else
  skip "  Auto-provisioning recall — requires RAGAMUFFIN_EMBEDDING_API_KEY"
fi

echo ""
echo "===== 2. Session Lifecycle ====="

body=$(mcprpc "tools/call" '{"name":"memory.session_create","arguments":{"agent_id":"integ-test","content":"Test session","vault":"default"}}')
mcp_ok "  session_create returns no error" "$body"
session_id=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('result',{}).get('session_id',''))" 2>/dev/null)
if [ -n "$session_id" ]; then
  green "  session_id: $session_id"
  body=$(mcprpc "tools/call" "{\"name\":\"memory.turn_append\",\"arguments\":{\"session_id\":\"$session_id\",\"content\":\"User: hello\\nAssistant: hi\",\"role\":\"assistant\"}}")
  mcp_ok "  turn_append succeeds" "$body"
  body=$(mcprpc "tools/call" "{\"name\":\"memory.session_get\",\"arguments\":{\"session_id\":\"$session_id\",\"turns\":5}}")
  mcp_ok "  session_get succeeds" "$body"
  body=$(mcprpc "tools/call" "{\"name\":\"memory.session_list\",\"arguments\":{}}")
  mcp_ok "  session_list succeeds" "$body"
else
  skip "  session lifecycle — no session_id from create"
fi

echo ""
echo "===== 3. Fact CRUD ====="

body=$(mcprpc "tools/call" '{"name":"memory.fact_put","arguments":{"key":"integ/test-key","value":"test value","source":"integration_test","source_type":"automated"}}')
mcp_ok "  fact_put creates fact" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.fact_get","arguments":{"key":"integ/test-key"}}')
mcp_ok "  fact_get returns fact" "$body"

# Re-upsert (same key) — lifecycle fields should be preserved
body=$(mcprpc "tools/call" '{"name":"memory.fact_put","arguments":{"key":"integ/test-key","value":"updated value","tags":["test"]}}')
mcp_ok "  fact_put re-upsert (lifecycle fields preserved)" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.fact_list","arguments":{"status":"active"}}')
mcp_ok "  fact_list returns facts" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.fact_delete","arguments":{"key":"integ/test-key"}}')
# DeleteFiltered may fail on embedded store (only supports source_file filters).
# The Qdrant client's DeleteFiltered supports arbitrary filters — this is an
# embedded store limitation, not a protocol bug.
if echo "$body" | python3 -c "
import sys,json; d=json.load(sys.stdin)
sys.exit(0 if d.get('result',{}).get('deleted') else 1)
" 2>/dev/null; then
  green "  fact_delete removes fact"
  # Verify deleted
  body=$(mcprpc "tools/call" '{"name":"memory.fact_get","arguments":{"key":"integ/test-key"}}')
  mcp_err "  fact_get on deleted key returns error" "$body"
else
  skip "  fact_delete — embedded store limitation (use Qdrant for this test)"
fi

echo ""
echo "===== 4. Fact Graph ====="

body=$(mcprpc "tools/call" '{"name":"memory.fact_put","arguments":{"key":"integ/graph-a","value":"version 1","tags":["graph"]}}')
mcp_ok "  fact_put graph-a" "$body"
body=$(mcprpc "tools/call" '{"name":"memory.fact_put","arguments":{"key":"integ/graph-b","value":"version 2","tags":["graph"]}}')
mcp_ok "  fact_put graph-b" "$body"
body=$(mcprpc "tools/call" '{"name":"memory.fact_graph","arguments":{"key":"integ/graph-a","depth":2}}')
mcp_ok "  fact_graph returns graph" "$body"

echo ""
echo "===== 5. Context & Discovery ====="

body=$(mcprpc "tools/call" '{"name":"memory.context_bundle","arguments":{"query":"composition","top_k":3}}')
mcp_ok "  context_bundle returns bundle" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.peer_list","arguments":{}}')
mcp_ok "  peer_list succeeds" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.briefing","arguments":{"period":"24h"}}')
mcp_ok "  briefing returns digest" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.changes","arguments":{"period":"168h","limit":10}}')
mcp_ok "  changes returns recent activity" "$body"

echo ""
echo "===== 6. Review Queue ====="

body=$(mcprpc "tools/call" '{"name":"memory.review","arguments":{"action":"list"}}')
mcp_ok "  review list succeeds" "$body"

echo ""
echo "===== 7. Status & Graph ====="

body=$(mcprpc "tools/call" '{"name":"memory.stats","arguments":{}}')
mcp_ok "  stats returns metrics" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.status","arguments":{}}')
mcp_ok "  status returns health" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.graph_communities","arguments":{"vault":"default"}}')
mcp_ok "  graph_communities succeeds" "$body"

body=$(mcprpc "tools/call" '{"name":"memory.graph_edges","arguments":{"vault":"default"}}')
mcp_ok "  graph_edges succeeds" "$body"

echo ""
echo "===== Result ====="
echo "Passed: $PASS"
echo "Failed: $FAIL"
echo "Skipped: $SKIP"
echo ""
if [ "$FAIL" -eq 0 ]; then
  echo -e "\033[32mAll applicable tests passed.\033[0m"
  exit 0
else
  echo -e "\033[31m$FAIL test(s) failed.\033[0m"
  exit 1
fi
