package server

// REACHLOCK contract test (R4).
//
// This file proves the C3 subset of the Ragamuffin REST surface that
// REACHLOCK binds to (see docs/REACHLOCK-VAULT-CONVENTIONS.md and the
// upstream docs/MEMORY-INTERFACE.md in the REACHLOCK repo). The test
// runs against the real HTTP routes of a fully-wired Server using the
// embedded vector store and a stub OpenAI-compatible embedding endpoint.
// No Qdrant, no cloud key — the same configuration a single-player
// REACHLOCK deployment will use.
//
// The five assertions:
//
//  1. POST /v1/documents into a named vault → indexed and recallable.
//  2. GET  /vault/{name}/v1/hybrid returns ranked results with source + score.
//  3. POST /v1/ingest/conversation accepts the shape in MEMORY-INTERFACE.md
//     and yields facts (status, conversation_id, fact_count, facts).
//  4. GET  /vault/{name}/v1/briefing returns the vault summary shape.
//  5. Vault isolation: a query against soul_a never returns soul_b content.
//
// The test is wired into Ragamuffin's CI (see .github/workflows/
// reachlock-contract.yml). A change that breaks any of the five
// assertions will fail Ragamuffin's own pipeline before it can
// ship to REACHLOCK.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	"github.com/chezgoulet/ragamuffin/internal/embeddedstore"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/events"
	"github.com/chezgoulet/ragamuffin/internal/extraction"
	"github.com/chezgoulet/ragamuffin/internal/indexer"
	"github.com/chezgoulet/ragamuffin/internal/ingress"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/pruner"
	"github.com/chezgoulet/ragamuffin/internal/ratelimit"
	"github.com/chezgoulet/ragamuffin/internal/server/testutil"
)

// ── Stub OpenAI-compatible embedding endpoint ────────────────────────────────

// stubEmbedServer returns deterministic embeddings: the vector for a
// text is a hash-based 4-element unit vector. Two texts that share
// content get vectors that point in the same direction; two that don't
// get vectors with cosine ≈ 0. This is enough to verify that
// POST /v1/documents → GET /v1/hybrid actually retrieves what was
// written, without depending on a real embedding model.
type stubEmbedServer struct {
	mu  sync.Mutex
	dim int
	hit int
}

func (s *stubEmbedServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/embeddings" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.hit += len(req.Input)
		s.mu.Unlock()
		type datum struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		out := struct {
			Data []datum `json:"data"`
		}{}
		for i, t := range req.Input {
			out.Data = append(out.Data, datum{
				Index:     i,
				Embedding: textVector(t, s.dim),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// textVector returns a deterministic unit vector for the given text.
// Words map to hash buckets, each contributing a small magnitude to
// one of the dim axes; cosine between texts that share words is high.
func textVector(text string, dim int) []float32 {
	if dim <= 0 {
		dim = 4
	}
	v := make([]float32, dim)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ".,!?:;\"'()[]")
		if w == "" {
			continue
		}
		idx := hashWord(w) % uint32(dim)
		v[idx] += 1.0
	}
	// L2 normalise so cosine = dot product.
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		// No tokens: a unit vector along axis 0. Two empty texts are
		// identical, which is the desired behaviour.
		v[0] = 1
		norm = 1
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func hashWord(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ── Test fixture: a fully-wired Ragamuffin server ────────────────────────────

type contractFixture struct {
	srv       *httptest.Server
	url       string // base URL of the HTTP server
	embed     *stubEmbedServer
	embedURL  string
	tmpDir    string
	indexers  *indexer.Manager
	store     *embeddedstore.Store // shared facts store; chunk stores are per-vault
	chunkA    *embeddedstore.Store
	chunkB    *embeddedstore.Store
	chunkLore *embeddedstore.Store
	llm       *testutil.MockLLM
	shutdown  context.CancelFunc
}

func newContractFixture(t *testing.T) *contractFixture {
	t.Helper()
	dir := t.TempDir()

	// 1. Stub embedding server
	es := &stubEmbedServer{dim: 4}
	embedSrv := httptest.NewServer(es.handler())
	t.Cleanup(embedSrv.Close)

	// 2. Real Ragamuffin config — embedded vector store, no Qdrant URL
	cfg := &config.Config{
		VaultPath: filepath.Join(dir, "vaults"),
		Vaults: map[string]*config.VaultConfig{
			"soul_a": {Path: filepath.Join(dir, "vaults", "soul_a")},
			"soul_b": {Path: filepath.Join(dir, "vaults", "soul_b")},
			"lore":   {Path: filepath.Join(dir, "vaults", "lore")},
		},
		VectorStore:         "embedded",
		EmbeddedDBPath:      filepath.Join(dir, "embedded.db"),
		FactsCollection:     "contract_facts",
		FactsVectorSize:     4,
		EmbeddingDims:       4,
		ChunkVectorSize:     4,
		AutoProvisionVaults: true,
		MultiTenantMode:     true,
		FactsMode:           "vault",
	}
	for _, vc := range cfg.Vaults {
		if err := os.MkdirAll(vc.Path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", vc.Path, err)
		}
	}

	// 3. Build the per-vault chunk stores and the global facts store
	mk := func(name string) *embeddedstore.Store {
		s, err := embeddedstore.Open(embeddedstore.Config{
			Path:       filepath.Join(dir, name+".db"),
			Collection: name,
			VectorSize: 4,
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			t.Fatalf("open embedded %s: %v", name, err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	chunkA := mk("chunks_soul_a")
	chunkB := mk("chunks_soul_b")
	chunkLore := mk("chunks_lore")
	sharedFacts := mk("contract_facts")

	// 4. Embedding client pointed at the stub server
	ec := embedding.New(embedSrv.URL, "", "stub-model", 30*time.Second)

	// 5. LLM stub — returns a canned extraction result for the
	//    /v1/ingest/conversation test. The contract is about the
	//    wire shape, not the LLM's reasoning.
	mockLLM := &testutil.MockLLM{
		SynthesizeFn: func(_ context.Context, _, _ string) (string, error) {
			return `[{"key":"user_is_ally","value":"The player is an ally of Tib","confidence":8,"category":"relationship","ttl_days":365}]`, nil
		},
	}

	// 6. Indexer manager with one indexer per vault; chunk store per vault.
	idxm := indexer.NewManager()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, store := range map[string]*embeddedstore.Store{
		"soul_a": chunkA,
		"soul_b": chunkB,
		"lore":   chunkLore,
	} {
		idx := indexer.New(filepath.Join(dir, "vaults", name), name, store, ec, logger)
		if err := idxm.Add(name, idx, store); err != nil {
			t.Fatalf("add indexer %s: %v", name, err)
		}
	}

	// 7. Per-vault facts stores — registered so the briefing handler can
	//    scroll-filter on a per-vault facts collection.
	for name, vc := range cfg.Vaults {
		_ = vc
		factsStore, err := embeddedstore.Open(embeddedstore.Config{
			Path:       filepath.Join(dir, "facts_"+name+".db"),
			Collection: cfg.FactsCollectionFor(name),
			VectorSize: 4,
		})
		if err != nil {
			t.Fatalf("open facts %s: %v", name, err)
		}
		t.Cleanup(func() { factsStore.Close() })
		idxm.AddFactClient(name, factsStore)
	}

	// 8. Log store (needed by briefing; in-memory is fine)
	ls, err := logstore.Open(":memory:")
	if err != nil {
		t.Fatalf("logstore: %v", err)
	}
	t.Cleanup(func() { ls.Close() })

	// 9. Pruner (disabled — single-player configuration; avoids the
	//    SetPayload call which the embedded store does not implement)
	p := pruner.New(sharedFacts, chunkA, ec, mockLLM, logger, pruner.PrunerConfig{Enabled: false})

	// 10. Extraction (disabled — not on the R4 contract surface)
	ext := extraction.New(extraction.Config{Enabled: false}, mockLLM, ec, sharedFacts, logger)

	// 11. Event emitter (minimal; the contract test does not assert on
	//     webhook delivery, but the Server constructor expects one)
	broker := events.NewBroker()
	emitter := events.NewEmitter("", "contract-test", logger, ls, broker, nil)

	rl := ratelimit.New(false)

	// 12. Build the server and register routes against a real mux
	srv := New(cfg, chunkA, sharedFacts, ec, mockLLM, idxm, nil, rl, nil, ls, p, emitter, broker, logger, ext, nil)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_ = ctx

	return &contractFixture{
		srv:       httpSrv,
		url:       httpSrv.URL,
		embed:     es,
		embedURL:  embedSrv.URL,
		tmpDir:    dir,
		indexers:  idxm,
		store:     sharedFacts,
		chunkA:    chunkA,
		chunkB:    chunkB,
		chunkLore: chunkLore,
		llm:       mockLLM,
		shutdown:  cancel,
	}
}

// postJSON is a small helper for JSON POSTs to the contract server.
func (c *contractFixture) postJSON(t *testing.T, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("encode body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.url+path, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp, respBody
}

func (c *contractFixture) get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(c.url + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp, respBody
}

// ── 1. POST /v1/documents → recallable ──────────────────────────────────────

func TestContract_PostDocument_IndexedAndRecallable(t *testing.T) {
	c := newContractFixture(t)

	doc := map[string]any{
		"vault":   "soul_a",
		"content": "Tib prefers dark roast, ground fine, brewed slow.",
		"source":  "seeds/coffee.md",
		"tags":    []string{"preference"},
	}
	resp, body := c.postJSON(t, "/v1/documents", doc)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/documents: %d %s", resp.StatusCode, body)
	}
	var ack struct {
		Status string `json:"status"`
		Vault  string `json:"vault"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		t.Fatalf("decode ack: %v (body: %s)", err, body)
	}
	if ack.Status != "ok" || ack.Vault != "soul_a" || ack.Source != "seeds/coffee.md" {
		t.Errorf("unexpected ack: %+v", ack)
	}

	// Embedding endpoint was called at least once.
	c.embed.mu.Lock()
	hits := c.embed.hit
	c.embed.mu.Unlock()
	if hits == 0 {
		t.Errorf("expected the stub embedding server to be called; got 0 hits")
	}

	// The chunk store for soul_a has at least one point.
	n, err := c.chunkA.Count(context.Background())
	if err != nil {
		t.Fatalf("chunkA count: %v", err)
	}
	if n == 0 {
		t.Errorf("expected at least one point in soul_a chunk store, got 0")
	}
}

// ── 2. GET /vault/{name}/v1/hybrid — ranked results, source + score ─────────

func TestContract_Hybrid_RankedResultsWithSourceAndScore(t *testing.T) {
	c := newContractFixture(t)

	// Ingest two memories with distinct vocabulary.
	for _, c2 := range []struct {
		vault   string
		content string
		source  string
	}{
		{"soul_a", "Tib prefers dark roast coffee brewed slowly.", "mem/coffee.md"},
		{"soul_a", "Tib once tended a small herb garden in sorrow station.", "mem/herb-garden.md"},
	} {
		_, body := c.postJSON(t, "/v1/documents", map[string]any{
			"vault":   c2.vault,
			"content": c2.content,
			"source":  c2.source,
		})
		if !bytes.Contains(body, []byte(`"status":"ok"`)) {
			t.Fatalf("ingest failed: %s", body)
		}
	}

	resp, body := c.get(t, "/vault/soul_a/v1/hybrid?query=coffee&limit=5")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hybrid GET: %d %s", resp.StatusCode, body)
	}
	var got struct {
		Results []struct {
			Kind    string  `json:"kind"`
			Score   float32 `json:"score"`
			Content string  `json:"content"`
			Source  string  `json:"source"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode hybrid: %v (body: %s)", err, body)
	}
	if len(got.Results) == 0 {
		t.Fatalf("expected hybrid results, got 0; body: %s", body)
	}
	// First result must be the coffee memory (text vectors are deterministic).
	first := got.Results[0]
	if first.Source != "mem/coffee.md" {
		t.Errorf("top hit source = %q, want mem/coffee.md (results: %+v)", first.Source, got.Results)
	}
	if first.Score <= 0 {
		t.Errorf("top hit score = %f, want > 0", first.Score)
	}
	// Score ordering.
	for i := 1; i < len(got.Results); i++ {
		if got.Results[i].Score > got.Results[i-1].Score {
			t.Errorf("results not sorted by score: %+v", got.Results)
			break
		}
	}
}

// ── 3. POST /v1/ingest/conversation — shape + facts ─────────────────────────

func TestContract_IngestConversation_AcceptsShapeYieldsFacts(t *testing.T) {
	c := newContractFixture(t)

	body := map[string]any{
		"vault": "soul_a",
		"messages": []map[string]string{
			{"role": "user", "content": "I have known Tib since the ruin years."},
			{"role": "assistant", "content": "Tib acknowledges the player's loyalty."},
		},
		"context": map[string]any{
			"tick":     10450,
			"location": "sorrow_station",
		},
	}
	resp, raw := c.postJSON(t, "/v1/ingest/conversation", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest conversation: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		Status         string   `json:"status"`
		ConversationID string   `json:"conversation_id"`
		FactCount      int      `json:"fact_count"`
		Facts          []string `json:"facts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, raw)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
	if got.ConversationID == "" {
		t.Errorf("conversation_id empty")
	}
	if got.FactCount == 0 || len(got.Facts) == 0 {
		t.Errorf("expected at least one fact, got fact_count=%d facts=%v", got.FactCount, got.Facts)
	}
	if c.llm.SynthesizeCallCount.Load() == 0 {
		t.Errorf("expected the LLM stub to be called at least once")
	}
}

// ── 4. GET /vault/{name}/v1/briefing — vault summary shape ──────────────────

func TestContract_Briefing_ReturnsVaultSummaryShape(t *testing.T) {
	c := newContractFixture(t)

	// Brief on an empty vault — the response shape must still match.
	resp, raw := c.get(t, "/vault/soul_a/v1/briefing?agent_id=player-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("briefing GET: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		StartedAt     string `json:"started_at"`
		UptimeSeconds int    `json:"uptime_seconds"`
		Vaults        []struct {
			Name         string  `json:"name"`
			Path         string  `json:"path"`
			IndexedFiles int     `json:"indexed_files"`
			TotalChunks  int     `json:"total_chunks"`
			LastIndexed  *string `json:"last_indexed"`
			Indexing     bool    `json:"indexing"`
		} `json:"vaults"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode briefing: %v (body: %s)", err, raw)
	}
	if got.Version == "" {
		t.Errorf("version empty; body: %s", raw)
	}
	if got.StartedAt == "" {
		t.Errorf("started_at empty")
	}
	// The briefing must list at least the requested vault.
	found := false
	for _, v := range got.Vaults {
		if v.Name == "soul_a" {
			found = true
			if v.Path == "" {
				t.Errorf("soul_a briefing entry has empty path")
			}
			break
		}
	}
	if !found {
		t.Errorf("briefing did not list soul_a; got: %+v", got.Vaults)
	}
}

// ── 5. Vault isolation ──────────────────────────────────────────────────────

func TestContract_VaultIsolation_NoCrossLeak(t *testing.T) {
	c := newContractFixture(t)

	// Write one memory into each of soul_a, soul_b, and lore.
	for _, c2 := range []struct {
		vault   string
		content string
		source  string
	}{
		{"soul_a", "Tib's private memory: a brass key hidden in the wall.", "mem/a-secret.md"},
		{"soul_b", "Mara's private memory: a debt to the merchant guild.", "mem/b-secret.md"},
		{"lore", "Sorrow Station was founded after the ruin years.", "lore/founding.md"},
	} {
		_, body := c.postJSON(t, "/v1/documents", map[string]any{
			"vault":   c2.vault,
			"content": c2.content,
			"source":  c2.source,
		})
		if !bytes.Contains(body, []byte(`"status":"ok"`)) {
			t.Fatalf("ingest %s failed: %s", c2.vault, body)
		}
	}

	// Query soul_a for "secret" — only the soul_a memory should come back.
	_, raw := c.get(t, "/vault/soul_a/v1/hybrid?query=secret&limit=10")
	var got struct {
		Results []struct {
			Source  string `json:"source"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, raw)
	}
	for _, r := range got.Results {
		if r.Source == "mem/b-secret.md" {
			t.Errorf("soul_b content leaked into soul_a query: %+v", r)
		}
		if r.Source == "lore/founding.md" {
			t.Errorf("lore content leaked into soul_a query: %+v", r)
		}
	}

	// Query soul_b for "secret" — only soul_b content must come back.
	_, raw = c.get(t, "/vault/soul_b/v1/hybrid?query=secret&limit=10")
	got = struct {
		Results []struct {
			Source  string `json:"source"`
			Content string `json:"content"`
		} `json:"results"`
	}{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, raw)
	}
	for _, r := range got.Results {
		if r.Source == "mem/a-secret.md" {
			t.Errorf("soul_a content leaked into soul_b query: %+v", r)
		}
		if r.Source == "lore/founding.md" {
			t.Errorf("lore content leaked into soul_b query: %+v", r)
		}
	}

	// Underlying store check: each vault's chunk table is a separate SQLite
	// table, so a misbehaving server route could not bridge them at the
	// storage layer even if it tried.
	for name, st := range map[string]*embeddedstore.Store{
		"soul_a": c.chunkA,
		"soul_b": c.chunkB,
		"lore":   c.chunkLore,
	} {
		count, err := st.Count(context.Background())
		if err != nil {
			t.Fatalf("%s count: %v", name, err)
		}
		if count == 0 {
			t.Errorf("expected %s chunk store to have ≥1 point, got 0", name)
		}
	}
}

// ── Test entry marker ───────────────────────────────────────────────────────

// TestContract_Ragamuffin_REACHLOCK_Binding is a single go-test entry that
// runs all five contract assertions in sequence. CI invokes it via
// `go test -run TestContract_Ragamuffin_REACHLOCK_Binding ./internal/server/`.
func TestContract_Ragamuffin_REACHLOCK_Binding(t *testing.T) {
	// Each subtest is a top-level *_test.go function; the wrapper just
	// fans them out so CI logs a single, easy-to-find test name.
	t.Run("PostDocument_IndexedAndRecallable", TestContract_PostDocument_IndexedAndRecallable)
	t.Run("Hybrid_RankedResultsWithSourceAndScore", TestContract_Hybrid_RankedResultsWithSourceAndScore)
	t.Run("IngestConversation_AcceptsShapeYieldsFacts", TestContract_IngestConversation_AcceptsShapeYieldsFacts)
	t.Run("Briefing_ReturnsVaultSummaryShape", TestContract_Briefing_ReturnsVaultSummaryShape)
	t.Run("VaultIsolation_NoCrossLeak", TestContract_VaultIsolation_NoCrossLeak)
}

// ── References to packages kept imported for the contract test ──────────────

// These references exist so the imports are flagged as used even if a
// future refactor changes the wiring. They are constants and never
// reach runtime; their only purpose is to keep `goimports` honest.
var (
	_ = filepath.Clean
	_ = fmt.Sprintf
	_ ingress.IngressDriver // interface kept in scope for future FileWatcher wiring
)
