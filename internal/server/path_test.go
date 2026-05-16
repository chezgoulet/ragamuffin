package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeVaultPath_NormalPath(t *testing.T) {
	dir := t.TempDir()
	full, err := safeVaultPath(dir, "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(dir, "test.md")
	if full != expected {
		t.Errorf("got %q, want %q", full, expected)
	}
}

func TestSafeVaultPath_Subdirectory(t *testing.T) {
	dir := t.TempDir()
	full, err := safeVaultPath(dir, "subdir/file.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(dir, "subdir/file.md")
	if full != expected {
		t.Errorf("got %q, want %q", full, expected)
	}
}

func TestSafeVaultPath_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := safeVaultPath(dir, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestSafeVaultPath_AbsolutePathJoinedRelative(t *testing.T) {
	dir := t.TempDir()
	// On Unix, filepath.Join treats absolute path elements as relative components
	// (unlike path.Join which discards prior elements).
	// So /etc/passwd joined under vault root = vaultRoot/etc/passwd, which is safe.
	full, err := safeVaultPath(dir, "/etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := dir + "/etc/passwd"
	if full != filepath.Clean(expected) {
		t.Errorf("got %q, want %q", full, filepath.Clean(expected))
	}
}

func TestSafeVaultPath_SymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	outsider := t.TempDir()

	// Create a symlink inside vault that points outside
	linkPath := filepath.Join(dir, "escape")
	if err := os.Symlink(outsider, linkPath); err != nil {
		t.Skip("symlink not supported on this system")
	}

	_, err := safeVaultPath(dir, "escape")
	if err == nil {
		t.Fatal("expected error for symlink escape, got nil")
	}
}

func TestSafeVaultPath_SymlinkWithinVault(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "target")
	os.MkdirAll(targetDir, 0755)
	os.WriteFile(filepath.Join(targetDir, "file.md"), []byte("content"), 0644)

	linkPath := filepath.Join(dir, "link")
	if err := os.Symlink("target", linkPath); err != nil {
		t.Skip("symlink not supported on this system")
	}

	full, err := safeVaultPath(dir, "link/file.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(dir, "link/file.md")
	if full != expected {
		t.Errorf("got %q, want %q", full, expected)
	}
}

func TestSafeVaultPath_NonExistentFile(t *testing.T) {
	dir := t.TempDir()
	// Non-existent files should pass lexical checks (they can't escape via symlink yet)
	full, err := safeVaultPath(dir, "nonexistent.md")
	if err != nil {
		t.Fatalf("unexpected error for non-existent file: %v", err)
	}
	expected := filepath.Join(dir, "nonexistent.md")
	if full != expected {
		t.Errorf("got %q, want %q", full, expected)
	}
}

func TestSafeVaultPath_EmptyRequestedPath(t *testing.T) {
	dir := t.TempDir()
	full, err := safeVaultPath(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if full != dir {
		t.Errorf("got %q, want vault root %q", full, dir)
	}
}

func TestSafeVaultPath_DotPath(t *testing.T) {
	dir := t.TempDir()
	full, err := safeVaultPath(dir, ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if full != dir {
		t.Errorf("got %q, want vault root %q", full, dir)
	}
}
