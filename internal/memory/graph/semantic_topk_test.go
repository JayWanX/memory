package graph

import (
	"fmt"
	"github.com/JayWanX/memory/internal/memory/embed"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func TestSemanticTopKMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	query := []float64{1, 0, 0, 0}
	cache := EmbedCache{"self": query, "zero": {0, 0, 0, 0}, "wrong-dimension": {1}}
	for i := 0; i < 100; i++ {
		cache[fmt.Sprintf("n-%03d", i)] = []float64{rng.Float64(), rng.Float64(), rng.Float64(), rng.Float64()}
	}
	for _, threshold := range []float64{reviewSemanticThreshold, autoSemanticThreshold} {
		var full []semanticScore
		for id, vec := range cache {
			if id == "self" {
				continue
			}
			if sim := embed.CosineSimilarity(query, vec); sim >= threshold {
				full = append(full, semanticScore{id, sim})
			}
		}
		sort.Slice(full, func(i, j int) bool { return full[i].similarity > full[j].similarity })
		for _, limit := range []int{1, 3, 5, 1000} {
			got := semanticTopK(cache, "self", query, threshold, limit)
			want := full[:min(limit, len(full))]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("threshold=%v limit=%d: got %v want %v", threshold, limit, got, want)
			}
		}
	}
}
