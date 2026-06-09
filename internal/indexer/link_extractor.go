package indexer

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Link represents a single extracted structural link.
type Link struct {
	Target   string // resolved vault path
	RawText  string // the original [[wikilink]] or path mention
	Context  string // first 200 chars of surrounding paragraph
	LinkType string // "wikilink" | "path_ref" | "tag_cluster"
}

var (
	// wikilinkRe matches [[Target]] and [[Target|Display Text]].
	wikilinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]+))?\]\]`)
)

// ExtractLinks parses raw text for structural links.
// sourcePath is the vault-relative path of the file being indexed.
// knownPaths is the list of all vault-relative paths (for path_ref matching).
// knownTags is the set of tag sets from previously indexed files (for tag_cluster).
// Returns all discovered links.
func ExtractLinks(rawText string, sourcePath string, knownPaths []string, knownTags map[string][]string) []Link {
	var links []Link

	// ── 1. Wikilinks ──────────────────────────────────────────────────
	matches := wikilinkRe.FindAllStringSubmatchIndex(rawText, -1)
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		raw := rawText[m[0]:m[1]] // the full [[...]] match
		target := rawText[m[2]:m[3]]
		if target == "" {
			continue
		}

		// Try to resolve to a vault path: strip extension variants
		resolved := resolveWikilink(target, knownPaths)
		if resolved == "" {
			continue
		}

		context := extractContext(rawText, m[0], m[1], 200)

		links = append(links, Link{
			Target:   resolved,
			RawText:  raw,
			Context:  context,
			LinkType: "wikilink",
		})
	}

	// ── 2. Path references ────────────────────────────────────────────
	// Check known paths that appear as substrings in the raw text.
	// Only match clean path references (preceded by whitespace, quote, or paren).
	for _, p := range knownPaths {
		if p == sourcePath {
			continue // don't self-reference
		}
		// Look for the path in the text as a distinct reference
		if strings.Contains(rawText, p) {
			// Verify it's a clean reference (not part of another word)
			idx := strings.Index(rawText, p)
			if idx > 0 {
				prev := rawText[idx-1]
				if prev != ' ' && prev != '\t' && prev != '\n' && prev != '"' && prev != '\'' && prev != '(' && prev != '`' {
					continue
				}
			}
			end := idx + len(p)
			if end < len(rawText) {
				next := rawText[end]
				if next != ' ' && next != '\t' && next != '\n' && next != '"' && next != '\'' && next != ')' && next != '.' && next != ',' && next != '`' && next != '}' && next != '>' {
					continue
				}
			}
			context := extractContext(rawText, idx, idx+len(p), 200)
			links = append(links, Link{
				Target:   p,
				RawText:  p,
				Context:  context,
				LinkType: "path_ref",
			})
		}
	}

	// ── 3. Tag clusters ───────────────────────────────────────────────
	// Compare current file's tags against known tag sets from other files.
	// This is done externally (caller provides knownTags with tags for other files)
	// and matched here.
	// knownTags is keyed by file path, with value being the file's tag list.
	// The caller extracts file tags from frontmatter separately.

	return links
}

// resolveWikilink converts a [[wikilink target]] to a known vault path.
// Handles:
//   - File name matches (e.g., "manifesto" matches "docs/manifesto.md")
//   - Path matches (e.g., "docs/manifesto.md" matches itself)
//   - Display text variants (called before pipe is stripped — target is what's before |)
func resolveWikilink(target string, knownPaths []string) string {
	if target == "" {
		return ""
	}

	// Direct match
	for _, p := range knownPaths {
		if p == target {
			return p
		}
	}

	// Try with .md extension appended
	targetWithExt := target
	if !strings.HasSuffix(target, ".md") {
		targetWithExt = target + ".md"
	}
	for _, p := range knownPaths {
		if p == targetWithExt {
			return p
		}
	}

	// Try matching basename (no dir, no ext)
	base := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	for _, p := range knownPaths {
		pBase := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if strings.EqualFold(pBase, base) || strings.EqualFold(pBase, strings.ReplaceAll(base, "-", " ")) || strings.EqualFold(pBase, strings.ReplaceAll(base, "_", " ")) {
			return p
		}
	}

	// Try matching relative path segments
	targetClean := filepath.ToSlash(target)
	for _, p := range knownPaths {
		if strings.HasSuffix(p, targetClean) || strings.HasSuffix(p, "/"+targetClean) {
			return p
		}
	}

	// Try matching by DisplayName: check last path segment before ext
	targetSeg := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	targetSeg = strings.ReplaceAll(targetSeg, "-", " ")
	targetSeg = strings.ReplaceAll(targetSeg, "_", " ")
	for _, p := range knownPaths {
		pSeg := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		pSeg = strings.ReplaceAll(pSeg, "-", " ")
		pSeg = strings.ReplaceAll(pSeg, "_", " ")
		if strings.EqualFold(pSeg, targetSeg) {
			return p
		}
	}

	return ""
}

// extractContext returns the paragraph surrounding the given range,
// capped at maxChars. Tries to find sentence/paragraph boundaries.
func extractContext(text string, start, end, maxChars int) string {
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	if start > end {
		start = end
	}

	// Expand to surrounding paragraph (between double newlines)
	paraStart := start
	for paraStart > 0 {
		if paraStart >= 2 && text[paraStart-2:paraStart] == "\n\n" {
			break
		}
		paraStart--
	}

	paraEnd := end
	for paraEnd < len(text) {
		if paraEnd+2 <= len(text) && text[paraEnd:paraEnd+2] == "\n\n" {
			break
		}
		paraEnd++
	}

	context := text[paraStart:paraEnd]
	context = strings.TrimSpace(context)

	if len(context) > maxChars {
		// Try to truncate at a sentence boundary
		truncated := context[:maxChars]
		if lastPeriod := strings.LastIndex(truncated, "."); lastPeriod > maxChars/2 {
			truncated = truncated[:lastPeriod+1]
		}
		context = truncated
	}

	return context
}
