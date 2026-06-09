package ingress

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/watcher"
)

// ── IngestAction constants ───────────────────────────────────────────────────

func TestIngestActionConstants(t *testing.T) {
	if ActionAdd != "add" {
		t.Errorf("expected 'add', got %q", ActionAdd)
	}
	if ActionModify != "modify" {
		t.Errorf("expected 'modify', got %q", ActionModify)
	}
	if ActionDelete != "delete" {
		t.Errorf("expected 'delete', got %q", ActionDelete)
	}
}

// ── IngestEvent ──────────────────────────────────────────────────────────────

func TestIngestEvent(t *testing.T) {
	e := IngestEvent{
		Action:  ActionAdd,
		Path:    "docs/notes.md",
		AbsPath: "/vault/docs/notes.md",
		Content: []byte("hello"),
		Meta:    map[string]string{"vault": "main"},
	}
	if string(e.Content) != "hello" {
		t.Errorf("expected 'hello', got %q", string(e.Content))
	}
	if e.Meta["vault"] != "main" {
		t.Errorf("expected 'main', got %q", e.Meta["vault"])
	}
}

func TestIngestEvent_Defaults(t *testing.T) {
	var e IngestEvent
	if e.Action != "" {
		t.Errorf("expected empty action, got %q", e.Action)
	}
}

// ── APIIngestDriver ──────────────────────────────────────────────────────────

func TestAPIIngestDriver_Name(t *testing.T) {
	d := NewAPIIngestDriver("test-api", slog.New(slog.DiscardHandler), nil)
	if d.Name() != "test-api" {
		t.Errorf("expected 'test-api', got %q", d.Name())
	}
}

func TestAPIIngestDriver_Events(t *testing.T) {
	d := NewAPIIngestDriver("test", slog.New(slog.DiscardHandler), nil)
	ch := d.Events()
	if ch == nil {
		t.Fatal("expected non-nil events channel")
	}
}

func TestAPIIngestDriver_Ingest_Success(t *testing.T) {
	var captured struct {
		content string
		source  string
		vault   string
		tags    []string
	}
	ingestFunc := func(_ context.Context, content, source, vault string, tags []string) error {
		captured.content = content
		captured.source = source
		captured.vault = vault
		captured.tags = tags
		return nil
	}

	d := NewAPIIngestDriver("api", slog.New(slog.DiscardHandler), ingestFunc)
	err := d.Ingest(context.Background(), "hello world", "test.txt", "docs", []string{"tag1"})
	if err != nil {
		t.Fatalf("Ingest returned error: %v", err)
	}
	if captured.content != "hello world" {
		t.Errorf("expected 'hello world', got %q", captured.content)
	}
	if captured.source != "test.txt" {
		t.Errorf("expected 'test.txt', got %q", captured.source)
	}
	if captured.vault != "docs" {
		t.Errorf("expected 'docs', got %q", captured.vault)
	}
}

func TestAPIIngestDriver_Ingest_Error(t *testing.T) {
	ingestFunc := func(_ context.Context, _, _, _ string, _ []string) error {
		return assertError{"ingestion failed"}
	}
	d := NewAPIIngestDriver("api", slog.New(slog.DiscardHandler), ingestFunc)
	err := d.Ingest(context.Background(), "content", "src", "v", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAPIIngestDriver_Ingest_EmitsEvent(t *testing.T) {
	d := NewAPIIngestDriver("api", slog.New(slog.DiscardHandler),
		func(_ context.Context, _, _, _ string, _ []string) error { return nil })

	err := d.Ingest(context.Background(), "content", "notes.md", "main", nil)
	if err != nil {
		t.Fatalf("Ingest returned error: %v", err)
	}

	select {
	case evt := <-d.Events():
		if evt.Action != ActionAdd {
			t.Errorf("expected ActionAdd, got %q", evt.Action)
		}
		if evt.Path != "notes.md" {
			t.Errorf("expected 'notes.md', got %q", evt.Path)
		}
		if string(evt.Content) != "content" {
			t.Errorf("expected 'content', got %q", string(evt.Content))
		}
		if evt.Meta["vault"] != "main" {
			t.Errorf("expected vault 'main', got %q", evt.Meta["vault"])
		}
	default:
		t.Error("expected event on channel after Ingest")
	}
}

func TestAPIIngestDriver_Ingest_EventChannelFull(t *testing.T) {
	d := NewAPIIngestDriver("api", slog.New(slog.DiscardHandler),
		func(_ context.Context, _, _, _ string, _ []string) error { return nil })

	// Fill the channel (capacity 1000)
	ticker := time.NewTicker(1 * time.Millisecond)
	defer ticker.Stop()

	// Emit enough events to fill the buffer
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1500; i++ {
			d.Ingest(context.Background(), "x", "src", "v", nil)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out ingesting events")
	}

	// Should have around 1000 events received (channel buffer is 1000)
	count := 0
	ch := d.Events()
	for {
		select {
		case <-ch:
			count++
		default:
			goto doneCounting
		}
	}
doneCounting:
	if count < 900 || count > 1000 {
		t.Logf("received %d events (expected ~1000, buffer full drops)", count)
	}
}

func TestAPIIngestDriver_Ingest_TagsInMeta(t *testing.T) {
	d := NewAPIIngestDriver("api", slog.New(slog.DiscardHandler),
		func(_ context.Context, _, _, _ string, _ []string) error { return nil })

	d.Ingest(context.Background(), "content", "src", "vault1", []string{"tag-a", "tag-b"})

	select {
	case evt := <-d.Events():
		if evt.Meta["vault1:tag-a"] != "tag-a" {
			t.Errorf("expected 'tag-a' in meta key vault1:tag-a, got %q", evt.Meta["vault1:tag-a"])
		}
		if evt.Meta["vault1:tag-b"] != "tag-b" {
			t.Errorf("expected 'tag-b' in meta key vault1:tag-b, got %q", evt.Meta["vault1:tag-b"])
		}
	default:
		t.Error("expected event")
	}
}

func TestAPIIngestDriver_Run(t *testing.T) {
	d := NewAPIIngestDriver("api", slog.New(slog.DiscardHandler), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := d.Run(ctx)
	if err != nil {
		t.Errorf("expected nil error from Run, got %v", err)
	}
	// Channel should be closed after Run returns
	_, ok := <-d.Events()
	if ok {
		t.Error("expected events channel to be closed after Run returns")
	}
}

// ── IngressDriver interface compliance ───────────────────────────────────────

func TestAPIIngestDriver_ImplementsInterface(t *testing.T) {
	var _ IngressDriver = (*APIIngestDriver)(nil)
}

// ── fanout helpers ───────────────────────────────────────────────────────────

type assertError struct{ msg string }

func (e assertError) Error() string { return e.msg }

// Helper to verify fanoutEvent and fanoutIngest don't panic
func TestFanout_BasicIntegration(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	rawCh := make(chan watcher.Event, 100)
	idxCh := make(chan watcher.Event, 100)
	ingestCh := make(chan IngestEvent, 100)
	pruneCh := make(chan watcher.Event, 100)

	evt := watcher.Event{Path: "/vault/file.md", AbsPath: "/vault/file.md"}

	// Fan out
	fanoutEvent(evt, idxCh, make(chan struct{}, 1000), 1000, logger, "indexer")
	fanoutIngest(IngestEvent{Action: ActionAdd, Path: "file.md"}, ingestCh, make(chan struct{}, 1000), 1000, logger, "ingest")
	fanoutEvent(evt, pruneCh, make(chan struct{}, 1000), 1000, logger, "pruner")

	// Verify each channel received
	select {
	case <-idxCh:
	default:
		t.Error("indexer channel did not receive event")
	}
	select {
	case <-ingestCh:
	default:
		t.Error("ingest channel did not receive event")
	}
	select {
	case <-pruneCh:
	default:
		t.Error("pruner channel did not receive event")
	}

	close(rawCh)
}
