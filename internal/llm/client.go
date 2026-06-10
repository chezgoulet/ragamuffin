package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls an LLM for synthesis and contradiction detection.
type Client struct {
	provider string
	baseURL  string
	apiKey   string
	model    string
	client   *http.Client
}

// New creates an LLM client. Returns nil if provider is empty.
func New(provider, baseURL, apiKey, model string, timeout time.Duration) *Client {
	if provider == "" {
		return nil
	}
	return &Client{
		provider: provider,
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		model:    model,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Synthesize asks the LLM a question with context and returns an answer.
func (c *Client) Synthesize(ctx context.Context, query, context string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("LLM not configured")
	}

	prompt := fmt.Sprintf(
		"Using the following context from a knowledge base, answer the question. "+
			"Be concise and cite specific sources when possible. "+
			"If multiple sources provide conflicting information about the same topic, "+
			"prefer the information from the later chunk (higher chunk number or more recently stated).\n\n"+
			"Context:\n%s\n\nQuestion: %s",
		context, query,
	)

	return c.chat(ctx, prompt)
}

// Compare asks the LLM whether two chunks contradict each other.
// Returns a summary if there's a conflict, empty string if none.
// Health checks connectivity to the LLM provider by making a GET to the base URL.
func (c *Client) Health(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("LLM not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (c *Client) Compare(ctx context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("LLM not configured")
	}

	prompt := fmt.Sprintf(
		"Compare these two passages from a knowledge base. If they contain "+
			"contradictory or inconsistent information, describe the conflict in one sentence. "+
			"If they are consistent or about different topics, respond with 'NO_CONFLICT'.\n\n"+
			"Passage A (from %s):\n%s\n\n"+
			"Passage B (from %s):\n%s",
		sourceA, chunkA, sourceB, chunkB,
	)

	result, err := c.chat(ctx, prompt)
	if err != nil {
		return "", err
	}

	result = strings.TrimSpace(result)
	if result == "NO_CONFLICT" || result == "" {
		return "", nil
	}
	return result, nil
}

func (c *Client) chat(ctx context.Context, userMessage string) (string, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []message{
			{Role: "user", Content: userMessage},
		},
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := c.baseURL + "/v1/chat/completions"

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(body))
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", fmt.Errorf("LLM decode: %w", err)
	}

	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return cr.Choices[0].Message.Content, nil
}
