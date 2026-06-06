package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/indexutil"
)

// pollWatcher polls the vault directory for file changes.
type pollWatcher struct {
	vaultPath string
	interval  time.Duration
	logger    *slog.Logger
	state     map[string]time.Time // path → last seen mtime
	mu        sync.Mutex
}

func (w *pollWatcher) Lock()   { w.mu.Lock() }
func (w *pollWatcher) Unlock() { w.mu.Unlock() }

func newPollWatcher(vaultPath string, interval time.Duration, logger *slog.Logger) *pollWatcher {
	return &pollWatcher{
		vaultPath: vaultPath,
		interval:  interval,
		logger:    logger,
		state:     make(map[string]time.Time),
	}
}

// Watch polls the vault directory and sends events to the channel.
// Blocks until done is closed.
func (w *pollWatcher) Watch(events chan<- Event, done <-chan struct{}) {
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

func (w *pollWatcher) scan(events chan<- Event, initial bool) {
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

		if !indexutil.IsIndexable(relPath) {
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
