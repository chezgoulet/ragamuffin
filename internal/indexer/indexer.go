package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/chunker"
	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/indexutil"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
	pb "github.com/qdrant/go-client/qdrant"
)

// Chunk is an alias for chunker.Chunk (backward compat).
type Chunk = chunker.Chunk

// FileEventCallback is called after a file is indexed or deleted.
// action is "created", "modified", or "deleted".
type FileEventCallback func(action, path string)

// Indexer processes file events and maintains the Qdrant index.
type Indexer struct {
	vaultPath      string
	vaultName      string
	qdrant         qdrant.FactStore
	embedder       embedding.Embedder
	logger         *slog.Logger
	chunkMaxTokens int

	mu          sync.RWMutex
	fileCount   int
	chunkCount  int
	lastIndexed time.Time
	indexing    bool
	progressPct int
	totalFiles  int
	knownFiles  map[string]struct{} // set of indexed files for dedup counting

	reindexCh chan struct{} // buffered channel (cap 1) for re-index requests

	// Optional callback for file change events
	onFileEvent FileEventCallback

	// Link index enrichment
	linkWriter logstore.LinkIndexWriter
	knownPaths []string // cached vault-relative paths for path_ref matching
}

// New creates an Indexer.
func New(vaultPath, vaultName string, qc qdrant.FactStore, ec embedding.Embedder, logger *slog.Logger) *Indexer {
	return &Indexer{
		vaultPath:  vaultPath,
		vaultName:  vaultName,
		qdrant:     qc,
		embedder:   ec,
		logger:     logger,
		knownFiles: make(map[string]struct{}),
		reindexCh:  make(chan struct{}, 1),
	}
}

// SetLinkWriter injects the link index writer for link persistence.
// Links are enrichment, not primary data — writer can be nil.
func (idx *Indexer) SetLinkWriter(w logstore.LinkIndexWriter) {
	idx.linkWriter = w
}

// VaultName returns the logical vault name this indexer belongs to.
func (idx *Indexer) VaultName() string {
	return idx.vaultName
}

// OnFileEvent registers a callback for file change events.
func (idx *Indexer) OnFileEvent(cb FileEventCallback) {
	idx.onFileEvent = cb
}

// VaultPath returns the filesystem path this indexer watches.
func (idx *Indexer) VaultPath() string {
	return idx.vaultPath
}

// SetChunkMaxTokens configures the maximum tokens per chunk (0 = unlimited).
func (idx *Indexer) SetChunkMaxTokens(n int) {
	idx.chunkMaxTokens = n
}

// Reindex triggers a full re-index. Returns false if a reindex is already queued.
func (idx *Indexer) Reindex() bool {
	select {
	case idx.reindexCh <- struct{}{}:
		return true
	default:
		return false // already queued or in progress
	}
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
	} else {
		// Qdrant already has data — sync file count from existing points
		fc, err := idx.qdrant.CountFiles(ctx)
		if err == nil {
			idx.mu.Lock()
			idx.fileCount = fc
			idx.lastIndexed = time.Now()
			idx.mu.Unlock()
			idx.logger.Info("indexer: synced file count from qdrant", "files", fc)
		}
	}

	// Signal that initial indexing is done
	close(initialDone)

	for {
		select {
		case <-ctx.Done():
			return
		case <-idx.reindexCh:
			idx.logger.Info("indexer: re-index triggered via API")
			idx.fullReindex(ctx)
		case evt, ok := <-events:
			if !ok {
				return
			}
			switch evt.Action {
			case watcher.ActionAdd, watcher.ActionModify:
				action := "modified"
				if evt.Action == watcher.ActionAdd {
					action = "created"
				}
				if err := idx.indexFile(ctx, evt.AbsPath, evt.Path); err != nil {
					idx.logger.Error("indexer: failed to index file", "path", evt.Path, "error", err)
				} else if idx.onFileEvent != nil {
					idx.onFileEvent(action, evt.Path)
				}
			case watcher.ActionDelete:
				if err := idx.qdrant.DeleteBySource(ctx, evt.Path); err != nil {
					idx.logger.Error("indexer: failed to delete chunks", "path", evt.Path, "error", err)
				} else if idx.onFileEvent != nil {
					idx.onFileEvent("deleted", evt.Path)
				}
			}
		}
	}
}

func (idx *Indexer) fullReindex(ctx context.Context) {
	idx.mu.Lock()
	idx.indexing = true
	idx.progressPct = 0
	idx.knownFiles = make(map[string]struct{})
	idx.chunkCount = 0
	idx.fileCount = 0
	idx.mu.Unlock()

	var files []string
	filepath.Walk(idx.vaultPath, func(absPath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(idx.vaultPath, absPath)
		if !indexutil.IsIndexable(relPath) {
			return nil
		}
		files = append(files, relPath)
		return nil
	})

	// Build known paths cache for link extraction
	idx.mu.Lock()
	idx.knownPaths = make([]string, len(files))
	copy(idx.knownPaths, files)
	idx.mu.Unlock()

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

	// Delete stale links for this source before extracting fresh ones
	if idx.linkWriter != nil {
		if err := idx.linkWriter.DeleteLinksBySource(ctx, relPath, idx.vaultName); err != nil {
			idx.logger.Warn("indexer: failed to delete stale links", "path", relPath, "error", err)
		}
	}

	// Extract structural links (wikilinks, path refs) from raw text
	rawText := string(content)
	idx.mu.RLock()
	kp := idx.knownPaths
	idx.mu.RUnlock()
	var extractedLinks []logstore.LinkRecord
	if idx.linkWriter != nil {
		links := ExtractLinks(rawText, relPath, kp, nil)
		if len(links) > 0 {
			extractedLinks = make([]logstore.LinkRecord, len(links))
			for i, l := range links {
				extractedLinks[i] = logstore.LinkRecord{
					SourcePath: relPath,
					TargetPath: l.Target,
					LinkType:   l.LinkType,
					Context:    l.Context,
				}
			}
		}
	}

	chunks := chunker.ChunkFile(rawText, relPath, filepath.Ext(relPath), modTime,
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
		if len(vectors) != len(batch) {
			return fmt.Errorf("embed batch: got %d vectors for %d texts", len(vectors), len(batch))
		}

		points := make([]*pb.PointStruct, len(batch))
		for j, c := range batch {
			// Deterministic UUID from file path + chunk index
			id := pointID(relPath, c.ChunkIndex)
			linksToValues := make([]*pb.Value, len(c.LinksTo))
			for li, link := range c.LinksTo {
				linksToValues[li] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: link}}
			}

			points[j] = &pb.PointStruct{
				Id: id,
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
					"links_to":          {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: linksToValues}}},
				},
			}
		}

		if err := idx.qdrant.Upsert(ctx, points); err != nil {
			return fmt.Errorf("upsert batch: %w", err)
		}
	}

	// Write extracted links after chunk upsert succeeds
	if idx.linkWriter != nil && len(extractedLinks) > 0 {
		if err := idx.linkWriter.WriteLinks(ctx, idx.vaultName, extractedLinks); err != nil {
			idx.logger.Warn("indexer: failed to write links", "path", relPath, "error", err)
		}
	}

	idx.mu.Lock()
	if _, seen := idx.knownFiles[relPath]; !seen {
		idx.knownFiles[relPath] = struct{}{}
		idx.fileCount++
	}
	idx.chunkCount += len(chunks)
	idx.lastIndexed = time.Now()
	// Update knownPaths on incremental index — non-fatal if stale
	if idx.linkWriter != nil {
		found := false
		for _, p := range idx.knownPaths {
			if p == relPath {
				found = true
				break
			}
		}
		if !found {
			idx.knownPaths = append(idx.knownPaths, relPath)
		}
	}
	idx.mu.Unlock()

	return nil
}

// Ingest directly indexes text content into Qdrant without going through
// the file watcher. Used for agent session persistence and direct memory storage.
// source should be a unique identifier for the ingested content (e.g., "session/2025-06-17").
// tags are optional metadata labels (e.g., ["session-log", "agent::dev"]).
// meta is an optional map of key-value pairs merged into the Qdrant payload (e.g., {"turn_index": "42"}).
func (idx *Indexer) Ingest(ctx context.Context, content, source string, tags []string, meta map[string]string) error {
	if idx.embedder == nil {
		return fmt.Errorf("cannot ingest: embedding client not configured")
	}
	if content == "" {
		return fmt.Errorf("cannot ingest: empty content")
	}

	chunks := chunker.ChunkFile(content, source, source, time.Now(),
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
		if len(vectors) != len(batch) {
			return fmt.Errorf("embed batch: got %d vectors for %d texts", len(vectors), len(batch))
		}

		points := make([]*pb.PointStruct, len(batch))
		for j, c := range batch {
			id := pointID(source, c.ChunkIndex)

			// Tags payload
			tagValues := make([]*pb.Value, len(tags))
			for ti, tag := range tags {
				tagValues[ti] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: tag}}
			}

			// Links to values
			linksToValues := make([]*pb.Value, len(c.LinksTo))
			for li, link := range c.LinksTo {
				linksToValues[li] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: link}}
			}

			payload := map[string]*pb.Value{
				"text":              {Kind: &pb.Value_StringValue{StringValue: c.Text}},
				"first_paragraph":   {Kind: &pb.Value_StringValue{StringValue: c.FirstParagraph}},
				"source_file":       {Kind: &pb.Value_StringValue{StringValue: c.SourceFile}},
				"header":            {Kind: &pb.Value_StringValue{StringValue: c.Header}},
				"chunk_index":       {Kind: &pb.Value_IntegerValue{IntegerValue: int64(c.ChunkIndex)}},
				"file_last_updated": {Kind: &pb.Value_StringValue{StringValue: c.UpdatedAt.Format(time.RFC3339)}},
				"links_to":          {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: linksToValues}}},
			}

			// Add tags if present
			if len(tags) > 0 {
				payload["tags"] = &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: tagValues}}}
			}

			// Merge optional meta fields into payload (#525)
			for k, v := range meta {
				payload[k] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: v}}
			}

			points[j] = &pb.PointStruct{
				Id: id,
				Vectors: &pb.Vectors{
					VectorsOptions: &pb.Vectors_Vector{
						Vector: &pb.Vector{Data: vectors[j]},
					},
				},
				Payload: payload,
			}
		}

		if err := idx.qdrant.Upsert(ctx, points); err != nil {
			return fmt.Errorf("upsert batch: %w", err)
		}
	}

	idx.mu.Lock()
	idx.chunkCount += len(chunks)
	idx.lastIndexed = time.Now()
	idx.mu.Unlock()

	return nil
}

// pointID generates a deterministic UUID from a file path and chunk index.
// Uses SHA-256 (not SHA-1) for compatibility with Qdrant's UUID parser,
// producing a valid RFC 4122 UUID.
func pointID(relPath string, chunkIndex int) *pb.PointId {
	raw := fmt.Sprintf("%s:%d", relPath, chunkIndex)
	h := sha256.Sum256([]byte(raw))
	// Take first 16 bytes, set version 4 bits and RFC 4122 variant
	buf := h[:16]
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	s := hex.EncodeToString(buf)
	uuid := s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	return pb.NewIDUUID(uuid)
}
