// Package mcp implements a minimal Model Context Protocol (MCP) SSE transport
// as a bolt-on to the REST API. Conforms to MCP spec 2024-11-05.
//
// GET /mcp  — open SSE stream (server → client notifications)
// POST /mcp — JSON-RPC request (client → server), responses via SSE
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

// JSONRPCRequest is a standard JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a standard JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError is a JSON-RPC error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ToolDefinition describes an MCP tool.
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// Handler dispatches MCP JSON-RPC requests to tool implementations.
type Handler struct {
	tools       []ToolDefinition
	callHandler ToolCallHandler
	logger      *slog.Logger

	mu      sync.Mutex
	clients map[string]chan []byte // sessionID → SSE message channel
}

// ToolCallHandler is called when a client invokes a tool.
// ctx is derived from the MCP HTTP request context — cancels when the
// client disconnects. Returns the tool result as a JSON-serializable
// value, or an error.
type ToolCallHandler func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)

// New creates an MCP handler.
func New(tools []ToolDefinition, handler ToolCallHandler, logger *slog.Logger) *Handler {
	return &Handler{
		tools:       tools,
		callHandler: handler,
		logger:      logger,
		clients:     make(map[string]chan []byte),
	}
}

// ServeHTTP handles both SSE (GET) and JSON-RPC (POST) on /mcp.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleSSE(w, r)
	case http.MethodPost:
		h.handleRPC(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (h *Handler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	flusher.Flush()

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = fmt.Sprintf("mcp-%d", len(h.clients))
	}

	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[sessionID] = ch
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, sessionID)
		h.mu.Unlock()
	}()

	// Send endpoint event
	endpoint := fmt.Sprintf(`event: endpoint
data: /mcp?session_id=%s

`, sessionID)
	w.Write([]byte(endpoint))
	flusher.Flush()

	h.logger.Info("MCP SSE client connected", "session", sessionID)

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (h *Handler) handleRPC(w http.ResponseWriter, r *http.Request) {
	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendRPCError(w, nil, -32700, "Parse error")
		return
	}

	if req.JSONRPC != "2.0" {
		sendRPCError(w, req.ID, -32600, "Invalid Request")
		return
	}

	sessionID := r.URL.Query().Get("session_id")

	switch req.Method {
	case "initialize":
		h.handleInitialize(w, req)
	case "notifications/initialized":
		// No response needed — client confirms initialization
		w.WriteHeader(202)
	case "tools/list":
		h.handleToolsList(w, req, sessionID)
	case "tools/call":
		h.handleToolsCall(r.Context(), w, req, sessionID)
	default:
		sendRPCError(w, req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
	}
}

func (h *Handler) handleInitialize(w http.ResponseWriter, req JSONRPCRequest) {
	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{
				"listChanged": false,
			},
		},
		"serverInfo": map[string]interface{}{
			"name":    "ragamuffin",
			"version": "0.1.0",
		},
	}
	sendRPCResult(w, req.ID, result)
}

func (h *Handler) handleToolsList(w http.ResponseWriter, req JSONRPCRequest, sessionID string) {
	sendRPCResult(w, req.ID, map[string]interface{}{
		"tools": h.tools,
	})

	// Also push via SSE if client is connected
	if sessionID != "" {
		h.pushSSE(sessionID, map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "notifications/tools/list_changed",
		})
	}
}

func (h *Handler) handleToolsCall(ctx context.Context, w http.ResponseWriter, req JSONRPCRequest, sessionID string) {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		sendRPCError(w, req.ID, -32602, "Invalid params")
		return
	}

	if h.callHandler == nil {
		sendRPCError(w, req.ID, -32603, "No tool handler registered")
		return
	}

	result, err := h.callHandler(ctx, params.Name, params.Arguments)
	if err != nil {
		sendRPCError(w, req.ID, -32603, err.Error())
		return
	}

	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}

	// Send response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(resp)

	// Also push via SSE for streaming clients
	if sessionID != "" {
		h.pushSSE(sessionID, resp)
	}
}

func (h *Handler) pushSSE(sessionID string, msg interface{}) {
	h.mu.Lock()
	ch, ok := h.clients[sessionID]
	h.mu.Unlock()
	if !ok {
		return
	}

	b, err := json.Marshal(msg)
	if err != nil {
		return
	}

	select {
	case ch <- b:
	default:
		// Client buffer full — drop
	}
}

func sendRPCResult(w http.ResponseWriter, id interface{}, result interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func sendRPCError(w http.ResponseWriter, id interface{}, code int, message string) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(resp)
}
