// Package qdrant provides a gRPC client for Qdrant vector database.
//
// ── Vector Utilities ───────────────────────────────────────────────────
//
// IsZeroVector and GetPointVector were extracted from duplicate copies
// across pruner/reembed.go, server/server.go, and extraction/extractor.go.
// See docs/specs/CONSOLIDATION-vector-utils.md for details.

package qdrant

import pb "github.com/qdrant/go-client/qdrant"

// IsZeroVector returns true if all elements of the vector are zero.
func IsZeroVector(vec []float32) bool {
	for _, v := range vec {
		if v != 0 {
			return false
		}
	}
	return true
}

// GetPointVector extracts the vector data from a RetrievedPoint.
// Returns nil if the point or its vector data is nil.
func GetPointVector(pt *pb.RetrievedPoint) []float32 {
	if pt == nil {
		return nil
	}
	vecs := pt.GetVectors()
	if vecs == nil {
		return nil
	}
	vec := vecs.GetVector()
	if vec == nil {
		return nil
	}
	return vec.GetData()
}
