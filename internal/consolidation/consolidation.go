// Package consolidation implements sleep-time memory consolidation: a
// background, idle-aware worker that performs the hippocampus→neocortex loop.
//
// It replays recent session transcripts (interleaved with a sample of older
// ones to avoid catastrophic forgetting, McClelland et al. CLS 1995), asks an
// LLM to distill each into a durable "gist" summary, and writes that gist to
// the semantic fact store with a long TTL. The raw session/turn log is left
// untouched — it remains the immutable "engram" (verbatim trace), while the
// gist is the reconstructed cortical memory (fuzzy-trace theory, Reyna &
// Brainerd).
//
// Consolidation never runs on the query path. It is gated by
// RAGAMUFFIN_CONSOLIDATION_ENABLED (default off) and only fires when the system
// has been idle, mimicking offline replay during sleep.
package consolidation

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"

	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
)

// Session is the minimal session shape the consolidator needs. It mirrors
// logstore.Session but is declared here to keep the package decoupled.
type Session struct {
	ID        string
	Vault     string
	TurnCount int
	CreatedAt string
	UpdatedAt string
}

// Turn is a single message within a session.
type Turn struct {
	Role    string
	Content string
}

// SessionSource supplies sessions and their transcripts for replay.
// Implemented by an adapter over logstore.Store.
type SessionSource interface {
	// RecentSessions returns up to limit sessions for a vault, most recently
	// updated first.
	RecentSessions(ctx context.Context, vault string, limit int) ([]Session, error)
	// Transcript returns the last n turns of a session, oldest first.
	Transcript(ctx context.Context, sessionID string, n int) ([]Turn, error)
}

// Emitter emits a CloudEvent when a consolidation run completes. Satisfied by
// *events.Emitter (nil-safe).
type Emitter interface {
	Emit(eventType string, data any)
}

// Config controls the consolidation worker.
type Config struct {
	Enabled bool
	// Interval between consolidation sweeps.
	Interval time.Duration
	// IdleWindow: only consolidate a vault if no session was updated within this
	// window (the system is "asleep"). Zero disables the idle gate.
	IdleWindow time.Duration
	// BatchSize: max recent sessions replayed per vault per sweep.
	BatchSize int
	// InterleaveRatio: fraction (0-1) of the batch drawn from older sessions to
	// mix with the newest ones (interleaved replay). 0.3 = 30% old, 70% new.
	InterleaveRatio float64
	// TurnLimit: max turns fetched per session transcript.
	TurnLimit int
	// GistTTLDays: TTL for written gist facts (long-lived).
	GistTTLDays int
}

// DefaultConfig returns sane defaults (still off unless Enabled is set).
func DefaultConfig() Config {
	return Config{
		Interval:        6 * time.Hour,
		IdleWindow:      30 * time.Minute,
		BatchSize:       20,
		InterleaveRatio: 0.3,
		TurnLimit:       50,
		GistTTLDays:     365,
	}
}

// Stats is a snapshot of consolidation activity.
type Stats struct {
	LastRunAt         string `json:"last_run_at,omitempty"`
	LastRunSessions   int    `json:"last_run_sessions"`
	LastRunGists      int    `json:"last_run_gists"`
	TotalRuns         int    `json:"total_runs"`
	TotalGistsWritten int    `json:"total_gists_written"`
	TotalSessionsSeen int    `json:"total_sessions_seen"`
	LastError         string `json:"last_error,omitempty"`
	Enabled           bool   `json:"enabled"`
	Running           bool   `json:"running"`
}

// Consolidator is the background worker.
type Consolidator struct {
	cfg      Config
	sessions SessionSource
	llm      llm.Synthesizer
	embedder embedding.Embedder
	facts    qdrant.FactStore
	emitter  Emitter
	vaults   func() []string
	logger   *slog.Logger

	mu    sync.Mutex
	stats Stats
}

// New creates a Consolidator. vaults returns the set of vault names to sweep
// (pass a closure over config); it may return a single "default" entry.
func New(cfg Config, sessions SessionSource, lm llm.Synthesizer, embedder embedding.Embedder, facts qdrant.FactStore, emitter Emitter, vaults func() []string, logger *slog.Logger) *Consolidator {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.TurnLimit <= 0 {
		cfg.TurnLimit = 50
	}
	if cfg.InterleaveRatio < 0 || cfg.InterleaveRatio > 1 {
		cfg.InterleaveRatio = 0.3
	}
	if cfg.GistTTLDays <= 0 {
		cfg.GistTTLDays = 365
	}
	return &Consolidator{
		cfg:      cfg,
		sessions: sessions,
		llm:      lm,
		embedder: embedder,
		facts:    facts,
		emitter:  emitter,
		vaults:   vaults,
		logger:   logger.With("component", "consolidation"),
		stats:    Stats{Enabled: cfg.Enabled},
	}
}

// Enabled reports whether consolidation is switched on.
func (c *Consolidator) Enabled() bool { return c.cfg.Enabled }

// Snapshot returns a copy of current stats.
func (c *Consolidator) Snapshot() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// Run starts the periodic sweep loop until ctx is cancelled. It is a no-op when
// disabled. Intended to be launched in its own goroutine from main.
func (c *Consolidator) Run(ctx context.Context) {
	if !c.cfg.Enabled {
		c.logger.Info("consolidation disabled")
		return
	}
	interval := c.cfg.Interval
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	c.logger.Info("consolidation worker started", "interval", interval.String())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.logger.Info("consolidation worker stopped")
			return
		case <-ticker.C:
			if err := c.RunOnce(ctx); err != nil {
				c.logger.Warn("consolidation sweep failed", "error", err)
			}
		}
	}
}

// RunOnce performs a single consolidation sweep across all vaults. Exposed for
// tests and manual triggering.
func (c *Consolidator) RunOnce(ctx context.Context) error {
	c.mu.Lock()
	if c.stats.Running {
		c.mu.Unlock()
		return fmt.Errorf("consolidation already running")
	}
	c.stats.Running = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.stats.Running = false
		c.mu.Unlock()
	}()

	var sweepSessions, sweepGists int
	var vaults []string
	if c.vaults != nil {
		vaults = c.vaults()
	}
	if len(vaults) == 0 {
		vaults = []string{"default"}
	}

	for _, vault := range vaults {
		s, g, err := c.consolidateVault(ctx, vault)
		if err != nil {
			c.recordError(err)
			c.logger.Warn("vault consolidation failed", "vault", vault, "error", err)
			continue
		}
		sweepSessions += s
		sweepGists += g
	}

	now := time.Now().UTC().Format(time.RFC3339)
	c.mu.Lock()
	c.stats.LastRunAt = now
	c.stats.LastRunSessions = sweepSessions
	c.stats.LastRunGists = sweepGists
	c.stats.TotalRuns++
	c.stats.TotalGistsWritten += sweepGists
	c.stats.TotalSessionsSeen += sweepSessions
	c.mu.Unlock()

	if c.emitter != nil {
		c.emitter.Emit("consolidation.complete", map[string]any{
			"sessions_replayed": sweepSessions,
			"gists_written":     sweepGists,
			"completed_at":      now,
		})
	}
	c.logger.Info("consolidation sweep complete", "sessions", sweepSessions, "gists", sweepGists)
	return nil
}

// consolidateVault replays a scheduled batch of sessions for one vault and
// writes gist facts. Returns (sessionsReplayed, gistsWritten).
func (c *Consolidator) consolidateVault(ctx context.Context, vault string) (int, int, error) {
	// Fetch a generous candidate pool, then schedule an interleaved batch.
	pool, err := c.sessions.RecentSessions(ctx, vault, c.cfg.BatchSize*4)
	if err != nil {
		return 0, 0, fmt.Errorf("list sessions: %w", err)
	}
	if len(pool) == 0 {
		return 0, 0, nil
	}

	// Idle gate: skip if the most-recently-updated session is too fresh.
	if c.cfg.IdleWindow > 0 {
		if newest := mostRecentUpdate(pool); !newest.IsZero() {
			if time.Since(newest) < c.cfg.IdleWindow {
				c.logger.Debug("vault not idle, skipping", "vault", vault)
				return 0, 0, nil
			}
		}
	}

	batch := scheduleReplay(pool, c.cfg.BatchSize, c.cfg.InterleaveRatio)

	var replayed, gists int
	for _, sess := range batch {
		turns, terr := c.sessions.Transcript(ctx, sess.ID, c.cfg.TurnLimit)
		if terr != nil {
			c.logger.Warn("transcript fetch failed", "session", sess.ID, "error", terr)
			continue
		}
		replayed++
		transcript := renderTranscript(turns)
		if transcript == "" {
			continue
		}
		gist, gerr := c.summarize(ctx, transcript)
		if gerr != nil {
			c.logger.Warn("gist summarize failed", "session", sess.ID, "error", gerr)
			continue
		}
		if gist == "" {
			continue
		}
		if werr := c.writeGist(ctx, vault, sess.ID, gist); werr != nil {
			c.logger.Warn("gist write failed", "session", sess.ID, "error", werr)
			continue
		}
		gists++
	}
	return replayed, gists, nil
}

// summarize asks the LLM to distill a transcript into a durable gist.
func (c *Consolidator) summarize(ctx context.Context, transcript string) (string, error) {
	if c.llm == nil {
		return "", fmt.Errorf("consolidation requires an LLM")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	prompt := "Distill the durable, reusable knowledge from this conversation into a concise " +
		"third-person summary (2-4 sentences). Capture stable facts, decisions, and preferences; " +
		"omit greetings, chit-chat, and ephemeral details. If nothing durable is present, reply with an empty line.\n\nConversation:\n" + transcript
	out, err := c.llm.Synthesize(cctx, prompt, "")
	if err != nil {
		return "", err
	}
	return trimGist(out), nil
}

// gistNamespace is a fixed UUID namespace for deriving deterministic gist point
// ids. Generated once; must never change or existing gists would orphan.
var gistNamespace = uuid.MustParse("6f3d5c8e-2b1a-4d9f-8c7e-1a2b3c4d5e6f")

// gistPointID derives a stable UUIDv5 from the gist fact key so that
// re-consolidating the same session upserts the existing gist point in place
// rather than creating a duplicate on every sweep (Qdrant upserts by point id).
func gistPointID(key string) string {
	return uuid.NewSHA1(gistNamespace, []byte(key)).String()
}

// writeGist stores a gist summary as a long-TTL fact. The fact is tagged
// gist=true and source_type=consolidation so it is distinguishable from
// extracted facts and the immutable session log.
func (c *Consolidator) writeGist(ctx context.Context, vault, sessionID, gist string) error {
	if c.facts == nil || c.embedder == nil {
		return fmt.Errorf("gist write requires facts store and embedder")
	}
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	expires := now.AddDate(0, 0, c.cfg.GistTTLDays)
	key := fmt.Sprintf("gist:%s", sessionID)

	payload := map[string]*pb.Value{
		"fact_key":           qutil.Nv(key),
		"fact_value":         qutil.Nv(gist),
		"status":             qutil.Nv("active"),
		"source_type":        qutil.Nv("consolidation"),
		"source":             qutil.Nv(fmt.Sprintf("session:%s", sessionID)),
		"session_id":         qutil.Nv(sessionID),
		"vault":              qutil.Nv(vault),
		"confidence":         qutil.Nv(0.7),
		"category":           qutil.Nv("gist"),
		"gist":               qutil.Nv(true),
		"extracted":          qutil.Nv(true),
		"created_at":         qutil.Nv(nowStr),
		"updated_at":         qutil.Nv(nowStr),
		"key_prefix":         qutil.Nv(key),
		"ttl_days":           qutil.Nv(float64(c.cfg.GistTTLDays)),
		"expires_at":         qutil.Nv(expires.Format(time.RFC3339)),
		"expires_at_unix":    qutil.Nv(float64(expires.Unix())),
		"valid_from":         qutil.Nv(nowStr),
		"valid_until":        qutil.Nv(""),
		"valid_until_unix":   qutil.Nv(float64(0)),
		"access_count":       qutil.Nv(float64(0)),
		"last_accessed_at":   qutil.Nv(""),
		"confirmation_count": qutil.Nv(float64(1)),
		"last_confirmed_at":  qutil.Nv(nowStr),
		"version":            qutil.Nv(float64(0)),
		"supersedes":         qutil.Nv(""),
		"superseded_by":      qutil.Nv(float64(0)),
		"refines":            qutil.Nv(""),
		"conflict_resolved":  qutil.Nv(true),
		"contradicts":        qutil.NvList(nil),
		"supports":           qutil.NvList(nil),
	}

	vec, err := c.embedder.EmbedSingle(ctx, gist)
	if err != nil {
		return fmt.Errorf("embed gist: %w", err)
	}
	point := &pb.PointStruct{
		Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: gistPointID(key)}},
		Payload: payload,
		Vectors: &pb.Vectors{VectorsOptions: &pb.Vectors_Vector{Vector: &pb.Vector{Data: vec}}},
	}
	return c.facts.Upsert(ctx, []*pb.PointStruct{point})
}

func (c *Consolidator) recordError(err error) {
	c.mu.Lock()
	c.stats.LastError = err.Error()
	c.mu.Unlock()
}

// scheduleReplay selects up to n sessions from the pool, mixing the newest with
// an interleaved sample of older ones. The pool is assumed newest-first. This
// implements interleaved replay: new memories are consolidated alongside a
// sample of old ones, weighted toward higher-turn-count ("more important")
// sessions, so the semantic store doesn't drift toward only-recent knowledge.
func scheduleReplay(pool []Session, n int, interleaveRatio float64) []Session {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	if len(pool) <= n {
		return pool
	}
	oldCount := int(float64(n) * interleaveRatio)
	newCount := n - oldCount

	newest := pool[:newCount]

	// Candidates for the "old" slice: everything after the newest window.
	oldPool := append([]Session(nil), pool[newCount:]...)
	// Weight older sessions by importance (turn count) so richer sessions are
	// replayed preferentially — the "ripple" scheduler.
	sort.SliceStable(oldPool, func(i, j int) bool {
		return replayImportance(oldPool[i]) > replayImportance(oldPool[j])
	})
	if oldCount > len(oldPool) {
		oldCount = len(oldPool)
	}
	old := oldPool[:oldCount]

	out := make([]Session, 0, len(newest)+len(old))
	out = append(out, newest...)
	out = append(out, old...)
	return out
}

// replayImportance scores a session for replay priority. Turn count is a proxy
// for information richness; recency provides a mild tiebreak.
func replayImportance(s Session) float64 {
	score := float64(s.TurnCount)
	if t, err := time.Parse(time.RFC3339, s.UpdatedAt); err == nil {
		days := time.Since(t).Hours() / 24.0
		if days < 0 {
			days = 0
		}
		score += (1.0 / (1.0 + days)) // recency bonus in [0,1]
	}
	return score
}

func mostRecentUpdate(pool []Session) time.Time {
	var newest time.Time
	for _, s := range pool {
		if t, err := time.Parse(time.RFC3339, s.UpdatedAt); err == nil && t.After(newest) {
			newest = t
		}
	}
	return newest
}

func renderTranscript(turns []Turn) string {
	var b []byte
	for _, t := range turns {
		role := t.Role
		if role == "" {
			role = "user"
		}
		content := trimGist(t.Content)
		if content == "" {
			continue
		}
		b = append(b, []byte(role+": "+content+"\n")...)
	}
	return string(b)
}

func trimGist(s string) string {
	// Trim surrounding whitespace and common markdown noise.
	out := s
	for len(out) > 0 && (out[0] == ' ' || out[0] == '\n' || out[0] == '\t' || out[0] == '\r') {
		out = out[1:]
	}
	for len(out) > 0 {
		last := out[len(out)-1]
		if last == ' ' || last == '\n' || last == '\t' || last == '\r' {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	return out
}
