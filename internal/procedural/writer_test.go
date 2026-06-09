package procedural

import (
	"context"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
)

// ── Mock Writer ────────────────────────────────────────────────────────────────

type mockFactWriter struct {
	upserts []*pb.PointStruct
	err     error
}

func (m *mockFactWriter) Upsert(ctx context.Context, points []*pb.PointStruct) error {
	if m.err != nil {
		return m.err
	}
	m.upserts = append(m.upserts, points...)
	return nil
}

// ── Tests ──────────────────────────────────────────────────────────────────────

func TestWriteProcedure(t *testing.T) {
	writer := &mockFactWriter{}
	proc := NewProcedure(
		"Fix nginx after syntax error",
		"nginx -t fails with unknown directive",
		[]string{
			"Run nginx -t to check syntax",
			"Read /etc/nginx/nginx.conf for errors",
			"Fix the syntax error",
			"Reload nginx with systemctl reload nginx",
			"Verify with curl -sI http://localhost | head -1",
		},
		"session-abc123",
	)

	err := Write(context.Background(), writer, proc, 4)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if len(writer.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(writer.upserts))
	}

	point := writer.upserts[0]
	if point.Id == nil {
		t.Fatal("expected non-nil point ID")
	}

	// Verify payload fields
	payload := point.GetPayload()
	if payload == nil {
		t.Fatal("expected non-nil payload")
	}

	key, ok := payload["fact_key"]
	if !ok {
		t.Fatal("expected fact_key in payload")
	}
	if key.GetStringValue() == "" {
		t.Error("expected non-empty fact_key")
	}

	factType, ok := payload["fact_type"]
	if !ok {
		t.Fatal("expected fact_type in payload")
	}
	if factType.GetStringValue() != FactTypeProcedure {
		t.Errorf("expected fact_type=%q, got %q", FactTypeProcedure, factType.GetStringValue())
	}
}

func TestWriteEmptySteps(t *testing.T) {
	writer := &mockFactWriter{}
	proc := NewProcedure(
		"Empty procedure",
		"test",
		nil,
		"session-test",
	)

	err := Write(context.Background(), writer, proc, 4)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(writer.upserts))
	}
}

func TestWriteWithVectorSize(t *testing.T) {
	writer := &mockFactWriter{}
	proc := NewProcedure(
		"Test procedure",
		"test",
		[]string{"step 1", "step 2", "step 3"},
		"session-test",
	)

	// Test with different vector sizes
	for _, vs := range []uint64{4, 384, 768} {
		writer.upserts = nil
		err := Write(context.Background(), writer, proc, vs)
		if err != nil {
			t.Fatalf("Write with vectorSize=%d failed: %v", vs, err)
		}
		point := writer.upserts[0]
		vec := point.GetVectors().GetVector().GetData()
		if len(vec) != int(vs) {
			t.Errorf("expected vector length %d, got %d", vs, len(vec))
		}
	}
}

func TestUpdateProcedure(t *testing.T) {
	writer := &mockFactWriter{}
	proc := NewProcedure(
		"Fix nginx",
		"nginx broken",
		[]string{"check", "fix", "verify"},
		"session-abc123",
	)

	key, err := Update(context.Background(), writer, proc, 4)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if key == "" {
		t.Error("expected non-empty key from Update")
	}

	if len(writer.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(writer.upserts))
	}
}

func TestDeriveKeyPrefix(t *testing.T) {
	key := DeriveKey("Fix nginx", "nginx broken")
	if len(key) < 10 {
		t.Errorf("expected key length >= 10, got %d (%q)", len(key), key)
	}
}

func TestPointUUIDDeterministic(t *testing.T) {
	u1 := pointUUID("procedure-abc123")
	u2 := pointUUID("procedure-abc123")
	if u1 != u2 {
		t.Errorf("expected deterministic UUIDs, got %q and %q", u1, u2)
	}
}

func TestPointUUIDDifferent(t *testing.T) {
	u1 := pointUUID("procedure-abc")
	u2 := pointUUID("procedure-xyz")
	if u1 == u2 {
		t.Error("expected different UUIDs for different keys")
	}
}

func TestWriteWriterError(t *testing.T) {
	writer := &mockFactWriter{err: assertAnError("upsert failed")}
	proc := NewProcedure("test", "test", []string{"a", "b", "c"}, "session-test")

	err := Write(context.Background(), writer, proc, 4)
	if err == nil {
		t.Fatal("expected error from writer, got nil")
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────────

type assertAnError string

func (e assertAnError) Error() string { return string(e) }
