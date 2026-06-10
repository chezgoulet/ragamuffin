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
// First applies lexical checks (Clean + Prefix), then walks ancestor
// directories until an existing path is found and resolves symlinks on it.
// Without this, a writer creating a new file through a symlinked parent
// directory (e.g., /vault/link-to-outside/newfile.md where link-to-outside
// is a symlink) would silently bypass the escape check.
func safeVaultPath(vaultRoot, requestedPath string) (string, error) {
	cleanRoot := filepath.Clean(vaultRoot)
	cleanRequested := filepath.Clean(requestedPath)
	fullPath := filepath.Join(cleanRoot, cleanRequested)

	// Normalize to prevent prefix-match confusion on "/" edge case.
	// Use a local copy so cleanRoot stays clean for ancestor comparison.
	rootPrefix := cleanRoot
	if !strings.HasSuffix(rootPrefix, string(os.PathSeparator)) {
		rootPrefix += string(os.PathSeparator)
	}
	absTarget := filepath.Clean(fullPath) + string(os.PathSeparator)

	if !strings.HasPrefix(absTarget, rootPrefix) {
		return "", fmt.Errorf("requested path %q escapes vault root", requestedPath)
	}

	// Walk ancestors until we find a path that exists, then resolve symlinks.
	// This catches cases where a parent directory is a symlink pointing
	// outside the vault. For the leaf being created, we check as high as
	// vaultRoot if nothing in the path exists yet.
	checkPath := fullPath
	for checkPath != cleanRoot && checkPath != "." && checkPath != "/" {
		if _, err := os.Stat(checkPath); err == nil {
			break
		}
		checkPath = filepath.Dir(checkPath)
	}
	// If we walked all the way up and still nothing exists, stat the
	// vault root itself — it must exist.
	if _, err := os.Stat(checkPath); err != nil {
		return "", fmt.Errorf("vault root %q is not accessible: %w", vaultRoot, err)
	}

	resolved, err := filepath.EvalSymlinks(checkPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve vault path %q: %w", checkPath, err)
	}

	// Use rootPrefix for symlink escape check (trailing slash ensures
	// we don't match a sibling dir that happens to share a prefix).
	resolvedTarget := filepath.Clean(resolved) + string(os.PathSeparator)
	if !strings.HasPrefix(resolvedTarget, rootPrefix) {
		return "", fmt.Errorf("requested path %q escapes vault root via symlink at %q", requestedPath, checkPath)
	}

	return fullPath, nil
}
