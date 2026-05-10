package qdrant

import (
	"context"
	"fmt"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the Qdrant gRPC client.
type Client struct {
	conn       *grpc.ClientConn
	points     pb.PointsClient
	collections pb.CollectionsClient
	collection string
}

// New connects to Qdrant and ensures the collection exists.
func New(ctx context.Context, url, collection string, vectorSize uint64) (*Client, error) {
	conn, err := grpc.NewClient(url,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("qdrant connect: %w", err)
	}

	c := &Client{
		conn:        conn,
		points:      pb.NewPointsClient(conn),
		collections: pb.NewCollectionsClient(conn),
		collection:  collection,
	}

	// Ensure collection exists
	if err := c.ensureCollection(ctx, vectorSize); err != nil {
		conn.Close()
		return nil, fmt.Errorf("qdrant ensure collection: %w", err)
	}

	return c, nil
}

func (c *Client) ensureCollection(ctx context.Context, vectorSize uint64) error {
	// Check if collection exists
	list, err := c.collections.List(ctx, &pb.ListCollectionsRequest{})
	if err != nil {
		return err
	}
	for _, col := range list.Collections {
		if col.Name == c.collection {
			return nil // already exists
		}
	}

	// Create it
	_, err = c.collections.Create(ctx, &pb.CreateCollection{
		CollectionName: c.collection,
		VectorsConfig: &pb.VectorsConfig{
			Config: &pb.VectorsConfig_Params{
				Params: &pb.VectorParams{
					Size:     vectorSize,
					Distance: pb.Distance_Cosine,
				},
			},
		},
	})
	return err
}

// Upsert inserts or updates points in the collection.
func (c *Client) Upsert(ctx context.Context, points []*pb.PointStruct) error {
	_, err := c.points.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: c.collection,
		Points:         points,
	})
	return err
}

// Search performs a vector similarity search.
func (c *Client) Search(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string) ([]*pb.ScoredPoint, error) {
	req := &pb.SearchPoints{
		CollectionName: c.collection,
		Vector:         vector,
		Limit:          limit,
		ScoreThreshold: &scoreThreshold,
		WithPayload:    pb.NewWithPayload(true),
	}

	if sourceFilter != "" {
		req.Filter = &pb.Filter{
			Must: []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "source_file",
							Match: &pb.Match{
								MatchValue: &pb.Match_Text{
									Text: sourceFilter,
								},
							},
						},
					},
				},
			},
		}
	}

	resp, err := c.points.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// DeleteBySource removes all chunks for a given source file.
func (c *Client) DeleteBySource(ctx context.Context, sourceFile string) error {
	_, err := c.points.Delete(ctx, &pb.DeletePoints{
		CollectionName: c.collection,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{
				Filter: &pb.Filter{
					Must: []*pb.Condition{
						{
							ConditionOneOf: &pb.Condition_Field{
								Field: &pb.FieldCondition{
									Key: "source_file",
									Match: &pb.Match{
										MatchValue: &pb.Match_Keyword{
											Keyword: sourceFile,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	return err
}

// Count returns the number of points in the collection.
func (c *Client) Count(ctx context.Context) (uint64, error) {
	resp, err := c.points.Count(ctx, &pb.CountPoints{
		CollectionName: c.collection,
	})
	if err != nil {
		return 0, err
	}
	return resp.Result.Count, nil
}

// DistinctSources returns the count of distinct source files.
func (c *Client) DistinctSources(ctx context.Context) (uint64, error) {
	// Qdrant doesn't have a direct distinct count. Use scroll to estimate.
	// For stats purposes, we count all points and track files separately.
	resp, err := c.points.Scroll(ctx, &pb.ScrollPoints{
		CollectionName: c.collection,
		Limit:         nil,
	})
	if err != nil {
		return 0, err
	}

	seen := make(map[string]bool)
	for _, p := range resp.Result {
		if src, ok := p.Payload["source_file"]; ok {
			if s, ok := src.Kind.(*pb.Value_StringValue); ok {
				seen[s.StringValue] = true
			}
		}
	}
	return uint64(len(seen)), nil
}

// Health checks if Qdrant is reachable.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.collections.List(ctx, &pb.ListCollectionsRequest{})
	return err
}

// Close shuts down the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
