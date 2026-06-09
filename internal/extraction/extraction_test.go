package extraction

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockSynthesizer struct {
	result string
	err    error
	mu     sync.Mutex
	called bool
	lastPrompt string
	lastSystem string
}

func (m *mockSynthesizer) Synthesize(_ context.Context, query, system string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
	m.lastPrompt = query
	m.lastSystem = system
	return m.result, m.err
}

func (m *mockSynthesizer) Compare(_ context.Context, _, _, _, _ string) (string, error) {
	return "", nil
}

func (m *mockSynthesizer) Health(_ context.Context) error {
	return nil
}

type mockEmbedder struct {
	vec   []float32
	vecs  [][]float32
	err   error
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(m.vecs) > 0 {
		return m.vecs, m.err
	}
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = m.vec
	}
	return vecs, m.err
}

func (m *mockEmbedder) EmbedSingle(_ context.Context, text string) ([]float32, error) {
	return m.vec, m.err
}

func (m *mockEmbedder) Health(_ context.Context) error { return m.err }

type mockFactStore struct {
	upsertErr       error
	scrollFilteredResult []*pb.RetrievedPoint
	scrollFilteredErr    error
	collectionName       string
}

func (m *mockFactStore) Upsert(_ context.Context, _ []*pb.PointStruct) error {
	return m.upsertErr
}

func (m *mockFactStore) Scroll(_ context.Context, _ uint32, _ *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) {
	return nil, nil, nil
}

func (m *mockFactStore) ScrollFiltered(_ context.Context, _ string, _ *pb.Filter, _ uint32, _ string) ([]*pb.RetrievedPoint, error) {
	return m.scrollFilteredResult, m.scrollFilteredErr
}

func (m *mockFactStore) Search(_ context.Context, _ []float32, _ uint64, _ float32, _ string, _ *pb.Filter) ([]*pb.ScoredPoint, error) {
	return nil, nil
}

func (m *mockFactStore) DeleteBySource(_ context.Context, _ string) error { return nil }
func (m *mockFactStore) DeleteFiltered(_ context.Context, _ string, _ *pb.Filter) error { return nil }
func (m *mockFactStore) Count(_ context.Context) (uint64, error) { return 0, nil }
func (m *mockFactStore) CountFiles(_ context.Context) (int, error) { return 0, nil }
func (m *mockFactStore) CreatePayloadIndex(_ context.Context, _, _, _ string) error { return nil }
func (m *mockFactStore) Health(_ context.Context) error { return nil }
func (m *mockFactStore) Close() error { return nil }
func (m *mockFactStore) GetVectorSize(_ context.Context, _ string) (uint64, error) { return 0, nil }
func (m *mockFactStore) GetPoints(_ context.Context, _ string, _ []*pb.PointId) ([]*pb.RetrievedPoint, error) { return nil, nil }
func (m *mockFactStore) SetPayload(_ context.Context, _ string, _ []*pb.PointId, _ map[string]*pb.Value) error { return nil }
func (m *mockFactStore) Collection() string { return m.collectionName }

// ── DefaultConfig ─────────────────────────────────────────────────────────────

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("expected disabled by default")
	}
	if cfg.Window != 10 {
		t.Errorf("expected window 10, got %d", cfg.Window)
	}
	if cfg.MaxConfidence != 0.85 {
		t.Errorf("expected max confidence 0.85, got %f", cfg.MaxConfidence)
	}
	if cfg.DedupThreshold != 0.85 {
		t.Errorf("expected dedup threshold 0.85, got %f", cfg.DedupThreshold)
	}
	if cfg.Concurrency != 2 {
		t.Errorf("expected concurrency 2, got %d", cfg.Concurrency)
	}
	if cfg.PerSessionCooldown != 30 {
		t.Errorf("expected per session cooldown 30, got %d", cfg.PerSessionCooldown)
	}
}

// ── CooldownTracker ───────────────────────────────────────────────────────────

func TestCooldownTracker_AcquireThenBlock(t *testing.T) {
	ct := NewCooldownTracker(3600) // 1 hour cooldown
	if !ct.TryAcquire("session-1") {
		t.Error("expected first acquire to succeed")
	}
	if ct.TryAcquire("session-1") {
		t.Error("expected second acquire to fail (cooldown)")
	}
}

func TestCooldownTracker_DifferentSessions(t *testing.T) {
	ct := NewCooldownTracker(3600)
	if !ct.TryAcquire("session-a") {
		t.Error("expected session-a to acquire")
	}
	if !ct.TryAcquire("session-b") {
		t.Error("expected session-b to acquire (different session)")
	}
	// Both should be in cooldown now
	if ct.TryAcquire("session-a") {
		t.Error("expected session-a to be in cooldown")
	}
	if ct.TryAcquire("session-b") {
		t.Error("expected session-b to be in cooldown")
	}
}

func TestCooldownTracker_NoCooldown(t *testing.T) {
	ct := NewCooldownTracker(0) // zero = no cooldown
	if !ct.TryAcquire("session-1") {
		t.Error("expected first acquire to succeed")
	}
	if !ct.TryAcquire("session-1") {
		t.Error("expected second acquire to succeed (no cooldown)")
	}
}

func TestCooldownTracker_Reset(t *testing.T) {
	ct := NewCooldownTracker(3600)
	if !ct.TryAcquire("session-1") {
		t.Error("expected first acquire to succeed")
	}
	ct.ResetCooldown("session-1")
	if !ct.TryAcquire("session-1") {
		t.Error("expected acquire to succeed after reset")
	}
}

func TestCooldownTracker_Expired(t *testing.T) {
	ct := NewCooldownTracker(1) // 1 second
	if !ct.TryAcquire("session-1") {
		t.Error("expected first acquire to succeed")
	}
	time.Sleep(1100 * time.Millisecond) // wait past cooldown
	if !ct.TryAcquire("session-1") {
		t.Error("expected acquire to succeed after cooldown expired")
	}
}

// ── Stats ─────────────────────────────────────────────────────────────────────

func TestStats_EmptySnapshot(t *testing.T) {
	s := NewStats()
	snap := s.Snapshot()
	if snap.TotalAttempted != 0 {
		t.Errorf("expected 0, got %d", snap.TotalAttempted)
	}
	if snap.AvgConfidence != 0 {
		t.Errorf("expected 0, got %f", snap.AvgConfidence)
	}
}

func TestStats_Incrementals(t *testing.T) {
	s := NewStats()
	s.IncAttempted()
	s.IncCreated()
	s.IncSkipped()
	s.IncRejected()

	snap := s.Snapshot()
	if snap.TotalAttempted != 1 {
		t.Errorf("expected 1, got %d", snap.TotalAttempted)
	}
	if snap.FactsCreated != 1 {
		t.Errorf("expected 1, got %d", snap.FactsCreated)
	}
	if snap.FactsSkipped != 1 {
		t.Errorf("expected 1, got %d", snap.FactsSkipped)
	}
	if snap.FactsRejected != 1 {
		t.Errorf("expected 1, got %d", snap.FactsRejected)
	}
}

func TestStats_ConfidenceAverage(t *testing.T) {
	s := NewStats()
	s.RecordConfidence(0.5)
	s.RecordConfidence(0.7)
	s.RecordConfidence(0.9)
	snap := s.Snapshot()
	expected := (0.5 + 0.7 + 0.9) / 3.0
	if math.Abs(snap.AvgConfidence-expected) > 0.01 {
		t.Errorf("expected avg %f, got %f", expected, snap.AvgConfidence)
	}
}

func TestStats_LastExtraction(t *testing.T) {
	s := NewStats()
	now := time.Now()
	s.SetLastExtraction(now)
	snap := s.Snapshot()
	if snap.LastExtraction == "" {
		t.Fatal("expected non-empty last extraction time")
	}
}

// ── buildContextWindow ────────────────────────────────────────────────────────

func TestBuildContextWindow_Empty(t *testing.T) {
	result := buildContextWindow(nil, "current turn")
	if result != "Current turn:\ncurrent turn" {
		t.Errorf("unexpected output: %q", result)
	}
}

func TestBuildContextWindow_EmptyCurrent(t *testing.T) {
	result := buildContextWindow(nil, "")
	if result != "Current turn:\n" {
		t.Errorf("unexpected output: %q", result)
	}
}

func TestBuildContextWindow_SkipsSystem(t *testing.T) {
	turns := []TurnEntry{
		{Role: "system", Content: "You are a helpful assistant"},
		{Role: "user", Content: "Hello"},
	}
	result := buildContextWindow(turns, "Hi there")
	if contains(result, "You are a helpful assistant") {
		t.Error("expected system turn to be excluded")
	}
	if !contains(result, "User: Hello") {
		t.Error("expected user turn to be included")
	}
}

func TestBuildContextWindow_SkipsShortAssistant(t *testing.T) {
	turns := []TurnEntry{
		{Role: "assistant", Content: "OK"},
		{Role: "user", Content: "Tell me about Rust"},
	}
	result := buildContextWindow(turns, "Rust is great")
	if contains(result, "OK") {
		t.Error("expected short assistant turn to be excluded")
	}
	if !contains(result, "User: Tell me about Rust") {
		t.Error("expected user turn to be included")
	}
}

func TestBuildContextWindow_MultipleTurns(t *testing.T) {
	turns := []TurnEntry{
		{Role: "user", Content: "First"},
		{Role: "assistant", Content: "Here is a long response with lots of useful information"},
	}
	result := buildContextWindow(turns, "Final turn")
	if !contains(result, "User: First") {
		t.Error("expected first user turn")
	}
	if !contains(result, "Assistant: Here is a long response") {
		t.Error("expected assistant turn")
	}
	if !contains(result, "Final turn") {
		t.Error("expected current turn")
	}
}

// ── parseExtractedFacts ───────────────────────────────────────────────────────

func TestParseExtractedFacts_Valid(t *testing.T) {
	raw := `[{"key":"user_prefers_rust","value":"User prefers Rust","confidence":0.85,"category":"preference","ttl_days":90}]`
	facts, err := parseExtractedFacts(raw)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Key != "user_prefers_rust" {
		t.Errorf("expected 'user_prefers_rust', got %q", facts[0].Key)
	}
	if facts[0].Confidence != 0.85 {
		t.Errorf("expected 0.85, got %f", facts[0].Confidence)
	}
	if facts[0].Category != "preference" {
		t.Errorf("expected 'preference', got %q", facts[0].Category)
	}
}

func TestParseExtractedFacts_WithMarkdown(t *testing.T) {
	raw := "```json\n[{\"key\":\"test\",\"value\":\"val\",\"confidence\":0.5,\"category\":\"knowledge\",\"ttl_days\":0}]\n```"
	facts, err := parseExtractedFacts(raw)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
}

func TestParseExtractedFacts_InvalidJSON(t *testing.T) {
	_, err := parseExtractedFacts("not json")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseExtractedFacts_Empty(t *testing.T) {
	facts, err := parseExtractedFacts("")
	if err == nil {
		t.Fatal("expected parse error for empty input")
	}
	_ = facts
}

func TestParseExtractedFacts_EmptyArray(t *testing.T) {
	facts, err := parseExtractedFacts("[]")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected empty array, got %d facts", len(facts))
	}
}

// ── cosineSimilarity ──────────────────────────────────────────────────────────

func TestCosineSimilarity_Identical(t *testing.T) {
	a := []float32{1, 0, 0}
	sim := cosineSimilarity(a, a)
	if sim != 1.0 {
		t.Errorf("expected 1.0, got %f", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	sim := cosineSimilarity(a, b)
	if sim != 0.0 {
		t.Errorf("expected 0.0, got %f", sim)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{-1, 0, 0}
	sim := cosineSimilarity(a, b)
	if sim != -1.0 {
		t.Errorf("expected -1.0, got %f", sim)
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	sim := cosineSimilarity([]float32{}, []float32{})
	if sim != 0.0 {
		t.Errorf("expected 0.0, got %f", sim)
	}
}

func TestCosineSimilarity_MismatchedLength(t *testing.T) {
	sim := cosineSimilarity([]float32{1, 0}, []float32{1, 0, 0})
	if sim != 0.0 {
		t.Errorf("expected 0.0, got %f", sim)
	}
}

func TestCosineSimilarity_Partial(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{4, 5, 6}
	sim := cosineSimilarity(a, b)
	// dot = 4+10+18 = 32, normA = sqrt(14), normB = sqrt(77)
	// sim = 32 / (sqrt(14)*sqrt(77)) = 32 / sqrt(1078) ≈ 32/32.83 ≈ 0.9746
	if sim < 0.97 || sim > 0.98 {
		t.Errorf("expected ~0.9746, got %f", sim)
	}
}

// ── avgConfidence ─────────────────────────────────────────────────────────────

func TestAvgConfidence_Empty(t *testing.T) {
	if avgConfidence(nil) != 0.0 {
		t.Errorf("expected 0.0 for empty list")
	}
}

func TestAvgConfidence_Single(t *testing.T) {
	facts := []ExtractedFact{{Confidence: 0.75}}
	if avgConfidence(facts) != 0.75 {
		t.Errorf("expected 0.75, got %f", avgConfidence(facts))
	}
}

func TestAvgConfidence_Multiple(t *testing.T) {
	facts := []ExtractedFact{
		{Confidence: 0.5},
		{Confidence: 0.7},
		{Confidence: 0.9},
	}
	expected := (0.5 + 0.7 + 0.9) / 3.0
	if avgConfidence(facts) != expected {
		t.Errorf("expected %f, got %f", expected, avgConfidence(facts))
	}
}

// ── SessionAutoExtract ────────────────────────────────────────────────────────

func TestSessionAutoExtract(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	ex := New(cfg, &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))

	if ex.SessionAutoExtract("session-x") {
		t.Error("expected false for unknown session")
	}

	ex.SetSessionAutoExtract("session-x", true)
	if !ex.SessionAutoExtract("session-x") {
		t.Error("expected true after set")
	}

	ex.SetSessionAutoExtract("session-x", false)
	if ex.SessionAutoExtract("session-x") {
		t.Error("expected false after disabling")
	}
}

// ── Extractor.Enabled ─────────────────────────────────────────────────────────

func TestEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	ex := New(cfg, &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))
	if ex.Enabled() {
		t.Error("expected disabled")
	}

	cfg2 := DefaultConfig()
	cfg2.Enabled = true
	ex2 := New(cfg2, &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))
	if !ex2.Enabled() {
		t.Error("expected enabled")
	}
}

// ── Extractor.Stats ───────────────────────────────────────────────────────────

func TestStats_InitialState(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	ex := New(cfg, &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))
	snap := ex.Stats()
	if snap.TotalAttempted != 0 {
		t.Errorf("expected 0 attempts, got %d", snap.TotalAttempted)
	}
}

// ── Extractor.Extract ─────────────────────────────────────────────────────────

func TestExtract_Disabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	lm := &mockSynthesizer{result: `[{"key":"test","value":"val","confidence":0.5,"category":"knowledge","ttl_days":0}]`}
	ex := New(cfg, lm, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))

	ex.Extract(context.Background(), "session-1", "hello", "user")
	if lm.called {
		t.Error("expected no LLM call when disabled")
	}
}

func TestExtract_EnabledWithLLM(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Concurrency = 10

	lm := &mockSynthesizer{
		result: `[{"key":"user_says_hello","value":"User said hello","confidence":0.8,"category":"knowledge","ttl_days":365}]`,
	}
	emb := &mockEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	fs := &mockFactStore{
		collectionName:       "facts",
		scrollFilteredResult: nil, // no existing facts
	}

	ex := New(cfg, lm, emb, fs, slog.New(slog.DiscardHandler))
	ex.Extract(context.Background(), "session-1", "hello", "user")

	if !lm.called {
		t.Error("expected LLM to be called")
	}
}

func TestExtract_LLMReturnsNoFacts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Concurrency = 10

	lm := &mockSynthesizer{result: `[]`}
	ex := New(cfg, lm, &mockEmbedder{vec: []float32{0.1}}, &mockFactStore{collectionName: "facts"}, slog.New(slog.DiscardHandler))

	// Should not panic with empty results
	ex.Extract(context.Background(), "session-1", "test", "user")
}

func TestExtract_LLMError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Concurrency = 10

	lm := &mockSynthesizer{err: assertError{"LLM failure"}}
	ex := New(cfg, lm, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))

	// Should not panic
	ex.Extract(context.Background(), "session-1", "test", "user")
}

func TestExtract_WriteError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Concurrency = 10

	lm := &mockSynthesizer{
		result: `[{"key":"fail_key","value":"will fail","confidence":0.5,"category":"knowledge","ttl_days":0}]`,
	}
	fs := &mockFactStore{
		collectionName:       "facts",
		upsertErr:            assertError{"write failed"},
		scrollFilteredResult: nil,
	}

	ex := New(cfg, lm, &mockEmbedder{vec: []float32{0.1}}, fs, slog.New(slog.DiscardHandler))
	ex.Extract(context.Background(), "session-1", "test", "user")

	snap := ex.Stats()
	if snap.FactsRejected != 1 {
		t.Errorf("expected 1 rejected, got %d", snap.FactsRejected)
	}
}

// ── Extractor.RecentTurnsFn ───────────────────────────────────────────────────

func TestExtract_WithRecentTurns(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Concurrency = 10

	lm := &mockSynthesizer{
		result: `[{"key":"context_test","value":"Has context","confidence":0.8,"category":"knowledge","ttl_days":0}]`,
	}
	ex := New(cfg, lm, &mockEmbedder{vec: []float32{0.1}}, &mockFactStore{collectionName: "facts"}, slog.New(slog.DiscardHandler))

	turnsReturned := false
	ex.RecentTurnsFn = func(_ context.Context, sessionID string, n int) ([]TurnEntry, error) {
		turnsReturned = true
		if sessionID != "session-ctx" {
			t.Errorf("expected 'session-ctx', got %q", sessionID)
		}
		if n != 10 {
			t.Errorf("expected window 10, got %d", n)
		}
		return []TurnEntry{
			{Role: "user", Content: "Previous question"},
			{Role: "assistant", Content: "Previous response with details"},
		}, nil
	}

	ex.Extract(context.Background(), "session-ctx", "current", "user")
	if !turnsReturned {
		t.Error("expected RecentTurnsFn to be called")
	}
}

func TestExtract_RecentTurnsFnError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Concurrency = 10

	lm := &mockSynthesizer{
		result: `[{"key":"err_ok","value":"Still works","confidence":0.5,"category":"knowledge","ttl_days":0}]`,
	}
	ex := New(cfg, lm, &mockEmbedder{vec: []float32{0.1}}, &mockFactStore{collectionName: "facts"}, slog.New(slog.DiscardHandler))

	ex.RecentTurnsFn = func(_ context.Context, _ string, _ int) ([]TurnEntry, error) {
		return nil, assertError{"turn fetch error"}
	}

	// Should not panic — extract falls back to content-only
	ex.Extract(context.Background(), "session-err", "current turn", "user")
}

// ── Extractor.SetEmitter ──────────────────────────────────────────────────────

func TestSetEmitter(t *testing.T) {
	ex := New(DefaultConfig(), &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))
	// Should not panic
	ex.SetEmitter(nil)
}

// ── buildExtractionPrompt ─────────────────────────────────────────────────────

func TestBuildExtractionPrompt_WithTurn(t *testing.T) {
	ex := New(DefaultConfig(), &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))
	prompt := ex.buildExtractionPrompt("Hello, I like Rust", "user")
	if !contains(prompt, "Hello, I like Rust") {
		t.Error("expected turn text in prompt")
	}
	if !contains(prompt, "confidence") {
		t.Error("expected confidence field in prompt")
	}
	if !contains(prompt, "snake_case") {
		t.Error("expected snake_case instruction")
	}
}

func TestBuildExtractionPrompt_Empty(t *testing.T) {
	ex := New(DefaultConfig(), &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))
	prompt := ex.buildExtractionPrompt("", "assistant")
	if prompt == "" {
		t.Error("expected non-empty prompt even with empty turn")
	}
}

// ── getPointVector ────────────────────────────────────────────────────────────

func TestGetPointVector_Nil(t *testing.T) {
	if v := getPointVector(nil); v != nil {
		t.Error("expected nil for nil point")
	}
}

func TestGetPointVector_NoVectors(t *testing.T) {
	pt := &pb.RetrievedPoint{}
	if v := getPointVector(pt); v != nil {
		t.Error("expected nil for point without vectors")
	}
}

func TestGetPointVector_HasVector(t *testing.T) {
	pt := &pb.RetrievedPoint{
		Vectors: &pb.VectorsOutput{
			VectorsOptions: &pb.VectorsOutput_Vector{
				Vector: &pb.Vector{
					Data: []float32{0.1, 0.2, 0.3},
				},
			},
		},
	}
	v := getPointVector(pt)
	if v == nil {
		t.Fatal("expected non-nil vector")
	}
	if len(v) != 3 || v[0] != 0.1 {
		t.Errorf("expected [0.1, 0.2, 0.3], got %v", v)
	}
}

// ── New concurrency clamping ──────────────────────────────────────────────────

func TestNew_ClampsConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Concurrency = 0
	ex := New(cfg, &mockSynthesizer{}, &mockEmbedder{}, &mockFactStore{}, slog.New(slog.DiscardHandler))
	if cap(ex.sem) != 1 {
		t.Errorf("expected semaphore capacity 1, got %d", cap(ex.sem))
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// assertError is a simple error type for testing.
type assertError struct{ msg string }

func (e assertError) Error() string { return e.msg }

// ── Fact serialization round-trip ─────────────────────────────────────────────

func TestExtractedFact_JSONRoundTrip(t *testing.T) {
	fact := ExtractedFact{
		Key:        "test_key",
		Value:      "test value",
		Confidence: 0.75,
		Category:   "preference",
		TTLDays:    90,
	}
	data, err := json.Marshal(fact)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ExtractedFact
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Key != fact.Key {
		t.Errorf("expected %q, got %q", fact.Key, decoded.Key)
	}
	if decoded.Confidence != fact.Confidence {
		t.Errorf("expected %f, got %f", fact.Confidence, decoded.Confidence)
	}
}

func TestJSONRoundTrip_ConfidenceEdge(t *testing.T) {
	tests := []float64{0.0, 1.0, 0.3333333, 0.5}
	for _, c := range tests {
		fact := ExtractedFact{Key: "k", Value: "v", Confidence: c, Category: "knowledge"}
		data, _ := json.Marshal(fact)
		var decoded ExtractedFact
		json.Unmarshal(data, &decoded)
		if decoded.Confidence != c {
			t.Errorf("confidence %f: got %f", c, decoded.Confidence)
		}
	}
}

// ── TurnEntry ─────────────────────────────────────────────────────────────────

func TestTurnEntryJSON(t *testing.T) {
	te := TurnEntry{Content: "hello", Role: "user"}
	data, err := json.Marshal(te)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded TurnEntry
	json.Unmarshal(data, &decoded)
	if decoded.Content != "hello" {
		t.Errorf("expected 'hello', got %q", decoded.Content)
	}
}
