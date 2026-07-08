package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
)

// ── Request / Response types ──────────────────────────────────────────────

// exportPoint is the JSON-friendly representation of a single Qdrant point
// for export. Vectors contain the unnamed vector data; payload is a flat
// map of JSON-compatible types (string, float64, bool, []any).
type exportPoint struct {
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload,omitempty"`
	Vectors []float32      `json:"vectors,omitempty"`
}

type exportResponse struct {
	Collection string        `json:"collection"`
	Points     []exportPoint `json:"points"`
	Total      int            `json:"total"`
}

// importRequest accepts the same shape as exportResponse.points.
type importRequest struct {
	Points []importPoint `json:"points"`
}

type importPoint struct {
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload,omitempty"`
	Vectors []float32      `json:"vectors,omitempty"`
}

// ── Export handler ────────────────────────────────────────────────────────

// handleVaultExport scrolls all points from the vault's Qdrant collection
// and returns them as a portable JSON array.
func (s *Server) handleVaultExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use GET")
		return
	}

	if s.qdrant == nil {
		writeError(w, 503, "QDRANT_UNAVAILABLE", "Qdrant not connected")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	var allPoints []exportPoint

	// Paginate through all points via Scroll API
	var offset *pb.PointId
	for {
		scrollCtx, scrollCancel := context.WithTimeout(ctx, 30*time.Second)
		points, nextOffset, err := s.qdrant.Scroll(scrollCtx, 100, offset)
		scrollCancel()
		if err != nil {
			s.log(r.Context()).Error("export: scroll failed", "error", err)
			writeError(w, 502, "SCROLL_FAILED", fmt.Sprintf("scroll failed: %s", err))
			return
		}

		for _, pt := range points {
			ep := exportPoint{
				ID:      pointIDToString(pt.GetId()),
				Payload: payloadToMap(pt.GetPayload()),
				Vectors: extractVectorData(pt.GetVectors()),
			}
			allPoints = append(allPoints, ep)
		}

		if nextOffset == nil {
			break
		}
		offset = nextOffset
	}

	writeJSON(w, 200, exportResponse{
		Collection: s.qdrant.Collection(),
		Points:     allPoints,
		Total:      len(allPoints),
	})
}

// ── Import handler ────────────────────────────────────────────────────────

// handleVaultImport accepts a JSON array of points and batch-upserts them
// into the vault's Qdrant collection.
func (s *Server) handleVaultImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	if s.qdrant == nil {
		writeError(w, 503, "QDRANT_UNAVAILABLE", "Qdrant not connected")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024*1024) // 64 MB limit

	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid JSON body: %s", err))
		return
	}

	if len(req.Points) == 0 {
		writeError(w, 400, "INVALID_REQUEST", "at least one point is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	// Convert import points to protobuf PointStructs
	points := make([]*pb.PointStruct, 0, len(req.Points))
	for _, ip := range req.Points {
		ptID, err := stringToPointID(ip.ID)
		if err != nil {
			writeError(w, 400, "INVALID_POINT_ID", fmt.Sprintf("invalid point id %q: %s", ip.ID, err))
			return
		}

		point := &pb.PointStruct{
			Id:      ptID,
			Payload: mapToPayload(ip.Payload),
		}

		if len(ip.Vectors) > 0 {
			point.Vectors = &pb.Vectors{
				VectorsOptions: &pb.Vectors_Vector{
					Vector: &pb.Vector{
						Data: ip.Vectors,
					},
				},
			}
		}

		points = append(points, point)
	}

	// Batch upsert in chunks of 100 to avoid large gRPC payloads
	batchSize := 100
	for i := 0; i < len(points); i += batchSize {
		end := i + batchSize
		if end > len(points) {
			end = len(points)
		}

		batchCtx, batchCancel := context.WithTimeout(ctx, 60*time.Second)
		err := s.qdrant.Upsert(batchCtx, points[i:end])
		batchCancel()
		if err != nil {
			s.log(r.Context()).Error("import: upsert batch failed", "offset", i, "error", err)
			writeError(w, 502, "UPSERT_FAILED", fmt.Sprintf("upsert failed at point %d: %s", i, err))
			return
		}
	}

	writeJSON(w, 200, map[string]any{
		"imported": len(points),
		"status":   "ok",
	})
}

// ── Payload conversion helpers ────────────────────────────────────────────

// payloadToMap converts a Qdrant protobuf payload map to a plain
// map[string]any suitable for JSON encoding.
func payloadToMap(payload map[string]*pb.Value) map[string]any {
	if payload == nil {
		return nil
	}
	result := make(map[string]any, len(payload))
	for k, v := range payload {
		if v == nil {
			continue
		}
		result[k] = valueToAny(v)
	}
	return result
}

// valueToAny converts a single Qdrant Value to its Go representation.
func valueToAny(v *pb.Value) any {
	switch kind := v.Kind.(type) {
	case *pb.Value_StringValue:
		return kind.StringValue
	case *pb.Value_IntegerValue:
		return float64(kind.IntegerValue) // JSON numbers are float64
	case *pb.Value_DoubleValue:
		return kind.DoubleValue
	case *pb.Value_BoolValue:
		return kind.BoolValue
	case *pb.Value_ListValue:
		if kind.ListValue == nil {
			return nil
		}
		items := make([]any, len(kind.ListValue.Values))
		for i, item := range kind.ListValue.Values {
			items[i] = valueToAny(item)
		}
		return items
	case *pb.Value_NullValue:
		return nil
	default:
		return nil
	}
}

// mapToPayload converts a plain map to a Qdrant protobuf payload map.
func mapToPayload(m map[string]any) map[string]*pb.Value {
	if m == nil {
		return nil
	}
	result := make(map[string]*pb.Value, len(m))
	for k, v := range m {
		result[k] = anyToValue(v)
	}
	return result
}

// anyToValue converts a Go value to a Qdrant protobuf Value.
func anyToValue(v any) *pb.Value {
	switch val := v.(type) {
	case string:
		return &pb.Value{Kind: &pb.Value_StringValue{StringValue: val}}
	case float64:
		return &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: val}}
	case bool:
		return &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: val}}
	case []any:
		items := make([]*pb.Value, len(val))
		for i, item := range val {
			items[i] = anyToValue(item)
		}
		return &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: items}}}
	case map[string]any:
		// Qdrant payload doesn't support nested objects natively, but
		// convert recursively for forward compatibility
		nested := make([]*pb.Value, 0, len(val))
		for _, mapVal := range val {
			nested = append(nested, anyToValue(mapVal))
		}
		return &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: nested}}}
	default:
		return &pb.Value{Kind: &pb.Value_NullValue{}}
	}
}

// ── Vector conversion helpers ─────────────────────────────────────────────

// extractVectorData extracts the unnamed vector data from a Vectors struct.
// Returns nil if the point has named vectors or no vectors.
func extractVectorData(v *pb.Vectors) []float32 {
	if v == nil {
		return nil
	}
	if vec, ok := v.VectorsOptions.(*pb.Vectors_Vector); ok && vec != nil && vec.Vector != nil {
		return vec.Vector.Data
	}
	return nil
}

// ── Point ID conversion helpers ───────────────────────────────────────────

// pointIDToString converts a protobuf PointId to its string representation.
func pointIDToString(id *pb.PointId) string {
	if id == nil {
		return ""
	}
	switch opt := id.PointIdOptions.(type) {
	case *pb.PointId_Uuid:
		return opt.Uuid
	case *pb.PointId_Num:
		return strconv.FormatUint(opt.Num, 10)
	default:
		return ""
	}
}

// stringToPointID converts a string ID to a protobuf PointId.
// Numeric strings are stored as Num, everything else as UUID.
func stringToPointID(s string) (*pb.PointId, error) {
	if s == "" {
		return nil, fmt.Errorf("empty point id")
	}
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return &pb.PointId{
			PointIdOptions: &pb.PointId_Num{Num: n},
		}, nil
	}
	return &pb.PointId{
		PointIdOptions: &pb.PointId_Uuid{Uuid: s},
	}, nil
}
