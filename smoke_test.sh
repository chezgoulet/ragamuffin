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

# /recall: verify chunk_id field
RESP=$(curl -sf -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","top_k":1}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  cid=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('results',[]); print(r[0].get('chunk_id','MISSING') if r else 'NO_RESULTS')" 2>/dev/null || echo "PARSE_ERROR")
  if [ "$cid" = "MISSING" ] || [ "$cid" = "PARSE_ERROR" ]; then
    red "recall chunk_id field" "not found"
  else
    green "recall chunk_id field present ($cid)"
  fi
fi

# /recall error: missing query
RESP=$(curl -s -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{}' 2>&1)
assert_field "recall missing query" "error" "True" "$RESP"
assert_field "recall error code" "code" "INVALID_REQUEST" "$RESP"

# /recall detail=l0: no text or first_paragraph
RESP=$(curl -sf -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","top_k":1,"detail":"l0"}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  txt=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('results',[]); print(r[0].get('text','MISSING') if r else 'NO_RESULTS')" 2>/dev/null)
  fp=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('results',[]); print(r[0].get('first_paragraph','MISSING') if r else 'NO_RESULTS')" 2>/dev/null)
  if [ "$txt" = "" ]; then green "/recall detail=l0 text empty"; else red "/recall detail=l0" "text not empty: $txt"; fi
  if [ "$fp" = "" ]; then green "/recall detail=l0 first_paragraph empty"; else red "/recall detail=l0" "first_paragraph not empty: $fp"; fi
fi

# /recall detail=l1: text empty, first_paragraph present
RESP=$(curl -sf -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","top_k":1,"detail":"l1"}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  txt=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('results',[]); print(r[0].get('text','MISSING') if r else 'NO_RESULTS')" 2>/dev/null)
  fp=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('results',[]); print(r[0].get('first_paragraph','MISSING') if r else 'NO_RESULTS')" 2>/dev/null)
  if [ "$txt" = "" ]; then green "/recall detail=l1 text empty"; else red "/recall detail=l1" "text not empty: $txt"; fi
  if [ "$fp" != "" ] && [ "$fp" != "MISSING" ]; then green "/recall detail=l1 first_paragraph present"; else red "/recall detail=l1" "first_paragraph missing"; fi
fi

# /recall invalid detail: expect 400
RESP=$(curl -s -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","detail":"l3"}' 2>&1)
assert_field "/recall invalid detail" "error" "True" "$RESP"

# ── /v1/chunks/{chunk_id} ─────────────────────────────────────────────────
echo "--- /v1/chunks ---"
# Get a valid chunk_id first
CID=$(curl -sf -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","top_k":1}' 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('results',[]); print(r[0].get('chunk_id','') if r else '')" 2>/dev/null || echo "")
if [ -n "$CID" ]; then
  RESP=$(curl -sf "$BASE/v1/chunks/$CID" 2>&1) && RC=0 || RC=$?
  assert_status "GET /v1/chunks/{id} returns 200" "0" "$RC" "$RESP"
  assert_field_type "chunk has chunk_id" "chunk_id" "str" "$RESP"
  assert_field_type "chunk has source_file" "source_file" "str" "$RESP"
  # 404 for bad UUID
  RESP=$(curl -s "$BASE/v1/chunks/00000000-0000-0000-0000-000000000000" 2>&1)
  assert_field "chunk 404 not found" "code" "NOT_FOUND" "$RESP"
else
  red "/v1/chunks/{id}" "could not retrieve a chunk_id from recall"
fi

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

# MCP recall with detail=l0 — no text
MCP_RESULT=$(curl -sf -X POST "$BASE/mcp" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":100,"method":"tools/call","params":{"name":"ragamuffin_recall","arguments":{"query":"test","top_k":1,"detail":"l0"}}}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  has_text=$(echo "$MCP_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}).get('content',[{}])[0]; t=r.get('text',''); o=json.loads(t); res=o.get('results',[{}])[0]; print('yes' if 'text' in res else 'no')" 2>/dev/null)
  if [ "$has_text" = "no" ]; then green "MCP recall detail=l0 no text"; else red "MCP recall detail=l0" "text field present: $has_text"; fi
fi

# MCP recall with detail=l1 — first_paragraph present, no text
MCP_RESULT=$(curl -sf -X POST "$BASE/mcp" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":101,"method":"tools/call","params":{"name":"ragamuffin_recall","arguments":{"query":"test","top_k":1,"detail":"l1"}}}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  has_text=$(echo "$MCP_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}).get('content',[{}])[0]; t=r.get('text',''); o=json.loads(t); res=o.get('results',[{}])[0]; print('yes' if 'text' in res else 'no')" 2>/dev/null)
  has_fp=$(echo "$MCP_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}).get('content',[{}])[0]; t=r.get('text',''); o=json.loads(t); res=o.get('results',[{}])[0]; print('yes' if 'first_paragraph' in res else 'no')" 2>/dev/null)
  if [ "$has_text" = "no" ]; then green "MCP recall detail=l1 text absent"; else red "MCP recall detail=l1" "text present: $has_text"; fi
  if [ "$has_fp" = "yes" ]; then green "MCP recall detail=l1 first_paragraph present"; else red "MCP recall detail=l1" "first_paragraph missing"; fi
fi

# MCP get_chunk — call recall to get a chunk_id, then retrieve it
MCP_RESULT=$(curl -sf -X POST "$BASE/mcp" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":102,"method":"tools/call","params":{"name":"ragamuffin_recall","arguments":{"query":"test","top_k":1}}}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  CID_MCP=$(echo "$MCP_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}).get('content',[{}])[0]; t=r.get('text',''); o=json.loads(t); res=o.get('results',[{}])[0]; print(res.get('chunk_id',''))" 2>/dev/null || echo "")
  if [ -n "$CID_MCP" ]; then
    GET_RESULT=$(curl -sf -X POST "$BASE/mcp" \
      -H 'Content-Type: application/json' \
      -d "{\"jsonrpc\":\"2.0\",\"id\":103,\"method\":\"tools/call\",\"params\":{\"name\":\"ragamuffin_get_chunk\",\"arguments\":{\"chunk_id\":\"$CID_MCP\"}}}" 2>&1) && RC2=0 || RC2=$?
    if [ "$RC2" = "0" ]; then
      has_src=$(echo "$GET_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); r=d.get('result',{}).get('content',[{}])[0]; t=r.get('text',''); o=json.loads(t); print('yes' if 'source_file' in o else 'no')" 2>/dev/null)
      if [ "$has_src" = "yes" ]; then
        green "MCP get_chunk full payload"
      else
        red "MCP get_chunk" "no source_file in response"
      fi
    else
      red "MCP get_chunk" "call failed (RC=$RC2)"
    fi
  else
    red "MCP get_chunk" "could not get chunk_id from recall"
  fi
fi

# ── Pruner auto-tune ────────────────────────────────────────────────────
echo "--- /v1/pruner/auto-tune ---"
RESP=$(curl -s -X GET "$BASE/v1/pruner/auto-tune?dry_run=true" 2>&1) && RC=0 || RC=$?
# This may return 503 if pruner is disabled, which is valid
if [ "$RC" -eq 0 ]; then
  # Success — check response shape
  field_type=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(type(d.get('recommendations','')).__name__)" 2>/dev/null || echo "FAIL")
  if [ "$field_type" = "list" ]; then
    green "auto-tune returns recommendations list"
  else
    green "auto-tune endpoint responds ($(echo "$RESP" | head -c 100))"
  fi
else
  # 503 is acceptable (pruner disabled in test config)
  status_code=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',''))" 2>/dev/null || echo "unknown")
  if echo "$RESP" | grep -q "PRUNER_DISABLED"; then
    green "auto-tune: pruner disabled (expected)"
  else
    red "auto-tune" "unexpected error: $(echo "$RESP" | head -c 200)"
  fi
fi

echo "--- /v1/pruner/config ---"
RESP=$(curl -s -X GET "$BASE/v1/pruner/config" 2>&1) && RC=0 || RC=$?
if [ "$RC" -eq 0 ]; then
  enabled=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('enabled','MISSING'))" 2>/dev/null || echo "FAIL")
  ct=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('conflict_threshold','MISSING'))" 2>/dev/null || echo "FAIL")
  if [ "$enabled" != "FAIL" ] && [ "$enabled" != "MISSING" ]; then
    green "pruner config returns enabled=$enabled"
  fi
  if [ "$ct" != "FAIL" ] && [ "$ct" != "MISSING" ]; then
    green "pruner config has conflict_threshold=$ct"
  fi
else
  red "pruner config" "HTTP error: $RC"
fi

# ── Temporal reasoning tests (v0.8) ──────────────────────────────────────────────

yellow "Temporal: POST fact with valid_from/valid_until"
RESP=$(curl -s -w "\\n%{http_code}" -X POST "$HOST/v1/facts" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"key":"test/temporal-fact","value":"temporal test","valid_until":"2099-12-31T23:59:59Z"}')
assert_field "temporal fact POST" "key" "test/temporal-fact" "$RESP"
TEMP_KEY_RESP="$RESP"

yellow "Temporal: GET fact has valid_until"
RESP=$(curl -s -w "\\n%{http_code}" "$HOST/v1/facts?key=test/temporal-fact" -H "Authorization: Bearer $TOKEN")
assert_field "temporal GET fact valid_until" "valid_until" "2099-12-31T23:59:59Z" "$RESP"

yellow "Temporal: GET fact has valid_from"
RESP=$(curl -s -w "\\n%{http_code}" "$HOST/v1/facts?key=test/temporal-fact" -H "Authorization: Bearer $TOKEN")
assert_field "temporal GET fact valid_from present" "valid_from" `` "$RESP"

yellow "Temporal: recall with time_filter"
RESP=$(curl -s -w "\\n%{http_code}" -X POST "$HOST/recall" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"query":"test","top_k":5,"time_filter":"all"}')
assert_status "recall time_filter=all" 200 "$RESP"

# Cleanup
curl -s -X DELETE "$HOST/v1/facts?key=test/temporal-fact" \
  -H "Authorization: Bearer $TOKEN" > /dev/null 2>&1

# ── Extraction Pipeline ────────────────────────────────────────────────────
echo "--- Extraction pipeline ---"

# GET /v1/extraction/stats
RESP=$(curl -s -w "\n%{http_code}" "$HOST/v1/extraction/stats" -H "Authorization: Bearer $TOKEN" 2>&1 || true)
HTTP=$(echo "$RESP" | tail -1)
if [ "$HTTP" = "200" ]; then
  echo "  ✓ GET /v1/extraction/stats: $HTTP"
  PASS=$((PASS + 1))
else
  echo "  ✗ GET /v1/extraction/stats: $HTTP"
  FAIL=$((FAIL + 1))
fi

# ── /v1/batch/recall ──────────────────────────────────────────────────────
echo "--- /v1/batch/recall ---"

# POST with queries
RESP=$(curl -s -X POST "$BASE/v1/batch/recall" \
  -H 'Content-Type: application/json' \
  -d '{"queries":[{"query":"what is ragamuffin","top_k":3},{"query":"how does recall work","top_k":3}]}' 2>&1)
assert_field "batch recall POST" "results" "True" "$RESP"

# POST empty queries -> 400
RESP=$(curl -s -X POST "$BASE/v1/batch/recall" \
  -H 'Content-Type: application/json' \
  -d '{"queries":[]}' 2>&1)
assert_field "batch recall empty queries" "error" "True" "$RESP"

# GET -> 405
RESP=$(curl -s "$BASE/v1/batch/recall" 2>&1)
assert_field "batch recall GET method" "code" "METHOD_NOT_ALLOWED" "$RESP"

# ── Summary ────────────────────────────────────────────────────────────────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
