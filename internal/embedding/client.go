// embedding/client.go — Embedding client with retry support.
//
// Embed and EmbedSingle retry on transient failures (network errors, 429,
// 5xx) with exponential backoff before returning an error. This gives the
// embedding service time to recover from rate limits or transient outages
// before callers fall back to zero-vector handling.

package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"
)

// Client generates embeddings via an OpenAI-compatible API.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	client     *http.Client
	maxRetries int
	baseDelay  time.Duration
}

// New creates an embedding client with retry support.
// A zero timeout means no timeout (use with caution).
func New(baseURL, apiKey, model string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		maxRetries: 3,
		baseDelay:  500 * time.Millisecond,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

type embeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// shouldRetry returns true for transient errors that warrant a retry.
func shouldRetry(statusCode int, err error) bool {
	if err != nil {
		return true // network errors, timeouts
	}
	if statusCode == 429 || statusCode >= 500 {
		return true // rate limit or server error
	}
	return false
}

// Embed generates embeddings for the given texts with retry on transient errors.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body := embeddingRequest{
		Input: texts,
		Model: c.model,
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("embedding marshal: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter
			delay := time.Duration(float64(c.baseDelay) * math.Pow(2, float64(attempt-1)))
			slog.Warn("retrying embedding call",
				"attempt", attempt,
				"max_retries", c.maxRetries,
				"delay_ms", delay.Milliseconds(),
				"error", lastErr,
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/embeddings", bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("embedding request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("embedding call: %w", err)
			if shouldRetry(0, err) {
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode != 200 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("embedding API error %d: %s", resp.StatusCode, string(bodyBytes))
			if shouldRetry(resp.StatusCode, nil) {
				continue
			}
			return nil, lastErr
		}

		var er embeddingResponse
		if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("embedding decode: %w", err)
		}
		resp.Body.Close()

		result := make([][]float32, len(er.Data))
		for i, d := range er.Data {
			result[i] = d.Embedding
		}
		return result, nil
	}

	return nil, fmt.Errorf("embedding call failed after %d retries: %w", c.maxRetries, lastErr)
}

// EmbedSingle generates a single embedding, with retry on transient errors.
func (c *Client) EmbedSingle(ctx context.Context, text string) ([]float32, error) {
	results, err := c.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("embedding returned no results")
	}
	return results[0], nil
}

// Health checks if the embedding API is reachable.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.Embed(ctx, []string{"health check"})
	return err
}
