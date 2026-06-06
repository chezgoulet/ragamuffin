// Package watcher detects file changes in the vault directory.
package watcher

import (
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


