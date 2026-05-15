package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeVaultPath resolves a path within the vault and verifies it doesn't
// escape via symlink or ../ traversal. Returns the absolute path.
//
// First applies lexical checks (Clean + Prefix), then resolves symlinks
// via EvalSymlinks when the path exists. A symlink pointing outside the
// vault root is rejected even if the lexical path appears safe.
func safeVaultPath(vaultRoot, requestedPath string) (string, error) {
	cleanRoot := filepath.Clean(vaultRoot)
	cleanRequested := filepath.Clean(requestedPath)
	fullPath := filepath.Join(cleanRoot, cleanRequested)

	// Normalize to prevent prefix-match confusion on "/" edge case
	if !strings.HasSuffix(cleanRoot, string(os.PathSeparator)) {
		cleanRoot += string(os.PathSeparator)
	}
	absTarget := filepath.Clean(fullPath) + string(os.PathSeparator)

	if !strings.HasPrefix(absTarget, cleanRoot) {
		return "", fmt.Errorf("requested path %q escapes vault root", requestedPath)
	}

	// Resolve symlinks for paths that exist on disk.
	// EvalSymlinks fails if the file doesn't exist yet — that's fine,
	// new file writes don't have a symlink to escape through.
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err == nil {
		resolvedTarget := filepath.Clean(resolved) + string(os.PathSeparator)
		if !strings.HasPrefix(resolvedTarget, cleanRoot) {
			return "", fmt.Errorf("requested path %q escapes vault root via symlink", requestedPath)
		}
	}

	return fullPath, nil
}
