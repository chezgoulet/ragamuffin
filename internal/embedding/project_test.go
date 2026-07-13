package embedding

import (
	"context"
	"math"
	"testing"
)

func BenchmarkProjectPCA(b *testing.B) {
	n := 1000
	d := 1536
	vectors := make([][]float32, n)
	for i := range vectors {
		vectors[i] = make([]float32, d)
		for j := range vectors[i] {
			vectors[i][j] = float32(math.Sin(float64(i+j))) * 0.5
		}
	}

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		result, err := ProjectPCA(ctx, vectors, nil, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Points) != n {
			b.Fatalf("expected %d points, got %d", n, len(result.Points))
		}
	}
}

func TestProjectPCA_Empty(t *testing.T) {
	ctx := context.Background()
	result, err := ProjectPCA(ctx, [][]float32{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Points) != 0 {
		t.Errorf("expected 0 points, got %d", len(result.Points))
	}
}

func TestProjectPCA_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ProjectPCA(ctx, [][]float32{{1, 2, 3}}, nil, nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestProjectPCA_StableCoordinates(t *testing.T) {
	ctx := context.Background()
	vectors := [][]float32{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}
	result, err := ProjectPCA(ctx, vectors, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(result.Points))
	}
	for i, p := range result.Points {
		if math.IsNaN(p.X) || math.IsNaN(p.Y) {
			t.Errorf("point %d has NaN coordinates: %f, %f", i, p.X, p.Y)
		}
	}
}
