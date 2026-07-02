package embeddedstore

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
)

// newTestStore returns a Store backed by a temp file. Caller must Close().
func newTestStore(t *testing.T, collection string, dim uint64) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(Config{
		Path:       filepath.Join(dir, "test.db"),
		Collection: collection,
		VectorSize: dim,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mkPoint(id string, vec []float32, source string, payload map[string]*pb.Value) *pb.PointStruct {
	if payload == nil {
		payload = map[string]*pb.Value{}
	}
	if source != "" {
		payload["source_file"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: source}}
	}
	return &pb.PointStruct{
		Id: &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: id}},
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{Data: vec},
			},
		},
		Payload: payload,
	}
}

func TestUpsertAndCount(t *testing.T) {
	s := newTestStore(t, "test1", 4)
	ctx := context.Background()
	if err := s.Upsert(ctx, []*pb.PointStruct{
		mkPoint("a", []float32{1, 0, 0, 0}, "a.md", nil),
		mkPoint("b", []float32{0, 1, 0, 0}, "b.md", nil),
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Count(ctx)
	if err != nil || n != 2 {
		t.Errorf("Count = %d, %v; want 2, nil", n, err)
	}
}

func TestSearch_CosineRanking(t *testing.T) {
	s := newTestStore(t, "test2", 4)
	ctx := context.Background()
	_ = s.Upsert(ctx, []*pb.PointStruct{
		mkPoint("a", []float32{1, 0, 0, 0}, "a.md", nil),
		mkPoint("b", []float32{0.9, 0.1, 0, 0}, "b.md", nil),
		mkPoint("c", []float32{0, 1, 0, 0}, "c.md", nil),
	})
	hits, err := s.Search(ctx, []float32{1, 0, 0, 0}, 3, 0.0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3", len(hits))
	}
	if hits[0].GetId().GetUuid() != "a" {
		t.Errorf("expected 'a' first (score %.3f), got %s (%.3f)", hits[0].GetScore(), hits[0].GetId().GetUuid(), hits[0].GetScore())
	}
	if hits[0].GetScore() < hits[1].GetScore() || hits[1].GetScore() < hits[2].GetScore() {
		t.Errorf("hits not sorted by score: %.3f, %.3f, %.3f", hits[0].GetScore(), hits[1].GetScore(), hits[2].GetScore())
	}
}

func TestSearch_SourceFilter(t *testing.T) {
	s := newTestStore(t, "test3", 4)
	ctx := context.Background()
	_ = s.Upsert(ctx, []*pb.PointStruct{
		mkPoint("a", []float32{1, 0, 0, 0}, "alice.md", nil),
		mkPoint("b", []float32{0.99, 0, 0, 0}, "bob.md", nil),
	})
	hits, err := s.Search(ctx, []float32{1, 0, 0, 0}, 10, 0.0, "alice.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].GetId().GetUuid() != "a" {
		t.Errorf("expected only 'a', got %+v", hits)
	}
}

func TestSearch_Filter(t *testing.T) {
	s := newTestStore(t, "test4", 4)
	ctx := context.Background()
	_ = s.Upsert(ctx, []*pb.PointStruct{
		mkPoint("a", []float32{1, 0, 0, 0}, "alice.md", nil),
		mkPoint("b", []float32{0.99, 0, 0, 0}, "bob.md", nil),
	})
	filter := &pb.Filter{
		Must: []*pb.Condition{{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "source_file",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: "bob.md"},
					},
				},
			},
		}},
	}
	hits, err := s.Search(ctx, []float32{1, 0, 0, 0}, 10, 0.0, "", filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].GetId().GetUuid() != "b" {
		t.Errorf("expected only 'b', got %+v", hits)
	}
}

func TestDeleteBySource(t *testing.T) {
	s := newTestStore(t, "test5", 4)
	ctx := context.Background()
	_ = s.Upsert(ctx, []*pb.PointStruct{
		mkPoint("a", []float32{1, 0, 0, 0}, "alice.md", nil),
		mkPoint("b", []float32{0, 1, 0, 0}, "bob.md", nil),
	})
	if err := s.DeleteBySource(ctx, "alice.md"); err != nil {
		t.Fatal(err)
	}
	n, _ := s.Count(ctx)
	if n != 1 {
		t.Errorf("Count after delete = %d, want 1", n)
	}
	hits, _ := s.Search(ctx, []float32{1, 0, 0, 0}, 10, 0.0, "", nil)
	if len(hits) != 1 || hits[0].GetId().GetUuid() != "b" {
		t.Errorf("unexpected remaining hits: %+v", hits)
	}
}

func TestScroll(t *testing.T) {
	s := newTestStore(t, "test6", 4)
	ctx := context.Background()
	_ = s.Upsert(ctx, []*pb.PointStruct{
		mkPoint("aaa", []float32{1, 0, 0, 0}, "a.md", nil),
		mkPoint("bbb", []float32{0, 1, 0, 0}, "b.md", nil),
		mkPoint("ccc", []float32{0, 0, 1, 0}, "c.md", nil),
	})
	pts, next, err := s.Scroll(ctx, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("page1 = %d, want 2", len(pts))
	}
	if next == nil {
		t.Fatalf("expected non-nil next page cursor")
	}
	pts2, _, err := s.Scroll(ctx, 2, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts2) != 1 {
		t.Errorf("page2 = %d, want 1", len(pts2))
	}
}

func TestPayload_RoundTrip(t *testing.T) {
	s := newTestStore(t, "test7", 4)
	ctx := context.Background()
	payload := map[string]*pb.Value{
		"text":        {Kind: &pb.Value_StringValue{StringValue: "hello world"}},
		"source_file": {Kind: &pb.Value_StringValue{StringValue: "test.md"}},
		"chunk_index": {Kind: &pb.Value_IntegerValue{IntegerValue: 3}},
		"tags": {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: []*pb.Value{
			{Kind: &pb.Value_StringValue{StringValue: "alpha"}},
			{Kind: &pb.Value_StringValue{StringValue: "beta"}},
		}}}},
	}
	_ = s.Upsert(ctx, []*pb.PointStruct{
		mkPoint("a", []float32{1, 0, 0, 0}, "", payload),
	})
	pts, _, err := s.Scroll(ctx, 10, nil)
	if err != nil || len(pts) != 1 {
		t.Fatalf("scroll: %v, %d", err, len(pts))
	}
	p := pts[0].GetPayload()
	if p["text"].GetStringValue() != "hello world" {
		t.Errorf("text = %q", p["text"].GetStringValue())
	}
	if p["chunk_index"].GetDoubleValue() != 3 {
		t.Errorf("chunk_index = %f", p["chunk_index"].GetDoubleValue())
	}
	if p["source_file"].GetStringValue() != "test.md" {
		t.Errorf("source_file = %q", p["source_file"].GetStringValue())
	}
	tags := p["tags"].GetListValue().GetValues()
	if len(tags) != 2 || tags[0].GetStringValue() != "alpha" {
		t.Errorf("tags = %+v", tags)
	}
}

func TestUpsertInto_MultipleCollections(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Config{Path: filepath.Join(dir, "multi.db"), Collection: "default", VectorSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.UpsertInto(ctx, "chunks_soul_a", []*pb.PointStruct{
		mkPoint("x", []float32{1, 0, 0, 0}, "x.md", nil),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertInto(ctx, "chunks_soul_b", []*pb.PointStruct{
		mkPoint("y", []float32{0, 1, 0, 0}, "y.md", nil),
	}); err != nil {
		t.Fatal(err)
	}
	hitsA, err := s.searchCollection(ctx, "chunks_soul_a", []float32{1, 0, 0, 0}, 10, 0.0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsA) != 1 || hitsA[0].GetId().GetUuid() != "x" {
		t.Errorf("vault A leaked: %+v", hitsA)
	}
	hitsB, err := s.searchCollection(ctx, "chunks_soul_b", []float32{1, 0, 0, 0}, 10, 0.0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsB) != 1 || hitsB[0].GetId().GetUuid() != "y" {
		t.Errorf("expected only the y point from vault B, got %+v", hitsB)
	}
}

func TestCosineSimilarity(t *testing.T) {
	// 90-degree vectors: cosine = 0
	got := cosineSimilarity([]float32{1, 0}, []float32{0, 1})
	if math.Abs(float64(got)) > 1e-6 {
		t.Errorf("orthogonal cosine = %f, want 0", got)
	}
	// Identical unit vectors: cosine = 1
	got = cosineSimilarity([]float32{0.6, 0.8}, []float32{0.6, 0.8})
	if math.Abs(float64(got)-1) > 1e-6 {
		t.Errorf("identical cosine = %f, want 1", got)
	}
}
