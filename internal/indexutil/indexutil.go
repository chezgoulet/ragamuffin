// Package indexutil provides shared file-indexing utilities
// used by both the indexer and watcher packages.
package indexutil

import (
	"path/filepath"
	"strings"
)

// IsIndexable reports whether a relative vault path should be
// indexed based on its extension and directory prefix.
//
// Dot-directories (.git, .github, .ragamuffin) are skipped.
// Recognized extensions: .md, .txt, .org, .rst.
// No extension is also treated as indexable (plain text files).
func IsIndexable(path string) bool {
	// Skip dot-directories — never useful retrieval targets
	if strings.Contains(path, "/.") || strings.HasPrefix(path, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt", ".org", ".rst":
		return true
	case "":
		return true
	default:
		return false
	}
}
