package graph

import (
	"database/sql"
	"errors"
	"github.com/JayWanX/memory/internal/memory/model"
	"github.com/JayWanX/memory/internal/memory/store"
)

// BFSNode represents a node discovered during BFS traversal.
type BFSNode struct {
	Insight *model.Insight
	Hop     int
	ViaEdge *model.Edge
}

// BFSOptions controls BFS traversal behavior.
type BFSOptions struct {
	MaxDepth   int            // maximum hop distance from start
	MaxNodes   int            // maximum nodes to return (0 = unlimited)
	EdgeFilter model.EdgeType // filter by edge type (empty = all types)
}

// BFS performs breadth-first traversal. Bounded neighborhoods use indexed
// reads so disconnected history is not loaded. Unlimited traversals preload the
// graph to avoid a query per node. Both paths preserve edge insertion order.
// The start node is excluded. Only active (non-deleted) nodes are visited.
func BFS(db *store.DB, startID string, opts BFSOptions) []BFSNode {
	if opts.MaxDepth <= 0 {
		return nil
	}
	view, err := newBFSView(db, opts)
	if err != nil {
		return nil
	}

	type entry struct {
		id  string
		hop int
	}

	visited := map[string]bool{startID: true}
	queue := []entry{{id: startID, hop: 0}}
	var result []BFSNode

	for len(queue) > 0 {
		if opts.MaxNodes > 0 && len(result) >= opts.MaxNodes {
			break
		}

		cur := queue[0]
		queue = queue[1:]

		if cur.hop >= opts.MaxDepth {
			continue
		}

		edges, err := view.edgesFor(cur.id, opts.EdgeFilter)
		if err != nil {
			return nil
		}
		for _, edge := range edges {
			if opts.EdgeFilter != "" && edge.EdgeType != opts.EdgeFilter {
				continue
			}

			neighborID := edge.TargetID
			if neighborID == cur.id {
				neighborID = edge.SourceID
			}

			if visited[neighborID] {
				continue
			}
			visited[neighborID] = true

			insight, err := view.insightFor(neighborID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if insight == nil {
				continue // soft-deleted or missing
			}

			result = append(result, BFSNode{
				Insight: insight,
				Hop:     cur.hop + 1,
				ViaEdge: edge,
			})

			if opts.MaxNodes > 0 && len(result) >= opts.MaxNodes {
				break
			}

			queue = append(queue, entry{
				id:  neighborID,
				hop: cur.hop + 1,
			})
		}
	}

	return result
}

// bfsView owns either the bulk snapshot or indexed reads for this traversal.
// It never retains state between requests or starts background work.
type bfsView struct {
	db       *store.DB
	insights map[string]*model.Insight
	adj      map[string][]*model.Edge
}

func newBFSView(db *store.DB, opts BFSOptions) (*bfsView, error) {
	v := &bfsView{db: db}
	if opts.MaxNodes > 0 {
		return v, nil
	}
	all, err := db.GetAllActiveInsights()
	if err != nil {
		return nil, err
	}
	v.insights = make(map[string]*model.Insight, len(all))
	for _, ins := range all {
		v.insights[ins.ID] = ins
	}
	edges, err := db.GetAllEdges()
	if err != nil {
		return nil, err
	}
	v.adj = make(map[string][]*model.Edge)
	for _, edge := range edges {
		v.adj[edge.SourceID] = append(v.adj[edge.SourceID], edge)
		if edge.SourceID != edge.TargetID {
			v.adj[edge.TargetID] = append(v.adj[edge.TargetID], edge)
		}
	}
	return v, nil
}

func (v *bfsView) edgesFor(id string, edgeType model.EdgeType) ([]*model.Edge, error) {
	if v.adj != nil {
		return v.adj[id], nil
	}
	return v.db.GetTraversalEdges(id, edgeType)
}

func (v *bfsView) insightFor(id string) (*model.Insight, error) {
	if v.insights != nil {
		return v.insights[id], nil
	}
	return v.db.GetInsightByID(id)
}
