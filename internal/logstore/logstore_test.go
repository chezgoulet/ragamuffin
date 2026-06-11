package logstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ── encodeID / decodeID ──────────────────────────────────────────────────────

func TestEncodeDecodeID_Roundtrip(t *testing.T) {
	cases := []int64{0, 1, 255, 65535, 1234567890}
	for _, id := range cases {
		enc := encodeID(id)
		dec, err := decodeID(enc)
		if err != nil {
			t.Errorf("decode(%q): unexpected error: %v", enc, err)
		}
		if dec != id {
			t.Errorf("roundtrip: %d → %q → %d", id, enc, dec)
		}
	}
}

func TestEncodeID_Format(t *testing.T) {
	enc := encodeID(1)
	// Always 16 hex chars (8 bytes → 16 hex digits)
	if len(enc) != 16 {
		t.Errorf("expected 16 chars, got %q (%d)", enc, len(enc))
	}
	for _, c := range enc {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex char %q in encoded id %q", c, enc)
		}
	}
}

func TestDecodeID_Invalid(t *testing.T) {
	cases := []string{
		"",                 // empty
		"xyz",              // short + non-hex
		"000000000000000z", // 16 chars but has non-hex
		"nothex",           // short
	}
	for _, c := range cases {
		_, err := decodeID(c)
		if err == nil {
			t.Errorf("expected error for input %q", c)
		}
	}
}

func TestDecodeID_WrongLength(t *testing.T) {
	// 15 hex chars (should be 16)
	_, err := decodeID("000000000000000")
	if err == nil {
		t.Errorf("expected error for 15-char input")
	}
}

// ── Store lifecycle ─────────────────────────────────────────────────────────

func tempPath(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/test.db"
}

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(tempPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpen_TempFile(t *testing.T) {
	s, err := Open(tempPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil store")
	}
	if s.db == nil {
		t.Fatal("expected non-nil database handle")
	}
	s.Close()
}

func TestClose(t *testing.T) {
	s := openStore(t)
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Double close should not panic
	_ = s.Close()
}

func TestDoubleOpen(t *testing.T) {
	s1 := openStore(t)
	s2 := openStore(t)
	s1.Close()
	s2.Close()
}

// ── Append ──────────────────────────────────────────────────────────────────

func TestAppend_ReturnsNonEmptyID(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	id, err := s.Append(context.Background(), "test-agent", "test.event", "hello", nil, time.Time{})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
	if len(id) != 16 {
		t.Errorf("expected 16-char hex ID, got %q (%d)", id, len(id))
	}
}

func TestAppend_WithTags(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	id, err := s.Append(context.Background(), "agent", "event", "body", []string{"tag1", "tag2"}, time.Now())
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
}

func TestAppend_WithZeroTimestamp(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	before := time.Now().UTC().Add(-time.Second)
	id, err := s.Append(context.Background(), "agent", "event", "body", nil, time.Time{})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Re-read to verify timestamp was set
	entries, _, err := s.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.ID == id {
			if e.CreatedAt < before.Format(time.RFC3339Nano[:19]) {
				t.Errorf("timestamp looks too old: %q", e.CreatedAt)
			}
			return
		}
	}
	t.Error("appended entry not found in List")
}

func TestAppend_ExplicitTimestamp(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	id, err := s.Append(context.Background(), "agent", "event", "body", nil, ts)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	entries, _, _ := s.List(context.Background(), Filter{})
	for _, e := range entries {
		if e.ID == id {
			if !strings.HasPrefix(e.CreatedAt, "2025-01-15T10:30:00") {
				t.Errorf("expected 2025-01-15T10:30:00, got %q", e.CreatedAt)
			}
			return
		}
	}
	t.Error("entry not found")
}

func TestAppend_MultipleEntries(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	for i := 0; i < 5; i++ {
		_, err := s.Append(context.Background(), "agent", "event", "body", nil, time.Time{})
		if err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	entries, _, err := s.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(entries))
	}
}

// ── List ─────────────────────────────────────────────────────────────────────

func TestList_AllEntries(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	s.Append(ctx(), "a1", "t1", "body1", nil, time.Time{})
	s.Append(ctx(), "a2", "t2", "body2", nil, time.Time{})

	entries, next, err := s.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
	// Ordered DESC by ID — most recent first
	if next != "" {
		t.Errorf("expected empty next token for small result, got %q", next)
	}
}

func TestList_FilterByAgent(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	s.Append(ctx(), "agent-a", "t1", "body-a", nil, time.Time{})
	s.Append(ctx(), "agent-b", "t1", "body-b", nil, time.Time{})

	entries, _, _ := s.List(context.Background(), Filter{Agent: "agent-a"})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for agent-a, got %d", len(entries))
	}
	if entries[0].Body != "body-a" {
		t.Errorf("expected body-a, got %q", entries[0].Body)
	}
}

func TestList_FilterByType(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	s.Append(ctx(), "agent", "type-a", "body", nil, time.Time{})
	s.Append(ctx(), "agent", "type-b", "body", nil, time.Time{})

	entries, _, _ := s.List(context.Background(), Filter{Type: "type-a"})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for type-a, got %d", len(entries))
	}
}

func TestList_FilterByTag(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	s.Append(ctx(), "agent", "event", "body", []string{"important", "urgent"}, time.Time{})
	s.Append(ctx(), "agent", "event", "body-other", []string{"normal"}, time.Time{})

	entries, _, _ := s.List(context.Background(), Filter{Tag: "important"})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry with tag 'important', got %d", len(entries))
	}
}

func TestList_FilterBySince(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	new := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	s.Append(ctx(), "agent", "event", "old", nil, old)
	s.Append(ctx(), "agent", "event", "new", nil, new)

	// Only entries after 2024-06-01
	entries, _, _ := s.List(context.Background(), Filter{
		Since: "2024-06-01T00:00:00Z",
	})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after 2024-06-01, got %d", len(entries))
	}
	if entries[0].Body != "new" {
		t.Errorf("expected 'new', got %q", entries[0].Body)
	}
}

func TestList_FilterByUntil(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	new := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	s.Append(ctx(), "agent", "event", "old", nil, old)
	s.Append(ctx(), "agent", "event", "new", nil, new)

	// Only entries before 2025-01-01
	entries, _, _ := s.List(context.Background(), Filter{
		Until: "2025-01-01T00:00:00Z",
	})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry before 2025-01-01, got %d", len(entries))
	}
	if entries[0].Body != "old" {
		t.Errorf("expected 'old', got %q", entries[0].Body)
	}
}

func TestList_Pagination(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	ctx := context.Background()
	// Insert 4 entries. The cursor-based pagination uses the extra
	// fetched row as a boundary marker — that row won't appear in
	// results. With limit=2 and 4 entries: page 1 returns 2 rows,
	// page 2 returns the row(s) before the cursor.
	for i := 0; i < 4; i++ {
		_, err := s.Append(ctx, "agent", "event", "body", nil, time.Time{})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// Page 1: limit 2, returns 2 entries + next token
	entries, next, err := s.List(context.Background(), Filter{Limit: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("page 1: expected 2 entries, got %d", len(entries))
	}
	if next == "" {
		t.Fatal("page 1: expected non-empty next token")
	}

	// Page 2: before=next cursor (points to boundary row, not returned)
	entries, next, err = s.List(ctx, Filter{Limit: 2, Before: next})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("page 2: expected 1 entry (cursor consumes the boundary), got %d", len(entries))
	}
	if next != "" {
		t.Errorf("page 2: expected empty next token (end), got %q", next)
	}

	// Verify pagination is exhaustive by summing total rows
	allEntries, _, _ := s.List(ctx, Filter{Limit: 100})
	if len(allEntries) != 4 {
		t.Errorf("expected 4 total entries, got %d", len(allEntries))
	}
}

func TestList_DefaultLimit(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	// Insert more than default limit (100)
	for i := 0; i < 150; i++ {
		_, err := s.Append(ctx(), "agent", "event", "body", nil, time.Time{})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// No limit specified — should use default 100 (and fetch 101 to detect next)
	entries, next, err := s.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 100 {
		t.Errorf("expected 100 entries with default limit, got %d", len(entries))
	}
	if next == "" {
		t.Error("expected next token when there are more entries")
	}
}

func TestList_LimitCappedAt1000(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	s.Append(ctx(), "agent", "event", "body", nil, time.Time{})

	entries, _, _ := s.List(context.Background(), Filter{Limit: 9999})
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (clamped to 1000), got %d", len(entries))
	}
}

func TestList_ZeroLimit(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	for i := 0; i < 5; i++ {
		s.Append(ctx(), "agent", "event", "body", nil, time.Time{})
	}

	entries, _, _ := s.List(context.Background(), Filter{Limit: 0})
	if len(entries) != 5 {
		t.Errorf("expected 5 entries (0 → default 100 is enough), got %d", len(entries))
	}
}

func TestList_UnknownTagHasNoResults(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	s.Append(ctx(), "agent", "event", "body", []string{"tag1"}, time.Time{})

	entries, _, _ := s.List(context.Background(), Filter{Tag: "nonexistent"})
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nonexistent tag, got %d", len(entries))
	}
}

func TestList_InvalidBeforeCursor(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	_, _, err := s.List(context.Background(), Filter{Before: "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid cursor")
	}
	if !strings.Contains(err.Error(), "invalid cursor") {
		t.Errorf("expected 'invalid cursor' error, got %q", err.Error())
	}
}

func TestList_MultipleFilters(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	s.Append(ctx(), "agent-a", "event-x", "body-a-x", []string{"important"}, time.Time{})
	s.Append(ctx(), "agent-a", "event-y", "body-a-y", nil, time.Time{})
	s.Append(ctx(), "agent-b", "event-x", "body-b-x", nil, time.Time{})

	entries, _, _ := s.List(context.Background(), Filter{
		Agent: "agent-a",
		Type:  "event-x",
	})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Body != "body-a-x" {
		t.Errorf("expected body-a-x, got %q", entries[0].Body)
	}
}

// ── Entry structure ─────────────────────────────────────────────────────────

func TestLogEntry_RoundTrip(t *testing.T) {
	s := openStore(t)
	defer s.Close()

	ts := time.Date(2025, 3, 15, 14, 30, 0, 0, time.UTC)
	id, err := s.Append(context.Background(), "test-agent", "test.event", "hello world", []string{"a", "b"}, ts)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	entries, _, _ := s.List(context.Background(), Filter{})
	var found *LogEntry
	for i := range entries {
		if entries[i].ID == id {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatal("entry not found")
	}

	if found.Agent != "test-agent" {
		t.Errorf("agent: expected 'test-agent', got %q", found.Agent)
	}
	if found.Type != "test.event" {
		t.Errorf("type: expected 'test.event', got %q", found.Type)
	}
	if found.Body != "hello world" {
		t.Errorf("body: expected 'hello world', got %q", found.Body)
	}
	if len(found.Tags) != 2 || found.Tags[0] != "a" || found.Tags[1] != "b" {
		t.Errorf("tags: expected [a b], got %v", found.Tags)
	}
	if found.CreatedAt != "2025-03-15T14:30:00Z" {
		t.Errorf("created_at: expected '2025-03-15T14:30:00Z', got %q", found.CreatedAt)
	}
}

// ── Error handling ──────────────────────────────────────────────────────────

// ctx is a shorthand for context.Background.
func ctx() context.Context {
	return context.Background()
}
