package indexer

import (
	"context"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/indexutil"
	"time"
)

func TestIsIndexable(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"file.md", true},
		{"file.MD", true},
		{"file.txt", true},
		{"file.org", true},
		{"file.rst", true},
		{"README", true}, // no extension
		{"file.json", false},
		{"file.go", false},
		{"file.py", false},
		{"file.yaml", false},
		{"file.yml", false},
		{"file.pdf", false},
		{".hidden.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := indexutil.IsIndexable(tt.path)
			if result != tt.expected {
				t.Errorf("indexutil.IsIndexable(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestNew(t *testing.T) {
	idx := New("/vault", "test-vault", nil, nil, nil)
	if idx == nil {
		t.Fatal("New() returned nil")
	}
	if idx.vaultPath != "/vault" {
		t.Errorf("vaultPath = %q, want /vault", idx.vaultPath)
	}
}

func TestSetChunkMaxTokens(t *testing.T) {
	idx := New("/vault", "test-vault", nil, nil, nil)
	idx.SetChunkMaxTokens(500)
	if idx.chunkMaxTokens != 500 {
		t.Errorf("chunkMaxTokens = %d, want 500", idx.chunkMaxTokens)
	}
	idx.SetChunkMaxTokens(0)
	if idx.chunkMaxTokens != 0 {
		t.Errorf("chunkMaxTokens = %d, want 0", idx.chunkMaxTokens)
	}
}

// ── Ingest ──────────────────────────────────────────────────────────────────

func TestIngest_NilEmbedder(t *testing.T) {
	idx := New("/vault", "test-vault", nil, nil, nil)
	err := idx.Ingest(context.Background(), "some content", "source-1", nil, nil)
	if err == nil {
		t.Error("expected error with nil embedder")
	}
}

func TestIngest_EmptyContent(t *testing.T) {
	idx := New("/vault", "test-vault", nil, nil, nil)
	err := idx.Ingest(context.Background(), "", "source-1", nil, nil)
	if err == nil {
		t.Error("expected error with empty content")
	}
}

func TestStats_Initial(t *testing.T) {
	idx := New("/vault", "test-vault", nil, nil, nil)
	fc, cc, li, indexing, pp, tf := idx.Stats()

	if fc != 0 {
		t.Errorf("fileCount = %d, want 0", fc)
	}
	if cc != 0 {
		t.Errorf("chunkCount = %d, want 0", cc)
	}
	if !li.IsZero() {
		t.Errorf("lastIndexed = %v, want zero", li)
	}
	if indexing {
		t.Error("indexing = true, want false")
	}
	if pp != 0 {
		t.Errorf("progressPct = %d, want 0", pp)
	}
	if tf != 0 {
		t.Errorf("totalFiles = %d, want 0", tf)
	}
}

func TestStats_AfterIndex(t *testing.T) {
	idx := New("/vault", "test-vault", nil, nil, nil)
	idx.mu.Lock()
	idx.fileCount = 5
	idx.chunkCount = 42
	idx.lastIndexed = time.Now()
	idx.indexing = false
	idx.progressPct = 100
	idx.totalFiles = 5
	idx.mu.Unlock()

	fc, cc, li, indexing, pp, tf := idx.Stats()
	if fc != 5 {
		t.Errorf("fileCount = %d, want 5", fc)
	}
	if cc != 42 {
		t.Errorf("chunkCount = %d, want 42", cc)
	}
	if li.IsZero() {
		t.Error("lastIndexed is zero")
	}
	if indexing {
		t.Error("indexing should be false")
	}
	if pp != 100 {
		t.Errorf("progressPct = %d, want 100", pp)
	}
	if tf != 5 {
		t.Errorf("totalFiles = %d, want 5", tf)
	}
}
