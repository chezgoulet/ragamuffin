package extraction

import (
	"sync"
	"sync/atomic"
	"time"
)

// Stats tracks extraction pipeline metrics via atomic counters.
type Stats struct {
	attempted      atomic.Uint64
	created        atomic.Uint64
	skipped        atomic.Uint64
	rejected       atomic.Uint64
	mu             sync.Mutex
	confidenceSum  float64
	confidenceN    int64
	lastExtraction atomic.Value // time.Time
}

// StatsSnapshot is a point-in-time view of pipeline statistics.
type StatsSnapshot struct {
	TotalAttempted      uint64    `json:"total_attempted"`
	FactsCreated        uint64    `json:"facts_created"`
	FactsSkipped        uint64    `json:"facts_skipped"`
	FactsRejected       uint64    `json:"facts_rejected"`
	AvgConfidence       float64   `json:"avg_confidence"`
	LastExtraction      string    `json:"last_extraction,omitempty"`
}

// NewStats creates a new Stats instance.
func NewStats() *Stats {
	s := &Stats{}
	s.lastExtraction.Store(time.Time{})
	return s
}

func (s *Stats) IncAttempted()                    { s.attempted.Add(1) }
func (s *Stats) IncCreated()                      { s.created.Add(1) }
func (s *Stats) IncSkipped()                      { s.skipped.Add(1) }
func (s *Stats) IncRejected()                     { s.rejected.Add(1) }
func (s *Stats) SetLastExtraction(t time.Time)    { s.lastExtraction.Store(t) }

// RecordConfidence records a confidence value for averaging.
func (s *Stats) RecordConfidence(c float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.confidenceSum += c
	s.confidenceN++
}

// Snapshot returns a point-in-time view.
func (s *Stats) Snapshot() StatsSnapshot {
	s.mu.Lock()
	avgC := 0.0
	if s.confidenceN > 0 {
		avgC = s.confidenceSum / float64(s.confidenceN)
	}
	s.mu.Unlock()

	le := s.lastExtraction.Load().(time.Time)
	leStr := ""
	if !le.IsZero() {
		leStr = le.Format(time.RFC3339)
	}

	return StatsSnapshot{
		TotalAttempted: s.attempted.Load(),
		FactsCreated:   s.created.Load(),
		FactsSkipped:   s.skipped.Load(),
		FactsRejected:  s.rejected.Load(),
		AvgConfidence:  avgC,
		LastExtraction: leStr,
	}
}
