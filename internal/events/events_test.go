package events

import (
	"encoding/json"
	"strings"
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
