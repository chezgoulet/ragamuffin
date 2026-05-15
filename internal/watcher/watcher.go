// Package watcher detects file changes in the vault directory.
package watcher

import (
	"path/filepath"
	"strings"
	"time"

	"log/slog"
)

// Event represents a file change.
type Event struct {
	Path    string // relative to vault root
	AbsPath string // absolute path
	Action  Action
}

// Action is the type of file change.
type Action int

const (
	ActionAdd    Action = iota
	ActionModify
	ActionDelete
)

func (a Action) String() string {
	switch a {
	case ActionAdd:
		return "add"
	case ActionModify:
		return "modify"
	case ActionDelete:
		return "delete"
	default:
		return "unknown"
	}
}

// Watcher detects file changes in the vault directory.
type Watcher interface {
	Watch(events chan<- Event, done <-chan struct{})

	// Lock/Unlock prevent the watcher from processing events during
	// snapshot or other read-consistent operations.
	Lock()
	Unlock()
}

// New creates a new Watcher based on the configured mode.
// "poll" (default) works everywhere. "inotify" requires Linux.
func New(vaultPath string, interval time.Duration, logger *slog.Logger, mode string) Watcher {
	switch mode {
	case "inotify":
		w, err := newInotifyWatcher(vaultPath, logger)
		if err != nil {
			logger.Warn("inotify unavailable, falling back to polling", "error", err)
			return newPollWatcher(vaultPath, interval, logger)
		}
		logger.Info("watcher: using inotify")
		return w
	default:
		logger.Info("watcher: using polling", "interval", interval)
		return newPollWatcher(vaultPath, interval, logger)
	}
}

// isIndexable returns true if the file extension is supported.
func isIndexable(path string) bool {
	// Skip dot-directories (.git, .github) — never useful retrieval targets
	if strings.Contains(path, "/.") || strings.HasPrefix(path, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt", ".org", ".rst":
		return true
	case "":
		return true // no extension = treat as text
	default:
		return false
	}
}
