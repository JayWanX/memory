package search

import (
	"container/heap"
	"sort"

	"github.com/JayWanX/memory/internal/memory/embed"
	"github.com/JayWanX/memory/internal/memory/model"
	"github.com/JayWanX/memory/internal/memory/store"
)

// referenceBeamSearch and referenceRankRecallTransitions preserve the traversal
// and intent-ranked visit budget from upstream 5c98ff9c. Keep their full-vector
// cosine calculations independent of the production scalar/transition cache.
func referenceBeamSearch(
	db *store.DB,
	startID string,
	startScore float64,
	queryVec []float64,
	weights IntentWeights,
	params TraversalParams,
	scoreMap map[string]float64,
	viaMap map[string]string,
	insightMap map[string]*model.Insight,
	embedCache map[string][]float64,
	activeByID map[string]*model.Insight,
	edgeCache map[string][]referenceRecallTransition,
) {
	visited := map[string]bool{startID: true}
	totalVisited := 1

	// Seed the beam with the anchor
	current := &beamHeap{{id: startID, score: startScore, depth: 0}}
	heap.Init(current)

	for depth := 0; depth < params.MaxDepth; depth++ {
		if current.Len() == 0 || totalVisited >= params.MaxVisited {
			break
		}

		// Collect all candidates for the next level
		next := &beamHeap{}
		heap.Init(next)

		// Process all nodes at the current level
		for current.Len() > 0 && totalVisited < params.MaxVisited {
			cur := heap.Pop(current).(beamItem)
			if cur.depth != depth {
				// Put it back — it's for a future level
				heap.Push(current, cur)
				break
			}

			transitions, cached := edgeCache[cur.id]
			if !cached {
				edges, err := db.GetNeighborEdges(cur.id)
				if err != nil {
					continue
				}
				transitions = referenceRankRecallTransitions(cur.id, edges, queryVec, weights, embedCache)
				edgeCache[cur.id] = transitions
			}

			for _, transition := range transitions {
				if totalVisited >= params.MaxVisited {
					break
				}
				neighborID := transition.id
				neighborScore := cur.score + transition.delta

				// Update global score map if this path is better
				if existing, ok := scoreMap[neighborID]; !ok || neighborScore > existing {
					scoreMap[neighborID] = neighborScore
					viaMap[neighborID] = string(transition.edgeType)
					if _, loaded := insightMap[neighborID]; !loaded {
						if ins, ok := activeByID[neighborID]; ok {
							insightMap[neighborID] = ins
						}
					}
				}

				if !visited[neighborID] {
					visited[neighborID] = true
					totalVisited++
					heap.Push(next, beamItem{
						id:    neighborID,
						score: neighborScore,
						depth: depth + 1,
					})
				}
			}
		}

		// Prune beam: keep only top beamWidth candidates for next level
		pruned := &beamHeap{}
		heap.Init(pruned)
		for next.Len() > 0 && pruned.Len() < params.BeamWidth {
			heap.Push(pruned, heap.Pop(next).(beamItem))
		}
		current = pruned
	}
}

type referenceRecallTransition struct {
	id       string
	edgeType model.EdgeType
	delta    float64
}

// referenceRankRecallTransitions applies the same intent and similarity terms as beam
// scoring before the visit budget can discard a neighbour. The ordering and
// deltas are reusable across anchors within this one recall, whose query and
// intent do not change. The current path score is added by the caller.
func referenceRankRecallTransitions(nodeID string, edges []*model.Edge, query []float64,
	weights IntentWeights, embeddings map[string][]float64) []referenceRecallTransition {
	transitions := make([]referenceRecallTransition, 0, len(edges))
	for _, e := range edges {
		id := e.TargetID
		if id == nodeID {
			id = e.SourceID
		}
		delta := lambda1 * weights[e.EdgeType] * e.Weight
		if query != nil {
			if vector, ok := embeddings[id]; ok {
				if similarity := embed.CosineSimilarity(query, vector); similarity > 0 {
					delta += lambda2 * similarity
				}
			}
		}
		transitions = append(transitions, referenceRecallTransition{id: id, edgeType: e.EdgeType, delta: delta})
	}
	sort.Slice(transitions, func(i, j int) bool {
		a, b := transitions[i], transitions[j]
		if a.delta != b.delta {
			return a.delta > b.delta
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return a.edgeType < b.edgeType
	})
	return transitions
}
