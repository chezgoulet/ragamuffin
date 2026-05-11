// Embedding proxy relays embedding requests from Ragamuffin to a local
// inference server (Ollama, llama.cpp, any OpenAI-compatible endpoint).
// Stateless, no caching.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

var (
	listen      string
	backendURL  string
	backendType string
	model       string
)

func main() {
	listen = envOrDefault("PROXY_LISTEN", ":8001")
	backendURL = envOrDefault("PROXY_BACKEND_URL", "http://ollama:11434")
	backendType = envOrDefault("PROXY_BACKEND_TYPE", "openai_compatible")
	model = envOrDefault("PROXY_MODEL", "nomic-embed-text")

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	logger.Info("embedding proxy starting",
		"listen", listen,
		"backend", backendURL,
		"type", backendType,
		"model", model)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/v1/embeddings", handleEmbeddings)

	srv := &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write([]byte(`{"status":"ok"}`))
}

// handleEmbeddings accepts OpenAI-compatible /v1/embeddings requests
// and relays to the backend, translating formats if needed.
func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", 400)
		return
	}

	var upstream []byte
	var upstreamErr error

	switch backendType {
	case "ollama":
		upstream, upstreamErr = translateToOllama(body)
	default:
		upstream = body
	}

	if upstreamErr != nil {
		slog.Error("translation failed", "error", upstreamErr)
		http.Error(w, "translation error", 500)
		return
	}

	// Forward to backend
	url := fmt.Sprintf("%s/v1/embeddings", backendURL)
	if backendType == "ollama" {
		url = fmt.Sprintf("%s/api/embeddings", backendURL)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(upstream))
	if err != nil {
		http.Error(w, "proxy error", 500)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("backend unreachable", "error", err, "url", url)
		http.Error(w, "backend unreachable", 502)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read backend response", 502)
		return
	}

	if resp.StatusCode >= 400 {
		slog.Error("backend error", "status", resp.StatusCode, "body", string(respBody))
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	// Translate response back to OpenAI format if needed
	if backendType == "ollama" {
		translated, err := translateOllamaResponse(respBody)
		if err != nil {
			slog.Error("response translation failed", "error", err)
			http.Error(w, "translation error", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(translated)
		return
	}

	// Pass through
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// ── Ollama translation ─────────────────────────────────────────────────────────

type openAIEmbeddingRequest struct {
	Input interface{} `json:"input"`
	Model string      `json:"model"`
}

type ollamaEmbeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbeddingResponse struct {
	Embedding []float64 `json:"embedding"`
}

func translateToOllama(body []byte) ([]byte, error) {
	var req openAIEmbeddingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}

	prompt := ""
	switch v := req.Input.(type) {
	case string:
		prompt = v
	case []interface{}:
		// Batch of strings — Ollama only handles single prompts,
		// so we just use the first one
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				prompt = s
			}
		}
	}

	ollamaReq := ollamaEmbeddingRequest{
		Model:  model,
		Prompt: prompt,
	}
	if req.Model != "" {
		ollamaReq.Model = req.Model
	}

	return json.Marshal(ollamaReq)
}

func translateOllamaResponse(body []byte) ([]byte, error) {
	var resp ollamaEmbeddingResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse ollama response: %w", err)
	}

	// Convert to OpenAI embedding response shape
	result := map[string]interface{}{
		"object": "list",
		"data": []map[string]interface{}{
			{
				"object":    "embedding",
				"index":     0,
				"embedding": resp.Embedding,
			},
		},
		"model": model,
		"usage": map[string]interface{}{
			"prompt_tokens": 0,
			"total_tokens":  0,
		},
	}

	return json.Marshal(result)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
