// Package qdrant provides a gRPC client for Qdrant vector database.
//
// The FactStore interface enables mock-based testing of consumers
// (pruner, server, indexer) without a live Qdrant instance.
package qdrant

import (
	"context"

	pb "github.com/qdrant/go-client/qdrant"
)

// FactStore is the persistence interface for vector-stored facts and chunks.
// Every method on the concrete Client satisfies this interface.
//
// Long-term goal: extract this into a dedicated domain package so the
// protobuf types don't leak into the interface. For now, the qdrant go-client
// protobufs are stable enough to depend on.
type FactStore interface {
	// Upsert writes one or more points (facts or chunks) to the collection.
	Upsert(ctx context.Context, points []*pb.PointStruct) error

	// Scroll returns a page of points ordered by ID, with cursor pagination.
	Scroll(ctx context.Context, limit uint32, offset *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error)

	// ScrollFiltered returns points from the given collection matching a filter.
	ScrollFiltered(ctx context.Context, collection string, filter *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error)

	// Search performs vector similarity search with an optional source filter.
	// scoreThreshold filters results below the given similarity score.
	Search(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string, filter *pb.Filter) ([]*pb.ScoredPoint, error)

	// DeleteBySource removes all points whose source_file matches.
	DeleteBySource(ctx context.Context, sourceFile string) error

	// DeleteFiltered removes points from the given collection matching a filter.
	DeleteFiltered(ctx context.Context, collection string, filter *pb.Filter) error

	// Count returns the total number of points in the collection.
	Count(ctx context.Context) (uint64, error)

	// CountFiles returns the number of unique source_file values.
	CountFiles(ctx context.Context) (int, error)

	// CreatePayloadIndex ensures an index exists on a payload field.
	CreatePayloadIndex(ctx context.Context, collection, field, fieldType string) error

	// Health checks connectivity by listing collections.
	Health(ctx context.Context) error

	// Close shuts down the underlying gRPC connection.
	Close() error

	// GetVectorSize probes the vector dimension of a named collection.
	// Returns 0 if the collection does not exist.
	GetVectorSize(ctx context.Context, collectionName string) (uint64, error)

	// GetPoints returns points by their IDs from the given collection.
	GetPoints(ctx context.Context, collection string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error)

	// SetPayload sets specific payload keys on existing points without
	// affecting other payload fields. Uses Qdrant's SetPayload gRPC API
	// for field-level partial updates.
	SetPayload(ctx context.Context, collection string, points []*pb.PointId, payload map[string]*pb.Value) error

	// UpdateVectors updates the vector data on existing points without
	// affecting their payload fields.
	UpdateVectors(ctx context.Context, collection string, points []*pb.PointVectors) error

	// Collection returns the primary collection name this client targets.
	Collection() string

	// ScrollWithVectors returns a page of points INCLUDING their vectors.
	// Use for export/backup or embedding projection. (#788, #809)
	ScrollWithVectors(ctx context.Context, limit uint32, offset *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error)
}

// Compile-time check: *Client satisfies FactStore.
var _ FactStore = (*Client)(nil)
