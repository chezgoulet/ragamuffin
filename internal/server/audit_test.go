package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
)

func TestCheckStaleness_EmptyVault(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{VaultPath: dir}
	srv := &Server{cfg: cfg}

	stale, err := srv.checkStaleness(cfg.VaultPath, 90)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected 0 stale files in empty dir, got %d", len(stale))
	}
}

func TestCheckStaleness_OldFile(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "old.md")
	os.WriteFile(oldFile, []byte("old content"), 0644)

	// Set mtime to 200 days ago
	oldTime := time.Now().AddDate(0, 0, -200)
	os.Chtimes(oldFile, oldTime, oldTime)

	cfg := &config.Config{VaultPath: dir}
	srv := &Server{cfg: cfg}

	stale, err := srv.checkStaleness(cfg.VaultPath, 90)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale file, got %d", len(stale))
	}
	if stale[0]["path"] != "old.md" {
		t.Errorf("path = %q, want old.md", stale[0]["path"])
	}
}

func TestCheckStaleness_RecentFile(t *testing.T) {
	dir := t.TempDir()
	recentFile := filepath.Join(dir, "recent.md")
	os.WriteFile(recentFile, []byte("recent"), 0644)

	cfg := &config.Config{VaultPath: dir}
	srv := &Server{cfg: cfg}

	stale, err := srv.checkStaleness(cfg.VaultPath, 90)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected 0 stale files for recent file, got %d", len(stale))
	}
}

func TestCheckGaps_EmptyDirs(t *testing.T) {
	dir := t.TempDir()
	emptyDir := filepath.Join(dir, "empty-dir")
	os.MkdirAll(emptyDir, 0755)

	cfg := &config.Config{VaultPath: dir}
	srv := &Server{cfg: cfg}

	gaps := srv.checkGaps(cfg.VaultPath)
	found := false
	for _, g := range gaps {
		if contains(g, "empty-dir") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gap for empty dir, got: %v", gaps)
	}
}

func TestCheckGaps_NoGaps(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("content"), 0644)

	cfg := &config.Config{VaultPath: dir}
	srv := &Server{cfg: cfg}

	gaps := srv.checkGaps(cfg.VaultPath)
	if len(gaps) != 0 {
		t.Errorf("expected no gaps with files present, got: %v", gaps)
	}
}

func TestCheckDuplicates_NoDupes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("b"), 0644)

	cfg := &config.Config{VaultPath: dir}
	srv := &Server{cfg: cfg}

	dupes := srv.checkDuplicates(cfg.VaultPath)
	if len(dupes) != 0 {
		t.Errorf("expected no duplicates, got: %v", dupes)
	}
}

func TestCheckDuplicates_InSubdirs(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(subDir, "readme.md"), []byte("b"), 0644)

	cfg := &config.Config{VaultPath: dir}
	srv := &Server{cfg: cfg}

	dupes := srv.checkDuplicates(cfg.VaultPath)
	if len(dupes) != 1 {
		t.Fatalf("expected 1 duplicate, got %d", len(dupes))
	}
	if dupes[0]["filename"] != "readme.md" {
		t.Errorf("filename = %q, want readme.md", dupes[0]["filename"])
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 100, "short"},
		{"1234567890", 5, "12345..."},
		{"exact", 5, "exact"},
		{"", 10, ""},
	}
	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s[1:], substr) || s[:len(substr)] == substr)
}
