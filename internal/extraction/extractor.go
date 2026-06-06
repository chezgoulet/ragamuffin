// Package extraction provides automatic fact extraction from conversation turns.
//
// The pipeline: LLM call → dedup via embedding similarity → fact write → stats.
// Cooldown and concurrency limits prevent runaway extraction.
package extraction

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/events"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"

	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
)

// Config controls extraction behaviour.
type Config struct {
	Enabled            bool    // master switch (default false)
	Window             int     // preceding turns in context (default 10)
	MaxConfidence      float64 // hard ceiling (default 0.85)
	DedupThreshold     float64 // cosine similarity (default 0.85)
	Concurrency        int     // max concurrent extractions (default 2)
	PerSessionCooldown int     // seconds between extractions for same session (default 30)
	ImportanceThreshold float64 // minimum importance for auto-accepted facts (default 0.5)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:            false,
		Window:             10,
		MaxConfidence:      0.85,
		DedupThreshold:     0.85,
		Concurrency:        2,
		PerSessionCooldown: 30,
		ImportanceThreshold: 0.5,
	}
}

// ExtractedFact is a single fact extracted from a turn.
type ExtractedFact struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"` // 0.0–1.0
	Category   string  `json:"category"`   // e.g., preference, knowledge, relationship
	TTLDays    int     `json:"ttl_days"`    // 0 = no expiry
}

// extractRequest is the LLM prompt payload.
type extractRequest struct {
	Turn   string `json:"turn"`
	Role   string `json:"role"`
	Recent string `json:"recent_turns,omitempty"` // preceding turns for context
}

// Extractor runs the automatic extraction pipeline.
type Extractor struct {
	cfg       Config
	lm        llm.Synthesizer
	embedder  embedding.Embedder
	facts     qdrant.FactStore
	logger    *slog.Logger
	cooldown  *CooldownTracker
	stats     *Stats
	sem       chan struct{} // concurrency semaphore
	mu        sync.RWMutex
	sessionAE map[string]bool // session_id → auto_extract
	emitter   *events.Emitter

	// Interface for getting recent turns — set by server
	RecentTurnsFn func(ctx context.Context, sessionID string, n int) ([]TurnEntry, error)
}

// TurnEntry is a lightweight turn used for context window building.
type TurnEntry struct {
	Content string `json:"content"`
	Role    string `json:"role"`
}

// New creates an Extractor.
func New(cfg Config, lm llm.Synthesizer, embedder embedding.Embedder, facts qdrant.FactStore, logger *slog.Logger) *Extractor {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	return &Extractor{
		cfg:       cfg,
		lm:        lm,
		embedder:  embedder,
		facts:     facts,
		logger:    logger.With("component", "extraction"),
		cooldown:  NewCooldownTracker(cfg.PerSessionCooldown),
		stats:     NewStats(),
		sem:       make(chan struct{}, cfg.Concurrency),
		sessionAE: make(map[string]bool),
	}
}

// SetEmitter attaches a CloudEvents emitter for extraction_complete events.
func (e *Extractor) SetEmitter(em *events.Emitter) { e.emitter = em }

// Enabled returns whether the extraction pipeline is enabled.
func (e *Extractor) Enabled() bool { return e.cfg.Enabled }

// Stats returns a snapshot of pipeline statistics.
func (e *Extractor) Stats() StatsSnapshot { return e.stats.Snapshot() }

// SetSessionAutoExtract stores the auto_extract preference for a session.
func (e *Extractor) SetSessionAutoExtract(sessionID string, enabled bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessionAE[sessionID] = enabled
}

// SessionAutoExtract returns the stored auto_extract preference.
func (e *Extractor) SessionAutoExtract(sessionID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessionAE[sessionID]
}

// Extract runs the pipeline: cooldown → concurrency → LLM → dedup → write → stats.
// Called asynchronously after storing a turn.
func (e *Extractor) Extract(ctx context.Context, sessionID, content, role string) {
	if !e.cfg.Enabled {
		return
	}

	// Cooldown check
	if !e.cooldown.TryAcquire(sessionID) {
		e.logger.Debug("extraction: cooldown active, skipping", "session", sessionID)
		return
	}

	// Acquire concurrency slot
	select {
	case e.sem <- struct{}{}:
	default:
		e.logger.Debug("extraction: concurrency limit, skipping", "session", sessionID)
		return
	}
	defer func() { <-e.sem }()

	start := time.Now()
	e.stats.IncAttempted()

	// 1. Build context window from recent turns
	ctxText := content
	if e.RecentTurnsFn != nil && e.cfg.Window > 0 {
		turns, err := e.RecentTurnsFn(ctx, sessionID, e.cfg.Window)
		if err == nil && len(turns) > 0 {
			ctxText = buildContextWindow(turns, content)
		}
	}

	// 2. Extract facts via LLM
	facts, err := e.extractFacts(ctx, ctxText, role)
	if err != nil {
		e.logger.Error("extraction: LLM call failed", "session", sessionID, "error", err)
		return
	}
	if len(facts) == 0 {
		e.logger.Debug("extraction: no facts found", "session", sessionID)
		return
	}

	// 3. Process each fact: cap confidence, dedup, write
	created := 0
	skipped := 0
	for _, f := range facts {
		// Cap confidence
		if f.Confidence > e.cfg.MaxConfidence {
			f.Confidence = e.cfg.MaxConfidence
		}

		// Dedup check
		skip, supersedeKey, err := e.dedupCheck(ctx, f.Key, f.Value)
		if err != nil {
			e.logger.Warn("extraction: dedup error, writing anyway", "key", f.Key, "error", err)
		}
		if skip {
			e.stats.IncSkipped()
			skipped++
			e.logger.Debug("extraction: skipped duplicate", "key", f.Key)
			continue
		}

		// Write fact
		if err := e.writeFact(ctx, f, sessionID, supersedeKey); err != nil {
			e.logger.Error("extraction: write failed", "key", f.Key, "error", err)
			e.stats.IncRejected()
			continue
		}
		e.stats.IncCreated()
		created++
	}

	duration := time.Since(start)
	avgC := avgConfidence(facts)
	e.stats.RecordConfidence(avgC)
	e.stats.SetLastExtraction(time.Now())

	// Emit CloudEvent if emitter is configured
	if e.emitter != nil {
		e.emitter.Emit("extraction_complete", map[string]interface{}{
			"session_id":     sessionID,
			"count":          created,
			"skipped":        skipped,
			"duration_ms":    duration.Milliseconds(),
			"avg_confidence": avgC,
		})
	}

	e.logger.Info("extraction complete",
		"session", sessionID,
		"created", created,
		"skipped", skipped,
		"duration_ms", duration.Milliseconds(),
	)
}

// extractFacts calls the LLM to extract factual claims from the turn context.
func (e *Extractor) extractFacts(ctx context.Context, turnText, role string) ([]ExtractedFact, error) {
	prompt := e.buildExtractionPrompt(turnText, role)

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	result, err := e.lm.Synthesize(ctx, prompt, "")
	if err != nil {
		return nil, fmt.Errorf("LLM extraction: %w", err)
	}

	return parseExtractedFacts(result)
}

// buildExtractionPrompt constructs the LLM prompt.
func (e *Extractor) buildExtractionPrompt(turnText, role string) string {
	var b strings.Builder
	b.WriteString(`Extract factual claims from the following conversation turn.

For each factual claim, return a JSON array of objects with these fields:
- "key": A short, unique identifier for this fact (snake_case, descriptive, e.g. "user_prefers_rust")
- "value": The factual statement itself
- "confidence": Float 0.0–1.0 estimating how certain the claim is
- "category": One of "preference", "knowledge", "relationship", "event", "goal", "identity", "opinion"
- "ttl_days": Days until expiry (0 = no expiry). Default 90 for preferences, 365 for knowledge.

Only return valid JSON. No markdown, no explanation.
Example:
[{"key":"user_prefers_rust","value":"The user prefers Rust over Go for CLI tools","confidence":0.85,"category":"preference","ttl_days":90}]

Turn:
`)
	if turnText != "" {
		b.WriteString(turnText)
	}
	b.WriteString("\n")

	return b.String()
}

// buildContextWindow joins recent turns with the current turn.
func buildContextWindow(turns []TurnEntry, currentTurn string) string {
	var b strings.Builder
	for _, t := range turns {
		label := "User"
		if t.Role == "assistant" || t.Role == "system" {
			label = strings.ToUpper(t.Role[:1]) + t.Role[1:]
		}
		b.WriteString(fmt.Sprintf("%s: %s\n", label, strings.TrimSpace(t.Content)))
	}
	b.WriteString(fmt.Sprintf("Current turn:\n"))
	if strings.TrimSpace(currentTurn) != "" {
		b.WriteString(currentTurn)
	}
	return b.String()
}

// parseExtractedFacts parses the LLM JSON response.
func parseExtractedFacts(raw string) ([]ExtractedFact, error) {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var facts []ExtractedFact
	if err := json.Unmarshal([]byte(cleaned), &facts); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return facts, nil
}

// writeFact stores an extracted fact to the Qdrant collection.
func (e *Extractor) writeFact(ctx context.Context, f ExtractedFact, sessionID string, supersedesKey string) error {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	payload := map[string]*pb.Value{
		"fact_key":              qutil.Nv(f.Key),
		"fact_value":            qutil.Nv(f.Value),
		"status":                qutil.Nv("active"),
		"source_type":           qutil.Nv("extraction"),
		"source":                qutil.Nv(fmt.Sprintf("session:%s", sessionID)),
		"session_id":            qutil.Nv(sessionID),
		"confidence":            qutil.Nv(f.Confidence),
		"category":              qutil.Nv(f.Category),
		"extracted":             qutil.Nv(true),
		"created_at":            qutil.Nv(nowStr),
		"updated_at":            qutil.Nv(nowStr),

		// Edge fields (empty by default)
		"supersedes":            qutil.Nv(""),
		"refines":               qutil.Nv(""),
		"contradicts":           qutil.NvList([]string{}),
		"supports":              qutil.NvList([]string{}),
		"confirmation_count":    qutil.Nv(float64(1)),
		"conflict_resolved":     qutil.Nv(true),
		"superseded_by":         qutil.Nv(float64(0)),
		"access_count":          qutil.Nv(float64(0)),
		"last_accessed_at":      qutil.Nv(""),
		"expires_at":            qutil.Nv(""),
		"expires_at_unix":       qutil.Nv(float64(0)),
		"version":               qutil.Nv(float64(0)),
		"key_prefix":            qutil.Nv(f.Key),
		"valid_from":            qutil.Nv(nowStr),
		"valid_from_unix":       qutil.Nv(float64(now.Unix())),
		"valid_until":           qutil.Nv(""),
		"valid_until_unix":      qutil.Nv(float64(0)),
	}

	if f.TTLDays > 0 {
		payload["ttl_days"] = qutil.Nv(float64(f.TTLDays))
		expiresAt := now.AddDate(0, 0, f.TTLDays)
		payload["expires_at"] = qutil.Nv(expiresAt.Format(time.RFC3339))
		payload["expires_at_unix"] = qutil.Nv(float64(expiresAt.Unix()))
	}

	// Handle supersedes
	if supersedesKey != "" {
		payload["supersedes"] = qutil.Nv(supersedesKey)
	}

	// Compute vector: embed the value for dedup and future search
	vec, err := e.embedder.EmbedSingle(ctx, f.Value)
	if err != nil {
		// Fallback: zero vector
		vec = make([]float32, 4)
	}

	point := &pb.PointStruct{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: uuid.New().String(),
			},
		},
		Payload: payload,
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{
					Data: vec,
				},
			},
		},
	}

	return e.facts.Upsert(ctx, []*pb.PointStruct{point})
}

// dedupCheck checks whether a fact with the same key already exists and
// whether the new value is semantically different enough to write.
// Returns (skip, supersedeKey, error).
func (e *Extractor) dedupCheck(ctx context.Context, key, value string) (bool, string, error) {
	if e.facts == nil {
		return false, "", nil
	}

	// Scroll for existing facts with this exact key
	keyFilter := &pb.Filter{
		Must: []*pb.Condition{
			{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: "fact_key",
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: key,
							},
						},
					},
				},
			},
		},
	}

	points, err := e.facts.ScrollFiltered(ctx, e.facts.Collection(), keyFilter, 10, "")
	if err != nil || len(points) == 0 {
		return false, "", nil // No existing fact → write
	}

	// Embed the new value
	newVec, err := e.embedder.EmbedSingle(ctx, value)
	if err != nil {
		return false, "", fmt.Errorf("embed new value: %w", err)
	}

	// Compare against existing fact values
	for _, pt := range points {
		existingValue, _ := qutil.GetPayloadString(pt.GetPayload(), "fact_value")
		if existingValue == "" {
			continue
		}

		// Embed existing value for comparison
		existingVec, err := e.embedder.EmbedSingle(ctx, existingValue)
		if err != nil {
			continue
		}

		sim := cosineSimilarity(newVec, existingVec)
		if sim >= float32(e.cfg.DedupThreshold) {
			// Semantic duplicate — skip
			return true, "", nil
		}

		// Same key, different value — supersede
		existingKey, _ := qutil.GetPayloadString(pt.GetPayload(), "fact_key")
		status, _ := qutil.GetPayloadString(pt.GetPayload(), "status")
		if status == "active" || status == "" {
			return false, existingKey, nil
		}
	}

	return false, "", nil
}

// cosineSimilarity computes the cosine of the angle between two vectors.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

func avgConfidence(facts []ExtractedFact) float64 {
	if len(facts) == 0 {
		return 0
	}
	var sum float64
	for _, f := range facts {
		sum += f.Confidence
	}
	return sum / float64(len(facts))
}
