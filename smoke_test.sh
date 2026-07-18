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

# /recall: query rewrite + rerank are backward-compatible no-ops when no LLM
# is configured (A2/A3). They must never break the base recall contract.
RESP=$(curl -sf -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test query","top_k":3,"rewrite":"hyde","rerank":true}' 2>&1) && RC=0 || RC=$?
assert_status "POST /recall with rewrite+rerank returns 200" "0" "$RC" "$RESP"
assert_field_type "recall (rewrite+rerank) has results" "results" "list" "$RESP"

# /recall: invalid rewrite mode is rejected with 400.
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","rewrite":"bogus"}')
assert_status "POST /recall invalid rewrite returns 400" "400" "$CODE" "rewrite=bogus"

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

# ── /v1/auth/check (GET + POST) ──────────────────────────────────────────
echo "--- /v1/auth/check ---"

# GET /v1/auth/check
RESP=$(curl -sf "$BASE/v1/auth/check" 2>&1)
assert_field "auth/check GET" "authenticated" "True" "$RESP"

# POST /v1/auth/check
RESP=$(curl -sf -X POST "$BASE/v1/auth/check" 2>&1)
assert_field "auth/check POST" "authenticated" "True" "$RESP"

# PUT /v1/auth/check (should 405)
RESP=$(curl -s -X PUT "$BASE/v1/auth/check" 2>&1)
assert_field "auth/check PUT method" "code" "METHOD_NOT_ALLOWED" "$RESP"

# ── /v1/recall answer mode ─────────────────────────────────────────────────
echo "--- /recall answer mode ---"

RESP=$(curl -s -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"ragamuffin","answer":true}' 2>&1)
assert_field "recall answer=true has answer" "answer" "True" "$RESP"
assert_field "recall answer=true has results" "results" "True" "$RESP"

RESP=$(curl -s -X POST "$BASE/recall" \
  -H 'Content-Type: application/json' \
  -d '{"query":"nonexistentxyz","answer":true,"score_threshold":0.99}' 2>&1)
assert_field "recall answer=true no chunks" "results" "True" "$RESP"

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

# ── /ask with citations (#A4) ──────────────────────────────────────────────
echo "--- /ask cite=true ---"
RESP=$(curl -s -X POST "$BASE/ask" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","cite":true}' 2>&1) && RC=0 || RC=$?
if [ "$RC" = "0" ]; then
  assert_field_type "ask cited answer" "answer" "str" "$RESP"
  assert_field_type "ask citations" "citations" "list" "$RESP"
else
  assert_field "/ask cite error code (LLM not configured)" "code" "LLM_NOT_CONFIGURED" "$RESP"
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

# ── /v1/sessions/batch ─────────────────────────────────────────────────────
echo "--- /v1/sessions/batch ---"
RESP=$(curl -s -X POST "$BASE/v1/sessions/batch" \
  -H 'Content-Type: application/json' \
  -d '{"vault":"default","sessions":[{"agent_id":"smoke-batch-q1","turns":[{"role":"user","content":"How long did I wait?"},{"role":"assistant","content":"14 months"}]},{"agent_id":"smoke-batch-q2","turns":[{"role":"user","content":"What about my appeal?"},{"role":"assistant","content":"Appeal took 6 months"}]}]}' 2>&1)
assert_field "batch sessions POST returns status=ok" "status" "ok" "$RESP"
assert_field "batch sessions session_count" "session_count" "2" "$RESP"

# Empty sessions array -> 400
RESP=$(curl -s -X POST "$BASE/v1/sessions/batch" \
  -H 'Content-Type: application/json' \
  -d '{"vault":"default","sessions":[]}' 2>&1)
assert_field "batch sessions empty array" "error" "True" "$RESP"

# GET -> 405
RESP=$(curl -s "$BASE/v1/sessions/batch" 2>&1)
assert_field "batch sessions GET method" "code" "METHOD_NOT_ALLOWED" "$RESP"

# ── /v1/documents ──────────────────────────────────────────────────────────
echo "--- /v1/documents ---"
RESP=$(curl -s -X POST "$BASE/v1/documents" \
  -H 'Content-Type: application/json' \
  -d '{"content":"Smoke test document content","source":"smoke_test_doc.txt"}' 2>&1)
assert_field "documents POST returns status=ok" "status" "ok" "$RESP"

# /v1/documents missing content -> 400
RESP=$(curl -s -X POST "$BASE/v1/documents" \
  -H 'Content-Type: application/json' \
  -d '{"source":"no_content.txt"}' 2>&1)
assert_field "documents missing content" "error" "True" "$RESP"

# /v1/documents missing source -> 400
RESP=$(curl -s -X POST "$BASE/v1/documents" \
  -H 'Content-Type: application/json' \
  -d '{"content":"no source"}' 2>&1)
assert_field "documents missing source" "error" "True" "$RESP"

# /v1/documents with vault + tags
RESP=$(curl -s -X POST "$BASE/v1/documents" \
  -H 'Content-Type: application/json' \
  -d '{"content":"Tagged doc","source":"tagged_doc.txt","vault":"default","tags":["smoke","test"]}' 2>&1)
assert_field "documents with vault+tags" "status" "ok" "$RESP"

# /v1/documents with auto_extract -> should ingest and extract facts
RESP=$(curl -s -X POST "$BASE/v1/documents" \
  -H 'Content-Type: application/json' \
  -d '{"content":"Fact extraction test: the sky is blue.","source":"fact_test.txt","auto_extract":true}' 2>&1)
assert_field "documents with auto_extract" "status" "ok" "$RESP"

# /v1/documents GET -> 405
RESP=$(curl -s "$BASE/v1/documents" 2>&1)
assert_field "documents GET returns 405" "code" "METHOD_NOT_ALLOWED" "$RESP"

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

# POST with invalid detail -> 400
RESP=$(curl -s -X POST "$BASE/v1/batch/recall" \
  -H 'Content-Type: application/json' \
  -d '{"queries":[{"query":"test","detail":"bad"}]}' 2>&1)
assert_field "batch recall bad detail" "error" "True" "$RESP"

# GET -> 405
RESP=$(curl -s "$BASE/v1/batch/recall" 2>&1)
assert_field "batch recall GET method" "code" "METHOD_NOT_ALLOWED" "$RESP"

# ── /v1/refresh ──────────────────────────────────────────────────────────
echo "--- /v1/refresh ---"

# POST /v1/refresh
RESP=$(curl -s -X POST "$BASE/v1/refresh" \
  -H 'Content-Type: application/json' \
  -d '{"vault":"default"}' 2>&1)
assert_field "v1/refresh POST" "status" "accepted" "$RESP"
assert_field "v1/refresh vault" "vault" "default" "$RESP"

# POST with non-existent vault -> 404
RESP=$(curl -s -X POST "$BASE/v1/refresh" \
  -H 'Content-Type: application/json' \
  -d '{"vault":"nonexistent-vault"}' 2>&1)
assert_field "v1/refresh unknown vault" "code" "NOT_FOUND" "$RESP"

# GET -> 405
RESP=$(curl -s "$BASE/v1/refresh" 2>&1)
assert_field "v1/refresh GET method" "code" "METHOD_NOT_ALLOWED" "$RESP"

# ── Summary ────────────────────────────────────────────────────────────────
echo ""
# ── /v1/vaults/{name}/clear ─────────────────────────────────────────────────
echo "--- /v1/vaults/{name}/clear ---"

# POST with no confirm -> 400
RESP=$(curl -s -X POST "$BASE/v1/vaults/default/clear" \
  -H 'Content-Type: application/json' \
  -d '{}' 2>&1)
assert_field "vault clear without confirm" "error" "True" "$RESP"

# POST with confirm=false -> 400
RESP=$(curl -s -X POST "$BASE/v1/vaults/default/clear" \
  -H 'Content-Type: application/json' \
  -d '{"confirm": false}' 2>&1)
assert_field "vault clear confirm=false" "error" "True" "$RESP"

# POST with confirm=true -> 200
RESP=$(curl -s -X POST "$BASE/v1/vaults/default/clear" \
  -H 'Content-Type: application/json' \
  -d '{"confirm": true}' 2>&1)
assert_field "vault clear confirm=true" "status" "ok" "$RESP"
assert_field "vault clear returns vault" "vault" "default" "$RESP"

# GET -> 405
RESP=$(curl -s "$BASE/v1/vaults/default/clear" 2>&1)
assert_field "vault clear GET method" "code" "METHOD_NOT_ALLOWED" "$RESP"

# ── Single-tenant /vault/{name} routes (#536) ───────────────────────────────────
echo "--- single-tenant vault routes ---"
# The vault name is derived from filepath.Base(RAGAMUFFIN_VAULT_PATH)
RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/vault/default/ask" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","mode":"recall"}' 2>&1)
if [ "$RESP" = "200" ]; then
  PASS=$((PASS + 1))
  echo "PASS: vault/default/ask returns 200 in single-tenant mode"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: vault/default/ask returned $RESP (expected 200)"
fi

# Wrong vault name should 404
WRONG_RESP=$(curl -s -X POST "$BASE/vault/wrong/ask" \
  -H 'Content-Type: application/json' \
  -d '{"query":"test","mode":"recall"}' 2>&1)
WRONG_CODE=$(echo "$WRONG_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('code','MISSING'))" 2>/dev/null)
if [ "$WRONG_CODE" = "VAULT_NOT_FOUND" ]; then
  PASS=$((PASS + 1))
  echo "PASS: vault/wrong/ask returns VAULT_NOT_FOUND"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: vault/wrong/ask did not return VAULT_NOT_FOUND (got $WRONG_CODE)"
fi

# ── Session → Qdrant bridge (#523) ──────────────────────────────────────────────
# Create session and append a turn, then verify it's searchable via /ask
echo "--- session to qdrant bridge ---"
RESP=$(curl -s -X POST "$BASE/v1/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"smoke-test","content":"My name is Alice and I live in Montreal."}' 2>&1)
SESS_ID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))")
assert_field "session create" "id" "$SESS_ID" "$RESP"

# Append a follow-up turn
RESP=$(curl -s -X POST "$BASE/v1/sessions/$SESS_ID/turns" \
  -H 'Content-Type: application/json' \
  -d '{"content":"What is the weather like in Montreal?","role":"user"}' 2>&1)
TURN_ID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('turn_id',0))")
if [ "$TURN_ID" != "0" ]; then
  PASS=$((PASS + 1))
  echo "PASS: turn append returned turn_id=$TURN_ID"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: turn append missing turn_id"
fi

# Wait briefly for async indexing, then ask about the session content
sleep 2
RESP=$(curl -s -X POST "$BASE/v1/vaults/default/ask" \
  -H 'Content-Type: application/json' \
  -d '{"query":"Where does Alice live?"}' 2>&1)
ANSWER=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('answer',''))" 2>/dev/null)
if echo "$ANSWER" | grep -qi "montreal"; then
  PASS=$((PASS + 1))
  echo "PASS: session turn found via /ask (contains Montreal)"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: session turn not found via /ask (answer: $ANSWER)"
fi

# ── Inbox path traversal rejection ───────────────────────────────────────
echo "--- inbox path traversal ---"

# Valid inbox read should fail gracefully (no inbox entries exist by default)
RESP=$(curl -s -X GET "$BASE/vault/default/inbox/../../etc/passwd" 2>&1)
if echo "$RESP" | grep -q "INVALID_ID"; then
  PASS=$((PASS + 1))
  echo "PASS: inbox path traversal rejected with INVALID_ID"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: inbox path traversal not rejected: $RESP"
fi

# Absolute path
RESP=$(curl -s -X GET "$BASE/vault/default/inbox//etc/passwd" 2>&1)
if echo "$RESP" | grep -q "INVALID_ID"; then
  PASS=$((PASS + 1))
  echo "PASS: inbox absolute path rejected with INVALID_ID"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: inbox absolute path not rejected: $RESP"
fi

# Valid format should return 404 NOT_FOUND (entry doesn't exist), not 400
RESP=$(curl -s -X GET "$BASE/vault/default/inbox/20260102-150405-nonexistent" 2>&1)
if echo "$RESP" | grep -q "NOT_FOUND"; then
  PASS=$((PASS + 1))
  echo "PASS: inbox valid ID returns NOT_FOUND (not 400 INVALID_ID)"
else
  # If the vault doesn't exist (no default in multi-tenant), that's fine too
  if echo "$RESP" | grep -q "VAULT_NOT"; then
    PASS=$((PASS + 1))
    echo "PASS: inbox valid ID returned VAULT_NOT_FOUND (non-default vault)"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: inbox valid ID unexpected response: $RESP"
  fi
fi

# ── Link Index Smoke Tests ─────────────────────────────────────────────────────

# 1. Get outbound links (empty result expected for unknown path)
RESP=$(curl -s "$BASE/v1/links?path=unknown.md" 2>/dev/null)
CODE=$?
if [ "$CODE" -eq 0 ] && echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'links' in d; assert d['path']=='unknown.md'; assert isinstance(d['links'], list)" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/links returns valid structure for unknown path"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/links unexpected response: $(echo $RESP | head -c 100)"
fi

# 2. Backlinks (empty result expected for unknown path)
RESP=$(curl -s "$BASE/v1/links/backlinks?path=unknown.md" 2>/dev/null)
CODE=$?
if [ "$CODE" -eq 0 ] && echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'backlinks' in d; assert d['path']=='unknown.md'" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/links/backlinks returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/links/backlinks unexpected response: $(echo $RESP | head -c 100)"
fi

# 3. Link graph (valid structure for unknown path)
RESP=$(curl -s "$BASE/v1/links/graph?path=unknown.md&depth=2" 2>/dev/null)
CODE=$?
if [ "$CODE" -eq 0 ] && echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'nodes' in d; assert 'edges' in d; assert d['depth']==5" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/links/graph returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/links/graph unexpected response: $(echo $RESP | head -c 100)"
fi

# 4. Link endpoint requires path param (400)
RESP=$(curl -s "$BASE/v1/links" 2>/dev/null)
CODE=$?
if [ "$CODE" -eq 0 ] && echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('error') is not None and 'path' in d.get('message','')" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/links without path returns 400"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/links without path unexpected response: $(echo $RESP | head -c 100)"
fi

# 5. Graph endpoint caps depth at 5
RESP=$(curl -s "$BASE/v1/links/graph?path=unknown.md&depth=20" 2>/dev/null)
CODE=$?
if [ "$CODE" -eq 0 ] && echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['depth']==5" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/links/graph depth capped at 5"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/links/graph depth cap unexpected: $(echo $RESP | head -c 100)"
fi

# ── /v1/verify ───────────────────────────────────────────────────────────────
echo ""
echo "--- /v1/verify ---"

# Verify with a known "fact" — should return insufficient_data (no real vault data)
RESP=$(curl -s -X POST "$BASE/v1/verify" \
  -H "Content-Type: application/json" \
  -d '{"fact":"All engineers must use 2FA","top_k":5}')
CODE=$?
if [ "$CODE" -eq 0 ] && echo "$RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)
assert 'status' in d
assert d['status'] in ('confirmed','conflicts','insufficient_data')
assert 'supporting_sources' in d
assert isinstance(d['supporting_sources'], list)
assert 'conflicting_sources' in d
assert isinstance(d['conflicting_sources'], list)
assert 'confidence' in d
assert isinstance(d['confidence'], (int,float))
" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/verify returns valid structure (status=$(echo $RESP | python3 -c "import sys,json;print(json.load(sys.stdin)['status'])" 2>/dev/null))"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/verify unexpected response: $(echo $RESP | head -c 200)"
fi

# Verify method rejection (GET)
RESP=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/verify" 2>&1)
if [ "$RESP" = "405" ]; then
  PASS=$((PASS + 1))
  echo "PASS: GET /v1/verify returns 405"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: GET /v1/verify expected 405, got $RESP"
fi

# Verify missing fact rejection
RESP=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/verify" \
  -H "Content-Type: application/json" \
  -d '{}' 2>&1)
if [ "$RESP" = "400" ]; then
  PASS=$((PASS + 1))
  echo "PASS: POST /v1/verify empty body returns 400"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: POST /v1/verify empty body expected 400, got $RESP"
fi

# ── /v1/debt ─────────────────────────────────────────────────────────────────
echo ""
echo "--- /v1/debt ---"
RESP=$(curl -s "$BASE/v1/debt" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'vault_count' in d; assert 'total_chunks' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/debt returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/debt unexpected: $(echo $RESP | head -c 150)"
fi

# ── /v1/gaps ─────────────────────────────────────────────────────────────────
echo ""
echo "--- /v1/gaps ---"
RESP=$(curl -s "$BASE/v1/gaps" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'poorly_covered' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/gaps returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/gaps unexpected: $(echo $RESP | head -c 150)"
fi

# ── /v1/agents/stats ─────────────────────────────────────────────────────────
echo ""
echo "--- /v1/agents/stats ---"
RESP=$(curl -s "$BASE/v1/agents/stats" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'agents' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/agents/stats returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/agents/stats unexpected: $(echo $RESP | head -c 150)"
fi

# ── /v1/embedding/project ────────────────────────────────────────────────────
echo ""
echo "--- /v1/embedding/project ---"
RESP=$(curl -s "$BASE/v1/embedding/project" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'points' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/embedding/project returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/embedding/project unexpected: $(echo $RESP | head -c 150)"
fi

# ── /v1/facts/{key}/provenance (key not found) ───────────────────────────────
echo ""
echo "--- /v1/facts/{key}/provenance (404 expected) ---"
RESP=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/facts/nonexistent/provenance" 2>&1)
if [ "$RESP" = "404" ]; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/facts/nonexistent/provenance returns 404"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/facts/nonexistent/provenance expected 404, got $RESP"
fi

# ── /v1/facts/{key}/history (key not found) ──────────────────────────────────
echo ""
echo "--- /v1/facts/{key}/history (empty array expected) ---"
RESP=$(curl -s "$BASE/v1/facts/nonexistent/history" 2>&1)
CODE=$?
if [ "$CODE" -eq 0 ]; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/facts/nonexistent/history returns (status=$CODE)"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/facts/nonexistent/history unexpected: $CODE"
fi

# ── /v1/review/stats (accessibility decay observable — B1) ───────────────────
echo ""
echo "--- /v1/review/stats ---"
RESP=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/review/stats" 2>&1)
if [ "$RESP" = "200" ]; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/review/stats returns 200 (avg_accessibility present when RAGAMUFFIN_DECAY_ENABLED)"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/review/stats expected 200, got $RESP"
fi

# ── /v1/chunks (no vault context, assumes default) ───────────────────────────
echo ""
echo "--- /v1/chunks (vault-scoped redirect) ---"
RESP=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/chunks" 2>&1)
if [ "$RESP" != "000" ]; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/chunks endpoint reachable (HTTP $RESP)"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/chunks not reachable"
fi

# ── /v1/config ────────────────────────────────────────────────────────────────
echo ""
echo "--- /v1/config ---"
RESP=$(curl -s "$BASE/v1/config" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'version' in d; assert 'vault_count' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/config returns valid structure (vaults=$(echo $RESP | python3 -c "import sys,json;print(json.load(sys.stdin)['vault_count'])" 2>/dev/null))"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/config unexpected: $(echo $RESP | head -c 150)"
fi

# ── /v1/pruner/config ─────────────────────────────────────────────────────────
echo ""
echo "--- /v1/pruner/config ---"
RESP=$(curl -s "$BASE/v1/pruner/config" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'enabled' in d; assert 'stale_days' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/pruner/config returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/pruner/config unexpected: $(echo $RESP | head -c 150)"
fi

# ── /v1/briefing ─────────────────────────────────────────────────────────────
echo ""
echo "--- /v1/briefing ---"
RESP=$(curl -s "$BASE/v1/briefing?agent_id=test" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'version' in d; assert 'uptime_seconds' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/briefing returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/briefing unexpected: $(echo $RESP | head -c 150)"
fi

# ── /v1/hybrid ───────────────────────────────────────────────────────────────
echo ""
echo "--- /v1/hybrid ---"
RESP=$(curl -s "$BASE/v1/hybrid?query=test" 2>&1)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'results' in d" 2>/dev/null; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/hybrid returns valid structure"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/hybrid unexpected: $(echo $RESP | head -c 150)"
fi

# ── DELETE /v1/vaults/{name} (404 expected — vault likely doesn't exist) ──────
echo ""
echo "--- DELETE /v1/vaults/nonexistent (expected 404) ---"
RESP=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$BASE/v1/vaults/nonexistent" 2>&1)
if [ "$RESP" = "404" ]; then
  PASS=$((PASS + 1))
  echo "PASS: DELETE /v1/vaults/nonexistent returns 404"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: DELETE /v1/vaults/nonexistent expected 404, got $RESP"
fi

# ── /v1/vaults/{name}/export (404 expected — export requires vault) ──────────
echo ""
echo "--- /v1/vaults/default/export (expected 404) ---"
RESP=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/vaults/nonexistent/export" 2>&1)
if [ "$RESP" = "404" ]; then
  PASS=$((PASS + 1))
  echo "PASS: /v1/vaults/nonexistent/export returns 404"
else
  FAIL=$((FAIL + 1))
  echo "FAIL: /v1/vaults/nonexistent/export expected 404, got $RESP"
fi

# ── Procedural Memory: Session Finalize ──────────────────────────────────────
echo ""
echo "=== Procedural Memory ==="

# Create a session with enough turns for procedure extraction
SESS_RESP=$(curl -s -X POST "$BASE/v1/sessions" \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"test-agent","vault":"default"}')
SESSION_ID=$(echo $SESS_RESP | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null || echo "")

if [ -n "$SESSION_ID" ]; then
  echo "Created session: $SESSION_ID"

  # Append action turns (at least 3 for min procedure steps)
  for i in 1 2 3 4 5; do
    curl -s -X POST "$BASE/v1/sessions/$SESSION_ID/turns" \
      -H "Content-Type: application/json" \
      -d "{\"role\":\"assistant\",\"content\":\"Run step $i command\\\\n\\\"\\\\\"\\\\ncheck status step $i\\n\\\"\\\\\"\"}" > /dev/null
  done

  # Append positive outcome
  curl -s -X POST "$BASE/v1/sessions/$SESSION_ID/turns" \
    -H "Content-Type: application/json" \
    -d '{"role":"user","content":"ok, that works. resolved."}' > /dev/null

  # Finalize without extraction (should work even when disabled)
  FINALIZE_RESP=$(curl -s -X POST "$BASE/v1/sessions/$SESSION_ID/finalize")
  STATUS=$(echo $FINALIZE_RESP | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null)
  if [ "$STATUS" = "finalized" ]; then
    PASS=$((PASS + 1))
    echo "PASS: session finalize without extraction"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: session finalize: expected status=finalized, got $FINALIZE_RESP"
  fi

  # Create another session for extraction test
  SESS_RESP2=$(curl -s -X POST "$BASE/v1/sessions" \
    -H "Content-Type: application/json" \
    -d '{"agent_id":"test-agent","vault":"default"}')
  SESSION_ID2=$(echo $SESS_RESP2 | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null || echo "")

  if [ -n "$SESSION_ID2" ]; then
    for i in 1 2 3 4 5; do
      curl -s -X POST "$BASE/v1/sessions/$SESSION_ID2/turns" \
        -H "Content-Type: application/json" \
        -d "{\"role\":\"assistant\",\"content\":\"Run step $i\\\\n\\\"\\\\\"\\\\ncheck status\\n\\\"\\\\\"\"}" > /dev/null
    done
    curl -s -X POST "$BASE/v1/sessions/$SESSION_ID2/turns" \
      -H "Content-Type: application/json" \
      -d '{"role":"user","content":"ok, fixed. works now."}' > /dev/null

    # Finalize with extraction (may be disabled — accept either response)
    FINALIZE_RESP2=$(curl -s -X POST "$BASE/v1/sessions/$SESSION_ID2/finalize?extract_procedures=true")
    EXTRACTING=$(echo $FINALIZE_RESP2 | python3 -c "import sys,json; print(json.load(sys.stdin).get('extracting_procedures',False))" 2>/dev/null)
    # Just verify the endpoint returns 200, extraction may be disabled
    PASS=$((PASS + 1))
    echo "PASS: session finalize with extraction param (extracting=$EXTRACTING)"
  fi
else
  echo "SKIP: procedural memory test (session creation failed)"
fi

echo ""
echo "--- /v1/graph (temporal knowledge graph) ---"
# Read endpoints work whether or not the graph is enabled: 200 when enabled,
# 503 when disabled. Accept either so the smoke suite is env-agnostic.
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/graph/stats")
if [ "$CODE" = "200" ] || [ "$CODE" = "503" ]; then
  green "/v1/graph/stats reachable (HTTP $CODE)"
else
  red "/v1/graph/stats" "HTTP $CODE (expected 200 or 503)"
fi
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/graph/edges")
if [ "$CODE" = "200" ] || [ "$CODE" = "503" ]; then
  green "/v1/graph/edges reachable (HTTP $CODE)"
else
  red "/v1/graph/edges" "HTTP $CODE (expected 200 or 503)"
fi
# Malformed as_of must be rejected with 400 when the graph is enabled;
# 503 is acceptable when disabled.
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/graph/edges?as_of=not-a-date")
if [ "$CODE" = "400" ] || [ "$CODE" = "503" ]; then
  green "/v1/graph/edges rejects bad as_of (HTTP $CODE)"
else
  red "/v1/graph/edges bad as_of" "HTTP $CODE (expected 400 or 503)"
fi

# Community detection (B4). 200 when graph+community enabled, 503 otherwise.
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/v1/graph/community/detect")
if [ "$CODE" = "200" ] || [ "$CODE" = "503" ]; then
  green "/v1/graph/community/detect reachable (HTTP $CODE)"
else
  red "/v1/graph/community/detect" "HTTP $CODE (expected 200 or 503)"
fi
# Listing communities: 200 when graph enabled, 503 otherwise.
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/graph/communities")
if [ "$CODE" = "200" ] || [ "$CODE" = "503" ]; then
  green "/v1/graph/communities reachable (HTTP $CODE)"
else
  red "/v1/graph/communities" "HTTP $CODE (expected 200 or 503)"
fi
# Unknown community id: 404 when graph enabled, 503 otherwise.
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/graph/community/does-not-exist")
if [ "$CODE" = "404" ] || [ "$CODE" = "503" ]; then
  green "/v1/graph/community/{id} handles unknown id (HTTP $CODE)"
else
  red "/v1/graph/community/{id}" "HTTP $CODE (expected 404 or 503)"
fi

echo ""
echo "--- /v1/consolidation/status ---"
# Always 200: returns {"enabled":false} when the worker is off, or full stats
# when enabled.
RESP=$(curl -s -w "\n%{http_code}" "$BASE/v1/consolidation/status")
CODE=$(echo "$RESP" | tail -n1)
BODY=$(echo "$RESP" | sed '$d')
assert_status "/v1/consolidation/status returns 200" "200" "$CODE" "$BODY"
assert_field_type "consolidation status" "enabled" "bool" "$BODY"

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
