package search

import (
	"github.com/JayWanX/memory/internal/memory/embed"
	"github.com/JayWanX/memory/internal/memory/model"
	"github.com/JayWanX/memory/internal/memory/store"
)

// recallCache belongs to one synchronous recall. It is discarded afterwards so
// updates, deletions and changed query vectors cannot leave stale cached state.
type recallCache struct {
	db           *store.DB
	insights     map[string]*model.Insight
	transitions  map[string][]recallTransition
	weights      IntentWeights
	similarities map[string]float64
}

func newRecallCache(db *store.DB, all []*model.Insight, queryVec []float64, weights IntentWeights) *recallCache {
	c := &recallCache{
		db: db, insights: make(map[string]*model.Insight, len(all)),
		transitions: make(map[string][]recallTransition), weights: weights,
	}
	for _, ins := range all {
		c.insights[ins.ID] = ins
	}
	if queryVec != nil {
		c.similarities = loadQuerySimilarities(db, queryVec)
	}
	return c
}

// transitionsFor keeps the complete intent-ranked neighborhood, as upstream
// requires before applying the per-anchor visit budget. Query and intent are
// fixed for this cache, so both ordering and deltas can be reused across anchors.
func (c *recallCache) transitionsFor(id string) ([]recallTransition, error) {
	if transitions, ok := c.transitions[id]; ok {
		return transitions, nil
	}
	edges, err := c.db.GetNeighborEdges(id)
	if err != nil {
		return nil, err // Failed reads may be retried by another anchor.
	}
	transitions := rankRecallTransitions(id, edges, c.weights, c.similarities)
	c.transitions[id] = transitions // Retain empty neighborhoods too.
	return transitions, nil
}

func loadQuerySimilarities(db *store.DB, queryVec []float64) map[string]float64 {
	scores := make(map[string]float64)
	var decoded []float64
	// The callback only computes a scalar; it never re-enters the single-conn DB.
	// Consume each vector immediately so the next row can reuse its decode buffer.
	err := db.ScanEmbeddings(func(id string, blob []byte) bool {
		if vec := embed.DeserializeVectorInto(blob, decoded); vec != nil {
			decoded = vec
			scores[id] = embed.CosineSimilarity(queryVec, vec)
		}
		return true
	})
	if err != nil {
		return nil // preserve recall's keyword-only fallback on embedding failure
	}
	return scores
}
