package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// handleSnapshot streams a gzipped tarball of the vault directory.
// Best-effort consistency: files may change during the walk.
// Skips the .ragamuffin/ directory (operational metadata, not vault content).
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	filename := fmt.Sprintf("vault-%s.tar.gz", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	gw, err := gzip.NewWriterLevel(w, gzip.DefaultCompression)
	if err != nil {
		s.log(r.Context()).Error("snapshot: create gzip writer", "error", err)
		writeError(w, 500, "INTERNAL", "failed to create gzip writer")
		return
	}
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	vaultRoot := filepath.Clean(s.vaultPathFromContext(r.Context()))

	err = filepath.WalkDir(vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// If a file was deleted between the walk start and read, skip it.
			s.log(r.Context()).Warn("snapshot: walk error, skipping", "path", path, "error", err)
			return nil
		}

		// Skip the .ragamuffin directory — operational data, not vault content
		if d.IsDir() && d.Name() == ".ragamuffin" {
			return filepath.SkipDir
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Compute the archive path relative to vault root
		relPath, err := filepath.Rel(vaultRoot, path)
		if err != nil {
			return nil
		}
		if relPath == "." {
			return nil // skip vault root itself
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		header.Name = relPath
		header.Format = tar.FormatPAX // UTF-8 filenames

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("write header: %w", err)
		}

		if !info.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				s.log(r.Context()).Warn("snapshot: open file, skipping", "path", path, "error", err)
				return nil
			}

			written, err := io.Copy(tw, f)
			if err != nil {
				f.Close()
				return fmt.Errorf("copy %s: %w", relPath, err)
			}
			f.Close()

			// Verify we wrote the correct number of bytes
			if written != info.Size() {
				s.log(r.Context()).Warn("snapshot: size mismatch", "path", path, "expected", info.Size(), "wrote", written)
			}
		}

		return nil
	})

	if err != nil {
		s.log(r.Context()).Error("snapshot: walk failed", "error", err)
		// We've already started writing the gzip stream — can't send error headers.
		// Best we can do is log and truncate.
		return
	}
}
