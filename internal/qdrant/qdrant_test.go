package qdrant

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
)

// ── Mock PointsClient ────────────────────────────────────────────────────────

type mockPointsClient struct {
	upsertFn           func(ctx context.Context, in *pb.UpsertPoints, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error)
	deleteFn           func(ctx context.Context, in *pb.DeletePoints, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error)
	getFn              func(ctx context.Context, in *pb.GetPoints, opts ...grpc.CallOption) (*pb.GetResponse, error)
	scrollFn           func(ctx context.Context, in *pb.ScrollPoints, opts ...grpc.CallOption) (*pb.ScrollResponse, error)
	countFn            func(ctx context.Context, in *pb.CountPoints, opts ...grpc.CallOption) (*pb.CountResponse, error)
	searchFn           func(ctx context.Context, in *pb.SearchPoints, opts ...grpc.CallOption) (*pb.SearchResponse, error)
	setPayloadFn       func(ctx context.Context, in *pb.SetPayloadPoints, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error)
	createFieldIndexFn func(ctx context.Context, in *pb.CreateFieldIndexCollection, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error)
}

func (m *mockPointsClient) Upsert(ctx context.Context, in *pb.UpsertPoints, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, in, opts...)
	}
	return &pb.PointsOperationResponse{}, nil
}

func (m *mockPointsClient) Delete(ctx context.Context, in *pb.DeletePoints, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, in, opts...)
	}
	return &pb.PointsOperationResponse{}, nil
}

func (m *mockPointsClient) Get(ctx context.Context, in *pb.GetPoints, opts ...grpc.CallOption) (*pb.GetResponse, error) {
	if m.getFn != nil {
		return m.getFn(ctx, in, opts...)
	}
	return &pb.GetResponse{}, nil
}

func (m *mockPointsClient) Scroll(ctx context.Context, in *pb.ScrollPoints, opts ...grpc.CallOption) (*pb.ScrollResponse, error) {
	if m.scrollFn != nil {
		return m.scrollFn(ctx, in, opts...)
	}
	return &pb.ScrollResponse{}, nil
}

func (m *mockPointsClient) Count(ctx context.Context, in *pb.CountPoints, opts ...grpc.CallOption) (*pb.CountResponse, error) {
	if m.countFn != nil {
		return m.countFn(ctx, in, opts...)
	}
	return &pb.CountResponse{}, nil
}

func (m *mockPointsClient) Search(ctx context.Context, in *pb.SearchPoints, opts ...grpc.CallOption) (*pb.SearchResponse, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, in, opts...)
	}
	return &pb.SearchResponse{}, nil
}

func (m *mockPointsClient) SetPayload(ctx context.Context, in *pb.SetPayloadPoints, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	if m.setPayloadFn != nil {
		return m.setPayloadFn(ctx, in, opts...)
	}
	return &pb.PointsOperationResponse{}, nil
}

func (m *mockPointsClient) CreateFieldIndex(ctx context.Context, in *pb.CreateFieldIndexCollection, opts ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	if m.createFieldIndexFn != nil {
		return m.createFieldIndexFn(ctx, in, opts...)
	}
	return &pb.PointsOperationResponse{}, nil
}

// Unused methods — return empty defaults
func (m *mockPointsClient) UpdateVectors(_ context.Context, _ *pb.UpdatePointVectors, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	return &pb.PointsOperationResponse{}, nil
}
func (m *mockPointsClient) DeleteVectors(_ context.Context, _ *pb.DeletePointVectors, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	return &pb.PointsOperationResponse{}, nil
}
func (m *mockPointsClient) OverwritePayload(_ context.Context, _ *pb.SetPayloadPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	return &pb.PointsOperationResponse{}, nil
}
func (m *mockPointsClient) DeletePayload(_ context.Context, _ *pb.DeletePayloadPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	return &pb.PointsOperationResponse{}, nil
}
func (m *mockPointsClient) ClearPayload(_ context.Context, _ *pb.ClearPayloadPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	return &pb.PointsOperationResponse{}, nil
}
func (m *mockPointsClient) DeleteFieldIndex(_ context.Context, _ *pb.DeleteFieldIndexCollection, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	return &pb.PointsOperationResponse{}, nil
}
func (m *mockPointsClient) SearchBatch(_ context.Context, _ *pb.SearchBatchPoints, _ ...grpc.CallOption) (*pb.SearchBatchResponse, error) {
	return &pb.SearchBatchResponse{}, nil
}
func (m *mockPointsClient) SearchGroups(_ context.Context, _ *pb.SearchPointGroups, _ ...grpc.CallOption) (*pb.SearchGroupsResponse, error) {
	return &pb.SearchGroupsResponse{}, nil
}
func (m *mockPointsClient) Recommend(_ context.Context, _ *pb.RecommendPoints, _ ...grpc.CallOption) (*pb.RecommendResponse, error) {
	return &pb.RecommendResponse{}, nil
}
func (m *mockPointsClient) RecommendBatch(_ context.Context, _ *pb.RecommendBatchPoints, _ ...grpc.CallOption) (*pb.RecommendBatchResponse, error) {
	return &pb.RecommendBatchResponse{}, nil
}
func (m *mockPointsClient) RecommendGroups(_ context.Context, _ *pb.RecommendPointGroups, _ ...grpc.CallOption) (*pb.RecommendGroupsResponse, error) {
	return &pb.RecommendGroupsResponse{}, nil
}
func (m *mockPointsClient) Discover(_ context.Context, _ *pb.DiscoverPoints, _ ...grpc.CallOption) (*pb.DiscoverResponse, error) {
	return &pb.DiscoverResponse{}, nil
}
func (m *mockPointsClient) DiscoverBatch(_ context.Context, _ *pb.DiscoverBatchPoints, _ ...grpc.CallOption) (*pb.DiscoverBatchResponse, error) {
	return &pb.DiscoverBatchResponse{}, nil
}
func (m *mockPointsClient) UpdateBatch(_ context.Context, _ *pb.UpdateBatchPoints, _ ...grpc.CallOption) (*pb.UpdateBatchResponse, error) {
	return &pb.UpdateBatchResponse{}, nil
}
func (m *mockPointsClient) Query(_ context.Context, _ *pb.QueryPoints, _ ...grpc.CallOption) (*pb.QueryResponse, error) {
	return &pb.QueryResponse{}, nil
}
func (m *mockPointsClient) QueryBatch(_ context.Context, _ *pb.QueryBatchPoints, _ ...grpc.CallOption) (*pb.QueryBatchResponse, error) {
	return &pb.QueryBatchResponse{}, nil
}
func (m *mockPointsClient) QueryGroups(_ context.Context, _ *pb.QueryPointGroups, _ ...grpc.CallOption) (*pb.QueryGroupsResponse, error) {
	return &pb.QueryGroupsResponse{}, nil
}
func (m *mockPointsClient) Facet(_ context.Context, _ *pb.FacetCounts, _ ...grpc.CallOption) (*pb.FacetResponse, error) {
	return &pb.FacetResponse{}, nil
}
func (m *mockPointsClient) SearchMatrixPairs(_ context.Context, _ *pb.SearchMatrixPoints, _ ...grpc.CallOption) (*pb.SearchMatrixPairsResponse, error) {
	return &pb.SearchMatrixPairsResponse{}, nil
}
func (m *mockPointsClient) SearchMatrixOffsets(_ context.Context, _ *pb.SearchMatrixPoints, _ ...grpc.CallOption) (*pb.SearchMatrixOffsetsResponse, error) {
	return &pb.SearchMatrixOffsetsResponse{}, nil
}

// ── Mock CollectionsClient ────────────────────────────────────────────────────

type mockCollectionsClient struct {
	getFn    func(ctx context.Context, in *pb.GetCollectionInfoRequest, opts ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error)
	listFn   func(ctx context.Context, in *pb.ListCollectionsRequest, opts ...grpc.CallOption) (*pb.ListCollectionsResponse, error)
	createFn func(ctx context.Context, in *pb.CreateCollection, opts ...grpc.CallOption) (*pb.CollectionOperationResponse, error)
	deleteFn func(ctx context.Context, in *pb.DeleteCollection, opts ...grpc.CallOption) (*pb.CollectionOperationResponse, error)
}

func (m *mockCollectionsClient) Get(ctx context.Context, in *pb.GetCollectionInfoRequest, opts ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
	if m.getFn != nil {
		return m.getFn(ctx, in, opts...)
	}
	return &pb.GetCollectionInfoResponse{}, nil
}

func (m *mockCollectionsClient) List(ctx context.Context, in *pb.ListCollectionsRequest, opts ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, in, opts...)
	}
	return &pb.ListCollectionsResponse{}, nil
}

func (m *mockCollectionsClient) Create(ctx context.Context, in *pb.CreateCollection, opts ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
	if m.createFn != nil {
		return m.createFn(ctx, in, opts...)
	}
	return &pb.CollectionOperationResponse{}, nil
}

func (m *mockCollectionsClient) Delete(ctx context.Context, in *pb.DeleteCollection, opts ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, in, opts...)
	}
	return &pb.CollectionOperationResponse{}, nil
}

// Unused methods
func (m *mockCollectionsClient) Update(_ context.Context, _ *pb.UpdateCollection, _ ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
	return &pb.CollectionOperationResponse{}, nil
}
func (m *mockCollectionsClient) UpdateAliases(_ context.Context, _ *pb.ChangeAliases, _ ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
	return &pb.CollectionOperationResponse{}, nil
}
func (m *mockCollectionsClient) ListCollectionAliases(_ context.Context, _ *pb.ListCollectionAliasesRequest, _ ...grpc.CallOption) (*pb.ListAliasesResponse, error) {
	return &pb.ListAliasesResponse{}, nil
}
func (m *mockCollectionsClient) ListAliases(_ context.Context, _ *pb.ListAliasesRequest, _ ...grpc.CallOption) (*pb.ListAliasesResponse, error) {
	return &pb.ListAliasesResponse{}, nil
}
func (m *mockCollectionsClient) CollectionClusterInfo(_ context.Context, _ *pb.CollectionClusterInfoRequest, _ ...grpc.CallOption) (*pb.CollectionClusterInfoResponse, error) {
	return &pb.CollectionClusterInfoResponse{}, nil
}
func (m *mockCollectionsClient) CollectionExists(_ context.Context, _ *pb.CollectionExistsRequest, _ ...grpc.CallOption) (*pb.CollectionExistsResponse, error) {
	return &pb.CollectionExistsResponse{}, nil
}
func (m *mockCollectionsClient) UpdateCollectionClusterSetup(_ context.Context, _ *pb.UpdateCollectionClusterSetupRequest, _ ...grpc.CallOption) (*pb.UpdateCollectionClusterSetupResponse, error) {
	return &pb.UpdateCollectionClusterSetupResponse{}, nil
}
func (m *mockCollectionsClient) CreateShardKey(_ context.Context, _ *pb.CreateShardKeyRequest, _ ...grpc.CallOption) (*pb.CreateShardKeyResponse, error) {
	return &pb.CreateShardKeyResponse{}, nil
}
func (m *mockCollectionsClient) DeleteShardKey(_ context.Context, _ *pb.DeleteShardKeyRequest, _ ...grpc.CallOption) (*pb.DeleteShardKeyResponse, error) {
	return &pb.DeleteShardKeyResponse{}, nil
}
func (m *mockCollectionsClient) ListShardKeys(_ context.Context, _ *pb.ListShardKeysRequest, _ ...grpc.CallOption) (*pb.ListShardKeysResponse, error) {
	return &pb.ListShardKeysResponse{}, nil
}

// ── Test helpers ──────────────────────────────────────────────────────────────

// newTestClient creates a Client wired to the given mock gRPC clients.
// nil conn is safe because the mock never uses the connection.
func newTestClient(mp *mockPointsClient, mc *mockCollectionsClient) *Client {
	return &Client{
		conn:        nil,
		points:      mp,
		collections: mc,
		collection:  "test_collection",
	}
}

func TestGrpcTarget(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://qdrant.example.com:6334", "qdrant.example.com:6334"},
		{"http://localhost:6334", "localhost:6334"},
		{"localhost:6334", "localhost:6334"},
		{"10.0.0.1:6334", "10.0.0.1:6334"},
		{"https://my-qdrant.internal:6333", "my-qdrant.internal:6333"},
		{"qdrant.internal", "qdrant.internal"},
		{"https://qdrant.internal", "qdrant.internal"},
		{"http://qdrant:6334/path", "qdrant:6334/path"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := grpcTarget(tt.input)
			if got != tt.want {
				t.Errorf("grpcTarget(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGrpcTarget_MultipleSchemes(t *testing.T) {
	got := grpcTarget("HTTP://HOST:6333")
	want := "HTTP://HOST:6333" // case-sensitive, only lowercase http/https stripped
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "HTTP://HOST:6333", got, want)
	}
}

// ── Client method tests ──────────────────────────────────────────────────────

func TestUpsert(t *testing.T) {
	var captured *pb.UpsertPoints
	mp := &mockPointsClient{
		upsertFn: func(_ context.Context, in *pb.UpsertPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			captured = in
			return &pb.PointsOperationResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	points := []*pb.PointStruct{}

	err := c.Upsert(context.Background(), points)
	if err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("Upsert was not called")
	}
	if captured.GetCollectionName() != "test_collection" {
		t.Errorf("expected collection test_collection, got %q", captured.GetCollectionName())
	}
}

func TestUpsert_Error(t *testing.T) {
	mp := &mockPointsClient{
		upsertFn: func(_ context.Context, _ *pb.UpsertPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	err := c.Upsert(context.Background(), []*pb.PointStruct{{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDeleteBySource(t *testing.T) {
	var capturedCollection string
	var capturedFilter *pb.Filter
	mp := &mockPointsClient{
		deleteFn: func(_ context.Context, in *pb.DeletePoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			capturedCollection = in.GetCollectionName()
			if sel := in.GetPoints(); sel != nil {
				if f := sel.GetFilter(); f != nil {
					capturedFilter = f
				}
			}
			return &pb.PointsOperationResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	err := c.DeleteBySource(context.Background(), "doc.md")
	if err != nil {
		t.Fatalf("DeleteBySource returned error: %v", err)
	}
	if capturedCollection != "test_collection" {
		t.Errorf("expected collection test_collection, got %q", capturedCollection)
	}
	if capturedFilter == nil {
		t.Fatal("expected filter with source_file match")
	}
	conds := capturedFilter.GetMust()
	if len(conds) == 0 {
		t.Fatal("expected at least one condition in filter")
	}
	field := conds[0].GetField()
	if field == nil || field.Key != "source_file" {
		t.Errorf("expected source_file filter, got key=%v", field.GetKey())
	}
}

func TestDeleteBySource_Error(t *testing.T) {
	mp := &mockPointsClient{
		deleteFn: func(_ context.Context, _ *pb.DeletePoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	err := c.DeleteBySource(context.Background(), "doc.md")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScroll(t *testing.T) {
	var capturedLimit *uint32
	var capturedOffset *pb.PointId
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, in *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			capturedLimit = in.Limit
			capturedOffset = in.Offset
			return &pb.ScrollResponse{
				Result: []*pb.RetrievedPoint{
					{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "p1"}}},
				},
				NextPageOffset: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "p2"}},
			}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	offset := &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "prev"}}
	results, next, err := c.Scroll(context.Background(), 50, offset)
	if err != nil {
		t.Fatalf("Scroll returned error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if capturedLimit == nil || *capturedLimit != 50 {
		t.Errorf("expected limit 50, got %v", capturedLimit)
	}
	if capturedOffset == nil || capturedOffset.GetUuid() != "prev" {
		t.Errorf("expected offset 'prev', got %v", capturedOffset)
	}
	if next == nil || next.GetUuid() != "p2" {
		t.Errorf("expected next page offset 'p2', got %v", next)
	}
}

func TestScroll_Error(t *testing.T) {
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, _ *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, _, err := c.Scroll(context.Background(), 10, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScroll_ZeroLimit(t *testing.T) {
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, in *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			if in.Limit != nil {
				t.Errorf("expected nil limit for zero value, got %d", *in.Limit)
			}
			return &pb.ScrollResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, _, err := c.Scroll(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Scroll returned error: %v", err)
	}
}

func TestScroll_NilOffset(t *testing.T) {
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, in *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			if in.Offset != nil {
				t.Errorf("expected nil offset, got %v", in.Offset)
			}
			return &pb.ScrollResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, _, err := c.Scroll(context.Background(), 100, nil)
	if err != nil {
		t.Fatalf("Scroll returned error: %v", err)
	}
}

func TestScrollFiltered(t *testing.T) {
	var capturedCollection string
	var capturedFilter *pb.Filter
	var capturedLimit *uint32
	var capturedOffset string

	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, in *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			capturedCollection = in.GetCollectionName()
			capturedFilter = in.GetFilter()
			capturedLimit = in.Limit
			if in.Offset != nil {
				capturedOffset = in.Offset.GetUuid()
			}
			return &pb.ScrollResponse{
				Result: []*pb.RetrievedPoint{
					{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "r1"}}},
				},
			}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	filter := &pb.Filter{
		Must: []*pb.Condition{{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key:   "status",
					Match: &pb.Match{MatchValue: &pb.Match_Keyword{Keyword: "active"}},
				},
			},
		}},
	}
	results, err := c.ScrollFiltered(context.Background(), "other_collection", filter, 100, "page1")
	if err != nil {
		t.Fatalf("ScrollFiltered returned error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if capturedCollection != "other_collection" {
		t.Errorf("expected collection 'other_collection', got %q", capturedCollection)
	}
	if capturedFilter == nil {
		t.Fatal("expected filter to be passed through")
	}
	if capturedLimit == nil || *capturedLimit != 100 {
		t.Errorf("expected limit 100, got %v", capturedLimit)
	}
	if capturedOffset != "page1" {
		t.Errorf("expected offset 'page1', got %q", capturedOffset)
	}
}

func TestScrollFiltered_Error(t *testing.T) {
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, _ *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, err := c.ScrollFiltered(context.Background(), "col", nil, 0, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScrollFiltered_PassthroughOptions(t *testing.T) {
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, in *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			if in.GetFilter() != nil {
				t.Error("expected nil filter when none provided")
			}
			if in.Limit != nil {
				t.Errorf("expected nil limit for 0, got %d", *in.Limit)
			}
			if in.Offset != nil {
				t.Errorf("expected nil offset for empty string, got %v", in.Offset)
			}
			return &pb.ScrollResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, err := c.ScrollFiltered(context.Background(), "col", nil, 0, "")
	if err != nil {
		t.Fatalf("ScrollFiltered returned error: %v", err)
	}
}

func TestDeleteFiltered(t *testing.T) {
	var capturedCollection string
	var capturedFilter *pb.Filter
	mp := &mockPointsClient{
		deleteFn: func(_ context.Context, in *pb.DeletePoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			capturedCollection = in.GetCollectionName()
			if sel := in.GetPoints(); sel != nil {
				capturedFilter = sel.GetFilter()
			}
			return &pb.PointsOperationResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	filter := &pb.Filter{
		Must: []*pb.Condition{{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key:   "status",
					Match: &pb.Match{MatchValue: &pb.Match_Keyword{Keyword: "archived"}},
				},
			},
		}},
	}
	err := c.DeleteFiltered(context.Background(), "test_collection", filter)
	if err != nil {
		t.Fatalf("DeleteFiltered returned error: %v", err)
	}
	if capturedCollection != "test_collection" {
		t.Errorf("expected collection 'test_collection', got %q", capturedCollection)
	}
	if capturedFilter == nil {
		t.Fatal("expected filter to be passed to delete")
	}
}

func TestDeleteFiltered_Error(t *testing.T) {
	mp := &mockPointsClient{
		deleteFn: func(_ context.Context, _ *pb.DeletePoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	err := c.DeleteFiltered(context.Background(), "col", &pb.Filter{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCount(t *testing.T) {
	mp := &mockPointsClient{
		countFn: func(_ context.Context, in *pb.CountPoints, _ ...grpc.CallOption) (*pb.CountResponse, error) {
			if in.GetCollectionName() != "test_collection" {
				t.Errorf("expected 'test_collection', got %q", in.GetCollectionName())
			}
			return &pb.CountResponse{
				Result: &pb.CountResult{Count: 42},
			}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	count, err := c.Count(context.Background())
	if err != nil {
		t.Fatalf("Count returned error: %v", err)
	}
	if count != 42 {
		t.Errorf("expected 42, got %d", count)
	}
}

func TestCount_Error(t *testing.T) {
	mp := &mockPointsClient{
		countFn: func(_ context.Context, _ *pb.CountPoints, _ ...grpc.CallOption) (*pb.CountResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, err := c.Count(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCountFiles(t *testing.T) {
	callCount := 0
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, _ *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			callCount++
			if callCount == 1 {
				return &pb.ScrollResponse{
					Result: []*pb.RetrievedPoint{
						{Payload: map[string]*pb.Value{"source_file": {Kind: &pb.Value_StringValue{StringValue: "a.md"}}}},
						{Payload: map[string]*pb.Value{"source_file": {Kind: &pb.Value_StringValue{StringValue: "b.md"}}}},
					},
					NextPageOffset: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "page2"}},
				}, nil
			}
			return &pb.ScrollResponse{
				Result: []*pb.RetrievedPoint{
					{Payload: map[string]*pb.Value{"source_file": {Kind: &pb.Value_StringValue{StringValue: "a.md"}}}}, // duplicate
				},
			}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	files, err := c.CountFiles(context.Background())
	if err != nil {
		t.Fatalf("CountFiles returned error: %v", err)
	}
	if files != 2 {
		t.Errorf("expected 2 unique files, got %d", files)
	}
	if callCount != 2 {
		t.Errorf("expected 2 scroll calls, got %d", callCount)
	}
}

func TestCountFiles_ScrollError(t *testing.T) {
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, _ *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, err := c.CountFiles(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCountFiles_NoPayload(t *testing.T) {
	mp := &mockPointsClient{
		scrollFn: func(_ context.Context, _ *pb.ScrollPoints, _ ...grpc.CallOption) (*pb.ScrollResponse, error) {
			return &pb.ScrollResponse{
				Result: []*pb.RetrievedPoint{
					{}, // no payload
				},
			}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	files, err := c.CountFiles(context.Background())
	if err != nil {
		t.Fatalf("CountFiles returned error: %v", err)
	}
	if files != 0 {
		t.Errorf("expected 0 files, got %d", files)
	}
}

func TestSearch(t *testing.T) {
	var reqCollection string
	var reqVector []float32
	var reqLimit uint64
	var reqScoreThreshold *float32

	mp := &mockPointsClient{
		searchFn: func(_ context.Context, in *pb.SearchPoints, _ ...grpc.CallOption) (*pb.SearchResponse, error) {
			reqCollection = in.GetCollectionName()
			reqVector = in.GetVector()
			reqLimit = in.GetLimit()
			reqScoreThreshold = in.ScoreThreshold
			return &pb.SearchResponse{
				Result: []*pb.ScoredPoint{
					{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "found"}}, Score: 0.95},
				},
			}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	vector := []float32{0.1, 0.2, 0.3}
	threshold := float32(0.5)
	results, err := c.Search(context.Background(), vector, 5, threshold, "", nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", results[0].Score)
	}
	if reqCollection != "test_collection" {
		t.Errorf("expected 'test_collection', got %q", reqCollection)
	}
	if reqLimit != 5 {
		t.Errorf("expected limit 5, got %d", reqLimit)
	}
	if reqScoreThreshold == nil || *reqScoreThreshold != threshold {
		t.Errorf("expected score threshold %f, got %v", threshold, reqScoreThreshold)
	}
}

func TestSearch_WithSourceFilter(t *testing.T) {
	var capturedFilter *pb.Filter
	mp := &mockPointsClient{
		searchFn: func(_ context.Context, in *pb.SearchPoints, _ ...grpc.CallOption) (*pb.SearchResponse, error) {
			capturedFilter = in.GetFilter()
			return &pb.SearchResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	_, err := c.Search(context.Background(), []float32{0.1}, 5, 0.0, "src/doc.md", nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if capturedFilter == nil {
		t.Fatal("expected source filter")
	}
	conds := capturedFilter.GetMust()
	if len(conds) == 0 {
		t.Fatal("expected at least one condition")
	}
	field := conds[0].GetField()
	if field == nil || field.GetKey() != "source_file" {
		t.Errorf("expected source_file filter, got %v", field)
	}
}

func TestSearch_WithCustomFilter(t *testing.T) {
	customFilter := &pb.Filter{
		Must: []*pb.Condition{{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "status",
					Match: &pb.Match{MatchValue: &pb.Match_Keyword{Keyword: "active"}},
				},
			},
		}},
	}
	var capturedFilter *pb.Filter
	mp := &mockPointsClient{
		searchFn: func(_ context.Context, in *pb.SearchPoints, _ ...grpc.CallOption) (*pb.SearchResponse, error) {
			capturedFilter = in.GetFilter()
			return &pb.SearchResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	// Custom filter takes precedence over sourceFilter
	_, err := c.Search(context.Background(), []float32{0.1}, 5, 0.0, "src/doc.md", customFilter)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if capturedFilter != customFilter {
		t.Error("expected custom filter to be passed through directly")
	}
}

func TestSearch_Error(t *testing.T) {
	mp := &mockPointsClient{
		searchFn: func(_ context.Context, _ *pb.SearchPoints, _ ...grpc.CallOption) (*pb.SearchResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, err := c.Search(context.Background(), nil, 0, 0, "", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetPoints(t *testing.T) {
	var capturedCollection string
	var capturedIDs []*pb.PointId
	var capturedVectors *pb.WithVectorsSelector

	mp := &mockPointsClient{
		getFn: func(_ context.Context, in *pb.GetPoints, _ ...grpc.CallOption) (*pb.GetResponse, error) {
			capturedCollection = in.GetCollectionName()
			capturedIDs = in.GetIds()
			capturedVectors = in.GetWithVectors()
			return &pb.GetResponse{
				Result: []*pb.RetrievedPoint{
					{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "p1"}}},
					{Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: "p2"}}},
				},
			}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	ids := []*pb.PointId{
		{PointIdOptions: &pb.PointId_Uuid{Uuid: "p1"}},
		{PointIdOptions: &pb.PointId_Uuid{Uuid: "p2"}},
	}
	results, err := c.GetPoints(context.Background(), "some_collection", ids)
	if err != nil {
		t.Fatalf("GetPoints returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if capturedCollection != "some_collection" {
		t.Errorf("expected 'some_collection', got %q", capturedCollection)
	}
	if len(capturedIDs) != 2 {
		t.Errorf("expected 2 IDs, got %d", len(capturedIDs))
	}
	if capturedVectors == nil || capturedVectors.GetEnable() != false {
		t.Error("expected vectors to be disabled")
	}
}

func TestGetPoints_Error(t *testing.T) {
	mp := &mockPointsClient{
		getFn: func(_ context.Context, _ *pb.GetPoints, _ ...grpc.CallOption) (*pb.GetResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	_, err := c.GetPoints(context.Background(), "col", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHealth(t *testing.T) {
	var called bool
	mc := &mockCollectionsClient{
		listFn: func(_ context.Context, _ *pb.ListCollectionsRequest, _ ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
			called = true
			return &pb.ListCollectionsResponse{}, nil
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
	if !called {
		t.Error("Health did not call ListCollections")
	}
}

func TestHealth_Error(t *testing.T) {
	mc := &mockCollectionsClient{
		listFn: func(_ context.Context, _ *pb.ListCollectionsRequest, _ ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSetPayload(t *testing.T) {
	var capturedCollection string
	var capturedIDs []*pb.PointId
	var capturedPayload map[string]*pb.Value
	mp := &mockPointsClient{
		setPayloadFn: func(_ context.Context, in *pb.SetPayloadPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			capturedCollection = in.GetCollectionName()
			if sel := in.GetPointsSelector(); sel != nil {
				if pts := sel.GetPoints(); pts != nil {
					capturedIDs = pts.GetIds()
				}
			}
			capturedPayload = in.GetPayload()
			return &pb.PointsOperationResponse{}, nil
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})

	ids := []*pb.PointId{
		{PointIdOptions: &pb.PointId_Uuid{Uuid: "p1"}},
	}
	payload := map[string]*pb.Value{
		"status": {Kind: &pb.Value_StringValue{StringValue: "needs_review"}},
	}
	err := c.SetPayload(context.Background(), "test_collection", ids, payload)
	if err != nil {
		t.Fatalf("SetPayload returned error: %v", err)
	}
	if capturedCollection != "test_collection" {
		t.Errorf("expected 'test_collection', got %q", capturedCollection)
	}
	if len(capturedIDs) != 1 || capturedIDs[0].GetUuid() != "p1" {
		t.Errorf("expected [p1], got %v", capturedIDs)
	}
	if capturedPayload["status"].GetStringValue() != "needs_review" {
		t.Errorf("expected status=needs_review, got %v", capturedPayload)
	}
}

func TestSetPayload_Error(t *testing.T) {
	mp := &mockPointsClient{
		setPayloadFn: func(_ context.Context, _ *pb.SetPayloadPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	err := c.SetPayload(context.Background(), "col", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection(t *testing.T) {
	c := newTestClient(&mockPointsClient{}, &mockCollectionsClient{})
	if c.Collection() != "test_collection" {
		t.Errorf("expected 'test_collection', got %q", c.Collection())
	}
}

func TestClose(t *testing.T) {
	c := newTestClient(&mockPointsClient{}, &mockCollectionsClient{})
	// Close with nil conn should not panic
	err := c.Close()
	if err == nil {
		t.Log("Close with nil conn returned nil")
	}
	// Close always passes through to conn.Close()
}

func TestCreatePayloadIndex(t *testing.T) {
	tests := []struct {
		fieldType string
		want      pb.FieldType
	}{
		{"keyword", pb.FieldType_FieldTypeKeyword},
		{"integer", pb.FieldType_FieldTypeInteger},
		{"float", pb.FieldType_FieldTypeFloat},
		{"geo", pb.FieldType_FieldTypeGeo},
		{"text", pb.FieldType_FieldTypeText},
		{"bool", pb.FieldType_FieldTypeBool},
		{"datetime", pb.FieldType_FieldTypeDatetime},
		{"uuid", pb.FieldType_FieldTypeUuid},
		{"unknown", pb.FieldType_FieldTypeKeyword}, // default
		{"", pb.FieldType_FieldTypeKeyword},         // default for empty
	}
	for _, tt := range tests {
		t.Run(tt.fieldType, func(t *testing.T) {
			var capturedFieldType *pb.FieldType
			mp := &mockPointsClient{
				createFieldIndexFn: func(_ context.Context, in *pb.CreateFieldIndexCollection, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
					capturedFieldType = in.FieldType
					return &pb.PointsOperationResponse{}, nil
				},
			}
			c := newTestClient(mp, &mockCollectionsClient{})

			err := c.CreatePayloadIndex(context.Background(), "col", "status", tt.fieldType)
			if err != nil {
				t.Fatalf("CreatePayloadIndex(%q) returned error: %v", tt.fieldType, err)
			}
			if capturedFieldType == nil {
				t.Fatal("expected field type to be set")
			}
			if *capturedFieldType != tt.want {
				t.Errorf("CreatePayloadIndex(%q): got %v, want %v", tt.fieldType, *capturedFieldType, tt.want)
			}
		})
	}
}

func TestCreatePayloadIndex_Error(t *testing.T) {
	mp := &mockPointsClient{
		createFieldIndexFn: func(_ context.Context, _ *pb.CreateFieldIndexCollection, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(mp, &mockCollectionsClient{})
	err := c.CreatePayloadIndex(context.Background(), "col", "field", "keyword")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetVectorSize(t *testing.T) {
	mc := &mockCollectionsClient{
		getFn: func(_ context.Context, _ *pb.GetCollectionInfoRequest, _ ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
			return &pb.GetCollectionInfoResponse{
				Result: &pb.CollectionInfo{
					Config: &pb.CollectionConfig{
						Params: &pb.CollectionParams{
							VectorsConfig: &pb.VectorsConfig{
								Config: &pb.VectorsConfig_Params{
									Params: &pb.VectorParams{
										Size: 384,
									},
								},
							},
						},
					},
				},
			}, nil
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	size, err := c.GetVectorSize(context.Background(), "my_collection")
	if err != nil {
		t.Fatalf("GetVectorSize returned error: %v", err)
	}
	if size != 384 {
		t.Errorf("expected 384, got %d", size)
	}
}

func TestGetVectorSize_ServerError(t *testing.T) {
	mc := &mockCollectionsClient{
		getFn: func(_ context.Context, _ *pb.GetCollectionInfoRequest, _ ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	_, err := c.GetVectorSize(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetVectorSize_EmptyResponse(t *testing.T) {
	mc := &mockCollectionsClient{
		getFn: func(_ context.Context, _ *pb.GetCollectionInfoRequest, _ ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
			return &pb.GetCollectionInfoResponse{}, nil // nil Result
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	_, err := c.GetVectorSize(context.Background(), "col")
	if err == nil {
		t.Fatal("expected error for nil result")
	}
}

func TestGetVectorSize_NilConfig(t *testing.T) {
	mc := &mockCollectionsClient{
		getFn: func(_ context.Context, _ *pb.GetCollectionInfoRequest, _ ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
			return &pb.GetCollectionInfoResponse{
				Result: &pb.CollectionInfo{}, // nil Config
			}, nil
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	_, err := c.GetVectorSize(context.Background(), "col")
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

// ── FactStore interface assertion ─────────────────────────────────────────────

func TestClientSatisfiesFactStore(t *testing.T) {
	// Compile-time check already exists (var _ FactStore = (*Client)(nil))
	// This test ensures the interface assertion compiles and the mock can also satisfy it.
	var _ FactStore = (*Client)(nil)
}

// ── ensureCollection ──────────────────────────────────────────────────────────

func TestEnsureCollection_Exists(t *testing.T) {
	mc := &mockCollectionsClient{
		listFn: func(_ context.Context, _ *pb.ListCollectionsRequest, _ ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
			return &pb.ListCollectionsResponse{
				Collections: []*pb.CollectionDescription{
					{Name: "test_collection"},
				},
			}, nil
		},
		getFn: func(_ context.Context, in *pb.GetCollectionInfoRequest, _ ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
			if in.GetCollectionName() != "test_collection" {
				t.Errorf("expected 'test_collection', got %q", in.GetCollectionName())
			}
			return &pb.GetCollectionInfoResponse{
				Result: &pb.CollectionInfo{
					Config: &pb.CollectionConfig{
						Params: &pb.CollectionParams{
							VectorsConfig: &pb.VectorsConfig{
								Config: &pb.VectorsConfig_Params{
									Params: &pb.VectorParams{
										Size: 384,
									},
								},
							},
						},
					},
				},
			}, nil
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.ensureCollection(context.Background(), 384)
	if err != nil {
		t.Fatalf("ensureCollection returned error: %v", err)
	}
}

func TestEnsureCollection_ExistsSizeMismatch(t *testing.T) {
	var deleted bool
	var created bool
	mc := &mockCollectionsClient{
		listFn: func(_ context.Context, _ *pb.ListCollectionsRequest, _ ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
			return &pb.ListCollectionsResponse{
				Collections: []*pb.CollectionDescription{
					{Name: "test_collection"},
				},
			}, nil
		},
		getFn: func(_ context.Context, in *pb.GetCollectionInfoRequest, _ ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
			// Return a different size to trigger recreate
			return &pb.GetCollectionInfoResponse{
				Result: &pb.CollectionInfo{
					Config: &pb.CollectionConfig{
						Params: &pb.CollectionParams{
							VectorsConfig: &pb.VectorsConfig{
								Config: &pb.VectorsConfig_Params{
									Params: &pb.VectorParams{
										Size: 768, // different from requested 384
									},
								},
							},
						},
					},
				},
			}, nil
		},
		deleteFn: func(_ context.Context, _ *pb.DeleteCollection, _ ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
			deleted = true
			return &pb.CollectionOperationResponse{}, nil
		},
		createFn: func(_ context.Context, in *pb.CreateCollection, _ ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
			created = true
			if in.GetCollectionName() != "test_collection" {
				t.Errorf("expected 'test_collection', got %q", in.GetCollectionName())
			}
			if in.GetVectorsConfig().GetParams().GetSize() != 384 {
				t.Errorf("expected size 384, got %d", in.GetVectorsConfig().GetParams().GetSize())
			}
			return &pb.CollectionOperationResponse{}, nil
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.ensureCollection(context.Background(), 384)
	if err != nil {
		t.Fatalf("ensureCollection returned error: %v", err)
	}
	if !deleted {
		t.Error("expected collection to be deleted on size mismatch")
	}
	if !created {
		t.Error("expected collection to be recreated after deletion")
	}
}

func TestEnsureCollection_CreateNew(t *testing.T) {
	var created bool
	mc := &mockCollectionsClient{
		listFn: func(_ context.Context, _ *pb.ListCollectionsRequest, _ ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
			return &pb.ListCollectionsResponse{
				Collections: []*pb.CollectionDescription{}, // empty
			}, nil
		},
		createFn: func(_ context.Context, in *pb.CreateCollection, _ ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
			created = true
			if in.GetCollectionName() != "test_collection" {
				t.Errorf("expected 'test_collection', got %q", in.GetCollectionName())
			}
			return &pb.CollectionOperationResponse{}, nil
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.ensureCollection(context.Background(), 384)
	if err != nil {
		t.Fatalf("ensureCollection returned error: %v", err)
	}
	if !created {
		t.Error("expected collection to be created")
	}
}

func TestEnsureCollection_ListError(t *testing.T) {
	mc := &mockCollectionsClient{
		listFn: func(_ context.Context, _ *pb.ListCollectionsRequest, _ ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
			return nil, fmt.Errorf("list error")
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.ensureCollection(context.Background(), 384)
	if err == nil {
		t.Fatal("expected error from list failure, got nil")
	}
}

func TestEnsureCollection_GetVectorSizeError(t *testing.T) {
	mc := &mockCollectionsClient{
		listFn: func(_ context.Context, _ *pb.ListCollectionsRequest, _ ...grpc.CallOption) (*pb.ListCollectionsResponse, error) {
			return &pb.ListCollectionsResponse{
				Collections: []*pb.CollectionDescription{
					{Name: "test_collection"},
				},
			}, nil
		},
		getFn: func(_ context.Context, _ *pb.GetCollectionInfoRequest, _ ...grpc.CallOption) (*pb.GetCollectionInfoResponse, error) {
			return nil, fmt.Errorf("get info error")
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.ensureCollection(context.Background(), 384)
	if err == nil {
		t.Fatal("expected error from get vector size failure, got nil")
	}
}

// ── deleteCollection ─────────────────────────────────────────────────────────

func TestDeleteCollection(t *testing.T) {
	var capturedName string
	mc := &mockCollectionsClient{
		deleteFn: func(_ context.Context, in *pb.DeleteCollection, _ ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
			capturedName = in.GetCollectionName()
			return &pb.CollectionOperationResponse{}, nil
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)
	c.collection = "to_delete"

	err := c.deleteCollection(context.Background())
	if err != nil {
		t.Fatalf("deleteCollection returned error: %v", err)
	}
	if capturedName != "to_delete" {
		t.Errorf("expected 'to_delete', got %q", capturedName)
	}
}

func TestDeleteCollection_Error(t *testing.T) {
	mc := &mockCollectionsClient{
		deleteFn: func(_ context.Context, _ *pb.DeleteCollection, _ ...grpc.CallOption) (*pb.CollectionOperationResponse, error) {
			return nil, fmt.Errorf("rpc error")
		},
	}
	c := newTestClient(&mockPointsClient{}, mc)

	err := c.deleteCollection(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
