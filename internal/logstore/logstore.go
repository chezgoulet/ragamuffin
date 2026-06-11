// Package logstore provides an append-only log stream backed by SQLite.
package logstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// LogEntry represents a single log entry.
type LogEntry struct {
	ID        string   `json:"id"`
	Agent     string   `json:"agent"`
	Type      string   `json:"type"`
	Body      string   `json:"body"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at"`
}

// Filter defines query parameters for listing log entries.
type Filter struct {
	Agent  string
	Type   string
	Tag    string
	Since  string // ISO8601
	Until  string // ISO8601
	Before string // cursor: entries before this ID
	Limit  int    // max results (1-1000, default 100)
}

// Store is an append-only SQLite-backed log store.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

// Open opens or creates the SQLite log database at the given path.
// Creates parent directories if they don't exist.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("logstore: create dir: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("logstore: open: %w", err)
	}

	// WAL mode + synchronous=NORMAL for better concurrent performance
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("logstore: enable WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA synchronous=NORMAL`); err != nil {
		return nil, fmt.Errorf("logstore: set synchronous: %w", err)
	}
	if _, err := db.Exec(`PRAGMA auto_vacuum=INCREMENTAL`); err != nil {
		return nil, fmt.Errorf("logstore: enable auto_vacuum: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("logstore: migrate: %w", err)
	}

	s := &Store{db: db, logger: slog.Default()}
	return s, nil
}

// Session represents a conversation session.
type Session struct {
	ID        string `json:"id"`
	Vault     string `json:"vault"`
	AgentID   string `json:"agent_id"`
	Source    string `json:"source,omitempty"`
	TurnCount int    `json:"turn_count"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Turn represents a single message in a session.
type Turn struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

// migrate creates the schema if it doesn't exist.
func migrate(db *sql.DB) error {
	// Schema v1: initial tables
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS log_entries (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			agent      TEXT NOT NULL,
			type       TEXT NOT NULL,
			body       TEXT NOT NULL,
			tags       TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_log_agent   ON log_entries(agent);
		CREATE INDEX IF NOT EXISTS idx_log_type    ON log_entries(type);
		CREATE INDEX IF NOT EXISTS idx_log_created ON log_entries(created_at);

		CREATE TABLE IF NOT EXISTS sessions (
			id         TEXT PRIMARY KEY,
			vault      TEXT NOT NULL,
			agent_id   TEXT NOT NULL,
			source     TEXT NOT NULL DEFAULT '',
			turn_count INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			archived   INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS session_turns (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES sessions(id),
			content    TEXT NOT NULL,
			role       TEXT NOT NULL DEFAULT 'user',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_vault   ON sessions(vault);
		CREATE INDEX IF NOT EXISTS idx_sessions_agent   ON sessions(agent_id);
		CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at);
		CREATE INDEX IF NOT EXISTS idx_turns_session    ON session_turns(session_id);

		CREATE TABLE IF NOT EXISTS review_resolutions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			fact_key    TEXT NOT NULL,
			action      TEXT NOT NULL,
			reason_type TEXT NOT NULL,
			similarity  REAL,
			created_at  TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_res_reason ON review_resolutions(reason_type);
		CREATE INDEX IF NOT EXISTS idx_res_created ON review_resolutions(created_at);

		CREATE TABLE IF NOT EXISTS link_index (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			source_path TEXT NOT NULL,
			target_path TEXT NOT NULL,
			link_type  TEXT NOT NULL,
			context    TEXT,
			vault      TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_link_source ON link_index(source_path);
		CREATE INDEX IF NOT EXISTS idx_link_target ON link_index(target_path);
		CREATE INDEX IF NOT EXISTS idx_link_vault ON link_index(vault);
	`)
	if err != nil {
		return err
	}

	// Schema v2: add finalized_at column for session finalization
	_, err = db.Exec(`ALTER TABLE sessions ADD COLUMN finalized_at TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		// Column may already exist (no-op), which is fine
		_ = err
	}

	return nil
}

// Append inserts a new log entry and returns its ID.
// timestamp is optional — pass zero time to use the current time.
func (s *Store) Append(ctx context.Context, agent, eventType, body string, tags []string, timestamp time.Time) (string, error) {
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("logstore: marshal tags: %w", err)
	}

	ts := timestamp.Format(time.RFC3339Nano)

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO log_entries (agent, type, body, tags, created_at) VALUES (?, ?, ?, ?, ?)`,
		agent, eventType, body, string(tagsJSON), ts,
	)
	if err != nil {
		return "", fmt.Errorf("logstore: insert: %w", err)
	}

	rowID, err := result.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("logstore: last insert id: %w", err)
	}

	// Encode the rowid as a URL-safe hex string for cursor pagination
	id := encodeID(rowID)
	return id, nil
}

// List queries the log stream with the given filter.
// Returns up to filter.Limit entries (default 100, max 1000).
// nextToken is the ID to pass as ?before= for the next page, or "" if no more.
func (s *Store) List(ctx context.Context, f Filter) (entries []LogEntry, nextToken string, err error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}

	var conditions []string
	var args []any

	if f.Agent != "" {
		conditions = append(conditions, "agent = ?")
		args = append(args, f.Agent)
	}
	if f.Type != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, f.Type)
	}
	if f.Tag != "" {
		conditions = append(conditions, "EXISTS (SELECT 1 FROM json_each(tags) WHERE value = ?)")
		args = append(args, f.Tag)
	}
	if f.Since != "" {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, f.Until)
	}
	if f.Before != "" {
		rowID, err := decodeID(f.Before)
		if err != nil {
			return nil, "", fmt.Errorf("logstore: invalid cursor: %w", err)
		}
		conditions = append(conditions, "id < ?")
		args = append(args, rowID)
	}

	query := "SELECT id, agent, type, body, tags, created_at FROM log_entries"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit+1) // fetch one extra to detect if there's a next page

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("logstore: query: %w", err)
	}
	defer rows.Close()

	entries = make([]LogEntry, 0, f.Limit)
	var count int
	for rows.Next() {
		var id int64
		var agent, eventType, body, tagsJSON, createdAt string
		if err := rows.Scan(&id, &agent, &eventType, &body, &tagsJSON, &createdAt); err != nil {
			return nil, "", fmt.Errorf("logstore: scan: %w", err)
		}

		var tags []string
		json.Unmarshal([]byte(tagsJSON), &tags) // best-effort, default to empty

		count++
		if count > f.Limit {
			// We fetched one extra — that's our next token
			nextToken = encodeID(id)
			break
		}

		entries = append(entries, LogEntry{
			ID:        encodeID(id),
			Agent:     agent,
			Type:      eventType,
			Body:      body,
			Tags:      tags,
			CreatedAt: createdAt,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("logstore: rows err: %w", err)
	}

	return entries, nextToken, nil
}

// CreateSession creates a new session with the given ID and returns it.
func (s *Store) CreateSession(ctx context.Context, id, vault, agentID, source string) (*Session, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, vault, agent_id, source, turn_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 0, ?, ?)`,
		id, vault, agentID, source, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("logstore: create session: %w", err)
	}

	return &Session{
		ID:        id,
		Vault:     vault,
		AgentID:   agentID,
		Source:    source,
		TurnCount: 0,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// AppendTurn appends a turn to a session and updates counters.
func (s *Store) AppendTurn(ctx context.Context, sessionID, content, role string) (*Turn, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("logstore: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Verify session exists before inserting a turn
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("logstore: check session: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("logstore: session %q not found", sessionID)
	}

	result, err := tx.ExecContext(ctx,
		`INSERT INTO session_turns (session_id, content, role, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, content, role, now,
	)
	if err != nil {
		return nil, fmt.Errorf("logstore: insert turn: %w", err)
	}

	turnID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("logstore: turn last insert id: %w", err)
	}

	// Update session counters
	_, err = tx.ExecContext(ctx,
		`UPDATE sessions SET turn_count = turn_count + 1, updated_at = ? WHERE id = ?`,
		now, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("logstore: update session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("logstore: commit: %w", err)
	}

	return &Turn{
		ID:        turnID,
		SessionID: sessionID,
		Content:   content,
		Role:      role,
		CreatedAt: now,
	}, nil
}

// GetSession returns session metadata and the last N turns.
// turnLimit sets max turns returned (0 = all).
func (s *Store) GetSession(ctx context.Context, id string, turnLimit int) (*Session, []Turn, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, vault, agent_id, source, turn_count, created_at, updated_at
		 FROM sessions WHERE id = ? AND archived = 0`, id,
	)

	var sess Session
	if err := row.Scan(&sess.ID, &sess.Vault, &sess.AgentID, &sess.Source, &sess.TurnCount, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, fmt.Errorf("logstore: session %q not found", id)
		}
		return nil, nil, fmt.Errorf("logstore: scan session: %w", err)
	}

	// Fetch turns
	query := `SELECT id, session_id, content, role, created_at FROM session_turns WHERE session_id = ? ORDER BY id ASC`
	var args []any
	args = append(args, id)

	if turnLimit > 0 {
		query = `SELECT id, session_id, content, role, created_at FROM session_turns WHERE session_id = ? ORDER BY id DESC LIMIT ?`
		args = append(args, turnLimit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("logstore: query turns: %w", err)
	}
	defer rows.Close()

	turns := make([]Turn, 0)
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Content, &t.Role, &t.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("logstore: scan turn: %w", err)
		}
		turns = append(turns, t)
	}

	// If we queried DESC with a limit, reverse to ASC order
	if turnLimit > 0 {
		for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
			turns[i], turns[j] = turns[j], turns[i]
		}
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("logstore: turns rows err: %w", err)
	}

	return &sess, turns, nil
}

// ListSessions lists sessions filtered by vault, ordered by updated_at DESC.
// Pass limit=0 for default (100), max 1000.
func (s *Store) ListSessions(ctx context.Context, vault string, limit, offset int) ([]Session, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	var query string
	var args []any

	if vault != "" {
		query = `SELECT id, vault, agent_id, source, turn_count, created_at, updated_at
			 FROM sessions WHERE vault = ? AND archived = 0 ORDER BY updated_at DESC LIMIT ? OFFSET ?`
		args = append(args, vault, limit, offset)
	} else {
		query = `SELECT id, vault, agent_id, source, turn_count, created_at, updated_at
			 FROM sessions WHERE archived = 0 ORDER BY updated_at DESC LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("logstore: list sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]Session, 0)
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.Vault, &sess.AgentID, &sess.Source, &sess.TurnCount, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, fmt.Errorf("logstore: scan session: %w", err)
		}
		sessions = append(sessions, sess)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("logstore: sessions rows err: %w", err)
	}

	return sessions, nil
}

// DeleteSession soft-deletes a session by setting archived = 1.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET archived = 1 WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("logstore: delete session: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("logstore: session %q not found", id)
	}
	return nil
}

// DeleteSessionsByVault hard-deletes all sessions and their turns for a vault.
func (s *Store) DeleteSessionsByVault(ctx context.Context, vault string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE vault = ?`, vault,
	)
	if err != nil {
		return 0, fmt.Errorf("logstore: delete sessions by vault: %w", err)
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// FinalizeSession marks a session as finalized by setting finalized_at.
// Returns an error if the session doesn't exist.
func (s *Store) FinalizeSession(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET finalized_at = ?, updated_at = ? WHERE id = ? AND archived = 0`,
		now, now, id,
	)
	if err != nil {
		return fmt.Errorf("logstore: finalize session: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("logstore: session %q not found or already archived", id)
	}
	return nil
}

// ── Hygiene ─────────────────────────────────────────────────────────────────

// IntegrityCheck runs PRAGMA integrity_check and returns the result.
func (s *Store) IntegrityCheck() error {
	var result string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("integrity check query: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}

// Prune deletes old rows from log_entries when the table exceeds maxRows.
// Returns total deleted rows across all pruned tables.
func (s *Store) Prune(ctx context.Context, maxRows int) (int64, error) {
	var totalDeleted int64

	tables := []string{"log_entries"}
	for _, table := range tables {
		var count int64
		err := s.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&count)
		if err != nil {
			return totalDeleted, fmt.Errorf("count %s: %w", table, err)
		}
		if count <= int64(maxRows) {
			continue
		}
		toDelete := count - int64(maxRows)

		// Bounded DELETE — always use LIMIT to avoid unbounded deletes
		result, err := s.db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE id IN (
				SELECT id FROM %s ORDER BY id ASC LIMIT ?
			)`, table, table), toDelete)
		if err != nil {
			return totalDeleted, fmt.Errorf("prune %s: %w", table, err)
		}
		deleted, _ := result.RowsAffected()
		totalDeleted += deleted

		// Incremental vacuum after bulk delete to reclaim space
		if deleted > 0 {
			if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum(10)`); err != nil {
				s.logger.Warn("logstore: incremental_vacuum failed", "error", err)
			}
		}
	}
	return totalDeleted, nil
}

// Flush forces a WAL checkpoint to flush pending writes to the main database.
func (s *Store) Flush() error {
	_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// SetLogger sets the logger for the store.
func (s *Store) SetLogger(l *slog.Logger) {
	s.logger = l
}

// Close closes the database connection.
// Resolution records a single review queue resolution.
type Resolution struct {
	ID         int64   `json:"id"`
	FactKey    string  `json:"fact_key"`
	Action     string  `json:"action"`
	ReasonType string  `json:"reason_type"`
	Similarity float64 `json:"similarity,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// ThresholdRecommendation is a single suggested threshold adjustment.
type ThresholdRecommendation struct {
	ReasonType  string  `json:"reason_type"`
	Recommended float64 `json:"recommended"`
	Current     float64 `json:"current"`
	AcceptRate  float64 `json:"accept_rate"`
	SampleSize  int     `json:"sample_size"`
	Rationale   string  `json:"rationale"`
}

// RecordResolution inserts a review resolution record into the logstore.
func (s *Store) RecordResolution(ctx context.Context, factKey, action, reasonType string, similarity *float64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	sim := sql.NullFloat64{}
	if similarity != nil {
		sim = sql.NullFloat64{Float64: *similarity, Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO review_resolutions (fact_key, action, reason_type, similarity, created_at) VALUES (?, ?, ?, ?, ?)`,
		factKey, action, reasonType, sim, now)
	if err != nil {
		return fmt.Errorf("logstore: record resolution: %w", err)
	}
	return nil
}

// ThresholdRecommendations queries the review_resolutions table and returns
// suggested threshold adjustments based on resolution history.
func (s *Store) ThresholdRecommendations(ctx context.Context, dryRun bool) ([]ThresholdRecommendation, error) {
	// Query acceptance rates per reason type
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			reason_type,
			COUNT(*) AS total,
			SUM(CASE WHEN action = 'confirm' OR action = 'reclassify' THEN 1 ELSE 0 END) AS accepted,
			SUM(CASE WHEN action = 'reject' THEN 1 ELSE 0 END) AS rejected
		FROM review_resolutions
		GROUP BY reason_type
		HAVING total >= 3
		ORDER BY total DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("logstore: query recommendations: %w", err)
	}
	defer rows.Close()

	var recs []ThresholdRecommendation
	for rows.Next() {
		var reasonType string
		var total, accepted, rejected int
		if err := rows.Scan(&reasonType, &total, &accepted, &rejected); err != nil {
			return nil, fmt.Errorf("logstore: scan recommendation: %w", err)
		}

		acceptRate := float64(accepted) / float64(total)
		var rec ThresholdRecommendation
		rec.ReasonType = reasonType
		rec.SampleSize = total
		rec.AcceptRate = acceptRate

		// Generate recommendations based on reason type
		switch reasonType {
		case "conflict":
			rec.Current = 0.85 // default conflict similarity threshold
			if rejectRate := float64(rejected) / float64(total); rejectRate > 0.3 {
				// Most conflict flags are being rejected -- threshold too low
				rec.Recommended = 0.90
				rec.Rationale = fmt.Sprintf("%.0f%% of conflict flags were rejected; consider raising similarity threshold from 0.85 to 0.90", rejectRate*100)
			} else if acceptRate > 0.85 {
				// Most conflict flags are accepted -- threshold may be too high
				rec.Recommended = 0.80
				rec.Rationale = fmt.Sprintf("%.0f%% of conflict flags were confirmed; consider lowering similarity threshold from 0.85 to 0.80", acceptRate*100)
			} else {
				rec.Recommended = rec.Current
				rec.Rationale = fmt.Sprintf("Current threshold (0.85) is appropriate based on %.0f%% acceptance rate", acceptRate*100)
			}
		case "low_confidence":
			rec.Current = 0.5
			if rejectRate := float64(rejected) / float64(total); rejectRate > 0.3 {
				rec.Recommended = 0.4
				rec.Rationale = fmt.Sprintf("%.0f%% of low-confidence flags were rejected; consider lowering threshold from 0.5 to 0.4", rejectRate*100)
			} else if acceptRate > 0.85 {
				rec.Recommended = 0.6
				rec.Rationale = fmt.Sprintf("%.0f%% of low-confidence flags were confirmed; consider raising threshold from 0.5 to 0.6", acceptRate*100)
			} else {
				rec.Recommended = rec.Current
				rec.Rationale = fmt.Sprintf("Current threshold (%.1f) is appropriate based on %.0f%% acceptance rate", rec.Current, acceptRate*100)
			}
		default:
			// For other reason types (stale, source_deleted), thresholds don't apply
			rec.Recommended = rec.Current
			rec.Rationale = "Threshold-based tuning not applicable for this reason type"
		}

		recs = append(recs, rec)
	}

	if recs == nil {
		recs = []ThresholdRecommendation{}
	}
	return recs, rows.Err()
}

func (s *Store) Close() error {
	return s.db.Close()
}

// encodeID converts an integer rowid to a 16-char hex string.
func encodeID(id int64) string {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(id))
	return hex.EncodeToString(b)
}

// decodeID reverses encodeID.
func decodeID(s string) (int64, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		return 0, fmt.Errorf("invalid id format")
	}
	return int64(binary.BigEndian.Uint64(b)), nil
}
