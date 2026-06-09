package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// LinkRecord represents a single extracted link in the link_index table.
type LinkRecord struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	LinkType   string `json:"type"`    // "wikilink" | "path_ref" | "tag_cluster"
	Context    string `json:"context"` // first 200 chars of surrounding text
}

// OutboundLink is a single link in response payloads (no source_path).
type OutboundLink struct {
	Target  string `json:"target"`
	Type    string `json:"type"`
	Context string `json:"context"`
}

// InboundLink is a backlink response (no target_path — it's the requested path).
type InboundLink struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Context string `json:"context"`
}

// GraphNode is a node in the link graph response.
type GraphNode struct {
	Path string `json:"path"`
	Type string `json:"type"` // "source", "wikilink", "path_ref", "tag_cluster"
}

// GraphEdge is an edge in the link graph response.
type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// LinkGraph is the full link graph response.
type LinkGraph struct {
	Path  string      `json:"path"`
	Depth int         `json:"depth"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// WriteLinks batch-inserts link records. Non-fatal on individual insert errors
// (logged upstream — link enrichment is not primary data).
func (s *Store) WriteLinks(ctx context.Context, vault string, links []LinkRecord) error {
	if len(links) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("links: begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO link_index (source_path, target_path, link_type, context, vault, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("links: prepare: %w", err)
	}
	defer stmt.Close()

	for _, l := range links {
		ctxStr := l.Context
		if len(ctxStr) > 200 {
			ctxStr = ctxStr[:200]
		}
		if _, err := stmt.ExecContext(ctx, l.SourcePath, l.TargetPath, l.LinkType, ctxStr, vault, now); err != nil {
			// Log and continue — link enrichment is non-fatal
			s.logger.Warn("links: write skipped", "source", l.SourcePath, "target", l.TargetPath, "error", err)
		}
	}

	return tx.Commit()
}

// DeleteLinksBySource removes all links originating from a given source path.
// Called during re-index (old links are stale).
func (s *Store) DeleteLinksBySource(ctx context.Context, sourcePath, vault string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM link_index WHERE source_path = ? AND vault = ?`, sourcePath, vault)
	if err != nil {
		return fmt.Errorf("links: delete source: %w", err)
	}
	return nil
}

// GetOutboundLinks returns all links FROM a given path.
// Returns empty slice (not nil) if no links exist.
func (s *Store) GetOutboundLinks(ctx context.Context, path, vault string) ([]OutboundLink, error) {
	var query string
	var args []any
	if vault != "" {
		query = "SELECT target_path, link_type, COALESCE(context, '') FROM link_index WHERE source_path = ? AND vault = ? ORDER BY id"
		args = []any{path, vault}
	} else {
		query = "SELECT target_path, link_type, COALESCE(context, '') FROM link_index WHERE source_path = ? ORDER BY id"
		args = []any{path}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("links: query outbound: %w", err)
	}
	defer rows.Close()

	var links []OutboundLink
	for rows.Next() {
		var l OutboundLink
		if err := rows.Scan(&l.Target, &l.Type, &l.Context); err != nil {
			return nil, fmt.Errorf("links: scan outbound: %w", err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("links: rows err: %w", err)
	}
	if links == nil {
		links = []OutboundLink{}
	}
	return links, nil
}

// GetInboundLinks returns all links TO a given path.
// Returns empty slice (not nil) if no backlinks exist.
func (s *Store) GetInboundLinks(ctx context.Context, path, vault string) ([]InboundLink, error) {
	var query string
	var args []any
	if vault != "" {
		query = "SELECT source_path, link_type, COALESCE(context, '') FROM link_index WHERE target_path = ? AND vault = ? ORDER BY id"
		args = []any{path, vault}
	} else {
		query = "SELECT source_path, link_type, COALESCE(context, '') FROM link_index WHERE target_path = ? ORDER BY id"
		args = []any{path}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("links: query inbound: %w", err)
	}
	defer rows.Close()

	var links []InboundLink
	for rows.Next() {
		var l InboundLink
		if err := rows.Scan(&l.Source, &l.Type, &l.Context); err != nil {
			return nil, fmt.Errorf("links: scan inbound: %w", err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("links: rows err: %w", err)
	}
	if links == nil {
		links = []InboundLink{}
	}
	return links, nil
}

// GetLinkGraph performs a BFS traversal of the link graph starting from seedPath,
// up to maxDepth hops (capped at 5). Returns nodes and edges.
func (s *Store) GetLinkGraph(ctx context.Context, seedPath string, depth, vault string) (*LinkGraph, error) {
	maxDepth := 5 // server cap
	if depth != "" {
		if d := 0; fmt.Sscanf(depth, "%d", &d) == nil && d > 0 {
			if d < maxDepth {
				maxDepth = d
			}
		}
	}

	graph := &LinkGraph{
		Path:  seedPath,
		Depth: maxDepth,
		Nodes: []GraphNode{},
		Edges: []GraphEdge{},
	}

	seen := map[string]bool{seedPath: true}
	current := []string{seedPath}

	graph.Nodes = append(graph.Nodes, GraphNode{Path: seedPath, Type: "source"})

	for hop := 0; hop < maxDepth && len(current) > 0; hop++ {
		var next []string

		for _, source := range current {
			var q string
				var a []any
				if vault != "" {
					q = "SELECT target_path, link_type FROM link_index WHERE source_path = ? AND vault = ?"
					a = []any{source, vault}
				} else {
					q = "SELECT target_path, link_type FROM link_index WHERE source_path = ?"
					a = []any{source}
				}
				rows, err := s.db.QueryContext(ctx, q, a...)
			if err != nil {
				return nil, fmt.Errorf("links: graph query at depth %d: %w", hop, err)
			}

			for rows.Next() {
				var target, linkType string
				if err := rows.Scan(&target, &linkType); err != nil {
					rows.Close()
					return nil, fmt.Errorf("links: graph scan: %w", err)
				}

				graph.Edges = append(graph.Edges, GraphEdge{
					Source: source,
					Target: target,
					Type:   linkType,
				})

				if !seen[target] {
					seen[target] = true
					next = append(next, target)
					var nodeType string
					if hop == 0 {
						nodeType = linkType
					} else {
						nodeType = linkType
					}
					graph.Nodes = append(graph.Nodes, GraphNode{Path: target, Type: nodeType})
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("links: graph rows err: %w", err)
			}
		}
		current = next
	}

	if graph.Nodes == nil {
		graph.Nodes = []GraphNode{}
	}
	if graph.Edges == nil {
		graph.Edges = []GraphEdge{}
	}
	return graph, nil
}

// GetAllPaths returns all distinct source_paths in the link_index for a given vault.
func (s *Store) GetAllPaths(ctx context.Context, vault string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT source_path FROM link_index WHERE vault = ?`, vault)
	if err != nil {
		return nil, fmt.Errorf("links: query paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("links: scan path: %w", err)
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("links: rows err: %w", err)
	}
	if paths == nil {
		paths = []string{}
	}
	return paths, nil
}

// LinkIndexWriter is the interface used by the indexer to persist links.
// The indexer defines its own interface; this logstore-level type is provided
// for convenience in tests and wiring.
type LinkIndexWriter interface {
	WriteLinks(ctx context.Context, vault string, links []LinkRecord) error
	DeleteLinksBySource(ctx context.Context, sourcePath, vault string) error
	Close() error
}

// EnsureStoreHasLinksWriter satisfies the compile-time check.
var _ LinkIndexWriter = (*Store)(nil)
