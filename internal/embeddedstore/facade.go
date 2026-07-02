// Package embeddedstore provides a pure-Go, in-process implementation of the
// qdrant.FactStore interface. It uses modernc.org/sqlite (no CGo) for storage
// and brute-force cosine similarity for vector search.
//
// The target use case is a single-player / small deployment that does not
// want to run a Qdrant container. It is not a replacement for Qdrant at
// production scale — the linear scan in Search is O(n) over the points in
// the collection.
//
// Like the qdrant.Client, every collection is independent. The
// collection name is a free-form string; the store creates a SQLite table
// per collection on first write.
package embeddedstore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	pb "github.com/qdrant/go-client/qdrant"
)

// Compile-time check: *Store satisfies qdrant.FactStore.
var _ qdrant.FactStore = (*Store)(nil)

// Store is an embedded vector store. Construct it with Open(cfg).
type Store struct {
	conn       *sqlDB
	collection string // primary collection (used by Collection() and many methods)
	logger     *slog.Logger
}

// Config is what callers pass to Open. Path is the SQLite file path; an
// empty path uses a temporary file. Collection is required. VectorSize is
// the dimension of vectors stored in this collection; a value of 0 means
// "infer on first write" (Search will then require the caller to send a
// vector of the dimension used in the first write).
type Config struct {
	Path       string
	Collection string
	VectorSize uint64
	Logger     *slog.Logger
}

// Open creates a new embedded store. It opens the underlying SQLite database
// and initialises the primary collection metadata.
func Open(cfg Config) (*Store, error) {
	if cfg.Collection == "" {
		return nil, fmt.Errorf("embeddedstore: collection name is required")
	}
	conn, err := openDB(cfg.Path)
	if err != nil {
		return nil, err
	}
	s := &Store{
		conn:       conn,
		collection: cfg.Collection,
		logger:     cfg.Logger,
	}
	if err := s.ensureCollection(context.Background(), cfg.Collection, cfg.VectorSize); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// Collection returns the primary collection name for this client.
// Mirrors qdrant.Client.Collection().
func (s *Store) Collection() string { return s.collection }

// Close releases the underlying SQLite handle.
func (s *Store) Close() error {
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// GetVectorSize returns the stored vector dimension for collection.
// Returns 0 if the collection has not been initialised.
func (s *Store) GetVectorSize(ctx context.Context, collectionName string) (uint64, error) {
	if collectionName == "" {
		collectionName = s.collection
	}
	return s.conn.getVectorSize(ctx, collectionName)
}

// SetPayload is not yet implemented for the embedded store. The current
// R4 contract test path does not exercise it, and production callers
// (pruner ReembedScan, audit SetPayload) remain on Qdrant. Returning an
// error keeps the door open for a future SQLite-backed implementation
// without silent partial behaviour.
func (s *Store) SetPayload(ctx context.Context, collection string, points []*pb.PointId, payload map[string]*pb.Value) error {
	return s.applyToCollection(collection, func(c string) error {
		return s.conn.setPayload(ctx, c, points, payload)
	})
}

// UpdateVectors is not yet implemented for the embedded store.
func (s *Store) UpdateVectors(ctx context.Context, collection string, points []*pb.PointVectors) error {
	return s.applyToCollection(collection, func(c string) error {
		return s.conn.updateVectors(ctx, c, points)
	})
}

// applyToCollection resolves the target collection name (defaulting to
// the primary collection) and runs fn with the resolved name.
func (s *Store) applyToCollection(collection string, fn func(string) error) error {
	if collection == "" {
		collection = s.collection
	}
	return fn(collection)
}
