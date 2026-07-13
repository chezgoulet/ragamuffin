package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"

	"github.com/chezgoulet/ragamuffin/internal/auth"
)

// exportChunk is a single chunk in the export format (#788).
type exportChunk struct {
	ChunkID         string    `json:"chunk_id"`
	SourceFile      string    `json:"source_file"`
	Text            string    `json:"text"`
	FirstParagraph  string    `json:"first_paragraph"`
	Header          string    `json:"header"`
	ChunkIndex      int       `json:"chunk_index"`
	FileLastUpdated string    `json:"file_last_updated"`
	Vector          []float32 `json:"vector,omitempty"`
}

// mustVal builds a *pb.Value from v, panicking on type errors.
// Safe for the known types we pass (string, float64).
func mustVal(v any) *pb.Value {
	val, err := pb.NewValue(v)
	if err != nil {
		panic("mustVal: " + err.Error())
	}
	return val
}

// handleExport streams all chunks from a vault as JSON (#788).
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only GET is accepted")
		return
	}

	vaultName := vaultNameFromRequest(r)
	if vaultName == "" {
		writeError(w, 400, "INVALID_REQUEST", "vault name is required")
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
		return
	}

	ctx := r.Context()
	chunks := make([]exportChunk, 0)
	const pageSize uint32 = 200
	var scrollOffset *pb.PointId

	for {
		points, nextOffset, err := qc.Scroll(ctx, pageSize, scrollOffset)
		if err != nil {
			writeError(w, 502, "SCROLL_FAILED", fmt.Sprintf("scroll failed: %v", err))
			return
		}
		for _, p := range points {
			payload := p.GetPayload()
			c := exportChunk{
				ChunkID: p.Id.GetUuid(),
			}
			if v, ok := payload["source_file"]; ok { c.SourceFile = v.GetStringValue() }
			if v, ok := payload["text"]; ok { c.Text = v.GetStringValue() }
			if v, ok := payload["first_paragraph"]; ok { c.FirstParagraph = v.GetStringValue() }
			if v, ok := payload["header"]; ok { c.Header = v.GetStringValue() }
			if v, ok := payload["chunk_index"]; ok { c.ChunkIndex = int(v.GetIntegerValue()) }
			if v, ok := payload["file_last_updated"]; ok { c.FileLastUpdated = v.GetStringValue() }
			chunks = append(chunks, c)
		}
		if nextOffset == nil {
			break
		}
		scrollOffset = nextOffset
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"vault":  vaultName,
		"count":  len(chunks),
		"chunks": chunks,
	})
}

// handleImport restores chunks into a vault from JSON (#788).
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	vaultName := vaultNameFromRequest(r)
	if vaultName == "" {
		writeError(w, 400, "INVALID_REQUEST", "vault name is required")
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil && !claims.HasAccess("write") {
		writeError(w, 403, "FORBIDDEN", "write access required")
		return
	}

	var req struct {
		Chunks []exportChunk `json:"chunks"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 100*1024*1024)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if len(req.Chunks) == 0 {
		writeError(w, 400, "INVALID_REQUEST", "chunks array is required")
		return
	}
	if len(req.Chunks) > 100000 {
		writeError(w, 400, "INVALID_REQUEST", "maximum 100000 chunks per import")
		return
	}

	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		writeError(w, 404, "NOT_FOUND", fmt.Sprintf("vault %q not found", vaultName))
		return
	}

	ctx := r.Context()
	points := make([]*pb.PointStruct, 0, len(req.Chunks))
	for _, c := range req.Chunks {
		payload := make(map[string]*pb.Value)
		if c.Text != "" {
			payload["text"] = mustVal(c.Text)
		}
		if c.SourceFile != "" {
			payload["source_file"] = mustVal(c.SourceFile)
		}
		if c.FirstParagraph != "" {
			payload["first_paragraph"] = mustVal(c.FirstParagraph)
		}
		if c.Header != "" {
			payload["header"] = mustVal(c.Header)
		}
		payload["chunk_index"] = mustVal(float64(c.ChunkIndex))
		if c.FileLastUpdated != "" {
			payload["file_last_updated"] = mustVal(c.FileLastUpdated)
		}

		id := &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{Uuid: uuid.New().String()},
		}

		pt := &pb.PointStruct{
			Id:      id,
			Payload: payload,
		}
		if c.Vector != nil {
			pt.Vectors = &pb.Vectors{
				VectorsOptions: &pb.Vectors_Vector{
					Vector: &pb.Vector{Data: c.Vector},
				},
			}
		}
		points = append(points, pt)
	}

	batchSize := 100
	for i := 0; i < len(points); i += batchSize {
		end := i + batchSize
		if end > len(points) {
			end = len(points)
		}
		if err := qc.Upsert(ctx, points[i:end]); err != nil {
			writeError(w, 502, "UPSERT_FAILED", fmt.Sprintf("batch %d: %v", i/batchSize, err))
			return
		}
	}

	writeJSON(w, 200, map[string]any{
		"vault":    vaultName,
		"imported": len(points),
	})
}
