// Package chunker splits file contents into indexable chunks.
package chunker

import (
	"strings"
	"time"
)

// Chunk represents a single indexed section of a file.
type Chunk struct {
	Text       string
	SourceFile string
	Header     string
	ChunkIndex int
	UpdatedAt  time.Time
}

// Options configures chunking behavior.
type Options struct {
	MaxTokens int // 0 = unlimited
}

// ChunkFile splits file content into chunks based on extension and options.
func ChunkFile(content, sourcePath, ext string, modTime time.Time, opts Options) []Chunk {
	var chunks []Chunk
	switch ext {
	case ".md":
		chunks = chunkMarkdown(content, sourcePath, modTime)
	default:
		chunks = chunkPlain(content, sourcePath, modTime)
	}

	if opts.MaxTokens > 0 {
		var enforced []Chunk
		for _, c := range chunks {
			enforced = append(enforced, enforceMaxTokens(c, opts.MaxTokens)...)
		}
		return enforced
	}
	return chunks
}

func chunkMarkdown(content, sourcePath string, modTime time.Time) []Chunk {
	lines := strings.Split(content, "\n")
	var chunks []Chunk
	var current strings.Builder
	currentHeader := ""
	chunkIndex := 0

	flush := func() {
		text := strings.TrimSpace(current.String())
		if text != "" {
			chunks = append(chunks, Chunk{
				Text:       text,
				SourceFile: sourcePath,
				Header:     currentHeader,
				ChunkIndex: chunkIndex,
				UpdatedAt:  modTime,
			})
			chunkIndex++
		}
		current.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect headings (H1, H2, H3)
		if strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "# ") {
			flush()
			currentHeader = trimmed
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	flush()

	return chunks
}

func chunkPlain(content, sourcePath string, modTime time.Time) []Chunk {
	// Split on double newlines (paragraph boundaries)
	paragraphs := strings.Split(content, "\n\n")
	var chunks []Chunk

	for i, p := range paragraphs {
		text := strings.TrimSpace(p)
		if text == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Text:       text,
			SourceFile: sourcePath,
			Header:     "",
			ChunkIndex: i,
			UpdatedAt:  modTime,
		})
	}

	return chunks
}

// enforceMaxTokens splits a chunk that exceeds maxTokens.
// Splits at paragraph boundary first, then sentence, then hard split.
func enforceMaxTokens(c Chunk, maxTokens int) []Chunk {
	if estTokens(c.Text) <= maxTokens {
		return []Chunk{c}
	}

	// Try paragraph boundaries
	paras := strings.Split(c.Text, "\n\n")
	if len(paras) > 1 {
		var result []Chunk
		var current strings.Builder
		idx := c.ChunkIndex
		for _, p := range paras {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if estTokens(current.String())+estTokens(p) > maxTokens && current.Len() > 0 {
				result = append(result, Chunk{
					Text:       strings.TrimSpace(current.String()),
					SourceFile: c.SourceFile,
					Header:     c.Header,
					ChunkIndex: idx,
					UpdatedAt:  c.UpdatedAt,
				})
				idx++
				current.Reset()
			}
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(p)
		}
		if current.Len() > 0 {
			result = append(result, Chunk{
				Text:       strings.TrimSpace(current.String()),
				SourceFile: c.SourceFile,
				Header:     c.Header,
				ChunkIndex: idx,
				UpdatedAt:  c.UpdatedAt,
			})
		}
		if len(result) > 0 {
			return result
		}
	}

	// Try sentence boundaries
	sentences := splitSentences(c.Text)
	if len(sentences) > 1 {
		var result []Chunk
		var current strings.Builder
		idx := c.ChunkIndex
		for _, s := range sentences {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if estTokens(current.String())+estTokens(s) > maxTokens && current.Len() > 0 {
				result = append(result, Chunk{
					Text:       strings.TrimSpace(current.String()),
					SourceFile: c.SourceFile,
					Header:     c.Header,
					ChunkIndex: idx,
					UpdatedAt:  c.UpdatedAt,
				})
				idx++
				current.Reset()
			}
			if current.Len() > 0 {
				current.WriteString(" ")
			}
			current.WriteString(s)
		}
		if current.Len() > 0 {
			result = append(result, Chunk{
				Text:       strings.TrimSpace(current.String()),
				SourceFile: c.SourceFile,
				Header:     c.Header,
				ChunkIndex: idx,
				UpdatedAt:  c.UpdatedAt,
			})
		}
		if len(result) > 0 {
			return result
		}
	}

	// Hard split at token boundary
	words := strings.Fields(c.Text)
	targetWords := maxTokens * 10 / 13 // reverse of ×1.3
	var result []Chunk
	idx := c.ChunkIndex
	for i := 0; i < len(words); i += targetWords {
		end := i + targetWords
		if end > len(words) {
			end = len(words)
		}
		result = append(result, Chunk{
			Text:       strings.Join(words[i:end], " "),
			SourceFile: c.SourceFile,
			Header:     c.Header,
			ChunkIndex: idx,
			UpdatedAt:  c.UpdatedAt,
		})
		idx++
	}
	return result
}

// estTokens returns an approximate token count (words × 1.3).
func estTokens(text string) int {
	words := len(strings.Fields(text))
	return int(float64(words) * 1.3)
}

// splitSentences splits text on period+space boundaries.
func splitSentences(text string) []string {
	// Split on . followed by space or newline
	var result []string
	start := 0
	for i := 0; i < len(text)-1; i++ {
		if text[i] == '.' && (text[i+1] == ' ' || text[i+1] == '\n') {
			result = append(result, text[start:i+1])
			start = i + 2
			i++ // skip the space
		}
	}
	if start < len(text) {
		result = append(result, text[start:])
	}
	if len(result) == 0 {
		result = append(result, text)
	}
	return result
}
