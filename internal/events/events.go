// Package events defines CloudEvents v1.0 structs and helpers for
// Ragamuffin vault change events. Zero external dependencies — pure
// encoding/json.
package events

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// CloudEvent is a minimal CloudEvents v1.0 envelope.
// https://github.com/cloudevents/spec/blob/v1.0/cloudevents/spec.md
type CloudEvent struct {
	SpecVersion string      `json:"specversion"`
	ID          string      `json:"id"`
	Source      string      `json:"source"`
	Type        string      `json:"type"`
	Time        string      `json:"time"`
	Data        any `json:"data,omitempty"`
}

// Event types Ragamuffin emits.
const (
	TypeFileChanged      = "vault.file.changed"
	TypeFileDeleted      = "vault.file.deleted"
	TypeCollectionIndex  = "vault.collection.reindexed"
	TypeServerStarted    = "ragamuffin.started"
	TypeServerHealthy    = "ragamuffin.healthy"
	TypeFactCreated      = "fact.created"
	TypeFactFlagged      = "fact.flagged"
	TypeFactReviewed     = "fact.reviewed"
	TypePrunerComplete   = "pruner.scan.complete"
)

// FileChangedData is the payload for vault.file.changed.
type FileChangedData struct {
	Path   string `json:"path"`
	Action string `json:"action"` // "created" or "modified"
	Size   int64  `json:"size,omitempty"`
	Hash   string `json:"hash,omitempty"`
}

// FileDeletedData is the payload for vault.file.deleted.
type FileDeletedData struct {
	Path string `json:"path"`
}

// CollectionIndexData is the payload for vault.collection.reindexed.
type CollectionIndexData struct {
	Vault      string `json:"vault"`
	FileCount  int    `json:"file_count"`
	ChunkCount int    `json:"chunk_count"`
	Duration   string `json:"duration"`
}

// FactCreatedData is the payload for fact.created.
type FactCreatedData struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Source     string  `json:"source,omitempty"`
	Vault      string  `json:"vault,omitempty"`
	Confidence float64 `json:"confidence"`
}

// FactFlaggedData is the payload for fact.flagged.
type FactFlaggedData struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// FactReviewedData is the payload for fact.reviewed.
type FactReviewedData struct {
	Key    string `json:"key"`
	Action string `json:"action"` // approve, reject, reclassify
}

// PrunerCompleteData is the payload for pruner.scan.complete.
type PrunerCompleteData struct {
	ScanName string `json:"scan_name"`
	Duration string `json:"duration"`
	Flagged  int    `json:"flagged"`
}

// ServerStartedData is the payload for ragamuffin.started.
type ServerStartedData struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	GoVersion string `json:"go_version"`
	Host    string `json:"host"`
	Port    string `json:"port"`
}

// New creates a CloudEvent with required fields populated.
func New(eventType, source string, data any) CloudEvent {
	return CloudEvent{
		SpecVersion: "1.0",
		ID:          uuidV7(),
		Source:      source,
		Type:        eventType,
		Time:        time.Now().UTC().Format(time.RFC3339),
		Data:        data,
	}
}

// MarshalJSON returns the JSON byte slice for the event.
func (e CloudEvent) MarshalJSON() ([]byte, error) {
	// Reuse the struct's json tags via an alias to avoid recursion
	type alias CloudEvent
	return json.Marshal(alias(e))
}

// String returns the JSON string for logging.
func (e CloudEvent) String() string {
	b, err := e.MarshalJSON()
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err)
	}
	return string(b)
}

// uuidV7 generates a time-ordered UUID v7 string.
// Uses crypto/rand — zero external dependencies.
func uuidV7() string {
	b := make([]byte, 16)
	// Read 16 random bytes (best-effort; zero fill on failure is fine for uniqueness)
	rand.Read(b)

	// Encode timestamp (ms) in first 6 bytes
	ts := uint64(time.Now().UnixMilli())
	b[0] = byte(ts >> 40)
	b[1] = byte(ts >> 32)
	b[2] = byte(ts >> 24)
	b[3] = byte(ts >> 16)
	b[4] = byte(ts >> 8)
	b[5] = byte(ts)

	// Set version 7 (0x70) and variant RFC 4122 (0x80)
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
