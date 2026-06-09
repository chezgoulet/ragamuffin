package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// ── Handler helpers ──────────────────────────────────────────────────────────

func defaultHandler() *Handler {
	return New(
		[]ToolDefinition{
			{Name: "test_tool", Description: "A test tool", InputSchema: map[string]interface{}{"type": "object"}},
		},
		func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
			if toolName == "ping" {
				return map[string]string{"pong": "ok"}, nil
			}
			return nil, fmt.Errorf("unknown tool: %s", toolName)
		},
		slog.New(slog.DiscardHandler),
		"1.0.0",
	)
}

func rpcRequest(method string, id interface{}, bodyJSON string) *http.Request {
	body := []byte(bodyJSON)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeRPCResponse(t *testing.T, w *httptest.ResponseRecorder) JSONRPCResponse {
	t.Helper()
	var resp JSONRPCResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// ── HTTP routing ─────────────────────────────────────────────────────────────

func TestServeHTTP_GET(t *testing.T) {
	h := defaultHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	// SSE handler blocks on message loop, so run in goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()

	// Let handler start, verify headers
	time.Sleep(50 * time.Millisecond)
	cancel() // kill the SSE loop
	<-done

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %q", ct)
	}
}

func TestServeHTTP_POST(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("initialize", 1, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
}

func TestServeHTTP_PUT(t *testing.T) {
	h := defaultHandler()
	req := httptest.NewRequest(http.MethodPut, "/mcp", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ── handleInitialize ─────────────────────────────────────────────────────────

func TestHandleInitialize(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("initialize", 1, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocol v2024-11-05, got %v", result["protocolVersion"])
	}
	caps := result["capabilities"].(map[string]interface{})
	tools := caps["tools"].(map[string]interface{})
	if tools["listChanged"] != false {
		t.Errorf("expected listChanged=false, got %v", tools["listChanged"])
	}
	serverInfo := result["serverInfo"].(map[string]interface{})
	if serverInfo["version"] != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %v", serverInfo["version"])
	}
}

// ── handleRPC: notifications/initialized ─────────────────────────────────────

func TestHandleInitialized(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("initialized", 2, `{"jsonrpc":"2.0","id":2,"method":"notifications/initialized"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Errorf("expected 202, got %d", w.Code)
	}
}

// ── handleRPC: tools/list ────────────────────────────────────────────────────

func TestHandleToolsList(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("list", 3, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	tools, ok := result["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %v", tools)
	}
	tool := tools[0].(map[string]interface{})
	if tool["name"] != "test_tool" {
		t.Errorf("expected 'test_tool', got %v", tool["name"])
	}
}

// ── handleRPC: tools/call ────────────────────────────────────────────────────

func TestHandleToolsCall_Success(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("call", 4, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ping","arguments":{}}}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("expected map result")
	}
	if result["pong"] != "ok" {
		t.Errorf("expected pong=ok, got %v", result["pong"])
	}
}

func TestHandleToolsCall_InvalidParams(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("call", 7, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":123}}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected -32602 (invalid params), got %d", resp.Error.Code)
	}
}

func TestHandleToolsCall_MissingName(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("call", 8, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{}}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	// Empty name passes JSON parse but handler returns error
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603 (handler error), got %d", resp.Error.Code)
	}
}

func TestHandleToolsCall_NoHandler(t *testing.T) {
	h := New([]ToolDefinition{}, nil, slog.New(slog.DiscardHandler), "1.0.0")
	req := rpcRequest("call", 9, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ping","arguments":{}}}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603 (internal error), got %d", resp.Error.Code)
	}
}

func TestHandleToolsCall_HandlerError(t *testing.T) {
	h := New(
		[]ToolDefinition{},
		func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
			return nil, fmt.Errorf("execution failed")
		},
		slog.New(slog.DiscardHandler),
		"1.0.0",
	)
	req := rpcRequest("call", 10, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"fail","arguments":{}}}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603 (internal error), got %d", resp.Error.Code)
	}
	if resp.Error.Message != "execution failed" {
		t.Errorf("expected 'execution failed', got %q", resp.Error.Message)
	}
}

// ── handleRPC: error cases ───────────────────────────────────────────────────

func TestHandleRPC_UnknownMethod(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("unknown", 5, `{"jsonrpc":"2.0","id":5,"method":"unknown_method"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected -32601, got %d", resp.Error.Code)
	}
}

func TestHandleRPC_BadJSON(t *testing.T) {
	h := defaultHandler()
	body := bytes.NewReader([]byte(`not json`))
	req := httptest.NewRequest(http.MethodPost, "/mcp", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("expected -32700 (parse error), got %d", resp.Error.Code)
	}
}

func TestHandleRPC_WrongVersion(t *testing.T) {
	h := defaultHandler()
	req := rpcRequest("wrong-ver", 6, `{"jsonrpc":"1.0","id":6,"method":"initialize"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	resp := decodeRPCResponse(t, w)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32600 {
		t.Errorf("expected -32600 (invalid request), got %d", resp.Error.Code)
	}
}

// ── pushSSE ──────────────────────────────────────────────────────────────────

func TestPushSSE_ClientConnected(t *testing.T) {
	h := defaultHandler()

	// Register an SSE client
	h.mu.Lock()
	ch := make(chan []byte, 10)
	h.clients["test-session"] = ch
	h.mu.Unlock()

	h.pushSSE("test-session", map[string]string{"hello": "world"})

	select {
	case msg := <-ch:
		var decoded map[string]string
		if err := json.Unmarshal(msg, &decoded); err != nil {
			t.Fatalf("unmarshal push msg: %v", err)
		}
		if decoded["hello"] != "world" {
			t.Errorf("expected 'world', got %q", decoded["hello"])
		}
	default:
		t.Fatal("expected message on SSE channel")
	}
}

func TestPushSSE_NoClient(t *testing.T) {
	h := defaultHandler()
	// pushSSE to non-existent session should not panic
	h.pushSSE("no-such-session", "hello")
}

func TestPushSSE_FullBuffer(t *testing.T) {
	h := defaultHandler()

	h.mu.Lock()
	ch := make(chan []byte, 1) // capacity 1
	h.clients["session-full"] = ch
	h.mu.Unlock()

	// Fill the buffer
	ch <- []byte("existing")
	// Push should drop (non-blocking on full buffer)
	h.pushSSE("session-full", "this should be dropped")

	if len(ch) != 1 {
		t.Errorf("expected 1 item (buffer full, drop), got %d", len(ch))
	}
}

// ── SSE endpoint ─────────────────────────────────────────────────────────────

func TestSSEEndpoint_GeneratesSessionID(t *testing.T) {
	h := defaultHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()

	// Wait for handler to register the client
	time.Sleep(100 * time.Millisecond)

	h.mu.Lock()
	sessionCount := len(h.clients)
	h.mu.Unlock()

	if sessionCount != 1 {
		// If we didn't capture it yet, check right after
		time.Sleep(100 * time.Millisecond)
		h.mu.Lock()
		sessionCount = len(h.clients)
		h.mu.Unlock()
		t.Errorf("expected 1 connected client, got %d", sessionCount)
	}

	cancel()
	<-done
}

func TestSSE_DisconnectRemovesClient(t *testing.T) {
	h := defaultHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/mcp?session_id=disc-test", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()

	time.Sleep(50 * time.Millisecond)

	// Client should be registered
	h.mu.Lock()
	_, exists := h.clients["disc-test"]
	h.mu.Unlock()
	if !exists {
		t.Error("expected client to be registered before disconnect")
	}

	cancel() // trigger disconnect
	<-done

	// After handler returns, the client should be removed
	h.mu.Lock()
	_, exists = h.clients["disc-test"]
	h.mu.Unlock()
	if exists {
		t.Error("expected client to be removed after disconnect")
	}
}

// ── ToolDefinition ───────────────────────────────────────────────────────────

func TestToolDefinitionMarshals(t *testing.T) {
	td := ToolDefinition{
		Name:        "search",
		Description: "Search the knowledge base",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
		},
	}
	data, err := json.Marshal(td)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ToolDefinition
	json.Unmarshal(data, &decoded)
	if decoded.Name != "search" {
		t.Errorf("expected 'search', got %q", decoded.Name)
	}
}

// ── SSE push from RPC handlers ───────────────────────────────────────────────

func TestHandleToolsList_PushesSSE(t *testing.T) {
	h := defaultHandler()

	// Register an SSE client
	h.mu.Lock()
	ch := make(chan []byte, 10)
	h.clients["sse-list-test"] = ch
	h.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/mcp?session_id=sse-list-test",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Should get an SSE notification on the channel
	select {
	case msg := <-ch:
		var notification map[string]interface{}
		if err := json.Unmarshal(msg, &notification); err != nil {
			t.Fatalf("unmarshal SSE msg: %v", err)
		}
		if notification["method"] != "notifications/tools/list_changed" {
			t.Errorf("expected list_changed notification, got %v", notification["method"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected SSE push for tools/list")
	}
}

func TestHandleToolsCall_PushesSSE(t *testing.T) {
	h := defaultHandler()

	// Register an SSE client
	h.mu.Lock()
	ch := make(chan []byte, 10)
	h.clients["sse-call-test"] = ch
	h.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/mcp?session_id=sse-call-test",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ping","arguments":{}}}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Should get an SSE push with the response
	select {
	case msg := <-ch:
		var resp JSONRPCResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			t.Fatalf("unmarshal SSE msg: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("unexpected error in SSE push: %+v", resp.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("expected SSE push for tools/call")
	}
}

func TestHandleSSE_NoFlusher(t *testing.T) {
	h := defaultHandler()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := &nonFlusherResponseWriter{ResponseRecorder: httptest.NewRecorder()}

	// Should return 500 because the ResponseWriter doesn't support Flusher
	h.ServeHTTP(w, req)
	if w.ResponseRecorder.Code != 500 {
		t.Errorf("expected 500 for non-flusher ResponseWriter, got %d", w.ResponseRecorder.Code)
	}
}

func TestSSE_EndpointEventContent(t *testing.T) {
	h := defaultHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/mcp?session_id=ep-test", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: endpoint") {
		t.Error("expected 'event: endpoint' in SSE body")
	}
	if !strings.Contains(body, "session_id=ep-test") {
		t.Error("expected session_id in endpoint URL")
	}
}

// nonFlusherResponseWriter wraps httptest.ResponseRecorder but does NOT implement http.Flusher.
type nonFlusherResponseWriter struct {
	*httptest.ResponseRecorder
}

func (w *nonFlusherResponseWriter) Header() http.Header {
	return w.ResponseRecorder.Header()
}

func (w *nonFlusherResponseWriter) Write(b []byte) (int, error) {
	return w.ResponseRecorder.Write(b)
}

func (w *nonFlusherResponseWriter) WriteHeader(code int) {
	w.ResponseRecorder.WriteHeader(code)
}
