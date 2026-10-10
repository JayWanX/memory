package search

import (
	"fmt"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/embed"
	"github.com/JayWanX/memory/internal/memory/model"
)

func TestRecallBudgetKeepsBestTransition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		intent   Intent
		kind     model.EdgeType
		weight   float64
		incoming bool
		vector   bool
	}{
		{"causal outgoing", IntentWhy, model.EdgeCausal, 0.9, false, false},
		{"causal incoming", IntentWhy, model.EdgeCausal, 0.9, true, false},
		{"temporal", IntentWhen, model.EdgeTemporal, 0.9, false, false},
		{"semantic similarity", IntentGeneral, model.EdgeEntity, 0.1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			now := time.Now().UTC()
			for _, id := range []string{"root", "gold", "noise1", "noise2", "noise3"} {
				insertInsight(t, db, id, id, "test", 3, nil, now)
			}
			for i := 1; i <= 3; i++ {
				if err := db.InsertEdge(&model.Edge{SourceID: "root", TargetID: fmt.Sprintf("noise%d", i), EdgeType: model.EdgeEntity, Weight: 1, CreatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			source, target := "root", "gold"
			if tc.incoming {
				source, target = target, source
			}
			if err := db.InsertEdge(&model.Edge{SourceID: source, TargetID: target, EdgeType: tc.kind, Weight: tc.weight, CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			var query []float64
			if tc.vector {
				query = []float64{1, 0}
				if err := db.UpdateEmbedding("gold", embed.SerializeVector(query)); err != nil {
					t.Fatal(err)
				}
			}
			scores := map[string]float64{"root": 1}
			via := map[string]string{}
			newRecallCache(db, nil, query, GetWeights(tc.intent)).beamSearchFromAnchor("root", 1, TraversalParams{BeamWidth: 2, MaxDepth: 1, MaxVisited: 3}, scores, via)
			if _, ok := scores["gold"]; !ok {
				t.Fatalf("best transition excluded by visit budget: scores=%v", scores)
			}
			if len(scores) > 3 {
				t.Fatalf("visit bound exceeded: %d", len(scores))
			}
		})
	}
}
