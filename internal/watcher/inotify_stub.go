//go:build !linux

package watcher

import (
	"fmt"
	"log/slog"
)

func newInotifyWatcher(vaultPath string, logger *slog.Logger) (Watcher, error) {
	_, _ = vaultPath, logger
	return nil, fmt.Errorf("inotify not available on this platform")
}
