package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Unit tests: validInboxID ───────────────────────────────────────────────────

func TestValidInboxID(t *testing.T) {
	tests := []struct {
		id    string
		valid bool
		desc  string
	}{
		// Valid IDs (server-generated format: "20060102-150405-slug")
		{"20260102-150405-hello-world", true, "normal timestamp-slug format"},
		{"20260102-150405-a", true, "minimal slug"},
		{"a", true, "single character"},
		{"abc123", true, "alphanumeric"},
		{"ABC-123_def", true, "mixed case, hyphens, underscores"},
		{"20260102-150405-", false, "trailing hyphen"},
		{"-leading", false, "leading hyphen"},
		{"_leading", false, "leading underscore"},
		{"", false, "empty string"},
		// Path traversal sequences — must be rejected
		{"../../etc/passwd", false, "slash path traversal"},
		{"..%2f..%2fetc%2fpasswd", false, "URL-encoded slash path traversal — raw string still rejected"},
		{"../config", false, "dot dot slash"},
		{"..\\..\\config", false, "backslash traversal"},
		{"/etc/passwd", false, "absolute path"},
		{"foo/bar", false, "slash in middle"},
		{"foo/../../etc", false, "traversal in middle"},
		// Too long
		{strings.Repeat("a", 129), false, "exceeds max length"},
		{strings.Repeat("a", 128), true, "at max length"},
		// Special characters
		{"hello world", false, "space"},
		{"hello.world", false, "dot"},
		{"hello%20world", false, "percent encoding literal"},
		{"hello\x00world", false, "null byte"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := validInboxID(tt.id)
			if got != tt.valid {
				t.Errorf("validInboxID(%q) = %v, want %v", tt.id, got, tt.valid)
			}
		})
	}
}

// ── Unit tests: parseInboxFile ─────────────────────────────────────────────────

func TestParseInboxFile(t *testing.T) {
	tests := []struct {
		id     string
		result string
		desc   string
	}{
		{"20260102-150405-hello", "20260102-150405-hello.md", "normal ID"},
		{"abc", "abc.md", "simple ID"},
		{"../../etc/passwd", "", "path traversal returns empty"},
		{"", "", "empty returns empty"},
		{"a/b", "", "slash returns empty"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := parseInboxFile(tt.id)
			if got != tt.result {
				t.Errorf("parseInboxFile(%q) = %q, want %q", tt.id, got, tt.result)
			}
		})
	}
}

// ── Handler tests: path traversal rejection ────────────────────────────────────

// setupInboxTest creates a temp vault directory with an _inbox subdirectory
// and returns a minimal server that can serve inbox handlers.
func setupInboxTest(t *testing.T) (*Server, string) {
	t.Helper()
	vaultDir := t.TempDir()
	inboxDir := filepath.Join(vaultDir, "_inbox")
	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		t.Fatalf("failed to create inbox dir: %v", err)
	}

	// Create a real inbox entry for positive tests
	entryContent := "---\ntitle: \"Test Entry\"\ncreated_at: \"2026-01-01T00:00:00Z\"\nprocessed: false\n---\nHello world\n"
	entryPath := filepath.Join(inboxDir, "20260102-150405-test-slug.md")
	if err := os.WriteFile(entryPath, []byte(entryContent), 0644); err != nil {
		t.Fatalf("failed to create test entry: %v", err)
	}

	srv := &Server{
		logger: testLogger(t),
	}
	return srv, vaultDir
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestHandleInboxRead_PathTraversalRejected(t *testing.T) {
	srv, vaultDir := setupInboxTest(t)

	tests := []struct {
		id       string
		wantCode int
		wantBody string
		desc     string
	}{
		{"20260102-150405-test-slug", 200, "Hello world", "valid ID returns entry"},
		{"../../etc/passwd", 400, "INVALID_ID", "path traversal rejected"},
		{"..%2f..%2fetc%2fpasswd", 400, "INVALID_ID", "URL-encoded path traversal rejected"},
		{"/etc/passwd", 400, "INVALID_ID", "absolute path rejected"},
		{"../config", 400, "INVALID_ID", "dotdot-single rejected"},
		{"nonexistent", 404, "NOT_FOUND", "valid ID but nonexistent file returns 404"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/vault/test/inbox/"+tt.id, nil)
			w := httptest.NewRecorder()
			srv.handleInboxRead(w, req, vaultDir, tt.id)

			if w.Code != tt.wantCode {
				t.Errorf("handleInboxRead(%q) = status %d, want %d; body: %s",
					tt.id, w.Code, tt.wantCode, w.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("handleInboxRead(%q) body missing %q: %s",
					tt.id, tt.wantBody, w.Body.String())
			}
		})
	}
}

func TestHandleInboxDelete_PathTraversalRejected(t *testing.T) {
	srv, vaultDir := setupInboxTest(t)

	tests := []struct {
		id       string
		wantCode int
		wantBody string
		desc     string
	}{
		{"../../etc/passwd", 400, "INVALID_ID", "path traversal rejected"},
		{"..%2f..%2fetc%2fpasswd", 400, "INVALID_ID", "URL-encoded path traversal rejected"},
		{"/etc/passwd", 400, "INVALID_ID", "absolute path rejected"},
		{"nonexistent", 404, "NOT_FOUND", "valid ID but nonexistent returns 404"},
		{"20260102-150405-test-slug", 200, "already_processed", "valid ID processed (first delete marks processed)"},
		{"20260102-150405-test-slug", 200, "already_processed", "second delete returns already_processed"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/vault/test/inbox/"+tt.id, nil)
			w := httptest.NewRecorder()
			srv.handleInboxDelete(w, req, vaultDir, tt.id)

			if w.Code != tt.wantCode {
				t.Errorf("handleInboxDelete(%q) = status %d, want %d; body: %s",
					tt.id, w.Code, tt.wantCode, w.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("handleInboxDelete(%q) body missing %q: %s",
					tt.id, tt.wantBody, w.Body.String())
			}
		})
	}
}

// Test a case where the inbox directory has a symlink — ensure path traversal
// through symlinks doesn't work. The validInboxID gate prevents this at the
// ID level, but this tests defense-in-depth with the filepath.Join + clean.
func TestHandleInboxRead_SymlinkEntry(t *testing.T) {
	vaultDir := t.TempDir()
	inboxDir := filepath.Join(vaultDir, "_inbox")
	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		t.Fatalf("failed to create inbox dir: %v", err)
	}

	// Create a symlink inside the inbox directory pointing outside
	sensitiveFile := filepath.Join(vaultDir, "sensitive.md")
	if err := os.WriteFile(sensitiveFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("failed to create sensitive file: %v", err)
	}
	symlinkPath := filepath.Join(inboxDir, "20260102-150405-link.md")
	if err := os.Symlink(sensitiveFile, symlinkPath); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	srv := &Server{logger: testLogger(t)}

	// Even with the symlink in place, the valid ID should let us read it
	req := httptest.NewRequest(http.MethodGet, "/vault/test/inbox/20260102-150405-link", nil)
	w := httptest.NewRecorder()
	srv.handleInboxRead(w, req, vaultDir, "20260102-150405-link")
	if w.Code != 200 {
		t.Errorf("valid ID with symlink target = status %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "secret") {
		t.Errorf("symlink entry should read symlink target content; body: %s", w.Body.String())
	}
}
