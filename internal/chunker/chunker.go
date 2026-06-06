// Package chunker splits file contents into indexable chunks.
package chunker

import (
	"strings"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/tokenutil"
)

// Chunk represents a single indexed section of a file.
type Chunk struct {
	Text           string
	FirstParagraph string // text up to first \n\n or 200 chars
	SourceFile     string
	Header         string
	ChunkIndex     int
	UpdatedAt      time.Time
	LinksTo        []string // wikilinks [[path]] and markdown links [text](path) within this chunk
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

// extractFirstParagraph returns the first paragraph of text: up to the first
// double-newline or 200 characters, whichever is shorter.
func extractFirstParagraph(text string) string {
	// Find first double-newline
	idx := strings.Index(text, "\n\n")
	if idx == -1 {
		// No double-newline; cap at 200 chars
		if len(text) > 200 {
			return text[:200]
		}
		return text
	}
	// Found double-newline; cap at 200 chars
	if idx > 200 {
		return text[:200]
	}
	return text[:idx]
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
				Text:           text,
				FirstParagraph: extractFirstParagraph(text),
				SourceFile:     sourcePath,
				Header:         currentHeader,
				ChunkIndex:     chunkIndex,
				UpdatedAt:      modTime,
				LinksTo:        extractLinks(text),
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
	chunkIndex := 0

	for _, p := range paragraphs {
		text := strings.TrimSpace(p)
		if text == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Text:           text,
			FirstParagraph: extractFirstParagraph(text),
			SourceFile:     sourcePath,
			Header:         "",
			ChunkIndex:     chunkIndex,
			UpdatedAt:      modTime,
		})
		chunkIndex++
	}

	return chunks
}

// enforceMaxTokens splits a chunk that exceeds maxTokens.
// maxTokens <= 0 means no enforcement (return chunk as-is).
func enforceMaxTokens(c Chunk, maxTokens int) []Chunk {
	if maxTokens <= 0 || tokenutil.EstTokens(c.Text) <= maxTokens {
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
			if tokenutil.EstTokens(current.String())+tokenutil.EstTokens(p) > maxTokens && current.Len() > 0 {
				chunkText := strings.TrimSpace(current.String())
				result = append(result, Chunk{
					Text:           chunkText,
					FirstParagraph: extractFirstParagraph(chunkText),
					SourceFile:     c.SourceFile,
					Header:         c.Header,
					ChunkIndex:     idx,
					UpdatedAt:      c.UpdatedAt,
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
			chunkText := strings.TrimSpace(current.String())
			result = append(result, Chunk{
				Text:           chunkText,
				FirstParagraph: extractFirstParagraph(chunkText),
				SourceFile:     c.SourceFile,
				Header:         c.Header,
				ChunkIndex:     idx,
				UpdatedAt:      c.UpdatedAt,
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
			if tokenutil.EstTokens(current.String())+tokenutil.EstTokens(s) > maxTokens && current.Len() > 0 {
				chunkText := strings.TrimSpace(current.String())
				result = append(result, Chunk{
					Text:           chunkText,
					FirstParagraph: extractFirstParagraph(chunkText),
					SourceFile:     c.SourceFile,
					Header:         c.Header,
					ChunkIndex:     idx,
					UpdatedAt:      c.UpdatedAt,
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
			chunkText := strings.TrimSpace(current.String())
			result = append(result, Chunk{
				Text:           chunkText,
				FirstParagraph: extractFirstParagraph(chunkText),
				SourceFile:     c.SourceFile,
				Header:         c.Header,
				ChunkIndex:     idx,
				UpdatedAt:      c.UpdatedAt,
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
		chunkText := strings.Join(words[i:end], " ")
		result = append(result, Chunk{
			Text:           chunkText,
			FirstParagraph: extractFirstParagraph(chunkText),
			SourceFile:     c.SourceFile,
			Header:         c.Header,
			ChunkIndex:     idx,
			UpdatedAt:      c.UpdatedAt,
		})
		idx++
	}
	return result
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

// extractLinks parses wikilinks and local markdown links from text.
// Returns deduplicated, cleaned link targets.
// External URLs (http://, https://) are excluded.
func extractLinks(text string) []string {
	seen := make(map[string]struct{})
	var links []string

	// Parse wikilinks: [[path/to/file]]
	runes := []rune(text)
	for i := 0; i < len(runes)-1; i++ {
		if runes[i] == '[' && i+1 < len(runes) && runes[i+1] == '[' {
			// Find the closing ]]
			end := i + 2
			for end < len(runes)-1 && !(runes[end] == ']' && runes[end+1] == ']') {
				end++
			}
			if end+1 < len(runes) && runes[end] == ']' && runes[end+1] == ']' {
				content := string(runes[i+2 : end])
				// Support aliases: [[target|display]]
				if pipeIdx := strings.Index(content, "|"); pipeIdx >= 0 {
					content = content[:pipeIdx]
				}
				// Support anchors: [[path/to/file#section]]
				if hashIdx := strings.Index(content, "#"); hashIdx >= 0 {
					content = content[:hashIdx]
				}
				content = strings.TrimSpace(content)
				if content != "" && !isExternalURL(content) {
					if _, ok := seen[content]; !ok {
						seen[content] = struct{}{}
						links = append(links, content)
					}
				}
				i = end + 1
			}
		}
	}

	// Parse markdown links: [text](path) where path is not external
	for i := 0; i < len(runes)-1; i++ {
		if runes[i] == '[' {
			// Find the closing bracket
			closeBracket := i + 1
			for closeBracket < len(runes) && runes[closeBracket] != ']' {
				closeBracket++
			}
			if closeBracket+1 < len(runes) && runes[closeBracket+1] == '(' {
				// Find the closing paren
				closeParen := closeBracket + 2
				for closeParen < len(runes) && runes[closeParen] != ')' {
					closeParen++
				}
				if closeParen < len(runes) && runes[closeParen] == ')' {
					url := string(runes[closeBracket+2 : closeParen])
					url = strings.TrimSpace(url)
					if url != "" && !isExternalURL(url) {
						// Strip anchor from local links
						if hashIdx := strings.Index(url, "#"); hashIdx >= 0 {
							url = url[:hashIdx]
						}
						if _, ok := seen[url]; !ok {
							seen[url] = struct{}{}
							links = append(links, url)
						}
					}
					i = closeParen
				}
			}
		}
	}

	return links
}

// isExternalURL checks if a URL is external (http:// or https://).
func isExternalURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}
