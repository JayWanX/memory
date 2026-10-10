package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/model"
)

func TestActiveReadPlansAvoidFullSort(t *testing.T) {
	db := testDB(t)
	const columns = `id, content, category, importance, tags, entities, source,
		access_count, created_at, updated_at, deleted_at`
	cases := []struct {
		name, where, order string
		args               []any
	}{
		{"all", `deleted_at IS NULL`, `created_at DESC`, nil},
		{"ranked", `deleted_at IS NULL`, `importance DESC, created_at DESC LIMIT 10`, nil},
		{"latest", `source = ? AND id != ? AND deleted_at IS NULL`, `created_at DESC, rowid DESC LIMIT 1`, []any{"s", ""}},
		{"recent", `source = ? AND id != ? AND deleted_at IS NULL`, `created_at DESC LIMIT 10`, []any{"s", ""}},
		{"window", `id != ? AND deleted_at IS NULL AND created_at >= ?`, `created_at DESC LIMIT 10`, []any{"", "2026-01-01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := fmt.Sprintf("EXPLAIN QUERY PLAN SELECT %s FROM insights WHERE %s ORDER BY %s", columns, tc.where, tc.order)
			rows, err := db.Conn().Query(query, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				t.Log(detail)
				if strings.Contains(detail, "TEMP B-TREE") {
					t.Errorf("active ordered read still sorts rows: %s", detail)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestActiveReadIndexesReopenAndDeletedRows(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the pre-index schema before reopening to exercise an upgrade.
	for _, index := range []string{"idx_insights_active_created", "idx_insights_active_source_created", "idx_insights_active_ranked"} {
		if _, err := db.Conn().Exec("DROP INDEX " + index); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"importance", "created_at", "source"} {
		name := strings.TrimSuffix(column, "_at")
		if _, err := db.Conn().Exec(fmt.Sprintf("CREATE INDEX idx_insights_%s ON insights(%s)", name, column)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"first", "second", "deleted"} {
		ins := makeInsight(id, "indexed read", 3+i)
		ins.CreatedAt = now
		if err := db.InsertInsight(ins); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SoftDeleteInsight("deleted"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 { // also exercise idempotent migration
		db, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		var redundant int
		if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
			AND name IN ('idx_insights_importance', 'idx_insights_created', 'idx_insights_source')`).Scan(&redundant); err != nil {
			t.Fatal(err)
		}
		if redundant != 0 {
			t.Fatal("upgrade kept redundant write indexes")
		}
		latest, err := db.GetLatestInsightBySource("test", "")
		if err != nil || latest.ID != "second" {
			t.Fatalf("latest = %v, err = %v", latest, err)
		}
		excluded, err := db.GetLatestInsightBySource("test", "second")
		if err != nil || excluded.ID != "first" {
			t.Fatalf("excluded = %v, err = %v", excluded, err)
		}
		ranked, err := db.QueryInsights(QueryFilter{Limit: 1})
		if err != nil || len(ranked) != 1 || ranked[0].ID != "second" {
			t.Fatalf("ranked = %v, err = %v", ranked, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTraversalReadPlansUseEndpointIndexes(t *testing.T) {
	db := testDB(t)
	for _, filter := range []model.EdgeType{"", model.EdgeCausal} {
		query, args := traversalEdgeQuery("a", filter)
		rows, err := db.Conn().Query("EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		joined := strings.Join(plan, "\n")
		t.Log(joined)
		if !strings.Contains(joined, "source_id=?") || !strings.Contains(joined, "target_id=?") {
			t.Errorf("traversal reads unrelated edges with filter %q: %s", filter, joined)
		}
	}
}
