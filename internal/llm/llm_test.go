package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── New / nil client guard ──────────────────────────────────────────────────

func TestNew_EmptyProvider(t *testing.T) {
	c := New("", "http://example.com", "key", "model", time.Second)
	if c != nil {
		t.Error("expected nil for empty provider")
	}
}

func TestNew_ValidProvider(t *testing.T) {
	c := New("openai", "http://example.com", "key", "gpt-4", time.Second)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.provider != "openai" {
		t.Errorf("expected provider 'openai', got %q", c.provider)
	}
	if c.model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got %q", c.model)
	}
	if c.baseURL != "http://example.com" {
		t.Errorf("expected baseURL 'http://example.com', got %q", c.baseURL)
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c := New("openai", "http://example.com/", "key", "model", time.Second)
	if c.baseURL != "http://example.com" {
		t.Errorf("expected baseURL without trailing slash, got %q", c.baseURL)
	}
}

// ── Synthesize ───────────────────────────────────────────────────────────────

func newTestLLMServer(t *testing.T, responseContent string, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			t.Errorf("expected /v1/chat/completions, got %s", r.URL.Path)
		}

		// Verify request structure
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model == "" {
			t.Error("expected model in request")
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Errorf("expected 1 user message, got %d messages", len(req.Messages))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: responseContent}},
			},
		})
	}))
}

func TestSynthesize_Success(t *testing.T) {
	srv := newTestLLMServer(t, "The answer is 42.", 200)
	defer srv.Close()

	c := New("openai", srv.URL, "test-key", "gpt-4", time.Second)
	result, err := c.Synthesize(context.Background(), "what is life?", "some context")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if result != "The answer is 42." {
		t.Errorf("expected 'The answer is 42.', got %q", result)
	}
}

func TestSynthesize_NilClient(t *testing.T) {
	var c *Client
	_, err := c.Synthesize(context.Background(), "q", "ctx")
	if err == nil {
		t.Fatal("expected error for nil client")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected 'not configured' error, got %q", err.Error())
	}
}

func TestSynthesize_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error": "invalid API key"}`))
	}))
	defer srv.Close()

	c := New("openai", srv.URL, "bad-key", "model", time.Second)
	_, err := c.Synthesize(context.Background(), "q", "ctx")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got %q", err.Error())
	}
}

func TestSynthesize_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatResponse{Choices: []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}{}})
	}))
	defer srv.Close()

	c := New("openai", srv.URL, "key", "model", time.Second)
	_, err := c.Synthesize(context.Background(), "q", "ctx")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	expected := "no choices"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected %q in error, got %q", expected, err.Error())
	}
}

func TestSynthesize_ServerDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	c := New("openai", srv.URL, "key", "model", time.Second)
	_, err := c.Synthesize(context.Background(), "q", "ctx")
	if err == nil {
		t.Fatal("expected error for closed server")
	}
}

func TestSynthesize_RequestIncludesContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		msg := req.Messages[0].Content
		if !strings.Contains(msg, "Context:") || !strings.Contains(msg, "my knowledge") {
			t.Errorf("context not included in prompt: %s", msg)
		}
		if !strings.Contains(msg, "Question:") || !strings.Contains(msg, "my question") {
			t.Errorf("question not included in prompt: %s", msg)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "answer"}},
			},
		})
	}))
	defer srv.Close()

	c := New("openai", srv.URL, "key", "model", time.Second)
	_, err := c.Synthesize(context.Background(), "my question", "my knowledge")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
}

func TestSynthesize_AuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "Bearer my-secret-key" {
			t.Errorf("expected 'Bearer my-secret-key', got %q", h)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "ok"}},
			},
		})
	}))
	defer srv.Close()

	c := New("openai", srv.URL, "my-secret-key", "model", time.Second)
	_, err := c.Synthesize(context.Background(), "q", "ctx")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
}

// ── Compare ──────────────────────────────────────────────────────────────────

func TestCompare_ConflictDetected(t *testing.T) {
	srv := newTestLLMServer(t, "These passages contradict each other on the date.", 200)
	defer srv.Close()

	c := New("openai", srv.URL, "key", "model", time.Second)
	result, err := c.Compare(context.Background(), "chunkA", "chunkB", "sourceA", "sourceB")
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if result != "These passages contradict each other on the date." {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestCompare_NoConflict(t *testing.T) {
	srv := newTestLLMServer(t, "NO_CONFLICT", 200)
	defer srv.Close()

	c := New("openai", srv.URL, "key", "model", time.Second)
	result, err := c.Compare(context.Background(), "chunkA", "chunkB", "sourceA", "sourceB")
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for NO_CONFLICT, got %q", result)
	}
}

func TestCompare_EmptyResponse(t *testing.T) {
	srv := newTestLLMServer(t, "", 200)
	defer srv.Close()

	c := New("openai", srv.URL, "key", "model", time.Second)
	result, err := c.Compare(context.Background(), "a", "b", "s1", "s2")
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestCompare_NilClient(t *testing.T) {
	var c *Client
	_, err := c.Compare(context.Background(), "a", "b", "s1", "s2")
	if err == nil {
		t.Fatal("expected error for nil client")
	}
}
