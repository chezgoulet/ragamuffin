package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// isIndexable returns true if the file extension is supported.
func isIndexable(path string) bool {
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

// Watcher polls a directory for file changes.
type Watcher struct {
	vaultPath string
	interval  time.Duration
	logger    *slog.Logger
	state     map[string]time.Time // path → last seen mtime
	mu        sync.Mutex
}

// New creates a new Watcher.
func New(vaultPath string, interval time.Duration, logger *slog.Logger) *Watcher {
	return &Watcher{
		vaultPath: vaultPath,
		interval:  interval,
		logger:    logger,
		state:     make(map[string]time.Time),
	}
}

// Watch polls the vault directory and sends events to the channel.
// Blocks until the context is cancelled.
func (w *Watcher) Watch(events chan<- Event, done <-chan struct{}) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Initial scan on startup
	w.scan(events, true)

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			w.scan(events, false)
		}
	}
}

func (w *Watcher) scan(events chan<- Event, initial bool) {
	currentFiles := make(map[string]time.Time)

	err := filepath.Walk(w.vaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil {
			w.logger.Warn("watcher walk error", "path", absPath, "error", err)
			return nil
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(w.vaultPath, absPath)
		if err != nil {
			return nil
		}

		if !isIndexable(relPath) {
			return nil
		}

		currentFiles[relPath] = info.ModTime()
		return nil
	})

	if err != nil {
		w.logger.Error("watcher walk failed", "error", err)
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Detect added and modified files
	for path, mtime := range currentFiles {
		prev, exists := w.state[path]
		if !exists {
			if !initial {
				w.logger.Info("file added", "path", path)
				events <- Event{Path: path, AbsPath: filepath.Join(w.vaultPath, path), Action: ActionAdd}
			}
		} else if mtime.After(prev) {
			w.logger.Info("file modified", "path", path)
			events <- Event{Path: path, AbsPath: filepath.Join(w.vaultPath, path), Action: ActionModify}
		}
	}

	// Detect deleted files
	for path := range w.state {
		if _, exists := currentFiles[path]; !exists {
			w.logger.Info("file deleted", "path", path)
			events <- Event{Path: path, Action: ActionDelete}
		}
	}

	w.state = currentFiles
}
