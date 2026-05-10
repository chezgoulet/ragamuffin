package indexer

import (
	"strings"
	"testing"
	"time"
)

func TestChunkMarkdown_Headings(t *testing.T) {
	content := `# Title
Some intro text.

## Section 1
Content for section 1.

### Subsection 1.1
Deeper content.

## Section 2
Final section text.
`
	chunks := chunkMarkdown(content, "test.md", time.Now())

	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}

	expected := []struct {
		header string
		body   string
	}{
		{"# Title", "# Title\nSome intro text."},
		{"## Section 1", "## Section 1\nContent for section 1."},
		{"### Subsection 1.1", "### Subsection 1.1\nDeeper content."},
		{"## Section 2", "## Section 2\nFinal section text."},
	}

	for i, exp := range expected {
		if chunks[i].Header != exp.header {
			t.Errorf("chunk %d: header = %q, want %q", i, chunks[i].Header, exp.header)
		}
		if chunks[i].Text != exp.body {
			t.Errorf("chunk %d: text = %q, want %q", i, chunks[i].Text, exp.body)
		}
		if chunks[i].SourceFile != "test.md" {
			t.Errorf("chunk %d: source = %q, want test.md", i, chunks[i].SourceFile)
		}
		if chunks[i].ChunkIndex != i {
			t.Errorf("chunk %d: index = %d, want %d", i, chunks[i].ChunkIndex, i)
		}
	}
}

func TestChunkMarkdown_Empty(t *testing.T) {
	chunks := chunkMarkdown("", "empty.md", time.Now())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func TestChunkMarkdown_WhitespaceOnly(t *testing.T) {
	chunks := chunkMarkdown("\n\n  \n\n", "blank.md", time.Now())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for whitespace-only file, got %d", len(chunks))
	}
}

func TestChunkMarkdown_NoHeadings(t *testing.T) {
	content := "Plain text without any headings.\nJust a paragraph."
	chunks := chunkMarkdown(content, "plain.md", time.Now())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for heading-less file, got %d", len(chunks))
	}
	if chunks[0].Header != "" {
		t.Errorf("header = %q, want empty", chunks[0].Header)
	}
	if !strings.Contains(chunks[0].Text, "Plain text") {
		t.Errorf("text missing content: %q", chunks[0].Text)
	}
}

func TestChunkMarkdown_SingleHeading(t *testing.T) {
	content := "# Only Heading\nContent under it."
	chunks := chunkMarkdown(content, "single.md", time.Now())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Header != "# Only Heading" {
		t.Errorf("header = %q", chunks[0].Header)
	}
}

func TestChunkMarkdown_CodeBlock(t *testing.T) {
	content := "# Code\n\n```go\nfunc main() {\n    fmt.Println(\"# not a heading\")\n}\n```\n\nAfter code."
	chunks := chunkMarkdown(content, "code.md", time.Now())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	// The # inside a code block is still treated as a heading by the simple chunker.
	// This is a known limitation — spec says H4+ stays in parent, and code blocks
	// with # at line start will be mis-chunked. That's acceptable for v0.1.
}

func TestChunkPlain(t *testing.T) {
	content := "Paragraph one.\n\nParagraph two.\n\nParagraph three."
	chunks := chunkPlain(content, "plain.txt", time.Now())

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	for i, expected := range []string{
		"Paragraph one.",
		"Paragraph two.",
		"Paragraph three.",
	} {
		if chunks[i].Text != expected {
			t.Errorf("chunk %d: text = %q, want %q", i, chunks[i].Text, expected)
		}
		if chunks[i].ChunkIndex != i {
			t.Errorf("chunk %d: index = %d, want %d", i, chunks[i].ChunkIndex, i)
		}
	}
}

func TestChunkPlain_Empty(t *testing.T) {
	chunks := chunkPlain("", "empty.txt", time.Now())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestChunkPlain_SingleParagraph(t *testing.T) {
	chunks := chunkPlain("Only one paragraph.", "single.txt", time.Now())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Text != "Only one paragraph." {
		t.Errorf("text = %q", chunks[0].Text)
	}
}

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
		{".hidden.md", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isIndexable(tt.path)
			if result != tt.expected {
				t.Errorf("isIndexable(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestChunkFile_ExtensionRouting(t *testing.T) {
	now := time.Now()

	// .md files use markdown chunker
	chunks := chunkFile("# Heading\nContent", "test.md", ".md", now)
	if len(chunks) != 1 {
		t.Errorf("md file: expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Header != "# Heading" {
		t.Errorf("md file: header = %q", chunks[0].Header)
	}

	// .txt files use plain chunker
	chunks = chunkFile("Para 1.\n\nPara 2.", "test.txt", ".txt", now)
	if len(chunks) != 2 {
		t.Errorf("txt file: expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Header != "" {
		t.Errorf("txt file: header should be empty, got %q", chunks[0].Header)
	}

	// Unknown extensions use plain chunker
	chunks = chunkFile("Some content.", "test.xyz", ".xyz", now)
	if len(chunks) != 1 {
		t.Errorf("unknown ext: expected 1 chunk, got %d", len(chunks))
	}
}

func TestNew(t *testing.T) {
	idx := New("/vault", nil, nil, nil)
	if idx == nil {
		t.Fatal("New() returned nil")
	}
	if idx.vaultPath != "/vault" {
		t.Errorf("vaultPath = %q, want /vault", idx.vaultPath)
	}
}

func TestStats_Initial(t *testing.T) {
	idx := New("/vault", nil, nil, nil)
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

func TestIndexer_StatsAfterIndex(t *testing.T) {
	idx := New("/vault", nil, nil, nil)
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
