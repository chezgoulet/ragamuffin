package procedural

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

// ── Interfaces ─────────────────────────────────────────────────────────────────

// FactWriter is the interface for writing fact points to Qdrant.
type FactWriter interface {
	Upsert(ctx context.Context, points []*pb.PointStruct) error
}

// ── Write ──────────────────────────────────────────────────────────────────────

// Write persists a procedure as a new fact in the given Qdrant collection.
func Write(ctx context.Context, writer FactWriter, proc Procedure, vectorSize uint64) error {
	key := DeriveKey(proc.Name, proc.Trigger)
	now := time.Now().UTC().Format(time.RFC3339)

	valBytes, err := json.Marshal(proc)
	if err != nil {
		return fmt.Errorf("procedural write marshal: %w", err)
	}

	payload := map[string]*pb.Value{
		"fact_key":       qdrantutil.Nv(key),
		"fact_value":     qdrantutil.Nv(string(valBytes)),
		"fact_type":      qdrantutil.Nv(FactTypeProcedure),
		"procedure_name": qdrantutil.Nv(proc.Name),
		"source":         qdrantutil.Nv("session:" + proc.SourceSession),
		"source_type":    qdrantutil.Nv("procedure"),
		"confidence":     qdrantutil.Nv(0.85),
		"status":         qdrantutil.Nv("active"),
		"version":        qdrantutil.Nv(1),
		"created_at":     qdrantutil.Nv(now),
		"updated_at":     qdrantutil.Nv(now),
	}

	point := &pb.PointStruct{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: pointUUID(key),
			},
		},
		Payload: payload,
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{
					Data: make([]float32, vectorSize),
				},
			},
		},
	}

	return writer.Upsert(ctx, []*pb.PointStruct{point})
}

// ── Update ─────────────────────────────────────────────────────────────────────

// Update updates an existing procedure fact by incrementing success_count
// and updating last_used. Returns the updated fact key.
func Update(ctx context.Context, writer FactWriter, proc Procedure, vectorSize uint64) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	proc.LastUsed = now
	proc.SuccessCount++

	key := DeriveKey(proc.Name, proc.Trigger)

	valBytes, err := json.Marshal(proc)
	if err != nil {
		return "", fmt.Errorf("procedural update marshal: %w", err)
	}

	payload := map[string]*pb.Value{
		"fact_key":       qdrantutil.Nv(key),
		"fact_value":     qdrantutil.Nv(string(valBytes)),
		"fact_type":      qdrantutil.Nv(FactTypeProcedure),
		"procedure_name": qdrantutil.Nv(proc.Name),
		"updated_at":     qdrantutil.Nv(now),
	}

	point := &pb.PointStruct{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: pointUUID(key),
			},
		},
		Payload: payload,
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{
					Data: make([]float32, vectorSize),
				},
			},
		},
	}

	return key, writer.Upsert(ctx, []*pb.PointStruct{point})
}

// ── UUID Generation ────────────────────────────────────────────────────────────

// pointUUID generates a deterministic UUID-style string from a key hash.
// Uses SHA-256 truncated to 16 bytes, formatted as UUID.
func pointUUID(key string) string {
	h := sha256.Sum256([]byte(key))
	b := h[:16]
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
