package search

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/model"
	"modernc.org/sqlite"
)

func TestIntentAwareRecallReportsSupersededLookupFailure(t *testing.T) {
	db := testDB(t)
	insertInsight(t, db, "stale", "release cache ttl defaults to thirty days", "test", 3, nil, time.Now().UTC())
	insertInsight(t, db, "fresh", "release cache ttl was corrected: use seven days", "test", 3, nil, time.Now().UTC())
	if err := db.InsertEdge(&model.Edge{
		SourceID: "fresh", TargetID: "stale", EdgeType: model.EdgeSupersedes,
		Weight: 1, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// Keep the candidate insights available, but model a partially recovered
	// SQLite schema in which the authority lookup cannot read its table.
	if _, err := db.Conn().Exec(`ALTER TABLE edges RENAME TO preserved_edges`); err != nil {
		t.Fatal(err)
	}
	response, err := IntentAwareRecall(db, "release cache ttl thirty days", nil, nil, 5, nil)
	if err == nil {
		t.Fatalf("recall silently served %d results without verifying supersession", len(response.Results))
	}
	if !strings.Contains(err.Error(), "lookup superseded insights") {
		t.Fatalf("missing lookup context: %v", err)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("underlying SQLite error was not preserved: %v", err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("failed recall returned %d unverified results", len(response.Results))
	}
}
