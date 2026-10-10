package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/JayWanX/memory/internal/memory/model"
)

// InsertEdge inserts or replaces an edge.
func (db *DB) InsertEdge(e *model.Edge) error {
	if e.EdgeType == model.EdgeSupersedes && e.SourceID == e.TargetID {
		return fmt.Errorf("supersedes requires distinct insights")
	}
	return db.execInsert(&db.txInsertEdge,
		`INSERT OR REPLACE INTO edges (source_id, target_id, edge_type, weight, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.SourceID, e.TargetID, string(e.EdgeType), e.Weight,
		e.MetadataJSON(), e.CreatedAt.Format(time.RFC3339),
	)
}

// GetEdgesByNode returns all edges where the given node is source or target.
// Uses UNION ALL to allow SQLite to use separate indexes on source_id and target_id.
func (db *DB) GetEdgesByNode(nodeID string) ([]*model.Edge, error) {
	rows, err := db.execer().Query(
		`SELECT source_id, target_id, edge_type, weight, metadata, created_at
		 FROM edges WHERE source_id = ?
		 UNION ALL
		 SELECT source_id, target_id, edge_type, weight, metadata, created_at
		 FROM edges WHERE target_id = ? AND source_id != ?`,
		nodeID, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// GetNeighborEdges returns every edge touching nodeID with the columns beam
// search scores on (metadata and created_at are left zero). Both halves are
// answered from covering indexes in a fixed store order. Recall ranks the full
// intent and similarity transition score before applying its visit budget.
func (db *DB) GetNeighborEdges(nodeID string) ([]*model.Edge, error) {
	rows, err := db.execer().Query(
		`SELECT source_id, target_id, edge_type, weight FROM edges WHERE source_id = ?
		 UNION ALL
		 SELECT source_id, target_id, edge_type, weight FROM edges WHERE target_id = ? AND source_id != ?
		 ORDER BY weight DESC, source_id, target_id, edge_type`,
		nodeID, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []*model.Edge
	for rows.Next() {
		var e model.Edge
		var edgeType string
		if err := rows.Scan(&e.SourceID, &e.TargetID, &edgeType, &e.Weight); err != nil {
			return nil, err
		}
		e.EdgeType = model.EdgeType(edgeType)
		results = append(results, &e)
	}
	return results, rows.Err()
}

// GetTraversalEdges returns incident edges in insertion order, optionally
// filtered by type. BFS previously obtained this order by scanning the entire
// edges table; preserving it matters when a node limit cuts a frontier.
func (db *DB) GetTraversalEdges(nodeID string, edgeType model.EdgeType) ([]*model.Edge, error) {
	query, args := traversalEdgeQuery(nodeID, edgeType)
	rows, err := db.execer().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges, err := scanEdges(rows)
	if err != nil {
		return nil, err
	}
	return edges, rows.Err()
}

func traversalEdgeQuery(nodeID string, edgeType model.EdgeType) (string, []any) {
	const columns = "source_id, target_id, edge_type, weight, metadata, created_at"
	source := "SELECT rowid AS edge_order, " + columns + " FROM edges WHERE source_id = ?"
	target := "SELECT rowid AS edge_order, " + columns + " FROM edges WHERE target_id = ? AND source_id != ?"
	args := []any{nodeID}
	if edgeType != "" {
		source += " AND edge_type = ?"
		target += " AND edge_type = ?"
		args = append(args, string(edgeType))
	}
	args = append(args, nodeID, nodeID)
	if edgeType != "" {
		args = append(args, string(edgeType))
	}
	// UNION keeps each endpoint lookup on its own composite index. A factored
	// OR with an edge_type filter can instead scan every edge of that type.
	// Excluding self-loops from the target branch returns each edge only once.
	query := "SELECT " + columns + " FROM (" + source + " UNION ALL " + target + ") ORDER BY edge_order"
	return query, args
}

// GetSupersededIDs checks only the requested targets, using the covering partial
// index when available. A store can accumulate an unbounded supersession history;
// unrelated history must not be scanned for a small recall candidate set.
// Read-only legacy stores without the partial index retain the scoped fallback.
func (db *DB) GetSupersededIDs(ids []string) (map[string]bool, error) {
	return scopedSupersededIDs(db.execer(), ids)
}

// supersededLookupChunk bounds how many ids go into one IN clause. SQLite's
// host-parameter ceiling is 32766 on current builds and 999 on older ones;
// 500 stays inside both. Recall's candidate set is normally far smaller, so
// the loop below runs once.
const supersededLookupChunk = 500

const supersededLookupSQL = `SELECT DISTINCT target_id FROM edges INDEXED BY idx_edges_supersedes
	WHERE edge_type = 'supersedes' AND source_id != target_id AND target_id IN (%s)`

// scopedSupersededIDs bounds each query to the SQLite parameter limit.
func scopedSupersededIDs(ex dbExecer, ids []string) (map[string]bool, error) {
	superseded := make(map[string]bool)
	for start := 0; start < len(ids); start += supersededLookupChunk {
		end := min(start+supersededLookupChunk, len(ids))
		if err := collectSupersededIDs(ex, ids[start:end], superseded); err != nil {
			return nil, err
		}
	}
	return superseded, nil
}

// collectSupersededIDs adds the superseded ids in one batch to into. The rows
// are closed before returning: the pool holds a single connection, so an open
// cursor would block the next batch.
func collectSupersededIDs(ex dbExecer, chunk []string, into map[string]bool) error {
	args := make([]any, 0, len(chunk))
	placeholders := make([]string, len(chunk))
	for i, id := range chunk {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(supersededLookupSQL, strings.Join(placeholders, ","))
	rows, err := ex.Query(query, args...)
	if err != nil && strings.Contains(err.Error(), "no such index: idx_edges_supersedes") {
		rows, err = ex.Query(strings.Replace(query, " INDEXED BY idx_edges_supersedes", "", 1), args...)
	}
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		into[id] = true
	}
	return rows.Err()
}

// GetEdgesByNodeAndType returns edges for a node filtered by edge type.
// Uses UNION ALL to allow SQLite to use composite indexes.
func (db *DB) GetEdgesByNodeAndType(nodeID string, edgeType model.EdgeType) ([]*model.Edge, error) {
	rows, err := db.execer().Query(
		`SELECT source_id, target_id, edge_type, weight, metadata, created_at
		 FROM edges WHERE source_id = ? AND edge_type = ?
		 UNION ALL
		 SELECT source_id, target_id, edge_type, weight, metadata, created_at
		 FROM edges WHERE target_id = ? AND edge_type = ? AND source_id != ?`,
		nodeID, string(edgeType), nodeID, string(edgeType), nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// GetEdgesBySourceAndType returns edges where the given node is source, filtered by type.
func (db *DB) GetEdgesBySourceAndType(sourceID string, edgeType model.EdgeType) ([]*model.Edge, error) {
	rows, err := db.execer().Query(
		`SELECT source_id, target_id, edge_type, weight, metadata, created_at
		 FROM edges WHERE source_id = ? AND edge_type = ?`, sourceID, string(edgeType))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// FindInsightsWithEntity returns insight IDs that have the given entity in their entities JSON array.
func (db *DB) FindInsightsWithEntity(entity string, excludeID string, limit int) ([]string, error) {
	rows, err := db.execer().Query(
		`SELECT DISTINCT i.id FROM insights i, json_each(i.entities) je
		 WHERE i.deleted_at IS NULL AND i.id != ? AND je.value = ?
		 ORDER BY i.created_at DESC LIMIT ?`,
		excludeID, entity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetAllEdges returns all edges in the graph.
func (db *DB) GetAllEdges() ([]*model.Edge, error) {
	rows, err := db.execer().Query(
		`SELECT source_id, target_id, edge_type, weight, metadata, created_at FROM edges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// DeleteEdge removes one typed edge between two nodes.
func (db *DB) DeleteEdge(sourceID string, targetID string, edgeType model.EdgeType) error {
	_, err := db.execer().Exec(
		`DELETE FROM edges WHERE source_id = ? AND target_id = ? AND edge_type = ?`,
		sourceID, targetID, string(edgeType))
	return err
}

// DeleteEdgesByNode removes all edges referencing a node.
func (db *DB) DeleteEdgesByNode(nodeID string) error {
	_, err := db.execer().Exec(
		`DELETE FROM edges WHERE source_id = ? OR target_id = ?`, nodeID, nodeID)
	return err
}

func scanEdges(rows interface {
	Next() bool
	Scan(...interface{}) error
}) ([]*model.Edge, error) {
	var results []*model.Edge
	for rows.Next() {
		var e model.Edge
		var edgeType, metadata, createdAt string
		err := rows.Scan(&e.SourceID, &e.TargetID, &edgeType, &e.Weight, &metadata, &createdAt)
		if err != nil {
			return nil, err
		}
		e.EdgeType = model.EdgeType(edgeType)
		e.ParseMetadata(metadata)
		if e.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
			return nil, fmt.Errorf("parse edge created_at (%s→%s): %w", e.SourceID, e.TargetID, err)
		}
		results = append(results, &e)
	}
	return results, nil
}
