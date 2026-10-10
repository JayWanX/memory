package graph

import (
	"fmt"
	"github.com/JayWanX/memory/internal/memory/model"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestBoundedBFSMatchesBulkTraversal(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("n-%02d", i)
		insertInsight(t, db, id, id, "test", 3, nil, now)
	}
	rng := rand.New(rand.NewSource(29))
	types := []model.EdgeType{model.EdgeSemantic, model.EdgeTemporal, model.EdgeCausal, model.EdgeEntity}
	for range 150 {
		e := &model.Edge{
			SourceID: fmt.Sprintf("n-%02d", rng.Intn(30)), TargetID: fmt.Sprintf("n-%02d", rng.Intn(30)),
			EdgeType: types[rng.Intn(len(types))], Weight: 0.5, CreatedAt: now,
		}
		if err := db.InsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	// Real recovered stores can contain both deleted and missing endpoints.
	// Leave their incident edges in place and ensure neither traversal returns them.
	if _, err := db.Conn().Exec(`UPDATE insights SET deleted_at = ? WHERE id = 'n-29'`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`DELETE FROM insights WHERE id = 'n-28'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	for _, filter := range append(types, "") {
		for _, depth := range []int{0, 1, 2, 4} {
			for _, start := range []string{"n-00", "n-10", "missing"} {
				full := BFS(db, start, BFSOptions{MaxDepth: depth, EdgeFilter: filter})
				for _, limit := range []int{1, 3, 10, 100} {
					got := BFS(db, start, BFSOptions{MaxDepth: depth, MaxNodes: limit, EdgeFilter: filter})
					want := full[:min(limit, len(full))]
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("start=%s filter=%s depth=%d limit=%d: indexed traversal differs from bulk", start, filter, depth, limit)
					}
				}
			}
		}
	}
}
