package testutil

import (
	"time"

	qdrant "github.com/qdrant/go-client/qdrant"
)

// NewFact creates a single point with the given key, value, source, and age.
func NewFact(key, value, source string, ageDays int) *qdrant.PointStruct {
	points := NewFacts([]FactInput{{
		Key: key, Value: value, Source: source, AgeDays: ageDays,
	}})
	if len(points) > 0 {
		return points[0]
	}
	return nil
}

// FactInput describes a single test fact payload.
type FactInput struct {
	Key      string
	Value    string
	Source   string
	AgeDays  int
	LinksTo  []string
	Metadata map[string]string
}

// NewFacts creates multiple test fact points with realistic payloads.
// Uses qdrant.NewValueMap / qdrant.NewID to mirror production point construction.
func NewFacts(inputs []FactInput) []*qdrant.PointStruct {
	points := make([]*qdrant.PointStruct, 0, len(inputs))
	for _, in := range inputs {
		ts := time.Now().AddDate(0, 0, -in.AgeDays).Format(time.RFC3339)
		payload := map[string]any{
			"key":         in.Key,
			"value":       in.Value,
			"source_file": in.Source,
			"timestamp":   ts,
			"text":        in.Key + ": " + in.Value,
		}
		if len(in.LinksTo) > 0 {
			links := make([]any, len(in.LinksTo))
			for i, l := range in.LinksTo {
				links[i] = l
			}
			payload["links_to"] = links
		}
		for k, v := range in.Metadata {
			payload[k] = v
		}
		points = append(points, &qdrant.PointStruct{
			Id:      qdrant.NewID(in.Key),
			Payload: qdrant.NewValueMap(payload),
		})
	}
	return points
}

// StaleFact creates a fact older than the pruner threshold (90 days by default).
func StaleFact(key, value, source string) *qdrant.PointStruct {
	return NewFact(key, value, source, 100)
}

// FreshFact creates a recently-written fact.
func FreshFact(key, value, source string) *qdrant.PointStruct {
	return NewFact(key, value, source, 1)
}

// ContradictoryPair creates two facts with different values for the same key.
func ContradictoryPair(key, oldValue, newValue, source string) []*qdrant.PointStruct {
	return []*qdrant.PointStruct{
		NewFact(key, oldValue, newValue+" (old)", source, 30),
		NewFact(key, newValue, source, 1),
	}
}
