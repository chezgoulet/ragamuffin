#!/bin/bash
# Ragamuffin curl smoke tests
# Usage: SMOKE_HOST=localhost SMOKE_PORT=8000 ./smoke_test.sh
# These tests verify every endpoint conforms to the SPEC.md response shapes.

set -euo pipefail

HOST="${SMOKE_HOST:-localhost}"
PORT="${SMOKE_PORT:-8000}"
BASE="http://${HOST}:${PORT}"
PASS=0
FAIL=0

green() { echo -e "\033[32m  PASS\033[0m $1"; PASS=$((PASS+1)); }
red() { echo -e "\033[31m  FAIL\033[0m $1 ($2)"; FAIL=$((FAIL+1)); }

assert_status() {
  local desc="$1" expected="$2" actual="$3" body="$4"
  if [ "$actual" = "$expected" ]; then
    green "$desc"
  else
    red "$desc" "HTTP $actual (expected $expected): $(echo "$body" | head -c 200)"
  fi
}

assert_field() {
  local desc="$1" field="$2" expected="$3" body="$4"
  local actual
  actual=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$field','MISSING'))" 2>/dev/null || echo "JSON_PARSE_ERROR")
  if [ "$actual" = "$expected" ]; then
    green "$desc ($field = $expected)"
  else
    red "$desc" "$field = $actual (expected $expected)"
  fi
}

assert_field_type() {
  local desc="$1" field="$2" expected_type="$3" body="$4"
  local actual
  actual=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); v=d.get('$field','MISSING'); print(type(v).__name__)" 2>/dev/null || echo "JSON_PARSE_ERROR")
  if [ "$actual" = "$expected_type" ]; then
    green "$desc ($field is $expected_type)"
  else
    red "$desc" "$field is $actual (expected $expected_type)"
  fi
}

echo "=== Ragamuffin Smoke Tests ==="
echo "Target: $BASE"
echo ""

# ── /health ────────────────────────────────────────────────────────────────
echo "--- /health ---"
RESP=$(curl -sf "$BASE/health" 2>&1) && RC=0 || RC=$?
assert_status "GET /health returns 200" "0" "$RC" "$RESP"
assert_field "health status" "status" "ok" "$RESP"
assert_field "health qdrant" "qdrant" "reachable" "$RESP"

# ── /stats ─────────────────────────────────────────────────────────────────
echo "--- /stats ---"
RESP=$(curl -sf "$BASE/stats" 2>&1) && RC=0 || RC=$?
assert_status "GET /stats returns 200" "0" "$RC" "$RESP"
assert_field_type "stats vault_path" "vault_path" "str" "$RESP"
assert_field_type "stats indexed_files" "indexed_files" "int" "$RESP"
assert_field_type "stats total_chunks" "total_chunks" "int" "$RESP"
assert_field_type "stats uptime_seconds" "uptime_seconds" "int" "$RESP"

# ── /recall (POST) ─────────────────────────────────────────────────────────
echo "--- /recall ---"
RESP=$(curl -sf -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test query","top_k":3}' 2>&1) && RC=0 || RC=$?
assert_status "POST /recall returns 200" "0" "$RC" "$RESP"
assert_field_type "recall has results" "results" "list" "$RESP"
assert_field_type "recall has top_score" "top_score" "float" "$RESP"

# /recall: verify first_paragraph is returned
RESP=$(curl -sf -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","top_k":3}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  # Check first result has first_paragraph field
  result=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('results',[]); print(r[0].get('first_paragraph','MISSING') if r else 'NO_RESULTS')" 2>/dev/null || echo "PARSE_ERROR")
  if [ "$result" = "MISSING" ] || [ "$result" = "PARSE_ERROR" ]; then
    red "recall first_paragraph field" "not found in response"
  else
    green "recall first_paragraph field present"
  fi
fi

# /recall error: missing query
RESP=$(curl -s -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{}' 2>&1)
assert_field "recall missing query" "error" "True" "$RESP"
assert_field "recall error code" "code" "INVALID_REQUEST" "$RESP"

# ── /draft (direct mode) ───────────────────────────────────────────────────
echo "--- /draft ---"
RESP=$(curl -sf -X POST "$BASE/draft" \
  -H 'Content-Type: application/json' \
  -d '{"title":"smoke test","content":"# Smoke Test\n\nThis is a test file.","target_path":"_smoke_test.md","mode":"direct"}' 2>&1) && RC=0 || RC=$?
assert_status "POST /draft direct returns 200" "0" "$RC" "$RESP"
assert_field "draft mode" "mode" "direct" "$RESP"
assert_field "draft path" "path" "_smoke_test.md" "$RESP"
assert_field "draft written" "written" "True" "$RESP"

# /draft delete
RESP=$(curl -sf -X POST "$BASE/draft" \
  -H 'Content-Type: application/json' \
  -d '{"title":"delete smoke test","content":"","target_path":"_smoke_test.md","mode":"direct"}' 2>&1) && RC=0 || RC=$?
assert_status "POST /draft delete returns 200" "0" "$RC" "$RESP"
# written may or may not be in delete response — check at least success
assert_field "draft delete mode" "mode" "direct" "$RESP"

# /draft error: missing title
RESP=$(curl -s -X POST "$BASE/draft" \
  -H 'Content-Type: application/json' \
  -d '{"content":"test","target_path":"test.md"}' 2>&1)
assert_field "draft missing title" "error" "True" "$RESP"

# /draft error: path traversal
RESP=$(curl -s -X POST "$BASE/draft" \
  -H 'Content-Type: application/json' \
  -d '{"title":"test","content":"test","target_path":"../../../etc/passwd"}' 2>&1)
assert_field "draft path traversal" "error" "True" "$RESP"

# ── /audit (stale only — no LLM required) ──────────────────────────────────
echo "--- /audit ---"
RESP=$(curl -sf -X POST "$BASE/audit" \
  -H 'Content-Type: application/json' \
  -d '{"checks":["stale"],"stale_days":365}' 2>&1) && RC=0 || RC=$?
assert_status "POST /audit returns 200" "0" "$RC" "$RESP"
assert_field_type "audit stale_files" "stale_files" "list" "$RESP"

# /audit with all checks (no LLM — semantic_conflict should be empty)
RESP=$(curl -sf -X POST "$BASE/audit" \
  -H 'Content-Type: application/json' \
  -d '{}' 2>&1) && RC=0 || RC=$?
assert_status "POST /audit all checks returns 200" "0" "$RC" "$RESP"
assert_field_type "audit checks_run" "checks_run" "list" "$RESP"

# ── /ask (may fail if no LLM configured) ───────────────────────────────────
echo "--- /ask ---"
RESP=$(curl -s -X POST "$BASE/ask" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","mode":"rag"}' 2>&1) && RC=0 || RC=$?
# 503 = LLM_NOT_CONFIGURED (valid, expected without LLM)
# 200 = LLM configured and working
if [ "$RC" = "0" ]; then
  assert_field_type "ask answer" "answer" "str" "$RESP"
  assert_field_type "ask sources" "sources" "list" "$RESP"
else
  assert_field "/ask error code (LLM not configured)" "code" "LLM_NOT_CONFIGURED" "$RESP"
fi

# ── /mcp (MCP bolt-on) ─────────────────────────────────────────────────────
echo "--- /mcp ---"
# POST initialize
RESP=$(curl -sf -X POST "$BASE/mcp" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' 2>&1) && RC=0 || RC=$?
assert_status "MCP initialize returns 200" "0" "$RC" "$RESP"
assert_field "MCP protocol version" "protocolVersion" "2024-11-05" "$RESP"

# POST tools/list
RESP=$(curl -sf -X POST "$BASE/mcp" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' 2>&1) && RC=0 || RC=$?
assert_status "MCP tools/list returns 200" "0" "$RC" "$RESP"

# Verify all 4 tools are in the response
for tool in ragamuffin_recall ragamuffin_ask ragamuffin_draft ragamuffin_audit; do
  if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); tools=[t['name'] for t in d.get('result',{}).get('tools',[])]; sys.exit(0 if '$tool' in tools else 1)" 2>/dev/null; then
    green "MCP tool: $tool"
  else
    red "MCP tool: $tool" "not found in tools/list"
  fi
done

# POST unknown method
RESP=$(curl -s -X POST "$BASE/mcp" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"nonexistent","params":{}}' 2>&1)
assert_field "MCP unknown method" "code" "-32601" "$RESP"

# ── Summary ────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
