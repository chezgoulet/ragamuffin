package qdrant

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the Qdrant gRPC client.
type Client struct {
	conn        *grpc.ClientConn
	points      pb.PointsClient
	collections pb.CollectionsClient
	collection  string
}

// grpcTarget strips http:// / https:// schemes from the URL.
// The Qdrant Go client uses gRPC which expects bare host:port.
func grpcTarget(url string) string {
	for _, prefix := range []string{"http://", "https://"} {
		if strings.HasPrefix(url, prefix) {
			return url[len(prefix):]
		}
	}
	return url
}

// New connects to Qdrant and ensures the collection exists.
func New(ctx context.Context, url, collection string, vectorSize uint64) (*Client, error) {
	target := grpcTarget(url)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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

// CreatePayloadIndex creates a payload field index on the specified collection.
// fieldType maps from string names ("keyword", "float", "bool") to pb.FieldType.
// The PointsClient service manages payload field indexes in this version of the API.
func (c *Client) CreatePayloadIndex(ctx context.Context, collection, field, fieldType string) error {
	var ft pb.FieldType
	switch fieldType {
	case "keyword":
		ft = pb.FieldType_FieldTypeKeyword
	case "integer":
		ft = pb.FieldType_FieldTypeInteger
	case "float":
		ft = pb.FieldType_FieldTypeFloat
	case "geo":
		ft = pb.FieldType_FieldTypeGeo
	case "text":
		ft = pb.FieldType_FieldTypeText
	case "bool":
		ft = pb.FieldType_FieldTypeBool
	case "datetime":
		ft = pb.FieldType_FieldTypeDatetime
	case "uuid":
		ft = pb.FieldType_FieldTypeUuid
	default:
		ft = pb.FieldType_FieldTypeKeyword
	}
	_, err := c.points.CreateFieldIndex(ctx, &pb.CreateFieldIndexCollection{
		CollectionName: collection,
		FieldName:      field,
		FieldType:      &ft,
	})
	return err
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
// source_filter uses Match_Text, which does substring matching, not strict prefix matching.
// A filter of "team/" will match "other-team/file.md". For exact directory prefix filtering,
// ensure your directory names are distinct enough that substring collisions don't occur.
// A proper prefix filter would require Qdrant payload index changes (Phase 2).
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

// CountFiles returns the number of distinct source_file values in the collection.
func (c *Client) CountFiles(ctx context.Context) (int, error) {
	seen := make(map[string]struct{})
	var offset *pb.PointId
	const pageSize uint32 = 200

	for {
		points, nextOffset, err := c.Scroll(ctx, pageSize, offset)
		if err != nil {
			return 0, fmt.Errorf("count files scroll: %w", err)
		}
		for _, p := range points {
			if v, ok := p.Payload["source_file"]; ok {
				if s, ok := v.GetKind().(*pb.Value_StringValue); ok {
					seen[s.StringValue] = struct{}{}
				}
			}
		}
		if nextOffset == nil {
			break
		}
		offset = nextOffset
	}

	return len(seen), nil
}

// Scroll returns a page of points from the collection, ordered by point ID.
// offset is the point ID to start from (nil for beginning). limit caps results.
func (c *Client) Scroll(ctx context.Context, limit uint32, offset *pb.PointId) ([]*pb.RetrievedPoint, *pb.PointId, error) {
	req := &pb.ScrollPoints{
		CollectionName: c.collection,
		WithPayload:    pb.NewWithPayload(true),
	}
	if limit > 0 {
		req.Limit = &limit
	}
	if offset != nil {
		req.Offset = offset
	}

	resp, err := c.points.Scroll(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	return resp.Result, resp.NextPageOffset, nil
}

// ScrollFiltered returns points matching a filter, paginated by string-based offset (UUID).
// Unlike Scroll, this uses a string offset for the pruner's pagination loop pattern.
func (c *Client) ScrollFiltered(ctx context.Context, filter *pb.Filter, limit uint32, offset string) ([]*pb.RetrievedPoint, error) {
	req := &pb.ScrollPoints{
		CollectionName: c.collection,
		WithPayload:    pb.NewWithPayload(true),
		WithVectors:    pb.NewWithVectors(true),
	}
	if filter != nil {
		req.Filter = filter
	}
	if limit > 0 {
		req.Limit = &limit
	}
	if offset != "" {
		req.Offset = &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: offset,
			},
		}
	}

	resp, err := c.points.Scroll(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
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

// Collection returns the collection name this client was created for.
func (c *Client) Collection() string {
	return c.collection
}
