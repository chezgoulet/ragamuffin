// Package logstore provides an append-only log stream backed by SQLite.
package logstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	db *sql.DB
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

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("logstore: migrate: %w", err)
	}

	return &Store{db: db}, nil
}

// migrate creates the schema if it doesn't exist.
func migrate(db *sql.DB) error {
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
	`)
	return err
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
	var args []interface{}

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

// Close closes the database connection.
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


