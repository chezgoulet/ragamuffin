package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/extraction"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/server/testutil"
	qdrant "github.com/qdrant/go-client/qdrant"
)

// mockQdrantStore is a minimal fact store mock for use in server handler tests.
type mockQdrantStore struct{}

func (m *mockQdrantStore) Upsert(_ context.Context, points []*qdrant.PointStruct) error { return nil }
func (m *mockQdrantStore) Scroll(_ context.Context, limit uint32, offset *qdrant.PointId) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error) { return nil, nil, nil }
func (m *mockQdrantStore) Close() error { return nil }
func (m *mockQdrantStore) Collection() string { return "test" }
func (m *mockQdrantStore) GetVectorSize(_ context.Context, _ string) (uint64, error) { return 0, nil }
func (m *mockQdrantStore) GetPoints(_ context.Context, _ string, _ []*qdrant.PointId) ([]*qdrant.RetrievedPoint, error) { return nil, nil }
func (m *mockQdrantStore) SetPayload(_ context.Context, _ string, _ []*qdrant.PointId, _ map[string]*qdrant.Value) error { return nil }
func (m *mockQdrantStore) ScrollFiltered(_ context.Context, collection string, filter *qdrant.Filter, limit uint32, offset string) ([]*qdrant.RetrievedPoint, error) { return nil, nil }
func (m *mockQdrantStore) Search(_ context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string, filter *qdrant.Filter) ([]*qdrant.ScoredPoint, error) { return nil, nil }
func (m *mockQdrantStore) DeleteBySource(_ context.Context, sourceFile string) error { return nil }
func (m *mockQdrantStore) DeleteFiltered(_ context.Context, collection string, filter *qdrant.Filter) error { return nil }
func (m *mockQdrantStore) Count(_ context.Context) (uint64, error) { return 0, nil }
func (m *mockQdrantStore) CountFiles(_ context.Context) (int, error) { return 0, nil }
func (m *mockQdrantStore) CreatePayloadIndex(_ context.Context, collection, field, fieldType string) error { return nil }
func (m *mockQdrantStore) UpdateVectors(_ context.Context, _ string, _ []*qdrant.PointVectors) error { return nil }
func (m *mockQdrantStore) Health(_ context.Context) error { return nil }

// ── Test: extraction goroutine survives request context cancellation ──────────

func TestDocumentUpload_ExtractionSurvivesRequestCancellation(t *testing.T) {
	// Build a chain: real extraction.Extractor → signalLLM wrapper → mockLLM backend
	mockLLM := &testutil.MockLLM{
		SynthesizeFn: func(_ context.Context, _, _ string) (string, error) {
			// Return empty fact array — extraction completes quickly
			return `[]`, nil
		},
	}

	// A real mock fact store for the extractor
	factStore := &mockQdrantStore{}

	// Create the extractor with mock dependencies
	ext := extraction.New(
		extraction.Config{Enabled: true},
		mockLLM,
		&testutil.MockEmbedder{},
		factStore,
		testLogger(t),
	)

	// Wrap with signal — not strictly needed for this test since the extractor
	// itself uses mock components that won't block, but kept for clarity

	// Set up server with background shutdown context
	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	defer cancelShutdown()

	srv := &Server{
		cfg: &config.Config{
			FactsCollection:     "test_facts",
			AutoProvisionVaults: true,
		},
		shutdownCtx: shutdownCtx,
		extractor:   ext,
		logger:      testLogger(t),
	}

	// Register a mock indexer for "default" vault so the handler passes
	// through the ingest phase and reaches the extraction goroutine.
	mg := indexer.NewManager()
	mockQC := &mockQdrantStore{}
	mockEC := &testutil.MockEmbedder{
		EmbedFn: func(_ context.Context, texts []string) ([][]float32, error) {
			vec := make([][]float32, len(texts))
			for i := range texts {
				vec[i] = []float32{0.1, 0.2, 0.3, 0.4}
			}
			return vec, nil
		},
	}
	testIdx := indexer.New("/tmp/test-vault", "test-vault", mockQC, mockEC, testLogger(t))
	if err := mg.Add("default", testIdx, mockQC); err != nil {
		t.Fatalf("failed to add test indexer: %v", err)
	}
	srv.indexers = mg

	// Build document upload request with auto_extract
	body := map[string]interface{}{
		"content":      "Alice is a software engineer who lives in Montreal and enjoys skiing.",
		"source":       "test-alice.md",
		"vault":        "default",
		"auto_extract": true,
	}
	bodyBytes, _ := json.Marshal(body)

	// Create request with cancellable context (simulates HTTP request lifecycle)
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	req := httptest.NewRequest(http.MethodPost, "/v1/documents", bytes.NewReader(bodyBytes))
	req = req.WithContext(reqCtx)

	w := httptest.NewRecorder()
	srv.handleDocuments(w, req)

	// Handler returned. Now cancel the request context, simulating the HTTP
	// response being sent and the request context being cleaned up.
	cancelReq()

	// The extraction goroutine was started with s.shutdownCtx, so it should
	// still be running after the request context is cancelled.
	// Verify the handler succeeded first.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("unexpected status: %v", resp["status"])
	}

	// The real test: if the extraction goroutine had used r.Context() (which
	// is now cancelled), it would fail silently. Because we fixed it to use
	// s.shutdownCtx, it completes. We can verify this by checking that the
	// handler didn't panic and returned successfully — the goroutine is
	// fire-and-forget, so any failure would be silent.
	//
	// More concretely: we can check that the mockLLM was called. If the
	// extraction goroutine was cancelled by r.Context(), the LLM call would
	// not complete. Give a brief delay for the goroutine to run.
	time.Sleep(100 * time.Millisecond)

	if mockLLM.SynthesizeCallCount.Load() == 0 {
		t.Error("extraction LLM was never called — goroutine may have been cancelled by request context")
	}
}
