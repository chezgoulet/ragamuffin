package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeVaultPath resolves a relative path within the vault and verifies it
// doesn't escape via symlink or ../ traversal. Returns the absolute path
// to the resolved file/directory.
//
// Handles the edge case where vaultRoot is "/" (root filesystem):
// filepath.Clean("/") returns "/", and adding the separator gives "//"
// which still works with HasPrefix but is cleaned here for correctness.
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
	return fullPath, nil
}
