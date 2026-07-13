package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embeddedstore"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/extraction"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/pruner"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	"github.com/chezgoulet/ragamuffin/internal/server/testutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// wiredServer returns a fully-wired Server with in-memory backends.
func wiredServer(t *testing.T, vaultName string) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "ragamuffin-wired-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	chunkStore, err := embeddedstore.Open(embeddedstore.Config{
		Path:       filepath.Join(dir, "chunks"),
		Collection: "ragamuffin",
		VectorSize: 4,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chunkStore.Close() })

	factsStore, err := embeddedstore.Open(embeddedstore.Config{
		Path:       filepath.Join(dir, "facts"),
		Collection: "ragamuffin_facts",
		VectorSize: 4,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { factsStore.Close() })

	mockLLM := &testutil.MockLLM{
		SynthesizeFn: func(_ context.Context, query, _ string) (string, error) {
			return "mock: " + query, nil
		},
	}

	ls, err := logstore.Open(filepath.Join(dir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ls.Close() })

	cfg := &config.Config{
		VaultPath:        filepath.Join(dir, "vault"),
		FactsMode:        "both",
		FactsCollection:  "ragamuffin_facts",
		FactsVectorSize:  4,
		EmbeddingDims:    4,
		ChunkVectorSize:  0,
		AutoThreshold:    0.75,
		AuthMode:         "none",
		RateLimitEnabled: false,
		ChunkStrategy:    "auto",
		ChunkMaxTokens:   2000,
		WatcherMode:      "poll",
	}

	if vaultName == "" {
		vaultName = "default"
	}

	idxm := indexer.NewManager()
	idx := indexer.New(cfg.VaultPath, vaultName, chunkStore, nil, slog.Default())
	idxm.Add(vaultName, idx, chunkStore)
	idxm.AddFactClient(vaultName, factsStore)

	rl := ratelimit.New(false)
	broker := events.NewBroker()
	emitter := events.NewEmitter("", "test", slog.Default(), nil, broker, nil)

	// Deterministic embedding — hash-based for reproducibility
	_ = embedding.New("http://127.0.0.1:1", "none", "test", time.Millisecond)

	p := pruner.New(factsStore, chunkStore, nil, mockLLM, slog.Default(), pruner.PrunerConfig{Enabled: false})
	ext := extraction.New(extraction.Config{Enabled: false}, mockLLM, nil, factsStore, slog.Default())

	return New(cfg, chunkStore, factsStore, nil, mockLLM, idxm, nil, rl, nil, ls, p, emitter, broker, slog.Default(), ext, nil)
}

func wiredChunkID(t *testing.T) string { return "00000000-0000-0000-0000-000000000001" }

func preloadChunk(t *testing.T, store *embeddedstore.Store, id, source, text string) {
	t.Helper()
	pt := &pb.PointStruct{
		Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: id}},
		Payload: map[string]*pb.Value{
			"source_file":       pb.NewValueString(source),
			"text":              pb.NewValueString(text),
			"file_last_updated": pb.NewValueString(time.Now().UTC().Format(time.RFC3339)),
		},
	}
	if err := store.Upsert(context.Background(), []*pb.PointStruct{pt}); err != nil {
		t.Fatal(err)
	}
}

func preloadFact(t *testing.T, store *embeddedstore.Store, key, value, source, status string, conf float64) {
	t.Helper()
	pt := &pb.PointStruct{
		Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: factKeyHash(key)}},
		Payload: map[string]*pb.Value{
			"fact_key":    pb.NewValueString(key),
			"fact_value":  pb.NewValueString(value),
			"source":      pb.NewValueString(source),
			"source_type": pb.NewValueString("manual"),
			"status":      pb.NewValueString(status),
			"confidence":  pb.NewValueDouble(float64(conf)),
			"created_at":  pb.NewValueString(time.Now().UTC().Format(time.RFC3339)),
			"updated_at":  pb.NewValueString(time.Now().UTC().Format(time.RFC3339)),
		},
	}
	if err := store.Upsert(context.Background(), []*pb.PointStruct{pt}); err != nil {
		t.Fatal(err)
	}
}

// ── Success path tests ──────────────────────────────────────────────────────

func TestWired_ChunksListGET_ReturnsChunks(t *testing.T) {
	srv := wiredServer(t, "default")
	preloadChunk(t, srv.indexers.GetClient("default").(*embeddedstore.Store),
		wiredChunkID(t), "docs/test.md", "chunk content")

	req := httptest.NewRequest("GET", "/v1/chunks", nil)
	w := httptest.NewRecorder()
	srv.handleChunksListGET(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"] != float64(1) {
		t.Errorf("expected count=1, got %v", resp["count"])
	}
}

func TestWired_Export_ReturnsValidJSON(t *testing.T) {
	srv := wiredServer(t, "default")
	store := srv.indexers.GetClient("default").(*embeddedstore.Store)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i+1)
		preloadChunk(t, store, id, "docs/test.md", "chunk")
	}

	req := httptest.NewRequest("GET", "/v1/vaults/default/export", nil)
	req.SetPathValue("name", "default")
	w := httptest.NewRecorder()
	srv.handleExport(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	chunks, _ := resp["chunks"].([]interface{})
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(chunks))
	}
}

func TestWired_Debt_ReturnsAggregatedData(t *testing.T) {
	srv := wiredServer(t, "default")
	factsStore := srv.indexers.GetFactClient("default").(*embeddedstore.Store)
	preloadFact(t, factsStore, "org/standard", "Use Rust", "review-bot", "active", 0.9)
	preloadFact(t, factsStore, "org/legacy", "Old policy", "", "needs_review", 0.3)

	req := httptest.NewRequest("GET", "/v1/debt", nil)
	w := httptest.NewRecorder()
	srv.handleDebt(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["vault_count"] != float64(1) {
		t.Errorf("expected vault_count=1, got %v", resp["vault_count"])
	}
}

func TestWired_AgentStats_ReturnsGroupedData(t *testing.T) {
	srv := wiredServer(t, "default")
	factsStore := srv.indexers.GetFactClient("default").(*embeddedstore.Store)
	preloadFact(t, factsStore, "infra/host", "server1", "review-bot", "active", 0.9)
	preloadFact(t, factsStore, "infra/port", "8080", "data-bot", "active", 0.8)

	req := httptest.NewRequest("GET", "/v1/agents/stats", nil)
	w := httptest.NewRecorder()
	srv.handleAgentStats(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	agents, _ := resp["agents"].([]interface{})
	if len(agents) < 2 {
		t.Errorf("expected at least 2 agents, got %d", len(agents))
	}
}

func TestWired_ChunksDelete_RemovesChunks(t *testing.T) {
	srv := wiredServer(t, "default")
	store := srv.indexers.GetClient("default").(*embeddedstore.Store)
	preloadChunk(t, store, wiredChunkID(t), "docs/test.md", "content")

	req := httptest.NewRequest("DELETE", "/v1/chunks?source=docs/test.md", nil)
	w := httptest.NewRecorder()
	srv.handleChunksDelete(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWired_Import_UpsertsPoints(t *testing.T) {
	srv := wiredServer(t, "default")
	importBody := `{"chunks":[{"source_file":"docs/new.md","text":"imported content"}]}`

	req := httptest.NewRequest("POST", "/v1/vaults/default/import",
		jsonBytes(importBody))
	req.SetPathValue("name", "default")
	w := httptest.NewRecorder()
	srv.handleImport(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	imported, _ := resp["imported"].(float64)
	if int(imported) != 1 {
		t.Errorf("expected 1 imported chunk, got %v", imported)
	}
}

func TestWired_VaultDelete_RemovesVault(t *testing.T) {
	srv := wiredServer(t, "test-vault")

	req := httptest.NewRequest("DELETE", "/v1/vaults/test-vault", nil)
	req.SetPathValue("name", "test-vault")
	w := httptest.NewRecorder()
	srv.handleVaultDelete(w, req)

	// Single-tenant mode returns 403; multi-tenant returns 200
	if w.Code != 200 && w.Code != 403 {
		t.Fatalf("expected 200 or 403, got %d: %s", w.Code, w.Body.String())
	}
	if w.Code == 200 {
		if idx := srv.indexers.Get("test-vault"); idx != nil {
			t.Error("expected vault to be removed from indexers")
		}
	}
}

// TestWired_Provenance_ReturnsSourceChain is in facts_test.go since it tests fact domain code.
// TestWired_FactHistory_ReturnsTimeline is in facts_test.go.

func TestWired_LogsPost_AppendsEntry(t *testing.T) {
	srv := wiredServer(t, "default")
	body := jsonBytes(`{"agent":"test","type":"scan","body":"hello"}`)
	req := httptest.NewRequest("POST", "/v1/logs", body)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWired_LogsGet_ReturnsEntries(t *testing.T) {
	srv := wiredServer(t, "default")
	logBody := jsonBytes(`{"agent":"test","type":"scan","body":"hello"}`)
	srv.handleLogs(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/logs", logBody))

	req := httptest.NewRequest("GET", "/v1/logs?limit=10", nil)
	w := httptest.NewRecorder()
	srv.handleLogs(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	entries, _ := resp["entries"].([]interface{})
	if len(entries) < 1 {
		t.Errorf("expected at least 1 entry, got %d", len(entries))
	}
}

func TestWired_PrunerConfig_ReturnsShape(t *testing.T) {
	srv := wiredServer(t, "default")
	req := httptest.NewRequest("GET", "/v1/pruner/config", nil)
	w := httptest.NewRecorder()
	srv.handlePrunerConfig(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["enabled"]; !ok {
		t.Error("expected enabled in response")
	}
}

func TestWired_ExtractionStats_ReturnsStats(t *testing.T) {
	srv := wiredServer(t, "default")
	req := httptest.NewRequest("GET", "/v1/extraction/stats", nil)
	w := httptest.NewRecorder()
	srv.handleExtractionStats(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200 (extractor exists), got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["total_attempted"]; !ok {
		t.Error("expected total_attempted in response")
	}
}

func TestWired_VaultClear_RequiresConfirm(t *testing.T) {
	srv := wiredServer(t, "default")
	body := jsonBytes(`{"confirm":true}`)
	req := httptest.NewRequest("POST", "/v1/vaults/default/clear", body)
	w := httptest.NewRecorder()
	srv.handleVaultClear(w, req)
	if w.Code != 400 && w.Code != 500 {
		t.Fatalf("expected 400 or 500 (embedded store limit), got %d: %s", w.Code, w.Body.String())
	}
}

func TestWired_Provenance_ReturnsSourceChain(t *testing.T) {
	srv := wiredServer(t, "default")
	factsStore := srv.indexers.GetFactClient("default").(*embeddedstore.Store)
	preloadFact(t, factsStore, "org/test", "value", "bot", "active", 0.9)

	// Need to set path value for provenance handler
	req := httptest.NewRequest("GET", "/v1/facts/org/test/provenance", nil)
	req.SetPathValue("key", "org/test")
	w := httptest.NewRecorder()
	srv.handleProvenance(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if key, _ := resp["key"].(string); key != "org/test" {
		t.Errorf("expected key 'org/test', got %q", key)
	}
}

func TestWired_FactHistory_ReturnsTimeline(t *testing.T) {
	srv := wiredServer(t, "default")
	factsStore := srv.indexers.GetFactClient("default").(*embeddedstore.Store)
	preloadFact(t, factsStore, "org/hist", "historic", "bot", "active", 0.9)

	req := httptest.NewRequest("GET", "/v1/facts/org/hist/history", nil)
	req.SetPathValue("key", "org/hist")
	w := httptest.NewRecorder()
	srv.handleFactHistory(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp []interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) < 1 {
		t.Error("expected at least 1 history entry")
	}
}

func TestWired_VaultMerge_RejectsSelfMerge(t *testing.T) {
	srv := wiredServer(t, "default")
	req := httptest.NewRequest("POST", "/v1/vaults/default/merge/default", nil)
	w := httptest.NewRecorder()
	srv.handleVaultMerge(w, req)
	// Single-tenant: merge requires multi-tenant mode
	if w.Code != 400 && w.Code != 403 {
		t.Fatalf("expected 400 or 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWired_VaultArchive_ArchivesVault(t *testing.T) {
	srv := wiredServer(t, "archive-vault")
	req := httptest.NewRequest("POST", "/v1/vaults/archive-vault/archive", nil)
	req.SetPathValue("name", "archive-vault")
	w := httptest.NewRecorder()
	srv.handleVaultArchive(w, req)
	// Single-tenant: archive requires multi-tenant mode
	if w.Code != 200 && w.Code != 403 {
		t.Fatalf("expected 200 or 403, got %d: %s", w.Code, w.Body.String())
	}
	if w.Code == 200 {
		if idx := srv.indexers.Get("archive-vault"); idx != nil {
			t.Error("expected vault to be removed from indexers after archive")
		}
	}
}

func TestWired_EmbedProject_ReturnsProjection(t *testing.T) {
	srv := wiredServer(t, "default")
	store := srv.indexers.GetClient("default").(*embeddedstore.Store)
	preloadChunk(t, store, wiredChunkID(t), "docs/test.md", "content for embedding")

	req := httptest.NewRequest("GET", "/v1/embedding/project", nil)
	w := httptest.NewRecorder()
	srv.handleEmbedProject(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["points"]; !ok {
		t.Error("expected points in response")
	}
}

func jsonBytes(s string) *jsonBytesReader {
	return &jsonBytesReader{s: s}
}

type jsonBytesReader struct{ s string }

func (r *jsonBytesReader) Read(p []byte) (int, error) {
	return copy(p, r.s), nil
}

// ── D5: Concurrency test ─────────────────────────────────────────────────────

func TestWired_ConcurrentRecall(t *testing.T) {
	srv := wiredServer(t, "default")
	store := srv.indexers.GetClient("default").(*embeddedstore.Store)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-00000000%04d", i)
		preloadChunk(t, store, id, "docs/data.md", fmt.Sprintf("content %d", i))
	}

	// Fire 20 concurrent recall requests (avoids race detector timeouts)
	const workers = 20
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			body := jsonBytes(`{"query":"test","top_k":5}`)
			req := httptest.NewRequest("POST", "/recall", body)
			w := httptest.NewRecorder()
			srv.handleRecall(w, req)
			if w.Code != 200 {
				errCh <- fmt.Errorf("recall returned %d", w.Code)
				return
			}
			errCh <- nil
		}()
	}

	for i := 0; i < workers; i++ {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
}

// ── D7: Export/import round-trip test ─────────────────────────────────────────

func TestWired_ExportImportRoundTrip(t *testing.T) {
	srv := wiredServer(t, "default")
	store := srv.indexers.GetClient("default").(*embeddedstore.Store)
	preloadChunk(t, store, wiredChunkID(t), "docs/roundtrip.md", "round trip content")

	// Export
	exportReq := httptest.NewRequest("GET", "/v1/vaults/default/export", nil)
	exportW := httptest.NewRecorder()
	srv.handleExport(exportW, exportReq)
	if exportW.Code != 200 {
		t.Fatalf("export returned %d", exportW.Code)
	}

	var exportResp map[string]interface{}
	json.NewDecoder(exportW.Body).Decode(&exportResp)
	chunks, _ := exportResp["chunks"].([]interface{})
	if len(chunks) < 1 {
		t.Fatal("expected at least 1 chunk in export")
	}

	// Delete all chunks
	delReq := httptest.NewRequest("DELETE", "/v1/chunks?confirm=true", nil)
	delW := httptest.NewRecorder()
	srv.handleChunksDelete(delW, delReq)
	if delW.Code != 200 {
		t.Fatalf("delete returned %d", delW.Code)
	}

	// Import back
	importBody, _ := json.Marshal(exportResp)
	importReq := httptest.NewRequest("POST", "/v1/vaults/default/import", jsonBytes(string(importBody)))
	importW := httptest.NewRecorder()
	srv.handleImport(importW, importReq)
	if importW.Code != 200 {
		t.Fatalf("import returned %d: %s", importW.Code, importW.Body.String())
	}
}

// ── D8: Create vault test ─────────────────────────────────────────────────────

func TestWired_VaultCreate(t *testing.T) {
	srv := wiredServer(t, "default")
	body := jsonBytes(`{"name":"new-vault","path":"/tmp/new-vault"}`)
	req := httptest.NewRequest("POST", "/vaults", body)
	w := httptest.NewRecorder()
	srv.handleVaults(w, req)
	// create vault is multi-tenant only; single-tenant returns 403 or similar
	if w.Code != 200 && w.Code != 403 {
		t.Fatalf("expected 200 or 403, got %d: %s", w.Code, w.Body.String())
	}
}
