package search

import (
	"sort"

	"github.com/JayWanX/memory/internal/memory/model"
)

type recallTransition struct {
	id       string
	edgeType model.EdgeType
	delta    float64
}

// rankRecallTransitions applies the same intent and similarity terms as beam
// scoring before the visit budget can discard a neighbour. The ordering and
// deltas are reusable across anchors within this one recall, whose query and
// intent do not change. The current path score is added by the caller.
func rankRecallTransitions(nodeID string, edges []*model.Edge,
	weights IntentWeights, similarities map[string]float64) []recallTransition {
	transitions := make([]recallTransition, 0, len(edges))
	for _, e := range edges {
		id := e.TargetID
		if id == nodeID {
			id = e.SourceID
		}
		delta := lambda1 * weights[e.EdgeType] * e.Weight
		if similarity := similarities[id]; similarity > 0 {
			delta += lambda2 * similarity
		}
		transitions = append(transitions, recallTransition{id: id, edgeType: e.EdgeType, delta: delta})
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
