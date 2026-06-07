// Package fileutil provides shared file-handling utilities used by the
// watcher and indexer packages.
package fileutil

import (
	"path/filepath"
	"strings"
)

var indexableExtensions = map[string]bool{
	".txt":  true,
	".md":   true,
	".json": true,
	".csv":  true,
	".xml":  true,
	".yaml": true,
	".yml":  true,
	".html": true,
	".htm":  true,
	".rst":  true,
	".adoc": true,
	".log":  true,
}

// IsIndexable returns true if the file path has an extension that Ragamuffin
// can index for knowledge retrieval. Hidden files (dotfiles) are excluded.
func IsIndexable(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return indexableExtensions[ext]
}
