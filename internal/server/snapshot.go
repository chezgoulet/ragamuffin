package server

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// handleSnapshot streams a gzipped tarball of the vault directory.
// Locks the watcher during snapshot to prevent concurrent file mutations.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	filename := fmt.Sprintf("vault-%s.tar.gz", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	// Lock the watcher so no events fire during the tarball walk.
	// Files can still change on disk, but the indexer won't race us.
	s.watcher.Lock()
	defer s.watcher.Unlock()

	gw, err := gzip.NewWriterLevel(w, gzip.DefaultCompression)
	if err != nil {
		s.log(r.Context()).Error("snapshot: create gzip writer", "error", err)
		writeError(w, 500, "INTERNAL", "failed to create gzip writer")
		return
	}
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	vaultRoot := filepath.Clean(s.cfg.VaultPath)

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
