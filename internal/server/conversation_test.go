package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
	qdrant "github.com/qdrant/go-client/qdrant"
)

// ── Local mocks for conversation tests ────────────────────────────────────────

type mockLLM struct {
	synthesizeFn       func(query, context string) string
	synthesizeCallCount int
}

func (m *mockLLM) Synthesize(_ context.Context, query, context string) (string, error) {
	m.synthesizeCallCount++
	if m.synthesizeFn != nil {
		return m.synthesizeFn(query, context), nil
	}
	return "", nil
}

func (m *mockLLM) Compare(_ context.Context, chunkA, chunkB, sourceA, sourceB string) (string, error) {
	return "", nil
}

func (m *mockLLM) Health(_ context.Context) error { return nil }

type mockEmbedder struct {
	embedSingleFn func(text string) []float32
	embedErr      bool
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	return nil, nil
}

func (m *mockEmbedder) EmbedSingle(_ context.Context, text string) ([]float32, error) {
	if m.embedErr {
		return nil, fmt.Errorf("mock: embed failed")
	}
	if m.embedSingleFn != nil {
		return m.embedSingleFn(text), nil
	}
	return []float32{}, nil
}

func (m *mockEmbedder) Health(_ context.Context) error { return nil }

// ── Mock FactStore for conversation tests ─────────────────────────────────────

type conversationMockStore struct {
	points []*qdrant.PointStruct
}

func (m *conversationMockStore) Upsert(_ context.Context, points []*qdrant.PointStruct) error {
	m.points = append(m.points, points...)
	return nil
}

func (m *conversationMockStore) Scroll(_ context.Context, limit uint32, offset *qdrant.PointId) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error) {
	return nil, nil, nil
}
func (m *conversationMockStore) ScrollFiltered(_ context.Context, collection string, filter *qdrant.Filter, limit uint32, offset string) ([]*qdrant.RetrievedPoint, error) {
	return nil, nil
}
func (m *conversationMockStore) Search(_ context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string, filter *qdrant.Filter) ([]*qdrant.ScoredPoint, error) {
	return nil, nil
}
func (m *conversationMockStore) DeleteBySource(_ context.Context, sourceFile string) error { return nil }
func (m *conversationMockStore) DeleteFiltered(_ context.Context, collection string, filter *qdrant.Filter) error { return nil }
func (m *conversationMockStore) Count(_ context.Context) (uint64, error) { return 0, nil }
func (m *conversationMockStore) CountFiles(_ context.Context) (int, error) { return 0, nil }
func (m *conversationMockStore) CreatePayloadIndex(_ context.Context, collection, field, fieldType string) error { return nil }
func (m *conversationMockStore) Health(_ context.Context) error { return nil }
func (m *conversationMockStore) Close() error { return nil }
func (m *conversationMockStore) GetVectorSize(_ context.Context, collectionName string) (uint64, error) { return 0, nil }
func (m *conversationMockStore) GetPoints(_ context.Context, collection string, ids []*qdrant.PointId) ([]*qdrant.RetrievedPoint, error) { return nil, nil }
func (m *conversationMockStore) SetPayload(_ context.Context, collection string, points []*qdrant.PointId, payload map[string]*qdrant.Value) error { return nil }
func (m *conversationMockStore) Collection() string { return "test_facts" }

// ── Test: conversation facts have non-zero vectors ────────────────────────────

func TestIngestConversation_FactsHaveNonZeroVectors(t *testing.T) {
	mockStore := &conversationMockStore{}
	mockLLM := &mockLLM{
		synthesizeFn: func(query, context string) string {
			return `[{"key":"user_likes_pizza","value":"The user prefers pizza over pasta","confidence":8,"category":"preference","ttl_days":90}]`
		},
	}
	embedCalled := false
	mockEmbed := &mockEmbedder{
		embedSingleFn: func(text string) []float32 {
			embedCalled = true
			if strings.Contains(text, "pizza") {
				return []float32{0.42, 0.18, 0.93, 0.67}
			}
			return []float32{0.1, 0.2, 0.3, 0.4}
		},
	}

	cfg := &config.Config{
		FactsCollection: "test_facts",
	}
	srv := &Server{
		facts:    mockStore,
		cfg:      cfg,
		llm:      mockLLM,
		embedder: mockEmbed,
		logger:   testLogger(t),
	}

	body := conversationRequest{
		Vault: "default",
		Messages: []conversationMessage{
			{Role: "user", Content: "I really love pizza"},
			{Role: "assistant", Content: "That's great! What toppings do you like?"},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/conversation", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()
	srv.handleIngestConversation(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the LLM was called
	if mockLLM.synthesizeCallCount == 0 {
		t.Error("LLM Synthesize was never called")
	}

	// Verify the embedder was called
	if !embedCalled {
		t.Error("embedder EmbedSingle was never called — vectors are still zero-filled")
	}

	// Verify points were upserted with non-zero vectors
	if len(mockStore.points) == 0 {
		t.Fatal("no points were upserted to the fact store")
	}

	for i, p := range mockStore.points {
		vec := p.GetVectors().GetVector().GetData()
		if len(vec) == 0 {
			t.Errorf("point %d has empty vector", i)
			continue
		}
		allZero := true
		for _, v := range vec {
			if v != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			t.Errorf("point %d has all-zero vector — embedding was not applied", i)
		}
	}
}

// ── Test: embedding failure falls back to zero vector ─────────────────────────

func TestIngestConversation_EmbeddingFailureFallsBack(t *testing.T) {
	mockStore := &conversationMockStore{}
	mockLLM := &mockLLM{
		synthesizeFn: func(query, context string) string {
			return `[{"key":"test_fact","value":"Test value","confidence":5,"category":"knowledge","ttl_days":30}]`
		},
	}
	mockEmbed := &mockEmbedder{
		embedErr: true, // EmbedSingle returns error
	}

	cfg := &config.Config{
		FactsCollection: "test_facts",
		FactsVectorSize: 4,
	}
	srv := &Server{
		facts:    mockStore,
		cfg:      cfg,
		llm:      mockLLM,
		embedder: mockEmbed,
		logger:   testLogger(t),
	}

	body := conversationRequest{
		Vault: "default",
		Messages: []conversationMessage{
			{Role: "user", Content: "Test conversation"},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/conversation", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()
	srv.handleIngestConversation(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Point should still be stored (with zero vector fallback)
	if len(mockStore.points) == 0 {
		t.Fatal("no points upserted — embedding failure should not prevent storage")
	}

	vec := mockStore.points[0].GetVectors().GetVector().GetData()
	if len(vec) != 4 {
		t.Fatalf("expected vector size 4, got %d", len(vec))
	}

	// Verify zero vector fallback
	allZero := true
	for _, v := range vec {
		if v != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Error("expected zero vector fallback on embedding failure, but got non-zero values")
	}
}

// ── Test: empty conversation returns error ────────────────────────────────────

func TestIngestConversation_EmptyMessages(t *testing.T) {
	body := conversationRequest{
		Vault:    "default",
		Messages: []conversationMessage{},
	}
	bodyBytes, _ := json.Marshal(body)

	srv := &Server{logger: testLogger(t)}
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/conversation", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()
	srv.handleIngestConversation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty messages, got %d", w.Code)
	}
}
