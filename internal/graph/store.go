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
	"encoding/json"
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

		CREATE TABLE IF NOT EXISTS graph_communities (
			id         TEXT PRIMARY KEY,
			vault      TEXT NOT NULL,
			label      INTEGER NOT NULL,
			member_ids TEXT NOT NULL DEFAULT '[]',
			size       INTEGER NOT NULL DEFAULT 0,
			summary    TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_community_vault ON graph_communities(vault);
	`)
	if err != nil {
		return err
	}
	return nil
}

// normalizeTimestamp reparses a caller-supplied timestamp into RFC3339 UTC so
// stored valid-time bounds sort lexicographically. Empty input stays empty;
// unparseable input is returned unchanged (best effort — never fail a write on
// a malformed optional bound).
func normalizeTimestamp(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339)
}

// UpsertEntity inserts an entity or returns the id of an existing one with the
// same (vault, name, kind). Entity identity is name-based within a vault.
func (s *Store) UpsertEntity(ctx context.Context, e Entity) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var existingID string
	// Dedup case-insensitively: the extractor resolves relation endpoints via a
	// lowercased name map, so "Alice" and "alice" must map to a single entity or
	// relations silently attach to the wrong (last-written) row.
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM graph_entities WHERE vault = ? AND LOWER(name) = LOWER(?) AND kind = ?`,
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
	// Normalize caller-supplied valid-time bounds to RFC3339 UTC so the
	// lexicographic comparisons used by as_of time-travel queries are correct
	// even when callers pass offset timestamps or fractional seconds.
	e.ValidFrom = normalizeTimestamp(e.ValidFrom)
	e.ValidUntil = normalizeTimestamp(e.ValidUntil)
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
		// Close the prior edge's valid-time interval at the new edge's
		// valid_from so the two intervals stay disjoint. If the prior edge
		// already had an explicit valid_until later than the new valid_from,
		// clamp it down to valid_from; otherwise set it. This guarantees an
		// as_of query in the transition window returns exactly one edge.
		if _, err := tx.ExecContext(ctx,
			`UPDATE graph_edges
			 SET invalidated_at = ?,
			     valid_until = CASE
			         WHEN valid_until = '' OR valid_until > ? THEN ?
			         ELSE valid_until
			     END
			 WHERE vault = ? AND source_id = ? AND type = ? AND invalidated_at = ''`,
			now, e.ValidFrom, e.ValidFrom, e.Vault, e.SourceID, e.Type); err != nil {
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Signal truncation: callers (e.g. community detection) that assume a
	// complete edge set will silently operate on a partial graph otherwise.
	if len(out) == limit {
		s.logger.Warn("graph: edge query hit result limit; results may be truncated",
			"limit", limit, "vault", q.Vault, "type", q.Type)
	}
	return out, nil
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

// ListEntities returns all entities in a vault, ordered by id for determinism.
func (s *Store) ListEntities(ctx context.Context, vault string) ([]Entity, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, vault, name, kind, summary, created_at, updated_at
		 FROM graph_entities WHERE vault = ? ORDER BY id`, vault)
	if err != nil {
		return nil, fmt.Errorf("graph: list entities: %w", err)
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		var kind string
		if err := rows.Scan(&e.ID, &e.Vault, &e.Name, &kind, &e.Summary, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("graph: scan entity: %w", err)
		}
		e.Kind = EntityKind(kind)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Community is a detected cluster of entities with an optional LLM summary.
type Community struct {
	ID        string   `json:"id"`
	Vault     string   `json:"vault"`
	Label     int      `json:"label"`
	MemberIDs []string `json:"member_ids"`
	Size      int      `json:"size"`
	Summary   string   `json:"summary,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// ReplaceCommunities atomically clears and rewrites the community set for a vault.
// Community detection is a full recompute, so prior rows are discarded.
func (s *Store) ReplaceCommunities(ctx context.Context, vault string, comms []Community) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("graph: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM graph_communities WHERE vault = ?`, vault); err != nil {
		return fmt.Errorf("graph: clear communities: %w", err)
	}
	for _, c := range comms {
		members, _ := json.Marshal(c.MemberIDs)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO graph_communities (id, vault, label, member_ids, size, summary, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, vault, c.Label, string(members), len(c.MemberIDs), c.Summary, now, now); err != nil {
			return fmt.Errorf("graph: insert community: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("graph: commit communities: %w", err)
	}
	return nil
}

// UpdateCommunitySummary sets the summary text for one community.
func (s *Store) UpdateCommunitySummary(ctx context.Context, id, summary string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE graph_communities SET summary = ?, updated_at = ? WHERE id = ?`,
		summary, now, id)
	if err != nil {
		return fmt.Errorf("graph: update community summary: %w", err)
	}
	return nil
}

// GetCommunity returns one community by id (nil when not found).
func (s *Store) GetCommunity(ctx context.Context, id string) (*Community, error) {
	var c Community
	var members string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, vault, label, member_ids, size, summary, created_at, updated_at
		 FROM graph_communities WHERE id = ?`, id).
		Scan(&c.ID, &c.Vault, &c.Label, &members, &c.Size, &c.Summary, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("graph: get community: %w", err)
	}
	_ = json.Unmarshal([]byte(members), &c.MemberIDs)
	return &c, nil
}

// Communities lists all communities in a vault, largest first.
func (s *Store) Communities(ctx context.Context, vault string) ([]Community, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, vault, label, member_ids, size, summary, created_at, updated_at
		 FROM graph_communities WHERE vault = ? ORDER BY size DESC, id`, vault)
	if err != nil {
		return nil, fmt.Errorf("graph: list communities: %w", err)
	}
	defer rows.Close()
	var out []Community
	for rows.Next() {
		var c Community
		var members string
		if err := rows.Scan(&c.ID, &c.Vault, &c.Label, &members, &c.Size, &c.Summary, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("graph: scan community: %w", err)
		}
		_ = json.Unmarshal([]byte(members), &c.MemberIDs)
		out = append(out, c)
	}
	return out, rows.Err()
}
