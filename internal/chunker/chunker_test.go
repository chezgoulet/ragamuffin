package chunker

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
	chunks := ChunkFile(content, "test.md", ".md", time.Now(), Options{})

	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}

	expected := []struct {
		header string
		body   string
	}{
		{"# Title", "# Title\nSome intro text.\n\n"},
		{"## Section 1", "## Section 1\nContent for section 1.\n\n"},
		{"### Subsection 1.1", "### Subsection 1.1\nDeeper content.\n\n"},
		{"## Section 2", "## Section 2\nFinal section text."},
	}

	for i, exp := range expected {
		if chunks[i].Header != exp.header {
			t.Errorf("chunk %d: header = %q, want %q", i, chunks[i].Header, exp.header)
		}
		if !strings.Contains(chunks[i].Text, exp.body) {
			t.Errorf("chunk %d: body = %q, want to contain %q", i, chunks[i].Text, exp.body)
		}
		if chunks[i].SourceFile != "test.md" {
			t.Errorf("chunk %d: source = %q, want test.md", i, chunks[i].SourceFile)
		}
	}

	t.Run("chunk indices are sequential", func(t *testing.T) {
		for i, c := range chunks {
			if c.ChunkIndex != i {
				t.Errorf("chunk %d: ChunkIndex = %d", i, c.ChunkIndex)
			}
		}
	})
}

func TestChunkPlain_Paragraphs(t *testing.T) {
	content := `First paragraph.

Second paragraph.

Third paragraph.`

	chunks := ChunkFile(content, "notes.txt", ".txt", time.Now(), Options{})

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	for i, c := range chunks {
		if c.Header != "" {
			t.Errorf("plain chunk %d should have no header, got %q", i, c.Header)
		}
		if c.ChunkIndex != i {
			t.Errorf("chunk %d: ChunkIndex = %d", i, c.ChunkIndex)
		}
	}
}

func TestChunkPlain_NoExtension(t *testing.T) {
	content := "Single paragraph, no extension."
	chunks := ChunkFile(content, "README", "", time.Now(), Options{})

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Text != "Single paragraph, no extension." {
		t.Errorf("text = %q", chunks[0].Text)
	}
}

func TestChunkPlain_Empty(t *testing.T) {
	chunks := ChunkFile("", "empty.md", ".md", time.Now(), Options{})
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func TestEnforceMaxTokens_NoSplit(t *testing.T) {
	c := Chunk{
		Text:       "small chunk",
		SourceFile: "test.md",
		Header:     "## Intro",
		ChunkIndex: 0,
	}
	result := enforceMaxTokens(c, 2000)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
	if result[0].Header != "## Intro" {
		t.Errorf("header = %q", result[0].Header)
	}
}

func TestEnforceMaxTokens_ParagraphSplit(t *testing.T) {
	// Build a chunk with many paragraphs that together exceed the token limit
	var paragraphs []string
	for i := 0; i < 20; i++ {
		paragraphs = append(paragraphs, strings.Repeat("word ", 50)) // ~50 words per paragraph
	}
	c := Chunk{
		Text:       strings.Join(paragraphs, "\n\n"),
		SourceFile: "big.md",
		Header:     "## Big Section",
		ChunkIndex: 5,
	}
	// estTokens per paragraph: 50 * 1.3 = 65. Total: 20 * 65 = 1300.
	// With max 400 tokens, we expect ~3-4 chunks
	result := enforceMaxTokens(c, 400)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks from split, got %d", len(result))
	}
	for _, r := range result {
		if r.SourceFile != "big.md" {
			t.Errorf("source = %q", r.SourceFile)
		}
		if r.Header != "## Big Section" {
			t.Errorf("header = %q", r.Header)
		}
		if estTokens(r.Text) > 400 {
			t.Errorf("chunk exceeds max tokens: %d > 400", estTokens(r.Text))
		}
	}
}

func TestEnforceMaxTokens_SentenceSplit(t *testing.T) {
	// One giant paragraph with no paragraph breaks — must split on sentences
	var sentences []string
	for i := 0; i < 30; i++ {
		sentences = append(sentences, "This is sentence number something with many words in it.")
	}
	c := Chunk{
		Text:       strings.Join(sentences, " "),
		SourceFile: "wall.md",
		Header:     "## Wall of Text",
		ChunkIndex: 0,
	}
	result := enforceMaxTokens(c, 100)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks from sentence split, got %d", len(result))
	}
	for _, r := range result {
		if estTokens(r.Text) > 100 {
			t.Errorf("chunk exceeds max tokens: %d > 100", estTokens(r.Text))
		}
	}
}

func TestEnforceMaxTokens_HardSplit(t *testing.T) {
	// One giant blob with no sentence breaks — must hard-split at token boundary
	words := strings.Repeat("word ", 200) // 200 words, ~260 tokens
	c := Chunk{
		Text:       words,
		SourceFile: "log.txt",
		Header:     "",
		ChunkIndex: 0,
	}
	result := enforceMaxTokens(c, 100)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks from hard split, got %d", len(result))
	}
	for _, r := range result {
		if estTokens(r.Text) > 100 {
			t.Errorf("chunk exceeds max tokens: %d > 100", estTokens(r.Text))
		}
	}
}

func TestEnforceMaxTokens_Unlimited(t *testing.T) {
	// MaxTokens = 0 means no enforcement
	c := Chunk{
		Text:       strings.Repeat("big ", 500),
		SourceFile: "big.md",
		ChunkIndex: 0,
	}
	result := enforceMaxTokens(c, 0)
	if len(result) != 1 {
		t.Errorf("unlimited should not split, got %d chunks", len(result))
	}
}

func TestChunkFile_WithMaxTokens(t *testing.T) {
	// Integration: chunk a markdown file with token limit
	content := strings.Repeat("# Section\n"+strings.Repeat("word ", 60)+"\n\n", 6)
	chunks := ChunkFile(content, "big.md", ".md", time.Now(), Options{MaxTokens: 200})

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	for _, c := range chunks {
		if estTokens(c.Text) > 200 {
			t.Errorf("chunk exceeds max: %d > 200", estTokens(c.Text))
		}
		if c.SourceFile != "big.md" {
			t.Errorf("source = %q", c.SourceFile)
		}
	}
}

func TestEstTokens(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"one two three", 3},  // 3 words × 1.3 = 3.9 → 3
		{"word", 1},           // 1 word × 1.3 = 1.3 → 1
		{strings.Repeat("a ", 10), 13}, // 10 words × 1.3 = 13
	}
	for _, tt := range tests {
		got := estTokens(tt.text)
		if got != tt.expected {
			t.Errorf("estTokens(%q) = %d, want %d", tt.text, got, tt.expected)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	text := "First sentence. Second sentence. Third sentence."
	result := splitSentences(text)
	if len(result) != 3 {
		t.Fatalf("expected 3 sentences, got %d: %v", len(result), result)
	}
	if result[0] != "First sentence." {
		t.Errorf("first = %q", result[0])
	}
	if result[2] != "Third sentence." {
		t.Errorf("third = %q", result[2])
	}
}

func TestSplitSentences_NoPeriod(t *testing.T) {
	text := "No punctuation here"
	result := splitSentences(text)
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
	if result[0] != text {
		t.Errorf("got %q", result[0])
	}
}
