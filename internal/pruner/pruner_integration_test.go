package pruner

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"log/slog"
)

// Integration tests for the pruner scan methods.
//
// These tests require a real Qdrant instance. They are skipped in short mode
// (go test -short) and require QDRANT_TEST_URL to be set.
//
// To run:
//   docker run -d -p 6334:6334 qdrant/qdrant
//   export QDRANT_TEST_URL=http://localhost:6334
//   go test -run TestPrunerIntegration ./internal/pruner/ -v
//
// Each test cleans up its own test data via DeleteFiltered.

// testPrunerSkipped returns true if integration tests should be skipped.
func testPrunerSkipped(t *testing.T) bool {
	if testing.Short() {
		t.Skip("skipping pruner integration test in short mode")
	}
	if u := os.Getenv("QDRANT_TEST_URL"); u == "" {
		t.Skip("QDRANT_TEST_URL not set — skipping pruner integration test")
		return true
	}
	return false
}

const testCollection = "ragamuffin_test_pruner_integration"

// testClient creates a Qdrant Client pointed at the shared test collection.
func testClient(ctx context.Context, t *testing.T) *qdrant.Client {
	t.Helper()
	url := os.Getenv("QDRANT_TEST_URL")
	if url == "" {
		t.Fatal("QDRANT_TEST_URL not set")
	}
	client, err := qdrant.New(ctx, url, testCollection, 384)
	if err != nil {
		t.Fatalf("failed to create Qdrant client: %v", err)
	}
	return client
}

// testPruner creates a Pruner with the given client and optional overrides.
func testPruner(client qdrant.FactStore, overrides map[string]interface{}) *Pruner {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StaleDays = 90

	if v, ok := overrides["low_confidence_threshold"]; ok {
		cfg.LowConfidenceThreshold = v.(float64)
	}
	if v, ok := overrides["conflict_sample_size"]; ok {
		cfg.ConflictSampleSize = v.(int)
	}
	if v, ok := overrides["stale_days"]; ok {
		cfg.StaleDays = v.(int)
	}

	return New(client, nil, nil, nil, logger, cfg)
}

// testPoint creates a single Qdrant point with payload fields the pruner scans
// expect: fact_key, fact_value, text, status, ttl_days, expires_at_unix, confidence.
func testPoint(key, value string, overrides map[string]*pb.Value) *pb.PointStruct {
	payload := map[string]*pb.Value{
		"fact_key":   qutil.Nv(key),
		"fact_value": qutil.Nv(value),
		"text":       qutil.Nv(key + ": " + value),
		"status":     qutil.Nv("active"),
		"ttl_days":   qutil.Nv(float64(0)),
		"confidence": qutil.Nv(float64(0.8)),
	}
	for k, v := range overrides {
		payload[k] = v
	}
	return &pb.PointStruct{
		Id:      pb.NewID(uuid.New().String()),
		Payload: payload,
	}
}

// upsertPoints inserts test points into the collection.
func upsertPoints(ctx context.Context, t *testing.T, client *qdrant.Client, points ...*pb.PointStruct) {
	t.Helper()
	if err := client.Upsert(ctx, points); err != nil {
		t.Fatalf("failed to upsert test points: %v", err)
	}
}

// cleanupPoints deletes all points with status=active or status=needs_review.
func cleanupPoints(ctx context.Context, t *testing.T, client *qdrant.Client) {
	t.Helper()
	for _, status := range []string{"active", "needs_review"} {
		_ = client.DeleteFiltered(ctx, testCollection, &pb.Filter{
			Must: []*pb.Condition{{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "status",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{Keyword: status},
						},
					},
				},
			}},
		})
	}
}

// countNeedsReview scrolls facts with status=needs_review and returns the count.
func countNeedsReview(ctx context.Context, t *testing.T, client qdrant.FactStore) int {
	t.Helper()
	points, err := client.ScrollFiltered(ctx, testCollection, &pb.Filter{
		Must: []*pb.Condition{{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "status",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: "needs_review"},
					},
				},
			},
		}},
	}, 1000, "")
	if err != nil {
		t.Fatalf("failed to scroll needs_review facts: %v", err)
	}
	return len(points)
}

// ── Unit tests (no Qdrant required) ──────────────────────────────────────────

func TestStaleFilterConditions(t *testing.T) {
	now := float64(time.Now().UTC().Unix())
	f := staleFilter(now)

	if f == nil {
		t.Fatal("expected non-nil filter")
	}
	if len(f.Must) != 3 {
		t.Fatalf("expected 3 must conditions, got %d", len(f.Must))
	}

	// Condition 0: status = "active"
	field0 := f.Must[0].GetField()
	if field0 == nil || field0.Key != "status" {
		t.Fatalf("condition 0: expected field 'status', got %+v", field0)
	}
	_ = f.Must[0].GetField().GetMatch().GetKeyword()

	// Condition 1: ttl_days > 0
	field1 := f.Must[1].GetField()
	if field1 == nil || field1.Key != "ttl_days" {
		t.Fatalf("condition 1: expected field 'ttl_days', got %+v", field1)
	}
	if field1.Range.Gt == nil {
		t.Fatal("condition 1 range: expected Gt > 0")
	}

	// Condition 2: expires_at_unix < now
	field2 := f.Must[2].GetField()
	if field2 == nil || field2.Key != "expires_at_unix" {
		t.Fatalf("condition 2: expected field 'expires_at_unix', got %+v", field2)
	}
	if field2.Range.Lt == nil {
		t.Fatal("condition 2 range: expected Lt < now")
	}
}

// ── Integration tests (require real Qdrant) ─────────────────────────────────

func TestPrunerIntegration_StaleScan(t *testing.T) {
	if testPrunerSkipped(t) {
		return
	}
	ctx := context.Background()
	client := testClient(ctx, t)
	defer cleanupPoints(ctx, t, client)

	pr := testPruner(client, nil)

	now := time.Now().UTC()

	// Expired: ttl_days > 0, expires_at_unix in the past
	expired := testPoint("fact/stale/expired", "expired", map[string]*pb.Value{
		"ttl_days":       qutil.Nv(float64(30)),
		"expires_at_unix": qutil.Nv(float64(now.Unix() - 86400)),
	})

	// Fresh: ttl_days > 0, expires_at_unix in the future
	fresh := testPoint("fact/fresh", "fresh", map[string]*pb.Value{
		"ttl_days":       qutil.Nv(float64(30)),
		"expires_at_unix": qutil.Nv(float64(now.Unix() + 86400)),
	})

	// No-TTL: ttl_days = 0 (default from testPoint)
	noTTL := testPoint("fact/no-ttl", "no TTL", nil)

	upsertPoints(ctx, t, client, expired, fresh, noTTL)

	pr.staleScan(ctx)

	count := countNeedsReview(ctx, t, client)
	if count != 1 {
		t.Fatalf("expected 1 stale fact flagged, got %d", count)
	}
}

func TestPrunerIntegration_ConflictScan_NilEmbedder(t *testing.T) {
	if testPrunerSkipped(t) {
		return
	}
	ctx := context.Background()
	client := testClient(ctx, t)
	defer cleanupPoints(ctx, t, client)

	pr := testPruner(client, nil)

	upsertPoints(ctx, t, client,
		testPoint("fact/a", "some knowledge", nil),
		testPoint("fact/b", "some knowledge (dup)", nil),
	)

	pr.conflictScan(ctx)

	count := countNeedsReview(ctx, t, client)
	if count != 0 {
		t.Fatalf("expected 0 flagged with nil embedder, got %d", count)
	}
}

func TestPrunerIntegration_SupersedeScan_Runs(t *testing.T) {
	if testPrunerSkipped(t) {
		return
	}
	ctx := context.Background()
	client := testClient(ctx, t)
	defer cleanupPoints(ctx, t, client)

	pr := testPruner(client, nil)

	upsertPoints(ctx, t, client,
		testPoint("config/url", "postgres://old", nil),
	)

	pr.supersedeScan(ctx)
	t.Log("Supersede scan completed without error")
}

func TestPrunerIntegration_LowConfidenceScan(t *testing.T) {
	if testPrunerSkipped(t) {
		return
	}
	ctx := context.Background()
	client := testClient(ctx, t)
	defer cleanupPoints(ctx, t, client)

	pr := testPruner(client, map[string]interface{}{
		"low_confidence_threshold": float64(0.5),
	})

	low := testPoint("fact/low", "uncertain", map[string]*pb.Value{
		"confidence": qutil.Nv(float64(0.3)),
	})
	high := testPoint("fact/high", "certain", map[string]*pb.Value{
		"confidence": qutil.Nv(float64(0.9)),
	})

	upsertPoints(ctx, t, client, low, high)

	pr.lowConfidenceScan(ctx)

	count := countNeedsReview(ctx, t, client)
	if count != 1 {
		t.Fatalf("expected 1 low-confidence fact flagged, got %d", count)
	}
}

func TestPrunerIntegration_Scheduler(t *testing.T) {
	if testPrunerSkipped(t) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := testClient(ctx, t)
	defer cleanupPoints(ctx, t, client)

	pr := testPruner(client, nil)
	pr.cfg.StaleScanInterval = 1 * time.Hour // prevent re-trigger within 3s

	pr.Run(ctx)

	hr := pr.Health()
	if !hr.Enabled {
		t.Error("expected pruner to be enabled")
	}
	if len(hr.Scans) == 0 {
		t.Error("expected at least one scan in health report")
	}
}

func TestPrunerIntegration_ExpiredScan(t *testing.T) {
	if testPrunerSkipped(t) {
		return
	}
	ctx := context.Background()
	client := testClient(ctx, t)
	defer cleanupPoints(ctx, t, client)

	pr := testPruner(client, nil)

	pastTime := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)

	expired := testPoint("fact/expired-temporal", "temporal expiry", map[string]*pb.Value{
		"valid_until": qutil.Nv(pastTime),
	})
	noExpiry := testPoint("fact/no-expiry", "no boundary", nil)

	upsertPoints(ctx, t, client, expired, noExpiry)

	pr.expiredScan(ctx)

	count := countNeedsReview(ctx, t, client)
	if count != 1 {
		t.Fatalf("expected 1 expired temporal fact flagged, got %d", count)
	}
}
