package consolidation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	pb "github.com/qdrant/go-client/qdrant"
)

// ── mocks ─────────────────────────────────────────────────────────────────

type mockSource struct {
	sessions   map[string][]Session
	transcript map[string][]Turn
	transErr   error
}

func (m *mockSource) RecentSessions(_ context.Context, vault string, _ int) ([]Session, error) {
	return m.sessions[vault], nil
}
func (m *mockSource) Transcript(_ context.Context, id string, _ int) ([]Turn, error) {
	if m.transErr != nil {
		return nil, m.transErr
	}
	return m.transcript[id], nil
}

type mockLLM struct {
	resp string
	err  error
}

func (m *mockLLM) Synthesize(_ context.Context, _, _ string) (string, error) {
	return m.resp, m.err
}
func (m *mockLLM) SynthesizeCited(_ context.Context, _, _ string) (string, error) {
	return m.resp, m.err
}
func (m *mockLLM) Compare(_ context.Context, _, _, _, _ string) (string, error) { return "", nil }
func (m *mockLLM) Health(_ context.Context) error                               { return nil }

type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (m *mockEmbedder) EmbedSingle(_ context.Context, _ string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}
func (m *mockEmbedder) Health(_ context.Context) error { return nil }

// mockFacts records upserts. Only Upsert is exercised; the rest satisfy the
// qdrant.FactStore interface as no-ops.
type mockFacts struct {
	mu        sync.Mutex
	upserted  []*pb.PointStruct
	upsertErr error
}

func (m *mockFacts) Upsert(_ context.Context, points []*pb.PointStruct) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.mu.Lock()
	m.upserted = append(m.upserted, points...)
	m.mu.Unlock()
	return nil
}
func (m *mockFacts) Scroll(context.Context, uint32, *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) {
	return nil, nil, nil
}
func (m *mockFacts) ScrollFiltered(context.Context, string, *pb.Filter, uint32, string) ([]*pb.RetrievedPoint, error) {
	return nil, nil
}
func (m *mockFacts) Search(context.Context, []float32, uint64, float32, string, *pb.Filter) ([]*pb.ScoredPoint, error) {
	return nil, nil
}
func (m *mockFacts) DeleteBySource(context.Context, string) error                     { return nil }
func (m *mockFacts) DeleteFiltered(context.Context, string, *pb.Filter) error         { return nil }
func (m *mockFacts) Count(context.Context) (uint64, error)                            { return 0, nil }
func (m *mockFacts) CountFiles(context.Context) (int, error)                          { return 0, nil }
func (m *mockFacts) CreatePayloadIndex(context.Context, string, string, string) error { return nil }
func (m *mockFacts) Health(context.Context) error                                     { return nil }
func (m *mockFacts) Close() error                                                     { return nil }
func (m *mockFacts) GetVectorSize(context.Context, string) (uint64, error)            { return 0, nil }
func (m *mockFacts) GetPoints(context.Context, string, []*pb.PointId) ([]*pb.RetrievedPoint, error) {
	return nil, nil
}
func (m *mockFacts) SetPayload(context.Context, string, []*pb.PointId, map[string]*pb.Value) error {
	return nil
}
func (m *mockFacts) UpdateVectors(context.Context, string, []*pb.PointVectors) error { return nil }
func (m *mockFacts) Collection() string                                              { return "facts" }
func (m *mockFacts) ScrollWithVectors(context.Context, uint32, *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) {
	return nil, nil, nil
}

var _ qdrant.FactStore = (*mockFacts)(nil)

type mockEmitter struct {
	mu     sync.Mutex
	events []string
}

func (m *mockEmitter) Emit(eventType string, _ any) {
	m.mu.Lock()
	m.events = append(m.events, eventType)
	m.mu.Unlock()
}

// ── tests ─────────────────────────────────────────────────────────────────

func oldTime(hoursAgo int) string {
	return time.Now().UTC().Add(time.Duration(-hoursAgo) * time.Hour).Format(time.RFC3339)
}

func TestScheduleReplayInterleaving(t *testing.T) {
	pool := make([]Session, 10)
	for i := range pool {
		pool[i] = Session{ID: fmt.Sprintf("s%d", i), TurnCount: i, UpdatedAt: oldTime(i)}
	}
	// n=5, ratio=0.4 → 2 old, 3 new.
	batch := scheduleReplay(pool, 5, 0.4)
	if len(batch) != 5 {
		t.Fatalf("expected 5, got %d", len(batch))
	}
	// First 3 must be the newest (s0, s1, s2).
	for i := 0; i < 3; i++ {
		if batch[i].ID != fmt.Sprintf("s%d", i) {
			t.Errorf("position %d: got %s, want s%d", i, batch[i].ID, i)
		}
	}
	// The old slice must be drawn from the remainder and weighted by importance
	// (turn count) → s9 (highest turn count) should be included.
	found := false
	for _, s := range batch[3:] {
		if s.ID == "s9" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected highest-importance old session s9 in interleaved slice, got %+v", batch[3:])
	}
}

func TestScheduleReplaySmallPool(t *testing.T) {
	pool := []Session{{ID: "a"}, {ID: "b"}}
	batch := scheduleReplay(pool, 5, 0.3)
	if len(batch) != 2 {
		t.Fatalf("small pool should return all: got %d", len(batch))
	}
}

func TestReplayImportanceOrdering(t *testing.T) {
	rich := Session{TurnCount: 50, UpdatedAt: oldTime(48)}
	sparse := Session{TurnCount: 2, UpdatedAt: oldTime(1)}
	if replayImportance(rich) <= replayImportance(sparse) {
		t.Error("richer session should score higher despite being older")
	}
}

func TestRunOnceWritesGistAndEmits(t *testing.T) {
	src := &mockSource{
		sessions: map[string][]Session{
			"v": {{ID: "s1", Vault: "v", TurnCount: 4, UpdatedAt: oldTime(5)}},
		},
		transcript: map[string][]Turn{
			"s1": {{Role: "user", Content: "I prefer Go for backends"}, {Role: "assistant", Content: "Noted."}},
		},
	}
	facts := &mockFacts{}
	emitter := &mockEmitter{}
	c := New(Config{Enabled: true, IdleWindow: time.Hour, BatchSize: 10, GistTTLDays: 100},
		src, &mockLLM{resp: "The user prefers Go for backend development."}, &mockEmbedder{}, facts, emitter,
		func() []string { return []string{"v"} }, nil)

	if err := c.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(facts.upserted) != 1 {
		t.Fatalf("expected 1 gist fact written, got %d", len(facts.upserted))
	}
	p := facts.upserted[0]
	if p.Payload["source_type"].GetStringValue() != "consolidation" {
		t.Errorf("gist source_type = %q", p.Payload["source_type"].GetStringValue())
	}
	if !p.Payload["gist"].GetBoolValue() {
		t.Error("gist flag not set")
	}
	if len(emitter.events) != 1 || emitter.events[0] != "consolidation.complete" {
		t.Errorf("expected consolidation.complete event, got %v", emitter.events)
	}
	st := c.Snapshot()
	if st.TotalRuns != 1 || st.LastRunGists != 1 || st.LastRunSessions != 1 {
		t.Errorf("unexpected stats: %+v", st)
	}
}

func TestRunOnceIdleGate(t *testing.T) {
	// Newest session updated 1 minute ago; idle window 1 hour → skip.
	src := &mockSource{
		sessions: map[string][]Session{
			"v": {{ID: "s1", Vault: "v", TurnCount: 4, UpdatedAt: time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)}},
		},
		transcript: map[string][]Turn{"s1": {{Role: "user", Content: "hi"}}},
	}
	facts := &mockFacts{}
	c := New(Config{Enabled: true, IdleWindow: time.Hour, BatchSize: 10},
		src, &mockLLM{resp: "gist"}, &mockEmbedder{}, facts, &mockEmitter{},
		func() []string { return []string{"v"} }, nil)

	if err := c.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(facts.upserted) != 0 {
		t.Errorf("idle gate should skip non-idle vault, wrote %d", len(facts.upserted))
	}
}

func TestRunOnceEmptyGistNotWritten(t *testing.T) {
	src := &mockSource{
		sessions:   map[string][]Session{"v": {{ID: "s1", Vault: "v", TurnCount: 1, UpdatedAt: oldTime(5)}}},
		transcript: map[string][]Turn{"s1": {{Role: "user", Content: "hello"}}},
	}
	facts := &mockFacts{}
	c := New(Config{Enabled: true, BatchSize: 10},
		src, &mockLLM{resp: "  \n  "}, &mockEmbedder{}, facts, &mockEmitter{},
		func() []string { return []string{"v"} }, nil)

	if err := c.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(facts.upserted) != 0 {
		t.Errorf("empty gist must not be written, wrote %d", len(facts.upserted))
	}
}

func TestRunOnceLLMErrorRecorded(t *testing.T) {
	src := &mockSource{
		sessions:   map[string][]Session{"v": {{ID: "s1", Vault: "v", TurnCount: 1, UpdatedAt: oldTime(5)}}},
		transcript: map[string][]Turn{"s1": {{Role: "user", Content: "hello world this is content"}}},
	}
	facts := &mockFacts{}
	c := New(Config{Enabled: true, BatchSize: 10},
		src, &mockLLM{err: errors.New("boom")}, &mockEmbedder{}, facts, &mockEmitter{},
		func() []string { return []string{"v"} }, nil)

	// Sweep still succeeds overall; the failing session is skipped.
	if err := c.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(facts.upserted) != 0 {
		t.Errorf("no gist should be written on LLM error")
	}
	if c.Snapshot().LastRunSessions != 1 {
		t.Errorf("session should still count as replayed")
	}
}

func TestDisabledRunIsNoop(t *testing.T) {
	c := New(Config{Enabled: false}, &mockSource{}, &mockLLM{}, &mockEmbedder{}, &mockFacts{}, &mockEmitter{}, nil, nil)
	if c.Enabled() {
		t.Error("should be disabled")
	}
	// Run should return immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Run(ctx) // returns without blocking
}

func TestRenderTranscriptSkipsEmpty(t *testing.T) {
	got := renderTranscript([]Turn{{Role: "user", Content: "  "}, {Role: "assistant", Content: "hi"}})
	if got != "assistant: hi\n" {
		t.Errorf("got %q", got)
	}
}
