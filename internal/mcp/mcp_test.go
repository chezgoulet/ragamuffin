package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// ── JSON-RPC message types ───────────────────────────────────────────────────

func TestJSONRPCRequest_Marshal(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded JSONRPCRequest
	json.Unmarshal(data, &decoded)
	if decoded.JSONRPC != "2.0" {
		t.Errorf("expected 2.0, got %q", decoded.JSONRPC)
	}
	if decoded.Method != "initialize" {
		t.Errorf("expected initialize, got %q", decoded.Method)
	}
}

func TestJSONRPCResponse_Marshal(t *testing.T) {
	// String ID
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      "req-1",
		Result:  map[string]interface{}{"status": "ok"},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded JSONRPCResponse
	json.Unmarshal(data, &decoded)
	if decoded.JSONRPC != "2.0" {
		t.Errorf("expected 2.0, got %q", decoded.JSONRPC)
	}
}

func TestJSONRPCError_Marshal(t *testing.T) {
	err := JSONRPCError{Code: -32601, Message: "Method not found"}
	data, err2 := json.Marshal(err)
	if err2 != nil {
		t.Fatalf("marshal: %v", err2)
	}

	var decoded JSONRPCError
	json.Unmarshal(data, &decoded)
	if decoded.Code != -32601 {
		t.Errorf("expected -32601, got %d", decoded.Code)
	}
	if decoded.Message != "Method not found" {
		t.Errorf("expected 'Method not found', got %q", decoded.Message)
	}
}

// ── sendRPCResult ────────────────────────────────────────────────────────────

func TestSendRPCResult_NumericID(t *testing.T) {
	w := httptest.NewRecorder()
	sendRPCResult(w, 1, map[string]string{"hello": "world"})

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}

	var resp JSONRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.JSONRPC != "2.0" {
		t.Errorf("expected 2.0, got %q", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

func TestSendRPCResult_StringID(t *testing.T) {
	w := httptest.NewRecorder()
	sendRPCResult(w, "req-1", "ok")

	var resp JSONRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.JSONRPC != "2.0" {
		t.Errorf("expected 2.0, got %q", resp.JSONRPC)
	}
}

func TestSendRPCResult_NilResult(t *testing.T) {
	w := httptest.NewRecorder()
	sendRPCResult(w, nil, nil)

	var resp JSONRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.JSONRPC != "2.0" {
		t.Errorf("expected 2.0, got %q", resp.JSONRPC)
	}
}

// ── sendRPCError ────────────────────────────────────────────────────────────

func TestSendRPCError_RPCError(t *testing.T) {
	w := httptest.NewRecorder()
	sendRPCError(w, 1, -32601, "Method not found")

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}

	var resp JSONRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.JSONRPC != "2.0" {
		t.Errorf("expected 2.0, got %q", resp.JSONRPC)
	}
	if resp.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected -32601, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "Method not found" {
		t.Errorf("expected 'Method not found', got %q", resp.Error.Message)
	}
}

func TestSendRPCError_InvalidParams(t *testing.T) {
	w := httptest.NewRecorder()
	sendRPCError(w, "req-1", -32602, "Invalid params")

	var resp JSONRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected -32602, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "Invalid params" {
		t.Errorf("expected 'Invalid params', got %q", resp.Error.Message)
	}
}

func TestSendRPCError_NullID(t *testing.T) {
	w := httptest.NewRecorder()
	sendRPCError(w, nil, -32700, "Parse error")

	var resp JSONRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("expected -32700, got %d", resp.Error.Code)
	}
}
