// Package graph implements a bi-temporal knowledge graph over SQLite.
//
// The graph stores entities (person, org, project, concept) and typed edges
// (relations) extracted from chunks and session turns. Edges carry validity
// intervals so the graph supports time-travel queries: given an `as_of`
// timestamp, callers see the graph state as it was known/valid at that time.
//
// Bi-temporal model (following Zep/Graphiti, Rasmussen et al. arXiv:2501.13956):
//   - valid_from / valid_until  — when the relation is/was true in the world.
//   - created_at                — when the system learned it (transaction time).
//   - invalidated_at            — when the system superseded it. Edges are
//     invalidated, never deleted, so history is reconstructable.
package graph

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// EntityKind classifies a graph node.
type EntityKind string

const (
	KindPerson  EntityKind = "person"
	KindOrg     EntityKind = "org"
	KindProject EntityKind = "project"
	KindConcept EntityKind = "concept"
)

// Entity is a graph node scoped to a vault.
type Entity struct {
	ID        string     `json:"id"`
	Vault     string     `json:"vault"`
	Name      string     `json:"name"`
	Kind      EntityKind `json:"kind"`
	Summary   string     `json:"summary,omitempty"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

// Edge is a typed, bi-temporal relation between two entities.
type Edge struct {
	ID            string `json:"id"`
	Vault         string `json:"vault"`
	SourceID      string `json:"source_id"`
	TargetID      string `json:"target_id"`
	Type          string `json:"type"`
	Fact          string `json:"fact,omitempty"`
	ValidFrom     string `json:"valid_from"`
	ValidUntil    string `json:"valid_until,omitempty"`
	CreatedAt     string `json:"created_at"`
	InvalidatedAt string `json:"invalidated_at,omitempty"`
}

// Store is the SQLite-backed bi-temporal graph.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

// Open opens or creates the graph database at path, creating parent dirs.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("graph: create dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("graph: open: %w", err)
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("graph: pragma %q: %w", pragma, err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("graph: migrate: %w", err)
	}
	return &Store{db: db, logger: slog.Default()}, nil
}

// SetLogger overrides the default logger.
func (s *Store) SetLogger(l *slog.Logger) {
	if l != nil {
		s.logger = l
	}
}

// Close releases the database.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS graph_entities (
			id         TEXT PRIMARY KEY,
			vault      TEXT NOT NULL,
			name       TEXT NOT NULL,
			kind       TEXT NOT NULL,
			summary    TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_vault_name ON graph_entities(vault, name, kind);
		CREATE INDEX IF NOT EXISTS idx_entity_vault ON graph_entities(vault);

		CREATE TABLE IF NOT EXISTS graph_edges (
			id             TEXT PRIMARY KEY,
			vault          TEXT NOT NULL,
			source_id      TEXT NOT NULL,
			target_id      TEXT NOT NULL,
			type           TEXT NOT NULL,
			fact           TEXT NOT NULL DEFAULT '',
			valid_from     TEXT NOT NULL,
			valid_until    TEXT NOT NULL DEFAULT '',
			created_at     TEXT NOT NULL,
			invalidated_at TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_edge_vault   ON graph_edges(vault);
		CREATE INDEX IF NOT EXISTS idx_edge_source  ON graph_edges(source_id);
		CREATE INDEX IF NOT EXISTS idx_edge_target  ON graph_edges(target_id);
		CREATE INDEX IF NOT EXISTS idx_edge_type    ON graph_edges(type);
	`)
	if err != nil {
		return err
	}
	return nil
}

// UpsertEntity inserts an entity or returns the id of an existing one with the
// same (vault, name, kind). Entity identity is name-based within a vault.
func (s *Store) UpsertEntity(ctx context.Context, e Entity) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var existingID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM graph_entities WHERE vault = ? AND name = ? AND kind = ?`,
		e.Vault, e.Name, string(e.Kind)).Scan(&existingID)
	switch {
	case err == sql.ErrNoRows:
		if e.ID == "" {
			return "", fmt.Errorf("graph: upsert entity: empty id")
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO graph_entities (id, vault, name, kind, summary, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.Vault, e.Name, string(e.Kind), e.Summary, now, now); err != nil {
			return "", fmt.Errorf("graph: insert entity: %w", err)
		}
		return e.ID, nil
	case err != nil:
		return "", fmt.Errorf("graph: lookup entity: %w", err)
	default:
		if e.Summary != "" {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE graph_entities SET summary = ?, updated_at = ? WHERE id = ?`,
				e.Summary, now, existingID); err != nil {
				return "", fmt.Errorf("graph: update entity: %w", err)
			}
		}
		return existingID, nil
	}
}

// AddEdge inserts a new relation. valid_from defaults to now when empty.
//
// If invalidatePrior is true, any currently-valid edge with the same
// (vault, source, type) is invalidated first, modeling a single-valued
// relation from the source (e.g. a person's current employer): asserting a new
// target supersedes the old one. Prior edges are invalidated, never deleted, so
// `as_of` time-travel reconstructs the earlier state. Callers that want
// multi-valued relations (e.g. "knows") should pass invalidatePrior=false.
func (s *Store) AddEdge(ctx context.Context, e Edge, invalidatePrior bool) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if e.ValidFrom == "" {
		e.ValidFrom = now
	}
	if e.ID == "" {
		return "", fmt.Errorf("graph: add edge: empty id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("graph: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if invalidatePrior {
		if _, err := tx.ExecContext(ctx,
			`UPDATE graph_edges
			 SET invalidated_at = ?, valid_until = CASE WHEN valid_until = '' THEN ? ELSE valid_until END
			 WHERE vault = ? AND source_id = ? AND type = ? AND invalidated_at = ''`,
			now, e.ValidFrom, e.Vault, e.SourceID, e.Type); err != nil {
			return "", fmt.Errorf("graph: invalidate prior edge: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO graph_edges (id, vault, source_id, target_id, type, fact, valid_from, valid_until, created_at, invalidated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		e.ID, e.Vault, e.SourceID, e.TargetID, e.Type, e.Fact, e.ValidFrom, e.ValidUntil, now); err != nil {
		return "", fmt.Errorf("graph: insert edge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("graph: commit: %w", err)
	}
	return e.ID, nil
}

// InvalidateEdge marks an edge invalidated as of now (soft delete).
func (s *Store) InvalidateEdge(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE graph_edges SET invalidated_at = ? WHERE id = ? AND invalidated_at = ''`,
		now, id)
	if err != nil {
		return fmt.Errorf("graph: invalidate edge: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("graph: edge %q not found or already invalidated", id)
	}
	return nil
}

// GetEntity returns an entity by id.
func (s *Store) GetEntity(ctx context.Context, id string) (*Entity, error) {
	var e Entity
	var kind string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, vault, name, kind, summary, created_at, updated_at FROM graph_entities WHERE id = ?`,
		id).Scan(&e.ID, &e.Vault, &e.Name, &kind, &e.Summary, &e.CreatedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("graph: get entity: %w", err)
	}
	e.Kind = EntityKind(kind)
	return &e, nil
}

// EdgeQuery filters temporal edge queries.
type EdgeQuery struct {
	Vault    string
	Type     string // optional
	EntityID string // optional — edges touching this entity (as source or target)
	AsOf     string // optional RFC3339 — only edges valid at this instant
	Limit    int
}

// Edges returns edges matching the query. When AsOf is set, edges whose
// validity interval [valid_from, valid_until) contains that instant are
// returned — valid-time travel ("what was true then"). When AsOf is empty,
// only currently-valid (non-invalidated) edges are returned.
func (s *Store) Edges(ctx context.Context, q EdgeQuery) ([]Edge, error) {
	var where []string
	var args []any
	where = append(where, "vault = ?")
	args = append(args, q.Vault)

	if q.Type != "" {
		where = append(where, "type = ?")
		args = append(args, q.Type)
	}
	if q.EntityID != "" {
		where = append(where, "(source_id = ? OR target_id = ?)")
		args = append(args, q.EntityID, q.EntityID)
	}
	if q.AsOf != "" {
		// Valid-time travel: "what was true in the world at instant t".
		//   valid_from <= t AND (valid_until == '' OR valid_until > t)
		// We intentionally do NOT filter on transaction time (created_at /
		// invalidated_at) here: callers asking `as_of` want the state of the
		// world, not what the system happened to know at that wall-clock moment.
		// An edge whose validity was later backdated still counts.
		where = append(where,
			"valid_from <= ?",
			"(valid_until = '' OR valid_until > ?)")
		args = append(args, q.AsOf, q.AsOf)
	} else {
		where = append(where, "invalidated_at = ''")
	}

	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `SELECT id, vault, source_id, target_id, type, fact, valid_from, valid_until, created_at, invalidated_at
	          FROM graph_edges WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY valid_from DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("graph: query edges: %w", err)
	}
	defer rows.Close()

	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.ID, &e.Vault, &e.SourceID, &e.TargetID, &e.Type,
			&e.Fact, &e.ValidFrom, &e.ValidUntil, &e.CreatedAt, &e.InvalidatedAt); err != nil {
			return nil, fmt.Errorf("graph: scan edge: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntityView bundles an entity with its temporal edges for the entity endpoint.
type EntityView struct {
	Entity *Entity `json:"entity"`
	Edges  []Edge  `json:"edges"`
	AsOf   string  `json:"as_of,omitempty"`
}

// EntityAsOf returns an entity and its edges valid at the given instant
// (empty asOf = current state).
func (s *Store) EntityAsOf(ctx context.Context, id, asOf string) (*EntityView, error) {
	ent, err := s.GetEntity(ctx, id)
	if err != nil {
		return nil, err
	}
	if ent == nil {
		return nil, nil
	}
	edges, err := s.Edges(ctx, EdgeQuery{Vault: ent.Vault, EntityID: id, AsOf: asOf})
	if err != nil {
		return nil, err
	}
	return &EntityView{Entity: ent, Edges: edges, AsOf: asOf}, nil
}

// Stats reports counts for a vault (or all vaults when vault is empty).
func (s *Store) Stats(ctx context.Context, vault string) (entities, edges, invalidated int, err error) {
	entQ := `SELECT COUNT(*) FROM graph_entities`
	edgeQ := `SELECT COUNT(*) FROM graph_edges`
	invQ := `SELECT COUNT(*) FROM graph_edges WHERE invalidated_at != ''`
	var args []any
	if vault != "" {
		entQ += ` WHERE vault = ?`
		edgeQ += ` WHERE vault = ?`
		invQ += ` AND vault = ?`
		args = append(args, vault)
	}
	if err = s.db.QueryRowContext(ctx, entQ, args...).Scan(&entities); err != nil {
		return 0, 0, 0, fmt.Errorf("graph: count entities: %w", err)
	}
	if err = s.db.QueryRowContext(ctx, edgeQ, args...).Scan(&edges); err != nil {
		return 0, 0, 0, fmt.Errorf("graph: count edges: %w", err)
	}
	if err = s.db.QueryRowContext(ctx, invQ, args...).Scan(&invalidated); err != nil {
		return 0, 0, 0, fmt.Errorf("graph: count invalidated: %w", err)
	}
	return entities, edges, invalidated, nil
}
