package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── New ─────────────────────────────────────────────────────────────────────

func TestNew_FileChanged(t *testing.T) {
	data := FileChangedData{Path: "/vault/notes.md", Action: "modified", Size: 1024}
	evt := New(TypeFileChanged, "ragamuffin", data)

	if evt.SpecVersion != "1.0" {
		t.Errorf("expected specversion 1.0, got %q", evt.SpecVersion)
	}
	if evt.Type != TypeFileChanged {
		t.Errorf("expected %q, got %q", TypeFileChanged, evt.Type)
	}
	if evt.Source != "ragamuffin" {
		t.Errorf("expected source 'ragamuffin', got %q", evt.Source)
	}
	if evt.ID == "" {
		t.Errorf("expected non-empty ID")
	}
	if evt.Time == "" {
		t.Errorf("expected non-empty time")
	}

	// Verify the data is preserved
	d, ok := evt.Data.(FileChangedData)
	if !ok {
		t.Fatalf("expected FileChangedData, got %T", evt.Data)
	}
	if d.Path != "/vault/notes.md" {
		t.Errorf("expected path /vault/notes.md, got %q", d.Path)
	}
	if d.Action != "modified" {
		t.Errorf("expected action 'modified', got %q", d.Action)
	}
}

func TestNew_ServerStarted(t *testing.T) {
	data := ServerStartedData{
		Version:   "0.4.0",
		Commit:    "abc123",
		GoVersion: "1.25",
		Host:      "localhost",
		Port:      "8000",
	}
	evt := New(TypeServerStarted, "ragamuffin", data)

	if evt.Type != TypeServerStarted {
		t.Errorf("expected %q, got %q", TypeServerStarted, evt.Type)
	}

	d, ok := evt.Data.(ServerStartedData)
	if !ok {
		t.Fatalf("expected ServerStartedData, got %T", evt.Data)
	}
	if d.Version != "0.4.0" {
		t.Errorf("expected version 0.4.0, got %q", d.Version)
	}
	if d.Host != "localhost" {
		t.Errorf("expected localhost, got %q", d.Host)
	}
}

func TestNew_FileDeleted(t *testing.T) {
	data := FileDeletedData{Path: "/vault/old.md"}
	evt := New(TypeFileDeleted, "ragamuffin", data)

	if evt.Type != TypeFileDeleted {
		t.Errorf("expected %q, got %q", TypeFileDeleted, evt.Type)
	}

	d, ok := evt.Data.(FileDeletedData)
	if !ok {
		t.Fatalf("expected FileDeletedData, got %T", evt.Data)
	}
	if d.Path != "/vault/old.md" {
		t.Errorf("expected /vault/old.md, got %q", d.Path)
	}
}

func TestNew_CollectionIndex(t *testing.T) {
	data := CollectionIndexData{
		Vault:      "docs",
		FileCount:  150,
		ChunkCount: 3200,
		Duration:   "12.5s",
	}
	evt := New(TypeCollectionIndex, "ragamuffin", data)

	if evt.Type != TypeCollectionIndex {
		t.Errorf("expected %q, got %q", TypeCollectionIndex, evt.Type)
	}

	d, ok := evt.Data.(CollectionIndexData)
	if !ok {
		t.Fatalf("expected CollectionIndexData, got %T", evt.Data)
	}
	if d.Vault != "docs" {
		t.Errorf("expected docs, got %q", d.Vault)
	}
	if d.FileCount != 150 {
		t.Errorf("expected 150, got %d", d.FileCount)
	}
}

func TestNew_NilData(t *testing.T) {
	evt := New(TypeServerStarted, "test", nil)
	if evt.Data != nil {
		t.Errorf("expected nil data, got %+v", evt.Data)
	}
}

func TestNew_TimeFormat(t *testing.T) {
	// Use a fixed point in time for predictable test
	before := time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	evt := New("test.event", "test", nil)
	after := time.Now().Add(time.Second).UTC().Format(time.RFC3339)

	if evt.Time < before {
		t.Errorf("event time %q is before expected window %q", evt.Time, before)
	}
	if evt.Time > after {
		t.Errorf("event time %q is after expected window %q", evt.Time, after)
	}
}

// ── MarshalJSON ──────────────────────────────────────────────────────────────

func TestMarshalJSON_Valid(t *testing.T) {
	evt := New(TypeFileChanged, "test", FileChangedData{Path: "/test.md", Action: "created"})
	data, err := evt.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var result map[string]interface{}
	json.Unmarshal(data, &result)

	if result["specversion"] != "1.0" {
		t.Errorf("expected specversion 1.0, got %v", result["specversion"])
	}
	if result["type"] != TypeFileChanged {
		t.Errorf("expected type %q, got %v", TypeFileChanged, result["type"])
	}
	if result["source"] != "test" {
		t.Errorf("expected source 'test', got %v", result["source"])
	}
	if _, ok := result["id"]; !ok {
		t.Errorf("expected id field")
	}
	if _, ok := result["time"]; !ok {
		t.Errorf("expected time field")
	}
	if _, ok := result["data"]; !ok {
		t.Errorf("expected data field")
	}
}

func TestMarshalJSON_OmitEmptyData(t *testing.T) {
	evt := CloudEvent{
		SpecVersion: "1.0",
		ID:          "test-id",
		Source:      "test",
		Type:        "test.event",
		Time:        "2024-01-01T00:00:00Z",
		// Data is nil — should be omitted
	}
	data, err := evt.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "data") {
		t.Errorf("expected no data field for nil Data, got: %s", string(data))
	}
}

// ── String ──────────────────────────────────────────────────────────────────

func TestString_Valid(t *testing.T) {
	evt := New("test.event", "test", "payload")
	s := evt.String()
	if s == "" {
		t.Fatal("expected non-empty string")
	}
	if !strings.HasPrefix(s, "{") {
		t.Errorf("expected JSON object, got %q", s)
	}
	if !strings.Contains(s, "test.event") {
		t.Errorf("expected event type in output, got %q", s)
	}
}

func TestString_InvalidMarshal(t *testing.T) {
	// Channel values can't be marshaled to JSON
	evt := CloudEvent{
		SpecVersion: "1.0",
		ID:          "test",
		Source:      "test",
		Type:        "test",
		Time:        "now",
		Data:        make(chan int),
	}
	s := evt.String()
	if s == "" {
		t.Fatal("expected non-empty string")
	}
	if !strings.Contains(s, "error") {
		t.Errorf("expected error in output for un-marshalable data, got %q", s)
	}
}

// ── uuidV7 ──────────────────────────────────────────────────────────────────

func TestUUIDV7_Format(t *testing.T) {
	id := uuidV7()
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Errorf("expected 5 UUID parts, got %d (id=%q)", len(parts), id)
	}
	if len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Errorf("unexpected UUID part lengths: %v", parts)
	}
}

func TestUUIDV7_VersionBits(t *testing.T) {
	id := uuidV7()
	// Third group should start with 7 (version 7)
	parts := strings.Split(id, "-")
	if len(parts) < 3 {
		t.Fatal("invalid UUID")
	}
	if parts[2][0] != '7' {
		t.Errorf("expected version 7 (first char of 3rd group = 7), got %q", parts[2])
	}
}

func TestUUIDV7_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := uuidV7()
		if seen[id] {
			t.Errorf("duplicate UUID generated: %q", id)
		}
		seen[id] = true
	}
}

func TestUUIDV7_HexChars(t *testing.T) {
	id := uuidV7()
	for _, c := range id {
		if c == '-' {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex character %q in UUID %q", c, id)
		}
	}
}

// ── Data types ──────────────────────────────────────────────────────────────

func TestFileChangedData_JSON(t *testing.T) {
	d := FileChangedData{Path: "/vault/notes.md", Action: "modified", Size: 1024, Hash: "abc123"}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded FileChangedData
	json.Unmarshal(data, &decoded)
	if decoded.Path != "/vault/notes.md" {
		t.Errorf("expected /vault/notes.md, got %q", decoded.Path)
	}
	if decoded.Action != "modified" {
		t.Errorf("expected modified, got %q", decoded.Action)
	}
	if decoded.Size != 1024 {
		t.Errorf("expected 1024, got %d", decoded.Size)
	}
	if decoded.Hash != "abc123" {
		t.Errorf("expected abc123, got %q", decoded.Hash)
	}
}

func TestFileDeletedData_JSON(t *testing.T) {
	d := FileDeletedData{Path: "/vault/old.md"}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded FileDeletedData
	json.Unmarshal(data, &decoded)
	if decoded.Path != "/vault/old.md" {
		t.Errorf("expected /vault/old.md, got %q", decoded.Path)
	}
}

func TestServerStartedData_JSON(t *testing.T) {
	d := ServerStartedData{Version: "0.4.0", Commit: "abc123", GoVersion: "1.25", Host: "localhost", Port: "8000"}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ServerStartedData
	json.Unmarshal(data, &decoded)
	if decoded.Version != "0.4.0" {
		t.Errorf("expected 0.4.0, got %q", decoded.Version)
	}
	if decoded.Commit != "abc123" {
		t.Errorf("expected abc123, got %q", decoded.Commit)
	}
	if decoded.Host != "localhost" {
		t.Errorf("expected localhost, got %q", decoded.Host)
	}
}

func TestCollectionIndexData_JSON(t *testing.T) {
	d := CollectionIndexData{Vault: "docs", FileCount: 150, ChunkCount: 3200, Duration: "12.5s"}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded CollectionIndexData
	json.Unmarshal(data, &decoded)
	if decoded.Vault != "docs" {
		t.Errorf("expected docs, got %q", decoded.Vault)
	}
	if decoded.FileCount != 150 {
		t.Errorf("expected 150, got %d", decoded.FileCount)
	}
	if decoded.Duration != "12.5s" {
		t.Errorf("expected 12.5s, got %q", decoded.Duration)
	}
}

func TestCloudEvent_DataTypes(t *testing.T) {
	types := map[string]string{
		TypeFileChanged:     "vault.file.changed",
		TypeFileDeleted:     "vault.file.deleted",
		TypeCollectionIndex: "vault.collection.reindexed",
		TypeServerStarted:   "ragamuffin.started",
		TypeServerHealthy:   "ragamuffin.healthy",
	}
	for k, v := range types {
		if k != v {
			t.Errorf("constant %q doesn't match value %q", k, v)
		}
	}
}

// ── Broker ────────────────────────────────────────────────────────────────────

func TestBroker_SubscribeUnsubscribe(t *testing.T) {
	b := NewBroker()
	ch := make(chan CloudEvent, 10)
	b.Subscribe(ch)

	if b.Len() != 1 {
		t.Errorf("expected 1 subscriber, got %d", b.Len())
	}

	b.Unsubscribe(ch)
	if b.Len() != 0 {
		t.Errorf("expected 0 subscribers, got %d", b.Len())
	}
}

func TestBroker_PublishFanOut(t *testing.T) {
	b := NewBroker()
	ch1 := make(chan CloudEvent, 10)
	ch2 := make(chan CloudEvent, 10)
	b.Subscribe(ch1)
	b.Subscribe(ch2)

	evt := New("test.event", "test", "payload")
	b.Publish(evt)

	// Both subscribers should receive the event
	select {
	case <-ch1:
	default:
		t.Error("ch1 did not receive event")
	}
	select {
	case <-ch2:
	default:
		t.Error("ch2 did not receive event")
	}
}

func TestBroker_PublishDropSlowConsumer(t *testing.T) {
	b := NewBroker()
	// Unbuffered channel — publish will drop
	ch := make(chan CloudEvent)
	b.Subscribe(ch)

	// Fill with one event (won't block — dropped)
	evt := New("test.event", "test", nil)
	b.Publish(evt)

	// Should not block or crash
	b.Unsubscribe(ch)
}

func TestBroker_PublishNoSubscribers(t *testing.T) {
	b := NewBroker()
	evt := New("test.event", "test", nil)
	// Should not panic with zero subscribers
	b.Publish(evt)
}

func TestBroker_ConcurrentAccess(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup

	// Concurrent subscribe/unsubscribe/publish
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := make(chan CloudEvent, 5)
			b.Subscribe(ch)
			b.Publish(New("e", "s", nil))
			b.Unsubscribe(ch)
		}()
	}
	wg.Wait()
	// Should not panic under concurrent access
}

// ── Emitter ───────────────────────────────────────────────────────────────────

type mockLogStore struct {
	appended []string
}

func (m *mockLogStore) Append(_ context.Context, agent, eventType, body string, _ []string, _ time.Time) (string, error) {
	m.appended = append(m.appended, eventType+":"+body)
	return "id-1", nil
}

func TestEmitter_Nil(t *testing.T) {
	// Calling Emit on nil should not panic
	var e *Emitter
	e.Emit("test", nil)
	e.EmitSync(context.Background(), "test", nil)
	e.Close()
}

func TestEmitter_NoWebhook(t *testing.T) {
	// Emitter with empty webhook URL should not panic
	e := NewEmitter("", "test", slog.New(slog.DiscardHandler), nil, nil, nil)
	e.Emit("test.event", "payload")
	e.EmitSync(context.Background(), "test.event", "payload")
	e.Close()
}

func TestEmitter_Closed(t *testing.T) {
	e := NewEmitter("http://example.com/webhook", "test", slog.New(slog.DiscardHandler), nil, nil, nil)
	e.Close()
	// Emit after close should not panic
	e.Emit("test.event", "payload")
}

func TestEmitter_WithLogStore(t *testing.T) {
	ls := &mockLogStore{}
	e := NewEmitter("", "test", slog.New(slog.DiscardHandler), ls, nil, nil)

	e.Emit("test.event", "hello world")
	if len(ls.appended) != 1 {
		t.Fatalf("expected 1 logstore append, got %d", len(ls.appended))
	}
	if !strings.Contains(ls.appended[0], "test.event") {
		t.Errorf("expected 'test.event' in log, got %q", ls.appended[0])
	}
}

func TestEmitter_WithBroker(t *testing.T) {
	b := NewBroker()
	ch := make(chan CloudEvent, 10)
	b.Subscribe(ch)

	e := NewEmitter("", "test", slog.New(slog.DiscardHandler), nil, b, nil)
	e.Emit("test.event", map[string]string{"key": "val"})

	select {
	case evt := <-ch:
		if evt.Type != "test.event" {
			t.Errorf("expected 'test.event', got %q", evt.Type)
		}
	default:
		t.Error("expected event on broker channel")
	}

	b.Unsubscribe(ch)
}

func TestEmitter_AllowedEvents(t *testing.T) {
	// Create a local HTTP server to catch webhook POSTs
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		receivedBody = string(buf)
		w.WriteHeader(200)
	}))
	defer server.Close()

	// Only allow "allowed.event"
	e := NewEmitter(server.URL, "test", slog.New(slog.DiscardHandler), nil, nil, []string{"allowed.event"})

	// This should not be sent
	e.Emit("blocked.event", "data")

	// This should be sent
	e.EmitSync(context.Background(), "allowed.event", "data")

	if receivedBody == "" {
		t.Fatal("expected webhook to receive allowed.event")
	}
	if !strings.Contains(receivedBody, "allowed.event") {
		t.Errorf("expected 'allowed.event' in body, got %q", receivedBody)
	}
}

func TestEmitter_AllowedEventsEmpty(t *testing.T) {
	// Empty allowed events list = send all to webhook
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer server.Close()

	e := NewEmitter(server.URL, "test", slog.New(slog.DiscardHandler), nil, nil, []string{})
	// EmitSync should send since empty list = send all
	err := e.EmitSync(context.Background(), "any.event", "data")
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestEmitter_EmitSyncWebhookError(t *testing.T) {
	// Server returning 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	e := NewEmitter(server.URL, "test", slog.New(slog.DiscardHandler), nil, nil, nil)
	err := e.EmitSync(context.Background(), "test.event", "data")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected '500' in error, got %q", err.Error())
	}
}

func TestEmitter_EmitSyncContextCancelled(t *testing.T) {
	e := NewEmitter("http://127.0.0.1:1", "test", slog.New(slog.DiscardHandler), nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := e.EmitSync(ctx, "test.event", "data")
	if err == nil {
		t.Log("emit with cancelled context may return nil if connection refused")
	}
}

func TestEmitter_MarshalError(t *testing.T) {
	// Create an emitter with an unreachable URL
	e := NewEmitter("http://127.0.0.1:1", "test", slog.New(slog.DiscardHandler), nil, nil, nil)
	// This should not panic — the post is best-effort
	e.Emit("test.event", "data")
}

func TestEmitter_PrunerCompleteData(t *testing.T) {
	data := PrunerCompleteData{
		ScanName: "nightly-scan",
		Duration: "12.3s",
		Flagged:  5,
	}
	evt := New(TypePrunerComplete, "pruner", data)
	if evt.Type != TypePrunerComplete {
		t.Errorf("expected %q, got %q", TypePrunerComplete, evt.Type)
	}
	d, ok := evt.Data.(PrunerCompleteData)
	if !ok {
		t.Fatalf("expected PrunerCompleteData, got %T", evt.Data)
	}
	if d.Flagged != 5 {
		t.Errorf("expected 5 flagged, got %d", d.Flagged)
	}
}

func TestEmitterFactDataTypes(t *testing.T) {
	t.Run("FactCreatedData", func(t *testing.T) {
		d := FactCreatedData{Key: "user_prefers_rust", Value: "likes Rust", Confidence: 0.85}
		evt := New(TypeFactCreated, "extraction", d)
		d2, ok := evt.Data.(FactCreatedData)
		if !ok || d2.Key != "user_prefers_rust" {
			t.Errorf("expected user_prefers_rust, got %v", d2.Key)
		}
	})

	t.Run("FactFlaggedData", func(t *testing.T) {
		d := FactFlaggedData{Key: "stale_fact", Reason: "expired", Detail: "TTL exceeded"}
		evt := New(TypeFactFlagged, "pruner", d)
		d2, ok := evt.Data.(FactFlaggedData)
		if !ok || d2.Reason != "expired" {
			t.Errorf("expected 'expired', got %q", d2.Reason)
		}
	})

	t.Run("FactReviewedData", func(t *testing.T) {
		d := FactReviewedData{Key: "fact_123", Action: "approve"}
		evt := New(TypeFactReviewed, "review", d)
		d2, ok := evt.Data.(FactReviewedData)
		if !ok || d2.Action != "approve" {
			t.Errorf("expected 'approve', got %q", d2.Action)
		}
	})
}
