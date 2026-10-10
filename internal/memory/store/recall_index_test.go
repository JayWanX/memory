package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/model"
)

func queryPlan(t *testing.T, db *DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.conn.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	return strings.Join(plan, "\n")
}

func TestGetNeighborEdges_BothDirectionsHeaviestFirst(t *testing.T) {
	db := testDB(t)
	for _, id := range []string{"n-a", "n-b", "n-c", "n-d"} {
		if err := db.InsertInsight(makeInsight(id, id, 3)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, e := range []*model.Edge{
		{SourceID: "n-a", TargetID: "n-b", EdgeType: model.EdgeSemantic, Weight: 0.2},
		{SourceID: "n-c", TargetID: "n-a", EdgeType: model.EdgeCausal, Weight: 0.9},
		{SourceID: "n-a", TargetID: "n-d", EdgeType: model.EdgeEntity, Weight: 0.5},
	} {
		e.Metadata, e.CreatedAt = map[string]string{}, now
		if err := db.InsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	edges, err := db.GetNeighborEdges("n-a")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range edges {
		got = append(got, e.SourceID+">"+e.TargetID)
	}
	want := []string{"n-c>n-a", "n-a>n-d", "n-a>n-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

func TestRecallEdgeQueries_UseCoveringIndexes(t *testing.T) {
	db := testDB(t)
	plan := queryPlan(t, db, `SELECT source_id, target_id, edge_type, weight FROM edges WHERE source_id = ?
		 UNION ALL
		 SELECT source_id, target_id, edge_type, weight FROM edges WHERE target_id = ? AND source_id != ?`, "x", "x", "x")
	if strings.Count(plan, "COVERING INDEX") != 2 {
		t.Fatalf("neighbour query not answered from covering indexes:\n%s", plan)
	}
	plan = queryPlan(t, db, fmt.Sprintf(supersededLookupSQL, "?,?"), "first", "second")
	if !strings.Contains(plan, "SEARCH edges USING COVERING INDEX idx_edges_supersedes (target_id=?)") || strings.Contains(plan, "SCAN edges") {
		t.Fatalf("superseded lookup must seek requested targets in the partial index:\n%s", plan)
	}
}

func TestKnownEntities_MatchesLoadKnownEntities(t *testing.T) {
	db := testDB(t)
	a := makeInsight("ke-a", "first", 3)
	a.Entities = []string{"Athena", "Hestia"}
	b := makeInsight("ke-b", "second", 3)
	b.Entities = []string{"Hestia", ""}
	for _, ins := range []*model.Insight{a, b} {
		if err := db.InsertInsight(ins); err != nil {
			t.Fatal(err)
		}
	}
	want, err := db.LoadKnownEntities()
	if err != nil {
		t.Fatal(err)
	}
	all, err := db.GetAllActiveInsights()
	if err != nil {
		t.Fatal(err)
	}
	if got := KnownEntities(all); !reflect.DeepEqual(got, want) {
		t.Fatalf("KnownEntities = %v, want %v", got, want)
	}
}

// A read-only open skips migrations, so a store last written by an older
// build has no idx_edges_supersedes; the lookup must still answer.
func TestGetSupersededIDs_FallsBackWithoutPartialIndex(t *testing.T) {
	db := testDB(t)
	for _, id := range []string{"fb-new", "fb-old", "fb-self"} {
		if err := db.InsertInsight(makeInsight(id, id, 3)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, e := range []*model.Edge{
		{SourceID: "fb-new", TargetID: "fb-old", EdgeType: model.EdgeSupersedes, Weight: 1},
		{SourceID: "fb-self", TargetID: "fb-self", EdgeType: model.EdgeSupersedes, Weight: 1},
	} {
		if _, err := db.conn.Exec(`INSERT INTO edges (source_id, target_id, edge_type, weight, metadata, created_at) VALUES (?, ?, ?, ?, '{}', ?)`,
			e.SourceID, e.TargetID, string(e.EdgeType), e.Weight, now.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{"fb-old": true}
	ids := []string{"fb-new", "fb-old", "fb-self"}
	got, err := db.GetSupersededIDs(ids)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("with index: %v, %v; want %v", got, err, want)
	}
	if _, err := db.conn.Exec(`DROP INDEX idx_edges_supersedes`); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetSupersededIDs(ids)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("without index: %v, %v; want %v", got, err, want)
	}
}
