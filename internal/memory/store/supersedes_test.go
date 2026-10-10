package store

import (
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/model"
)

func TestInsertEdgeRejectsSelfSupersession(t *testing.T) {
	db := testDB(t)
	if err := db.InsertInsight(makeInsight("only", "one current fact", 3)); err != nil {
		t.Fatal(err)
	}
	edge := &model.Edge{
		SourceID: "only", TargetID: "only", EdgeType: model.EdgeSupersedes,
		Weight: 1, CreatedAt: time.Now().UTC(),
	}
	if err := db.InsertEdge(edge); err == nil || !strings.Contains(err.Error(), "distinct insights") {
		t.Fatalf("self supersession must be rejected: %v", err)
	}
	edges, err := db.GetAllEdges()
	if err != nil || len(edges) != 0 {
		t.Fatalf("invalid relation was persisted: edges=%v err=%v", edges, err)
	}
	// Existing edge types keep their previous behavior.
	edge.EdgeType = model.EdgeSemantic
	if err := db.InsertEdge(edge); err != nil {
		t.Fatalf("semantic self edge: %v", err)
	}
}

func TestGetSupersededIDsIgnoresExistingSelfEdges(t *testing.T) {
	db := testDB(t)
	for _, id := range []string{"self", "stale", "fresh"} {
		if err := db.InsertInsight(makeInsight(id, "current fact", 3)); err != nil {
			t.Fatal(err)
		}
	}
	// Older writers and recovered stores can contain self edges. Retain the
	// row for audit, but it cannot assert replacement by another insight.
	if _, err := db.Conn().Exec(`INSERT INTO edges VALUES
		('self','self','supersedes',1,'{}','2026-01-01T00:00:00Z'),
		('fresh','stale','supersedes',1,'{}','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetSupersededIDs([]string{"self", "stale", "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["stale"] {
		t.Fatalf("only a distinct replacement can supersede an insight: %v", got)
	}
}
