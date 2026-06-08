package chunker

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chezgoulet/ragamuffin/internal/tokenutil"
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
		{"# Title", "# Title\nSome intro text."},
		{"## Section 1", "## Section 1\nContent for section 1."},
		{"### Subsection 1.1", "### Subsection 1.1\nDeeper content."},
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
		if chunks[i].FirstParagraph == "" {
			t.Errorf("chunk %d: FirstParagraph should not be empty", i)
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
	for i := 0; i < 10; i++ {
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
		if tokenutil.EstTokens(r.Text) > 400 {
			t.Errorf("chunk exceeds max tokens: %d > 400", tokenutil.EstTokens(r.Text))
		}
	}
}

func TestEnforceMaxTokens_SentenceSplit(t *testing.T) {
	// One giant paragraph with no paragraph breaks — must split on sentences
	var sentences []string
	for i := 0; i < 15; i++ {
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
		if tokenutil.EstTokens(r.Text) > 100 {
			t.Errorf("chunk exceeds max tokens: %d > 100", tokenutil.EstTokens(r.Text))
		}
	}
}

func TestEnforceMaxTokens_HardSplit(t *testing.T) {
	// One giant blob with no sentence breaks — must hard-split at token boundary
	words := strings.Repeat("word ", 100) // 100 words, ~130 tokens
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
		if tokenutil.EstTokens(r.Text) > 100 {
			t.Errorf("chunk exceeds max tokens: %d > 100", tokenutil.EstTokens(r.Text))
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
		if tokenutil.EstTokens(c.Text) > 200 {
			t.Errorf("chunk exceeds max: %d > 200", tokenutil.EstTokens(c.Text))
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
		got := tokenutil.EstTokens(tt.text)
		if got != tt.expected {
			t.Errorf("tokenutil.EstTokens(%q) = %d, want %d", tt.text, got, tt.expected)
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

func TestExtractFirstParagraph(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "empty", text: "", want: ""},
		{name: "short_text", text: "hello world", want: "hello world"},
		{name: "double_newline", text: "first paragraph\n\nsecond paragraph", want: "first paragraph"},
		{name: "capped_at_200", text: strings.Repeat("a", 250), want: strings.Repeat("a", 200)},
		{name: "newline_before_cap", text: strings.Repeat("a", 100) + "\n\n" + strings.Repeat("b", 100), want: strings.Repeat("a", 100)},
		{name: "cap_beats_newline", text: strings.Repeat("a", 250) + "\n\n" + "trailing", want: strings.Repeat("a", 200)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFirstParagraph(tt.text)
			if got != tt.want {
				t.Errorf("extractFirstParagraph() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractFirstParagraph_UTF8Boundary(t *testing.T) {
	// A 3-byte UTF-8 character (U+121B, ማ) spanning the 200-byte truncation point.
	// 197 ASCII bytes + 3-byte char at byte 197-199 → truncation at 200 cuts mid-character.
	prefix := strings.Repeat("a", 197)
	multiByte := "ማ" // U+121B, 3 bytes in UTF-8: E1 88 9B
	text := prefix + multiByte + " trailing text"

	got := extractFirstParagraph(text)
	if !utf8.ValidString(got) {
		t.Errorf("extractFirstParagraph() produced invalid UTF-8: %q", got)
	}
	// Should truncate to valid UTF-8 by dropping the incomplete rune
	expected := prefix // 197 a's, dropped the split multi-byte character
	if got != expected {
		t.Errorf("extractFirstParagraph() = %q (len=%d), want %q (len=%d)",
			got, len(got), expected, len(expected))
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

func TestSanitizeUTF8(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "valid_ascii", input: "hello", want: "hello"},
		{name: "valid_utf8", input: "héllo", want: "héllo"},
		{name: "truncated_2byte", input: "a\xc3", want: "a"},               // Ã cut after first byte
		{name: "truncated_3byte", input: "a\xe1\x88", want: "a"},             // ማ cut after two bytes
		{name: "truncated_4byte", input: "a\xf0\x9f\x98", want: "a"},         // 😀 cut after three bytes
		{name: "mid_2byte", input: "ab\xc3world", want: "ab"},                // Ã cut mid-sequence
		{name: "empty", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUTF8(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeUTF8(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
