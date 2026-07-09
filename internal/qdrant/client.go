package qdrant

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

// NewReconnecting wraps New but enters a reconnection loop on failure.
// It retries with exponential backoff (1s, 5s, 30s, 60s then gives up) and
// never calls os.Exit — the caller handles degraded mode instead.
func NewReconnecting(ctx context.Context, url, collection string, vectorSize uint64, logger *slog.Logger) (*Client, error) {
	backoff := []time.Duration{1 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}
	var lastErr error
	for i := 0; ; i++ {
		c, err := New(ctx, url, collection, vectorSize)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if i >= len(backoff)-1 {
			break
		}
		logger.Warn("qdrant reconnecting",
			"attempt", i+1, "backoff", backoff[i], "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff[i]):
		}
	}
	return nil, fmt.Errorf("qdrant connection failed after all retries: %w", lastErr)
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
			// Collection exists — verify vector dimension matches config
			existingSize, err := c.GetVectorSize(ctx, c.collection)
			if err != nil {
				return fmt.Errorf("check vector size for %s: %w", c.collection, err)
			}
			if existingSize != vectorSize {
				// Dimension mismatch — delete and recreate with correct size
				if err := c.deleteCollection(ctx); err != nil {
					return fmt.Errorf("recreate %s: delete failed: %w", c.collection, err)
				}
				break // fall through to create below
			}
			return nil // dimension matches
		}
	}

	// Create collection
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

func (c *Client) deleteCollection(ctx context.Context) error {
	_, err := c.collections.Delete(ctx, &pb.DeleteCollection{
		CollectionName: c.collection,
	})
	return err
}

// Upsert inserts or updates points in the collection.
// Sets wait=true so Qdrant validates the operation synchronously before returning.
// Without wait, Qdrant returns "acknowledged" immediately and silently drops points
// with mismatched vector dimensions, causing data loss (#630).
func (c *Client) Upsert(ctx context.Context, points []*pb.PointStruct) error {
	wait := true
	_, err := c.points.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: c.collection,
		Points:         points,
		Wait:           &wait,
	})
	return err
}

// Search performs a vector similarity search.
// source_filter uses Match_Keyword for exact prefix matching.
func (c *Client) Search(ctx context.Context, vector []float32, limit uint64, scoreThreshold float32, sourceFilter string, filter *pb.Filter) ([]*pb.ScoredPoint, error) {
	req := &pb.SearchPoints{
		CollectionName: c.collection,
		Vector:         vector,
		Limit:          limit,
		ScoreThreshold: &scoreThreshold,
		WithPayload:    pb.NewWithPayload(true),
	}

	switch {
	case filter != nil:
		req.Filter = filter
	case sourceFilter != "":
		req.Filter = &pb.Filter{
			Must: []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "source_file",
							Match: &pb.Match{
								MatchValue: &pb.Match_Keyword{
									Keyword: sourceFilter,
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

// ScrollOrdered returns points from the given collection ordered by a payload
// field via Qdrant's order_by scroll. Used by the facts freshness check (#795)
// to fetch the single most-recently-written fact in O(1) regardless of volume.
func (c *Client) ScrollOrdered(ctx context.Context, req *pb.ScrollPoints) ([]*pb.RetrievedPoint, error) {
	resp, err := c.points.Scroll(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// GetPoints returns points by their IDs from the given collection.
// Excludes vectors since we only need payload fields.
func (c *Client) GetPoints(ctx context.Context, collection string, ids []*pb.PointId) ([]*pb.RetrievedPoint, error) {
	req := &pb.GetPoints{
		CollectionName: collection,
		Ids:            ids,
		WithVectors:    &pb.WithVectorsSelector{SelectorOptions: &pb.WithVectorsSelector_Enable{Enable: false}},
	}

	resp, err := c.points.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("get points: %w", err)
	}
	return resp.GetResult(), nil
}

// Health checks if Qdrant is reachable.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.collections.List(ctx, &pb.ListCollectionsRequest{})
	return err
}

// SetPayload sets specific payload keys on existing points without affecting
// other payload fields. Uses Qdrant's SetPayload gRPC API for field-level
// partial updates — unlike Upsert, this only touches the specified keys.
func (c *Client) SetPayload(ctx context.Context, collection string, points []*pb.PointId, payload map[string]*pb.Value) error {
	req := &pb.SetPayloadPoints{
		CollectionName: collection,
		PointsSelector: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Points{
				Points: &pb.PointsIdsList{
					Ids: points,
				},
			},
		},
		Payload: payload,
	}
	_, err := c.points.SetPayload(ctx, req)
	return err
}

// UpdateVectors updates the vector data on existing points without affecting
// their payload fields. Uses Qdrant's UpdatePointVectors gRPC API.
func (c *Client) UpdateVectors(ctx context.Context, collection string, points []*pb.PointVectors) error {
	req := &pb.UpdatePointVectors{
		CollectionName: collection,
		Points:         points,
	}
	_, err := c.points.UpdateVectors(ctx, req)
	return err
}

// Close shuts down the gRPC connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Collection returns the collection name this client was created for.
func (c *Client) Collection() string {
	return c.collection
}

// GetVectorSize probes the vector dimension of a named collection by calling
// DescribeCollection via the gRPC CollectionsClient. Returns 0 if the
// collection does not exist or the size cannot be determined.
func (c *Client) GetVectorSize(ctx context.Context, collectionName string) (uint64, error) {
	resp, err := c.collections.Get(ctx, &pb.GetCollectionInfoRequest{
		CollectionName: collectionName,
	})
	if err != nil {
		return 0, fmt.Errorf("get collection info: %w", err)
	}

	// Navigate to the vector config params size
	if resp == nil || resp.Result == nil {
		return 0, fmt.Errorf("empty collection info response")
	}

	// Try the primary vectors config
	if cfg := resp.Result.GetConfig(); cfg != nil {
		if params := cfg.GetParams(); params != nil {
			if vectors := params.GetVectorsConfig(); vectors != nil {
				if params := vectors.GetParams(); params != nil {
					return params.Size, nil
				}
			}
		}
	}

	return 0, fmt.Errorf("could not determine vector size from collection info")
}

// ProbeQdrantCollection is a standalone helper that connects to Qdrant,
// describes a collection by name, and returns its vector dimension. This
// is useful at startup to auto-detect embedding dimensions instead of
// relying on hardcoded values or env overrides.
func ProbeQdrantCollection(qdrantURL, collectionName string) (uint64, error) {
	target := grpcTarget(qdrantURL)

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return 0, fmt.Errorf("probe: connect: %w", err)
	}
	defer conn.Close()

	collections := pb.NewCollectionsClient(conn)
	ctx := context.Background()

	resp, err := collections.Get(ctx, &pb.GetCollectionInfoRequest{
		CollectionName: collectionName,
	})
	if err != nil {
		return 0, fmt.Errorf("probe: get collection info: %w", err)
	}

	if resp == nil || resp.Result == nil {
		return 0, fmt.Errorf("probe: empty response")
	}

	if cfg := resp.Result.GetConfig(); cfg != nil {
		if params := cfg.GetParams(); params != nil {
			if vectors := params.GetVectorsConfig(); vectors != nil {
				if params := vectors.GetParams(); params != nil {
					return params.Size, nil
				}
			}
		}
	}

	return 0, fmt.Errorf("probe: could not determine vector size")
}
