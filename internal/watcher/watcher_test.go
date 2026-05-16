package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── Action type ──────────────────────────────────────────────────────────────

func TestAction_String(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{ActionAdd, "add"},
		{ActionModify, "modify"},
		{ActionDelete, "delete"},
		{Action(999), "unknown"},
	}
	for _, c := range cases {
		if got := c.a.String(); got != c.want {
			t.Errorf("Action(%d).String() = %q, want %q", c.a, got, c.want)
		}
	}
}

// ── Event structure ─────────────────────────────────────────────────────────

func TestEvent_Fields(t *testing.T) {
	e := Event{Path: "notes.md", AbsPath: "/tmp/vault/notes.md", Action: ActionAdd}
	if e.Path != "notes.md" {
		t.Errorf("expected notes.md, got %q", e.Path)
	}
	if e.Action != ActionAdd {
		t.Errorf("expected ActionAdd, got %v", e.Action)
	}
}

// ── isIndexable ──────────────────────────────────────────────────────────────

func TestIsIndexable_Markdown(t *testing.T) {
	if !isIndexable("file.md") {
		t.Error("expected .md to be indexable")
	}
}

func TestIsIndexable_Txt(t *testing.T) {
	if !isIndexable("file.txt") {
		t.Error("expected .txt to be indexable")
	}
}

func TestIsIndexable_Org(t *testing.T) {
	if !isIndexable("file.org") {
		t.Error("expected .org to be indexable")
	}
}

func TestIsIndexable_RST(t *testing.T) {
	if !isIndexable("file.rst") {
		t.Error("expected .rst to be indexable")
	}
}

func TestIsIndexable_NoExtension(t *testing.T) {
	if !isIndexable("README") {
		t.Error("expected file without extension to be indexable")
	}
}

func TestIsIndexable_Image(t *testing.T) {
	if isIndexable("image.png") {
		t.Error("expected .png to not be indexable")
	}
}

func TestIsIndexable_PDF(t *testing.T) {
	if isIndexable("doc.pdf") {
		t.Error("expected .pdf to not be indexable")
	}
}

func TestIsIndexable_Code(t *testing.T) {
	cases := []string{"main.go", "script.py", "style.css", "app.js", "index.html"}
	for _, c := range cases {
		if isIndexable(c) {
			t.Errorf("expected %q to not be indexable", c)
		}
	}
}

func TestIsIndexable_DotDir(t *testing.T) {
	if isIndexable(".git/config") {
		t.Error("expected .git/config to not be indexable")
	}
}

func TestIsIndexable_DotDirInSubpath(t *testing.T) {
	if isIndexable("repo/.github/workflows.yml") {
		t.Error("expected subpath with .github to not be indexable")
	}
}

func TestIsIndexable_Dotfile(t *testing.T) {
	if isIndexable(".gitignore") {
		t.Error("expected .gitignore to not be indexable")
	}
}

func TestIsIndexable_UppercaseExtension(t *testing.T) {
	if !isIndexable("file.MD") {
		t.Error("expected .MD (uppercase) to be indexable")
	}
	if !isIndexable("file.TXT") {
		t.Error("expected .TXT (uppercase) to be indexable")
	}
}

func TestIsIndexable_ExtensionCaseMixed(t *testing.T) {
	if !isIndexable("file.Md") {
		t.Error("expected .Md to be indexable")
	}
}

// ── pollWatcher ──────────────────────────────────────────────────────────────

func tempVault(t *testing.T) (string, *pollWatcher) {
	t.Helper()
	dir := t.TempDir()
	w := newPollWatcher(dir, 100*time.Millisecond, slog.Default())
	return dir, w
}

func TestNewPollWatcher(t *testing.T) {
	_, w := tempVault(t)
	if w == nil {
		t.Fatal("expected non-nil watcher")
	}
	if w.vaultPath == "" {
		t.Error("expected non-empty vaultPath")
	}
	if w.interval != 100*time.Millisecond {
		t.Errorf("expected 100ms interval, got %v", w.interval)
	}
	if w.state == nil {
		t.Error("expected non-nil state map")
	}
	if len(w.state) != 0 {
		t.Errorf("expected empty initial state, got %d", len(w.state))
	}
}

func TestPollWatcher_InitialScan(t *testing.T) {
	dir, w := tempVault(t)

	// Create a file before scan
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	events := make(chan Event, 10)
	w.scan(events, true) // initial scan: no events

	select {
	case <-events:
		t.Error("initial scan should not produce events")
	default:
		// OK — no events on initial scan
	}

	if len(w.state) != 1 {
		t.Errorf("expected 1 tracked file, got %d", len(w.state))
	}
}

func TestPollWatcher_DetectAdd(t *testing.T) {
	dir, w := tempVault(t)
	w.scan(make(chan Event, 10), true) // populate initial state

	events := make(chan Event, 10)
	w.scan(events, false) // no changes yet

	select {
	case <-events:
		t.Error("expected no events before any file change")
	default:
	}

	// Now create a file and scan again
	if err := os.WriteFile(filepath.Join(dir, "new.md"), []byte("new"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.scan(events, false)

	select {
	case e := <-events:
		if e.Action != ActionAdd {
			t.Errorf("expected ActionAdd, got %v", e.Action)
		}
		if e.Path != "new.md" {
			t.Errorf("expected 'new.md', got %q", e.Path)
		}
	default:
		t.Error("expected add event")
	}
}

func TestPollWatcher_DetectModify(t *testing.T) {
	dir, w := tempVault(t)

	// Create and scan once
	filePath := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(filePath, []byte("v1"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.scan(make(chan Event, 10), true)

	// Modify the file
	time.Sleep(10 * time.Millisecond) // ensure different mtime
	if err := os.WriteFile(filePath, []byte("v2"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	events := make(chan Event, 10)
	w.scan(events, false)

	select {
	case e := <-events:
		if e.Action != ActionModify {
			t.Errorf("expected ActionModify, got %v", e.Action)
		}
		if e.Path != "doc.md" {
			t.Errorf("expected 'doc.md', got %q", e.Path)
		}
	default:
		t.Error("expected modify event")
	}
}

func TestPollWatcher_DetectDelete(t *testing.T) {
	dir, w := tempVault(t)

	filePath := filepath.Join(dir, "old.md")
	if err := os.WriteFile(filePath, []byte("old"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.scan(make(chan Event, 10), true)

	// Delete the file
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	events := make(chan Event, 10)
	w.scan(events, false)

	select {
	case e := <-events:
		if e.Action != ActionDelete {
			t.Errorf("expected ActionDelete, got %v", e.Action)
		}
		if e.Path != "old.md" {
			t.Errorf("expected 'old.md', got %q", e.Path)
		}
	default:
		t.Error("expected delete event")
	}
}

func TestPollWatcher_MultipleEvents(t *testing.T) {
	dir, w := tempVault(t)
	w.scan(make(chan Event, 10), true)

	// Add 2 files, modify 1, delete 1
	files := []string{"a.md", "b.md", "c.md", "d.md"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	w.scan(make(chan Event, 10), true) // now tracking all 4

	// Delete a.md, modify b.md, add e.md
	if err := os.Remove(filepath.Join(dir, "a.md")); err != nil {
		t.Fatalf("remove a.md: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("modified"), 0644); err != nil {
		t.Fatalf("modify b.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "e.md"), []byte("new"), 0644); err != nil {
		t.Fatalf("write e.md: %v", err)
	}

	events := make(chan Event, 10)
	w.scan(events, false)

	got := make(map[string]Action)
	for i := 0; i < 3; i++ {
		select {
		case e := <-events:
			got[e.Path] = e.Action
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i+1)
		}
	}

	if got["a.md"] != ActionDelete {
		t.Errorf("expected ActionDelete for a.md, got %v", got["a.md"])
	}
	if got["b.md"] != ActionModify {
		t.Errorf("expected ActionModify for b.md, got %v", got["b.md"])
	}
	if got["e.md"] != ActionAdd {
		t.Errorf("expected ActionAdd for e.md, got %v", got["e.md"])
	}
}

func TestPollWatcher_NonIndexableIgnored(t *testing.T) {
	dir, w := tempVault(t)

	// Create non-indexable files
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("img"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme"), []byte("text"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	w.scan(make(chan Event, 10), true)

	// Only "readme" should be tracked (no extension → indexable)
	if len(w.state) != 1 {
		t.Errorf("expected 1 tracked file (readme), got %d", len(w.state))
	}
	if _, ok := w.state["readme"]; !ok {
		t.Error("expected 'readme' in state")
	}
}

func TestPollWatcher_LockUnlock(t *testing.T) {
	_, w := tempVault(t)

	w.Lock()
	// While locked, scan should be blocked
	// (Lock acquires the mutex, so other operations can't proceed)
	w.Unlock()

	// After unlock, should work fine
	events := make(chan Event, 10)
	w.scan(events, true)

	select {
	case <-events:
		t.Error("initial scan should not produce events")
	default:
	}
}

func TestPollWatcher_SubdirectoryFiles(t *testing.T) {
	dir, w := tempVault(t)

	// Create files in subdirectory
	subDir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "notes.md"), []byte("deep"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	w.scan(make(chan Event, 10), true)

	if len(w.state) != 1 {
		t.Fatalf("expected 1 tracked file, got %d", len(w.state))
	}
	relPath := "subdir" + string(filepath.Separator) + "notes.md"
	if _, ok := w.state[relPath]; !ok {
		t.Errorf("expected %q in state", relPath)
	}
	// Verify OS path separator — on Windows this could be \\, on Unix /
	for p := range w.state {
		if p != relPath && p != "subdir/notes.md" {
			t.Errorf("unexpected path: %q", p)
		}
	}
}
