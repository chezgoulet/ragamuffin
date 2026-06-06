//go:build linux

package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chezgoulet/ragamuffin/internal/indexutil"
	"sync"
	"time"

	"log/slog"

	"golang.org/x/sys/unix"
)

const inotifyDebounce = 500 * time.Millisecond

// inotifyWatcher watches the vault using Linux inotify.
type inotifyWatcher struct {
	vaultPath string
	logger    *slog.Logger
	state     map[string]time.Time
	debounce  map[string]time.Time
	mu        sync.Mutex
}

func (w *inotifyWatcher) Lock()   { w.mu.Lock() }
func (w *inotifyWatcher) Unlock() { w.mu.Unlock() }

func newInotifyWatcher(vaultPath string, logger *slog.Logger) (Watcher, error) {
	// Test that we can create an inotify instance
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("inotify_init1: %w", err)
	}
	unix.Close(fd)

	return &inotifyWatcher{
		vaultPath: vaultPath,
		logger:    logger,
		state:     make(map[string]time.Time),
		debounce:  make(map[string]time.Time),
	}, nil
}

// Watch uses inotify to detect file changes. Falls back to scanning for initial state.
func (w *inotifyWatcher) Watch(events chan<- Event, done <-chan struct{}) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		w.logger.Error("inotify_init1 failed", "error", err)
		return
	}
	defer unix.Close(fd)

	// Initial scan to populate state
	w.fullScan(events, true)

	// Add watches recursively
	if err := w.addWatchRecursive(fd, w.vaultPath); err != nil {
		w.logger.Error("inotify: failed to add initial watches", "error", err)
	}

	// Poll inotify events
	buf := make([]byte, (unix.SizeofInotifyEvent+unix.NAME_MAX+1)*64)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			n, err := unix.Read(fd, buf)
			if err != nil {
				if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
					continue
				}
				w.logger.Error("inotify read error", "error", err)
				return
			}

			w.processEvents(buf[:n], events)
		}
	}
}

func (w *inotifyWatcher) processEvents(buf []byte, events chan<- Event) {
	var pending []Event

	func() {
		w.mu.Lock()
		defer w.mu.Unlock()

		for offset := 0; offset < len(buf); {
			if offset+unix.SizeofInotifyEvent > len(buf) {
				break
			}
			// Parse raw event
			wd := int32(buf[offset]) | int32(buf[offset+1])<<8 | int32(buf[offset+2])<<16 | int32(buf[offset+3])<<24
			mask := uint32(buf[offset+4]) | uint32(buf[offset+5])<<8 | uint32(buf[offset+6])<<16 | uint32(buf[offset+7])<<24
			_ = wd
			nameLen := uint32(buf[offset+12]) | uint32(buf[offset+13])<<8 | uint32(buf[offset+14])<<16 | uint32(buf[offset+15])<<24

			var name string
			if nameLen > 0 {
				nameStart := offset + unix.SizeofInotifyEvent
				nameEnd := nameStart + int(nameLen)
				if nameEnd <= len(buf) {
					name = strings.TrimRight(string(buf[nameStart:nameEnd]), "\x00")
				}
			}

			offset += unix.SizeofInotifyEvent + int(nameLen)

			_ = mask
			if name == "" {
				continue
			}

			// Debounce: coalesce rapid events on the same file
			now := time.Now()
			if last, ok := w.debounce[name]; ok && now.Sub(last) < inotifyDebounce {
				continue
			}
			w.debounce[name] = now

			absPath := filepath.Join(w.vaultPath, name)
			info, err := os.Stat(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					// File deleted
					w.logger.Info("file deleted", "path", name)
					pending = append(pending, Event{Path: name, Action: ActionDelete})
					delete(w.state, name)
				}
				continue
			}
			if info.IsDir() {
				continue
			}

			if !indexutil.IsIndexable(name) {
				continue
			}

			prev, existed := w.state[name]
			w.state[name] = info.ModTime()

			if !existed {
				w.logger.Info("file added", "path", name)
				pending = append(pending, Event{Path: name, AbsPath: absPath, Action: ActionAdd})
			} else if info.ModTime().After(prev) {
				w.logger.Info("file modified", "path", name)
				pending = append(pending, Event{Path: name, AbsPath: absPath, Action: ActionModify})
			}
		}
	}()

	// Send events outside the lock so the channel send doesn't block the mutex
	for _, ev := range pending {
		select {
		case events <- ev:
		default:
			w.logger.Warn("event dropped: channel full", "path", ev.Path)
		}
	}
}

func (w *inotifyWatcher) addWatchRecursive(fd int, dir string) error {
	wd, err := unix.InotifyAddWatch(fd, dir,
		unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|
			unix.IN_CLOSE_WRITE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
	if err != nil {
		if err == unix.ENOSPC {
			w.logger.Warn("inotify: watch limit reached, watch may be incomplete")
			return nil
		}
		return fmt.Errorf("inotify_add_watch %s: %w", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		unix.InotifyRmWatch(fd, uint32(wd))
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() {
			subdir := filepath.Join(dir, entry.Name())
			// Skip symlinks that point outside vault
			if info, err := os.Stat(subdir); err == nil {
				if resolved, err := filepath.EvalSymlinks(subdir); err == nil {
					if !strings.HasPrefix(resolved, w.vaultPath) {
						continue
					}
				}
				_ = info
			}
			w.addWatchRecursive(fd, subdir)
		}
	}
	return nil
}

func (w *inotifyWatcher) fullScan(events chan<- Event, initial bool) {
	currentFiles := make(map[string]time.Time)

	filepath.Walk(w.vaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(w.vaultPath, absPath)
		if !indexutil.IsIndexable(relPath) {
			return nil
		}
		currentFiles[relPath] = info.ModTime()
		return nil
	})

	w.mu.Lock()
	defer w.mu.Unlock()

	for path, mtime := range currentFiles {
		w.state[path] = mtime
	}
}
