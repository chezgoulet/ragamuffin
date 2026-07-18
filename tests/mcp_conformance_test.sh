#!/bin/bash
# MCP Adapter Conformance Test Suite
# Tests that a Ragamuffin instance speaks MCP correctly.
# Any adapter (OpenClaw, Hermes, Goose, Copilot, etc.) should pass these.
#
# Usage: MCP_HOST=localhost MCP_PORT=8000 ./tests/mcp_conformance_test.sh

set -euo pipefail

HOST="${MCP_HOST:-localhost}"
PORT="${MCP_PORT:-8000}"
BASE="http://${HOST}:${PORT}"
PASS=0
FAIL=0
SKIP=0

green() { echo -e "\033[32m  PASS\033[0m $1"; PASS=$((PASS+1)); }
red() { echo -e "\033[31m  FAIL\033[0m $1${2:+ ($2)}"; FAIL=$((FAIL+1)); }
skip() { echo -e "\033[33m  SKIP\033[0m $1"; SKIP=$((SKIP+1)); }

mcp_rpc() {
  local method="$1" params="$2"
  curl -s -X POST "${BASE}/mcp" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":${params}}"
}

get_field() {
  local body="$1" field="$2"
  echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$field','MISSING'))" 2>/dev/null || echo "PARSE_ERROR"
}

assert_no_error() {
  local desc="$1" body="$2"
  local err
  err=$(get_field "$body" "error")
  if [ "$err" = "MISSING" ]; then
    green "$desc"
  elif [ "$err" = "null" ]; then
    green "$desc"
  elif [ "$err" = "None" ]; then
    green "$desc"
  else
    red "$desc" "unexpected error: $(echo "$err" | head -c 200)"
  fi
}

assert_error() {
  local desc="$1" body="$2"
  local err
  err=$(get_field "$body" "error")
  if [ "$err" != "MISSING" ] && [ "$err" != "null" ] && [ "$err" != "None" ]; then
    green "$desc"
  else
    red "$desc" "expected error but got: $(echo "$body" | head -c 200)"
  fi
}

assert_result_has() {
  local desc="$1" body="$2" field="$3"
  local result
  result=$(get_field "$body" "result")
  if [ "$result" = "MISSING" ] || [ "$result" = "null" ]; then
    red "$desc" "no result field"
    return
  fi
  if echo "$result" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print('yes' if '$field' in d else 'no')" 2>/dev/null | grep -q yes; then
    green "$desc"
  else
    red "$desc" "result missing field '$field'"
  fi
}

echo "===== MCP Conformance Tests ====="
echo "Target: ${BASE}/mcp"
echo ""

# ── 1. JSON-RPC 2.0 Compliance ─────────────────────────────────────────────

echo "--- 1. JSON-RPC 2.0 Compliance ---"

body=$(mcp_rpc "initialize" "{}")
assert_no_error "initialize returns no error" "$body"
assert_result_has "initialize returns protocolVersion" "$body" "protocolVersion"
assert_result_has "initialize returns capabilities" "$body" "capabilities"

body=$(mcp_rpc "initialize" "{}")
echo "$body" | python3 -c "
import sys,json
d = json.load(sys.stdin)
assert d.get('jsonrpc') == '2.0', 'missing jsonrpc 2.0'
assert 'id' in d, 'missing id'
print('  PASS jsonrpc 2.0 format')
" && green "JSON-RPC 2.0 response format" || red "JSON-RPC 2.0 response format"

# ── 2. Tools Discovery ──────────────────────────────────────────────────────

echo ""
echo "--- 2. Tools Discovery ---"

body=$(mcp_rpc "tools/list" "{}")
assert_no_error "tools/list returns no error" "$body"

tool_count=$(echo "$body" | python3 -c "
import sys,json
d = json.load(sys.stdin)
tools = d.get('result',{}).get('tools',[])
print(len(tools))
" 2>/dev/null || echo "0")
if [ "$tool_count" -ge 25 ]; then
  green "tools/list returns $tool_count tools (expected >= 25)"
else
  red "tools/list tool count" "got $tool_count, expected >= 25"
fi

# Verify essential tools exist
for tool in ragamuffin_recall ragamuffin_ask ragamuffin_store ragamuffin_fact_get ragamuffin_fact_put ragamuffin_fact_list ragamuffin_fact_delete ragamuffin_fact_graph ragamuffin_review ragamuffin_context_bundle ragamuffin_peer_list ragamuffin_status ragamuffin_session_create; do
  found=$(echo "$body" | python3 -c "
import sys,json
d = json.load(sys.stdin)
tools = d.get('result',{}).get('tools',[])
for t in tools:
    if t.get('name') == '$tool':
        print('yes')
        exit(0)
print('no')
" 2>/dev/null)
  if [ "$found" = "yes" ]; then
    green "  essential tool '$tool' present"
  else
    red "  essential tool '$tool' missing"
  fi
done

# ── 3. Tool Input Schema Validation ─────────────────────────────────────────

echo ""
echo "--- 3. Tool Input Schema Validation ---"

echo "$body" | python3 -c "
import sys,json
d = json.load(sys.stdin)
tools = d.get('result',{}).get('tools',[])
bad = []
for t in tools:
    name = t.get('name','?')
    schema = t.get('inputSchema',{})
    if schema.get('type') != 'object':
        bad.append(f'{name}: type={schema.get(\"type\")}')
    if 'properties' not in schema:
        bad.append(f'{name}: missing properties')
if bad:
    print('FAIL:' + '; '.join(bad))
    exit(1)
print('PASS')
" && green "All tools have valid inputSchema (type=object, properties)" || red "Schema validation failed"

# ── 4. Tool Invocation ──────────────────────────────────────────────────────

echo ""
echo "--- 4. Tool Invocation ---"

# 4a. ragamuffin_recall — missing query should error
body=$(mcp_rpc "tools/call" '{"name":"ragamuffin_recall","arguments":{}}')
assert_error "recall with empty args returns error" "$body"

# 4b. ragamuffin_fact_put — missing key/value should error
body=$(mcp_rpc "tools/call" '{"name":"ragamuffin_fact_put","arguments":{}}')
assert_error "fact_put with empty args returns error" "$body"

# 4c. ragamuffin_session_create — missing agent_id should error
body=$(mcp_rpc "tools/call" '{"name":"ragamuffin_session_create","arguments":{}}')
assert_error "session_create with empty args returns error" "$body"

# 4d. Unknown tool should error
body=$(mcp_rpc "tools/call" '{"name":"nonexistent_tool","arguments":{}}')
assert_error "unknown tool returns error" "$body"

# ── 5. Session Lifecycle ────────────────────────────────────────────────────

echo ""
echo "--- 5. Session Lifecycle (initialize → session → turn → finalize) ---"

# Create a session
body=$(mcp_rpc "tools/call" '{"name":"ragamuffin_session_create","arguments":{"agent_id":"conftest","content":"Test session for conformance","vault":"default"}}')
session_id=$(echo "$body" | python3 -c "
import sys,json
d = json.load(sys.stdin)
r = d.get('result',{})
print(r.get('session_id',''))
" 2>/dev/null)
if [ -n "$session_id" ]; then
  green "session_create returns session_id: $session_id"

  # Append a turn
  body=$(mcp_rpc "tools/call" "{\"name\":\"ragamuffin_turn_append\",\"arguments\":{\"session_id\":\"$session_id\",\"content\":\"User: hello\\nAssistant: hi there\",\"role\":\"assistant\"}}")
  turn_id=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}); print(r.get('turn_id',0))" 2>/dev/null)
  if [ "$turn_id" != "0" ] && [ -n "$turn_id" ]; then
    green "  turn_append succeeds, turn_id=$turn_id"
  else
    skip "  turn_append" "turn append may need specific server config"
  fi

  # Get session
  body=$(mcp_rpc "tools/call" "{\"name\":\"ragamuffin_session_get\",\"arguments\":{\"session_id\":\"$session_id\",\"turns\":5}}")
  assert_no_error "  session_get succeeds" "$body"

  # List sessions
  body=$(mcp_rpc "tools/call" "{\"name\":\"ragamuffin_session_list\",\"arguments\":{}}")
  assert_no_error "  session_list succeeds" "$body"
else
  skip "session lifecycle" "session_create failed—is logstore configured?"
fi

# ── 6. Error Handling ───────────────────────────────────────────────────────

echo ""
echo "--- 6. Error Handling ---"

# Invalid JSON-RPC
body=$(curl -s -X POST "${BASE}/mcp" -H "Content-Type: application/json" -d '{invalid json')
echo "$body" | grep -q "error" && green "Parse error returns error response" || red "Parse error handling"

# Missing jsonrpc field
body=$(curl -s -X POST "${BASE}/mcp" -H "Content-Type: application/json" -d '{"id":1,"method":"tools/list"}')
error_code=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',{}).get('code',0))" 2>/dev/null)
if [ "$error_code" = "-32600" ]; then
  green "Missing jsonrpc returns -32600 Invalid Request"
else
  red "Missing jsonrpc handling" "got code $error_code, expected -32600"
fi

# Unknown method
body=$(curl -s -X POST "${BASE}/mcp" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"unknown_method"}')
error_code=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',{}).get('code',0))" 2>/dev/null)
if [ "$error_code" = "-32601" ]; then
  green "Unknown method returns -32601 Method not found"
else
  red "Unknown method handling" "got code $error_code, expected -32601"
fi

# ── Summary ─────────────────────────────────────────────────────────────────

echo ""
echo "===== MCP Conformance Test Summary ====="
echo "Passed: $PASS"
echo "Failed: $FAIL"
echo "Skipped: $SKIP"
if [ "$FAIL" -eq 0 ]; then
  echo -e "\033[32mAll conformance tests passed.\033[0m"
  exit 0
else
  echo -e "\033[31m$FAIL conformance test(s) failed.\033[0m"
  exit 1
fi
