package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chezgoulet/ragamuffin/internal/indexer"
)

// RestoreConsistencyCheck compares indexed file counts against the filesystem
// per vault. If more than `threshold` of files are missing or the count delta
// exceeds threshold, the vault is flagged for re-indexing.
//
// Returns:
//   - snapshotRestore: true if any vault exceeds the mismatch threshold
//   - affected: list of vault names that need re-indexing
//   - err: any error encountered during the check
func (s *Server) RestoreConsistencyCheck(ctx context.Context, threshold float64) (snapshotRestore bool, affected []string, err error) {
	s.indexers.ForEach(func(name string, idx *indexer.Indexer) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		fc, _, _, _, _, _ := idx.Stats()
		if fc == 0 {
			// Empty index — no consistency issue (first run)
			return
		}

		// Count actual files on disk
		diskFiles, err := countFilesInVault(idx.VaultPath())
		if err != nil {
			s.logger.Warn("restore check: count files", "vault", name, "error", err)
			return
		}

		// If the disk has far fewer files than the index, something is off
		if diskFiles == 0 {
			// No files on disk but index has entries — likely a restore
			affected = append(affected, name)
			s.logger.Warn("restore check: vault has zero files on disk but index has entries",
				"vault", name, "indexed_files", fc)
			return
		}

		delta := float64(fc-diskFiles) / float64(fc)
		if delta > threshold {
			affected = append(affected, name)
			s.logger.Warn("restore check: vault file count mismatch exceeds threshold",
				"vault", name,
				"indexed_files", fc,
				"disk_files", diskFiles,
				"delta_pct", fmt.Sprintf("%.1f%%", delta*100),
				"threshold", fmt.Sprintf("%.1f%%", threshold*100),
			)
		}
	})

	return len(affected) > 0, affected, nil
}

// countFilesInVault returns the number of files in a vault path, recursively.
func countFilesInVault(vaultPath string) (int, error) {
	var count int
	err := filepath.WalkDir(vaultPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden directories and the .ragamuffin metadata dir
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip hidden files
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		count++
		return nil
	})
	return count, err
}
