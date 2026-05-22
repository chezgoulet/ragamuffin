// Package pruner provides a scheduled background process for fact lifecycle
// management — scanning for staleness, contradictions, and supersessions.
//
// The Pruner reads and writes the facts collection (payload-only) and reads
// the vault chunk collection (never modifies). It marks facts with lifecycle
// statuses like "needs_review", "superseded", "rejected" — it never deletes.
//
// Architectural principle (from SPEC-v0.5):
//   The Pruner is a reader and status updater only. It never deletes facts.
//   It marks them superseded, rejected, needs_review, or adjusts their
//   confidence_score and expires_at. The storage layer remains the single
//   source of truth; pruning is an annotation layer on top.
package pruner

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"log/slog"

	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	"github.com/chezgoulet/ragamuffin/internal/watcher"
	pb "github.com/qdrant/go-client/qdrant"
)


// ── Config ────────────────────────────────────────────────────────────────────

// PrunerConfig controls the Pruner's scan intervals and thresholds.
// All intervals have sensible defaults; zero values disable that scan type.
type PrunerConfig struct {
	Enabled                bool          // master switch (default false)
	StaleScanInterval      time.Duration // default 24h
	ConflictScanInterval   time.Duration // default 72h
	SupersedeScanInterval  time.Duration // default 24h
	StaleDays              int           // default 90 — facts past this TTL expiry are flagged
	ConflictSampleSize     int           // default 50 — pairs per scan cycle
	LowConfidenceThreshold float64       // default 0.5 — below this → needs_review
	ConfidenceBoost        float64       // default 0.1 — added on confirmation via review queue

	// LogScanFn is called after each scan completes. Optional; used by the server
	// to log scan summaries to /v1/logs.
	LogScanFn func(scanName string, duration time.Duration, factsFlagged int, errStr string)
}

// DefaultConfig returns a PrunerConfig with sensible defaults.
func DefaultConfig() PrunerConfig {
	return PrunerConfig{
		Enabled:                false,
		StaleScanInterval:      24 * time.Hour,
		ConflictScanInterval:   72 * time.Hour,
		SupersedeScanInterval:  24 * time.Hour,
		StaleDays:              90,
		ConflictSampleSize:     50,
		LowConfidenceThreshold: 0.5,
		ConfidenceBoost:        0.1,
	}
}

// ── Pruner ────────────────────────────────────────────────────────────────────

// Pruner manages scheduled fact lifecycle scans.
type Pruner struct {
	facts         qdrant.FactStore
	vaultClient   qdrant.FactStore
	embedder      embedding.Embedder
	llm           llm.Synthesizer
	logger        *slog.Logger
	cfg           PrunerConfig
	mu            sync.Mutex
	lastScans     map[string]time.Time // scan name → last run timestamp
	totalScans    map[string]int64     // scan name → total runs
	factsFlagged  int64                // total facts flagged across all scans
	factsResolved int64                // total facts resolved via review queue
}

// ScanHealthReport describes one scan's status for audit reporting.
type ScanHealthReport struct {
	Enabled     bool   `json:"enabled"`
	LastRun     string `json:"last_run,omitempty"`
	TotalRuns   int64  `json:"total_runs"`
	Interval    string `json:"interval"`
}

// HealthReport is the full Pruner health report for audit integration.
type HealthReport struct {
	Enabled          bool                         `json:"enabled"`
	Scans            map[string]ScanHealthReport  `json:"scans"`
	FactsFlaggedTotal int64                       `json:"facts_flagged_total"`
	FactsResolvedTotal int64                      `json:"facts_resolved_total"`
}

// New creates a Pruner. Pass nil for any unused client (e.g., no vault client
// means SupersedeScan skips vault chunk cross-referencing).
func New(facts, vaultClient qdrant.FactStore, ec embedding.Embedder, lm llm.Synthesizer, logger *slog.Logger, cfg PrunerConfig) *Pruner {
	return &Pruner{
		facts:       facts,
		vaultClient: vaultClient,
		embedder:    ec,
		llm:         lm,
		logger:      logger,
		cfg:         cfg,
		lastScans:   make(map[string]time.Time),
		totalScans:  make(map[string]int64),
	}
}

// ── Health (for Audit integration) ────────────────────────────────────────────

// Health returns a thread-safe snapshot of Pruner health for the audit endpoint.
func (p *Pruner) Health() HealthReport {
	p.mu.Lock()
	defer p.mu.Unlock()

	hr := HealthReport{
		Enabled:           p.cfg.Enabled,
		Scans:             make(map[string]ScanHealthReport),
		FactsFlaggedTotal: p.factsFlagged,
		FactsResolvedTotal: p.factsResolved,
	}

	scanDefs := map[string]struct {
		interval time.Duration
	}{
		"StaleScan":       {p.cfg.StaleScanInterval},
		"ConflictScan":    {p.cfg.ConflictScanInterval},
		"SupersedeScan":   {p.cfg.SupersedeScanInterval},
		"LowConfidenceScan": {0},
	}

	for name, def := range scanDefs {
		lastRun := p.lastScans[name]
		rpt := ScanHealthReport{
			Enabled:   def.interval > 0 || name == "LowConfidenceScan",
			TotalRuns: p.totalScans[name],
			Interval:  def.interval.String(),
		}
		if !lastRun.IsZero() {
			rpt.LastRun = lastRun.Format(time.RFC3339)
		}
		hr.Scans[name] = rpt
	}

	return hr
}

// RecordFlagged increments the flagged counter. Called by scan implementations.
func (p *Pruner) RecordFlagged(count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.factsFlagged += int64(count)
}

// RecordResolved increments the resolved counter. Called by the server when
// a review item is resolved via POST /v1/review.
func (p *Pruner) RecordResolved(count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.factsResolved += int64(count)
}

// factsFlaggedCount returns the current flagged count (thread-safe).
func (p *Pruner) factsFlaggedCount() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.factsFlagged
}

// Metrics returns pruner Prometheus-style metric values.
func (p *Pruner) Metrics() (scanCounts map[string]int64, factsFlagged, factsResolved int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	scanCounts = make(map[string]int64, len(p.totalScans))
	for k, v := range p.totalScans {
		scanCounts[k] = v
	}
	return scanCounts, p.factsFlagged, p.factsResolved
}

// ProcessEvents drains watcher file events from the channel. This is a
// placeholder for Phase 6 source-file staleness tracking — for now it
// just drops events with a debug log.
func (p *Pruner) ProcessEvents(ctx context.Context, events <-chan watcher.Event) {
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return
			}
			p.logger.Debug("pruner: file event received (not yet tracked)",
				"path", e.Path, "action", e.Action.String())
		case <-ctx.Done():
			return
		}
	}
}

// ── Scheduler ─────────────────────────────────────────────────────────────────

// Run starts the scan scheduler goroutines. Blocks until ctx is cancelled.
// Each scan runs immediately on start, then at its configured interval.
func (p *Pruner) Run(ctx context.Context) {
	if !p.cfg.Enabled {
		p.logger.Info("pruner disabled, skipping scans")
		<-ctx.Done()
		return
	}

	p.logger.Info("pruner starting",
		"stale_interval", p.cfg.StaleScanInterval,
		"conflict_interval", p.cfg.ConflictScanInterval,
		"supersede_interval", p.cfg.SupersedeScanInterval)

	// Start each scan in its own goroutine
	if p.cfg.StaleScanInterval > 0 {
		go p.runScan(ctx, "StaleScan", p.cfg.StaleScanInterval, p.staleScan)
	}
	if p.cfg.ConflictScanInterval > 0 {
		go p.runScan(ctx, "ConflictScan", p.cfg.ConflictScanInterval, p.conflictScan)
	}
	if p.cfg.SupersedeScanInterval > 0 {
		go p.runScan(ctx, "SupersedeScan", p.cfg.SupersedeScanInterval, p.supersedeScan)
	}

	// Also run a one-time low-confidence scan
	if p.cfg.LowConfidenceThreshold > 0 {
		go p.runScan(ctx, "LowConfidenceScan", 0, p.lowConfidenceScan)
	}

	<-ctx.Done()
	p.logger.Info("pruner shutting down")
}

// runScan runs scanFn immediately, then every interval (if > 0) until ctx done.
// After each run, calls cfg.LogScanFn if configured.
func (p *Pruner) runScan(ctx context.Context, name string, interval time.Duration, scanFn func(context.Context)) {
	p.logger.Info("pruner scan starting", "scan", name)

	run := func() {
		p.recordScanRun(name)
		beforeFlagged := p.factsFlaggedCount()
		start := time.Now()
		scanFn(ctx)
		elapsed := time.Since(start)
		afterFlagged := p.factsFlaggedCount()
		flagged := afterFlagged - beforeFlagged
		if p.cfg.LogScanFn != nil {
			p.cfg.LogScanFn(name, elapsed, int(flagged), "")
		}
	}

	// Run first scan
	run()

	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.logger.Info("pruner scan running", "scan", name)
			run()
		}
	}
}

// recordScanRun records the last run time and increments the counter.
func (p *Pruner) recordScanRun(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastScans[name] = time.Now().UTC()
	p.totalScans[name]++
}

// ── Scan helpers ──────────────────────────────────────────────────────────────

// scrollAllFacts returns all facts from the collection.
func (p *Pruner) scrollAllFacts(ctx context.Context) ([]*pb.RetrievedPoint, error) {
	var all []*pb.RetrievedPoint
	var offset string
	const pageSize uint32 = 200

	for {
		points, err := p.facts.ScrollFiltered(ctx, p.facts.Collection(), nil, pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("scroll facts: %w", err)
		}
		if len(points) == 0 {
			break
		}
		all = append(all, points...)
		if id := points[len(points)-1].GetId().GetUuid(); id != "" {
			offset = id
		} else {
			break
		}
	}
	return all, nil
}

// scrollFilteredFacts returns facts matching the given filter.
// If limit is 0, paginates through all results (up to Qdrant's max scroll).
func (p *Pruner) scrollFilteredFacts(ctx context.Context, filter *pb.Filter, limit uint32) ([]*pb.RetrievedPoint, error) {
	if limit == 0 {
		return p.scrollAllFilteredFacts(ctx, filter)
	}
	return p.facts.ScrollFiltered(ctx, p.facts.Collection(), filter, limit, "")
}

// scrollAllFilteredFacts paginates through ALL facts matching the filter.
func (p *Pruner) scrollAllFilteredFacts(ctx context.Context, filter *pb.Filter) ([]*pb.RetrievedPoint, error) {
	var all []*pb.RetrievedPoint
	var offset string
	const pageSize uint32 = 200

	for {
		points, err := p.facts.ScrollFiltered(ctx, p.facts.Collection(), filter, pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("scroll filtered facts: %w", err)
		}
		if len(points) == 0 {
			break
		}
		all = append(all, points...)
		if id := points[len(points)-1].GetId().GetUuid(); id != "" {
			offset = id
		} else {
			break
		}
	}
	return all, nil
}

// updateFactStatus sets the status field on a single fact point.
func (p *Pruner) updateFactStatus(ctx context.Context, pointID string, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	point := &pb.PointStruct{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: pointID,
			},
		},
		Payload: map[string]*pb.Value{
			"status":     qutil.Nv(status),
			"updated_at": qutil.Nv(now),
		},
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{
					Data: []float32{0, 0, 0, 0},
				},
			},
		},
	}
	return p.facts.Upsert(ctx, []*pb.PointStruct{point})
}

// updateFactPayload applies a map of payload updates to a fact point.
// Reads the existing point first and merges the updates on top to avoid data loss.
func (p *Pruner) updateFactPayload(ctx context.Context, pointID string, updates map[string]*pb.Value) error {
	now := time.Now().UTC().Format(time.RFC3339)

	// Read existing point to avoid wiping payload fields
	points, err := scrollByPointID(ctx, p.facts, p.cfg.FactsCollection, pointID)
	if err != nil {
		return fmt.Errorf("read point %s for payload update: %w", pointID, err)
	}
	if len(points) == 0 {
		return fmt.Errorf("point %s not found for payload update", pointID)
	}

	payload := make(map[string]*pb.Value)
	for k, v := range points[0].GetPayload() {
		payload[k] = v
	}
	for k, v := range updates {
		payload[k] = v
	}
	payload["updated_at"] = qutil.Nv(now)

	point := &pb.PointStruct{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: pointID,
			},
		},
		Payload: payload,
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{
					Data: []float32{0, 0, 0, 0},
				},
			},
		},
	}

	return p.facts.Upsert(ctx, []*pb.PointStruct{point})
}

// getPayloadString extracts a string from a Qdrant payload.
func getPayloadString(payload map[string]*pb.Value, key string) (string, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return "", false
	}
	return v.GetStringValue(), true
}

// getPayloadFloat extracts a float64 from a Qdrant payload.
func getPayloadFloat(payload map[string]*pb.Value, key string) (float64, bool) {
	v, ok := payload[key]
	if !ok || v == nil {
		return 0, false
	}
	return v.GetDoubleValue(), true
}

// getPayloadStringList extracts a []string from a Qdrant payload list value.
func getPayloadStringList(payload map[string]*pb.Value, key string) []string {
	v, ok := payload[key]
	if !ok || v == nil {
		return nil
	}
	if s := v.GetStringValue(); s != "" {
		return []string{s}
	}
	values := v.GetListValue()
	if values == nil {
		return nil
	}
	items := values.GetValues()
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s := item.GetStringValue(); s != "" {
			result = append(result, s)
		}
	}
	return result
}

// getPayloadInt extracts an int from a Qdrant payload (stored as double).
func getPayloadInt(payload map[string]*pb.Value, key string) (int, bool) {
	f, ok := getPayloadFloat(payload, key)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// cosineSimilarity computes cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
