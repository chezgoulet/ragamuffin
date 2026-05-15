package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/chunker"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
	pb "github.com/qdrant/go-client/qdrant"
)

// Chunk is an alias for chunker.Chunk (backward compat).
type Chunk = chunker.Chunk

// Indexer processes file events and maintains the Qdrant index.
type Indexer struct {
	vaultPath     string
	qdrant        *qdrant.Client
	embedder      *embedding.Client
	logger        *slog.Logger
	chunkMaxTokens int

	mu        sync.RWMutex
	fileCount int
	chunkCount int
	lastIndexed time.Time
	indexing   bool
	progressPct int
	totalFiles  int
}

// New creates an Indexer.
func New(vaultPath string, qc *qdrant.Client, ec *embedding.Client, logger *slog.Logger) *Indexer {
	return &Indexer{
		vaultPath: vaultPath,
		qdrant:    qc,
		embedder:  ec,
		logger:    logger,
	}
}

// SetChunkMaxTokens configures the maximum tokens per chunk (0 = unlimited).
func (idx *Indexer) SetChunkMaxTokens(n int) {
	idx.chunkMaxTokens = n
}

// Stats returns current indexing statistics.
func (idx *Indexer) Stats() (fileCount, chunkCount int, lastIndexed time.Time, indexing bool, progressPct, totalFiles int) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.fileCount, idx.chunkCount, idx.lastIndexed, idx.indexing, idx.progressPct, idx.totalFiles
}

// ProcessEvents runs the indexing loop, handling file events from the watcher.
func (idx *Indexer) ProcessEvents(ctx context.Context, events <-chan watcher.Event, initialDone chan<- struct{}) {
	// Check if Qdrant is empty — if so, do a full re-index
	count, err := idx.qdrant.Count(ctx)
	if err != nil {
		idx.logger.Error("indexer: failed to check qdrant count", "error", err)
	} else if count == 0 {
		idx.logger.Info("indexer: empty collection, starting full re-index")
		idx.fullReindex(ctx)
	}

	// Signal that initial indexing is done
	close(initialDone)

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			switch evt.Action {
			case watcher.ActionAdd, watcher.ActionModify:
				if err := idx.indexFile(ctx, evt.AbsPath, evt.Path); err != nil {
					idx.logger.Error("indexer: failed to index file", "path", evt.Path, "error", err)
				}
			case watcher.ActionDelete:
				if err := idx.qdrant.DeleteBySource(ctx, evt.Path); err != nil {
					idx.logger.Error("indexer: failed to delete chunks", "path", evt.Path, "error", err)
				}
			}
		}
	}
}

func (idx *Indexer) fullReindex(ctx context.Context) {
	idx.mu.Lock()
	idx.indexing = true
	idx.progressPct = 0
	idx.mu.Unlock()

	var files []string
	filepath.Walk(idx.vaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(idx.vaultPath, absPath)
		if !isIndexable(relPath) {
			return nil
		}
		files = append(files, relPath)
		return nil
	})

	total := len(files)
	idx.mu.Lock()
	idx.totalFiles = total
	idx.mu.Unlock()

	if total == 0 {
		idx.logger.Debug("indexer: vault is empty, skipping reindex")
		return
	}

	for i, relPath := range files {
		absPath := filepath.Join(idx.vaultPath, relPath)
		if err := idx.indexFile(ctx, absPath, relPath); err != nil {
			idx.logger.Error("indexer: re-index failed", "path", relPath, "error", err)
		}
		idx.mu.Lock()
		idx.progressPct = (i + 1) * 100 / total
		idx.mu.Unlock()
	}

	idx.mu.Lock()
	idx.indexing = false
	idx.lastIndexed = time.Now()
	idx.mu.Unlock()

	idx.logger.Info("indexer: full re-index complete", "files", total)
}

func (idx *Indexer) indexFile(ctx context.Context, absPath, relPath string) error {
	if idx.embedder == nil {
		return nil // no embedding client — skip indexing
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read file %s: %w", relPath, err)
	}

	stat, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	modTime := stat.ModTime()

	// Delete old chunks before re-indexing
	if err := idx.qdrant.DeleteBySource(ctx, relPath); err != nil {
		idx.logger.Warn("indexer: failed to delete old chunks", "path", relPath, "error", err)
	}

	chunks := chunker.ChunkFile(string(content), relPath, filepath.Ext(relPath), modTime,
		chunker.Options{MaxTokens: idx.chunkMaxTokens})
	if len(chunks) == 0 {
		return nil
	}

	// Generate embeddings in batches
	batchSize := 20
	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]

		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Text
		}

		vectors, err := idx.embedder.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed batch: %w", err)
		}

		points := make([]*pb.PointStruct, len(batch))
		for j, c := range batch {
			id := fmt.Sprintf("%s:%d", relPath, c.ChunkIndex)
			points[j] = &pb.PointStruct{
				Id: pb.NewIDUUID(id),
				Vectors: &pb.Vectors{
					VectorsOptions: &pb.Vectors_Vector{
						Vector: &pb.Vector{
							Data: vectors[j],
						},
					},
				},
				Payload: map[string]*pb.Value{
					"text":              {Kind: &pb.Value_StringValue{StringValue: c.Text}},
					"source_file":       {Kind: &pb.Value_StringValue{StringValue: c.SourceFile}},
					"header":            {Kind: &pb.Value_StringValue{StringValue: c.Header}},
					"chunk_index":       {Kind: &pb.Value_IntegerValue{IntegerValue: int64(c.ChunkIndex)}},
					"file_last_updated": {Kind: &pb.Value_StringValue{StringValue: c.UpdatedAt.Format(time.RFC3339)}},
				},
			}
		}

		if err := idx.qdrant.Upsert(ctx, points); err != nil {
			return fmt.Errorf("upsert batch: %w", err)
		}
	}

	idx.mu.Lock()
	idx.fileCount++
	idx.chunkCount += len(chunks)
	idx.lastIndexed = time.Now()
	idx.mu.Unlock()

	return nil
}

func isIndexable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt", ".org", ".rst":
		return true
	case "":
		return true
	default:
		return false
	}
}
