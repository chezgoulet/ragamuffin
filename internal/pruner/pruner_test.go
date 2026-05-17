package pruner

import (
	"math"
	"testing"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

// ── parseVersionedKey tests ─────────────────────────────────────────────

func TestParseVersionedKey(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantPre  string
		wantVer  int
	}{
		{name: "v2 in middle", key: "org/v2/decision", wantPre: "org", wantVer: 2},
		{name: "v1 at start", key: "v1/thing", wantPre: "", wantVer: 1},
		{name: "v10 multi-digit", key: "project/feature/v10/rule", wantPre: "project/feature", wantVer: 10},
		{name: "no version", key: "org/thing/decision", wantPre: "", wantVer: 0},
		{name: "plain key", key: "just-a-key", wantPre: "", wantVer: 0},
		{name: "v at end", key: "org/thing/v3", wantPre: "org/thing", wantVer: 3},
		{name: "v0 rejected", key: "org/v0/thing", wantPre: "", wantVer: 0},
		{name: "non-numeric version", key: "org/vx/thing", wantPre: "", wantVer: 0},
		{name: "empty key", key: "", wantPre: "", wantVer: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPre, gotVer := parseVersionedKey(tt.key)
			if gotPre != tt.wantPre {
				t.Errorf("parseVersionedKey(%q) prefix = %q, want %q", tt.key, gotPre, tt.wantPre)
			}
			if gotVer != tt.wantVer {
				t.Errorf("parseVersionedKey(%q) version = %d, want %d", tt.key, gotVer, tt.wantVer)
			}
		})
	}
}

// ── cosineSimilarity tests ──────────────────────────────────────────────

func TestCosineSimilarity(t *testing.T) {
	t.Run("identical vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{1, 0, 0}
		got := cosineSimilarity(a, b)
		if math.Abs(got-1.0) > 0.0001 {
			t.Errorf("cosineSimilarity = %f, want 1.0", got)
		}
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{0, 1, 0}
		got := cosineSimilarity(a, b)
		if math.Abs(got) > 0.0001 {
			t.Errorf("cosineSimilarity = %f, want 0.0", got)
		}
	})

	t.Run("opposite vectors", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{-1, 0}
		got := cosineSimilarity(a, b)
		if math.Abs(got+1.0) > 0.0001 {
			t.Errorf("cosineSimilarity = %f, want -1.0", got)
		}
	})

	t.Run("zero vector", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 0, 0}
		got := cosineSimilarity(a, b)
		if got != 0 {
			t.Errorf("cosineSimilarity = %f, want 0", got)
		}
	})

	t.Run("mismatched lengths", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{1, 0, 0}
		got := cosineSimilarity(a, b)
		if got != 0 {
			t.Errorf("cosineSimilarity = %f, want 0", got)
		}
	})

	t.Run("partial similarity", func(t *testing.T) {
		a := []float32{3, 4, 0}
		b := []float32{3, 4, 5}
		got := cosineSimilarity(a, b)
		// dot=(9+16)=25, |a|=5, |b|=sqrt(9+16+25)=sqrt(50)=7.07
		// similarity = 25/(5*7.07) = 25/35.36 = 0.707
		want := 25.0 / (5.0 * math.Sqrt(50))
		if math.Abs(got-want) > 0.0001 {
			t.Errorf("cosineSimilarity = %f, want %f", got, want)
		}
	})
}

// ── getPayload helpers tests ────────────────────────────────────────────

func TestGetPayloadString(t *testing.T) {
	payload := map[string]*pb.Value{
		"name": pb.NewValue("alice"),
	}
	got, ok := getPayloadString(payload, "name")
	if !ok || got != "alice" {
		t.Errorf("getPayloadString = %q, %v, want %q, true", got, ok, "alice")
	}

	_, ok = getPayloadString(payload, "missing")
	if ok {
		t.Error("getPayloadString for missing key returned ok=true")
	}

	_, ok = getPayloadString(nil, "name")
	if ok {
		t.Error("getPayloadString for nil payload returned ok=true")
	}
}

func TestGetPayloadFloat(t *testing.T) {
	payload := map[string]*pb.Value{
		"score": pb.NewValue(0.85),
	}
	got, ok := getPayloadFloat(payload, "score")
	if !ok || math.Abs(got-0.85) > 0.0001 {
		t.Errorf("getPayloadFloat = %f, %v, want 0.85, true", got, ok)
	}

	_, ok = getPayloadFloat(payload, "missing")
	if ok {
		t.Error("getPayloadFloat for missing key returned ok=true")
	}
}

func TestGetPayloadInt(t *testing.T) {
	payload := map[string]*pb.Value{
		"ttl_days": pb.NewValue(90.0),
	}
	got, ok := getPayloadInt(payload, "ttl_days")
	if !ok || got != 90 {
		t.Errorf("getPayloadInt = %d, %v, want 90, true", got, ok)
	}

	_, ok = getPayloadInt(payload, "missing")
	if ok {
		t.Error("getPayloadInt for missing key returned ok=true")
	}
}

func TestGetPayloadStringList(t *testing.T) {
	t.Run("string value", func(t *testing.T) {
		payload := map[string]*pb.Value{
			"tag": pb.NewValue("single"),
		}
		got := getPayloadStringList(payload, "tag")
		if len(got) != 1 || got[0] != "single" {
			t.Errorf("getPayloadStringList = %v, want [single]", got)
		}
	})

	t.Run("list value", func(t *testing.T) {
		tagVals := []*pb.Value{
			pb.NewValue("a"),
			pb.NewValue("b"),
			pb.NewValue("c"),
		}
		v := &pb.Value{
			Kind: &pb.Value_ListValue{
				ListValue: &pb.ListValue{Values: tagVals},
			},
		}
		payload := map[string]*pb.Value{
			"contradicts": v,
		}
		got := getPayloadStringList(payload, "contradicts")
		if len(got) != 3 || got[0] != "a" {
			t.Errorf("getPayloadStringList = %v, want [a b c]", got)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		got := getPayloadStringList(nil, "nonexistent")
		if got != nil {
			t.Errorf("getPayloadStringList = %v, want nil", got)
		}
	})
}

// ── Health / Metrics tests ──────────────────────────────────────────────

func TestPrunerHealth_Disabled(t *testing.T) {
	p := New(nil, nil, nil, nil, nil, DefaultConfig())
	hr := p.Health()
	if hr.Enabled {
		t.Error("expected Health().Enabled = false")
	}
	if len(hr.Scans) == 0 {
		t.Error("expected Health().Scans to contain scan definitions")
	}
}

func TestPrunerHealth_Enabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StaleScanInterval = time.Hour
	cfg.ConflictScanInterval = 2 * time.Hour
	cfg.SupersedeScanInterval = 3 * time.Hour

	p := New(nil, nil, nil, nil, nil, cfg)
	hr := p.Health()
	if !hr.Enabled {
		t.Error("expected Health().Enabled = true")
	}
	if _, ok := hr.Scans["StaleScan"]; !ok {
		t.Error("expected StaleScan in health scans")
	}
}

func TestRecordFlaggedAndResolved(t *testing.T) {
	p := New(nil, nil, nil, nil, nil, DefaultConfig())
	p.RecordFlagged(5)
	p.RecordFlagged(3)
	p.RecordResolved(2)

	_, flagged, resolved := p.Metrics()
	if flagged != 8 {
		t.Errorf("flagged = %d, want 8", flagged)
	}
	if resolved != 2 {
		t.Errorf("resolved = %d, want 2", resolved)
	}
}

// ── Stale filter tests ────────────────────────────────────────────────

func TestStaleFilter_NowInFuture(t *testing.T) {
	filter := staleFilter(1000000.0)
	if filter == nil {
		t.Fatal("staleFilter returned nil")
	}
	if len(filter.Must) != 3 {
		t.Fatalf("staleFilter: got %d conditions, want 3", len(filter.Must))
	}
}

// verify the staleFilter conditions match expected field keys
func TestStaleFilter_FieldKeys(t *testing.T) {
	filter := staleFilter(100.0)
	var keys []string
	for _, c := range filter.Must {
		if fc := c.GetField(); fc != nil {
			keys = append(keys, fc.Key)
		}
	}
	expected := []string{"status", "ttl_days", "expires_at_unix"}
	if len(keys) != len(expected) {
		t.Fatalf("staleFilter field keys = %v, want %v", keys, expected)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("staleFilter field[%d] = %q, want %q", i, k, expected[i])
		}
	}
}

// ── Low-confidence filter tests ────────────────────────────────────────

func TestLowConfidenceFilter_Threshold(t *testing.T) {
	filter := lowConfidenceFilter(0.5)
	if filter == nil {
		t.Fatal("lowConfidenceFilter returned nil")
	}
	if len(filter.Must) != 2 {
		t.Fatalf("lowConfidenceFilter: got %d conditions, want 2", len(filter.Must))
	}
}

func TestLowConfidenceFilter_FieldKeys(t *testing.T) {
	filter := lowConfidenceFilter(0.5)
	var keys []string
	for _, c := range filter.Must {
		if fc := c.GetField(); fc != nil {
			keys = append(keys, fc.Key)
		}
	}
	expected := []string{"status", "confidence"}
	if len(keys) != len(expected) {
		t.Fatalf("lowConfidenceFilter field keys = %v, want %v", keys, expected)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("lowConfidenceFilter field[%d] = %q, want %q", i, k, expected[i])
		}
	}
}

func TestLowConfidenceFilter_ConfidenceRange(t *testing.T) {
	filter := lowConfidenceFilter(0.5)
	// The filter should have confidence < 0.5 - 0.001 = 0.499
	rng := filter.Must[1].GetField().GetRange()
	if rng == nil {
		t.Fatal("expected a Range condition on confidence")
	}
	if rng.Lt == nil {
		t.Fatal("expected Lt on confidence range")
	}
	if *rng.Lt > 0.5 || *rng.Lt < 0.49 {
		t.Errorf("confidence Lt = %f, want ~0.499", *rng.Lt)
	}
}

func TestMetrics_AfterScans(t *testing.T) {
	p := New(nil, nil, nil, nil, nil, DefaultConfig())
	p.recordScanRun("StaleScan")
	p.recordScanRun("StaleScan")
	p.recordScanRun("ConflictScan")

	counts, _, _ := p.Metrics()
	if counts["StaleScan"] != 2 {
		t.Errorf("StaleScan count = %d, want 2", counts["StaleScan"])
	}
	if counts["ConflictScan"] != 1 {
		t.Errorf("ConflictScan count = %d, want 1", counts["ConflictScan"])
	}
}
