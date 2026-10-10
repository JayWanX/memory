package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/memory/model"
)

func TestSupersedesUpgradePreservesClosedLegacyDatabase(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		name := "valid"
		if orphan {
			name = "existing orphan"
		}
		t.Run(name, func(t *testing.T) {
			dir := createLegacySupersedesFixture(t, orphan)
			db, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			edges, err := db.GetAllEdges()
			wantEdges := 4
			if orphan {
				wantEdges++
			}
			if err != nil || len(edges) != wantEdges {
				t.Fatalf("legacy edges lost: %v, %v", edges, err)
			}
			for _, edge := range edges {
				if edge.Weight != 0.375 || edge.Metadata["audit"] != "原文" || edge.CreatedAt.Format("2006-01-02T15:04:05Z") != "2026-01-01T01:02:03Z" {
					t.Fatalf("legacy edge payload changed: %+v", edge)
				}
			}
			var root, schemaVersion int
			if err := db.Conn().QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='edges'`).Scan(&root); err != nil {
				t.Fatal(err)
			}
			if err := db.Conn().QueryRow(`PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
				t.Fatal(err)
			}
			if err := db.InsertEdge(&model.Edge{SourceID: "a", TargetID: "b", EdgeType: model.EdgeSupersedes, Weight: 1}); err != nil {
				t.Fatalf("upgraded schema rejects supersedes: %v", err)
			}
			if err := db.InsertEdge(&model.Edge{SourceID: "missing-new", TargetID: "b", EdgeType: model.EdgeSupersedes}); err == nil {
				t.Fatal("migration did not restore foreign key enforcement")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			checkSupersedesReopens(t, dir, root, schemaVersion, wantEdges+1, orphan)
		})
	}
}

func createLegacySupersedesFixture(t *testing.T, orphan bool) string {
	t.Helper()
	dir := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Construct an actual historical file, then close it before Open reads its
	// schema. Do not rewrite sqlite_master or rely on a stale connection cache.
	if _, err := conn.Exec(`CREATE TABLE insights (
		id TEXT PRIMARY KEY, content TEXT NOT NULL, category TEXT DEFAULT 'general',
		importance INTEGER DEFAULT 3, tags TEXT DEFAULT '[]', entities TEXT DEFAULT '[]',
		source TEXT DEFAULT 'user', access_count INTEGER DEFAULT 0,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL, deleted_at TEXT);
		INSERT INTO insights(id,content,created_at,updated_at) VALUES
		('a','保留旧记忆','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		('b','retain another memory','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		CREATE TABLE edges (
			source_id TEXT NOT NULL, target_id TEXT NOT NULL,
			edge_type TEXT NOT NULL CHECK(edge_type IN ('temporal','semantic','causal','entity')),
			weight REAL DEFAULT 1.0, metadata TEXT DEFAULT '{}', created_at TEXT NOT NULL,
			PRIMARY KEY(source_id,target_id,edge_type),
			FOREIGN KEY(source_id) REFERENCES insights(id) ON DELETE CASCADE,
			FOREIGN KEY(target_id) REFERENCES insights(id) ON DELETE CASCADE);`); err != nil {
		t.Fatal(err)
	}
	for _, edgeType := range []string{"temporal", "semantic", "causal", "entity"} {
		if _, err := conn.Exec(`INSERT INTO edges VALUES ('a','b',?,0.375,'{"audit":"原文"}','2026-01-01T01:02:03Z')`, edgeType); err != nil {
			t.Fatal(err)
		}
	}
	if orphan {
		if _, err := conn.Exec(`INSERT INTO edges VALUES ('missing-old','a','semantic',0.375,'{"audit":"原文"}','2026-01-01T01:02:03Z')`); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func checkSupersedesReopens(t *testing.T, dir string, root, schemaVersion, edgeCount int, orphan bool) {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		var gotRoot, gotVersion int
		if err := db.Conn().QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='edges'`).Scan(&gotRoot); err != nil {
			t.Fatal(err)
		}
		if err := db.Conn().QueryRow(`PRAGMA schema_version`).Scan(&gotVersion); err != nil {
			t.Fatal(err)
		}
		if gotRoot != root || gotVersion != schemaVersion {
			t.Fatalf("reopen rebuilt schema: root %d→%d version %d→%d", root, gotRoot, schemaVersion, gotVersion)
		}
		var integrity string
		if err := db.Conn().QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity: %s, %v", integrity, err)
		}
		var fkViolations, gotEdges int
		if err := db.Conn().QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fkViolations); err != nil {
			t.Fatal(err)
		}
		wantFK := 0
		if orphan {
			wantFK = 1
		}
		if fkViolations != wantFK {
			t.Fatalf("foreign key violations changed: %d, want %d", fkViolations, wantFK)
		}
		if err := db.Conn().QueryRow(`SELECT count(*) FROM edges`).Scan(&gotEdges); err != nil || gotEdges != edgeCount {
			t.Fatalf("edges after reopen: %d, %v", gotEdges, err)
		}
		ins, err := db.GetInsightByID("a")
		if err != nil || ins.Content != "保留旧记忆" || countProbeRows(t, db) != 0 {
			t.Fatalf("legacy insight or probe invariant changed: %v, %v", ins, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSupersedesMigrationRejectsUnexpectedProbeFailure(t *testing.T) {
	db := testDB(t)
	if _, err := db.Conn().Exec(`CREATE TRIGGER edge_guard BEFORE INSERT ON edges
		BEGIN SELECT RAISE(ABORT, 'edge guard denied insert'); END`); err != nil {
		t.Fatal(err)
	}
	// The widened schema is already present. A trigger failure is not evidence
	// that supersedes violates its CHECK and must never authorize a rebuild.
	err := db.migrateAddSupersedesEdgeType()
	if err == nil || !strings.Contains(err.Error(), "probe supersedes edge type") {
		t.Errorf("unexpected probe failure must be reported: %v", err)
	}
	var guards, foreignKeys int
	if err := db.Conn().QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='edge_guard'`).Scan(&guards); err != nil || guards != 1 {
		t.Errorf("probe failure removed the existing guard: %d, %v", guards, err)
	}
	if err := db.Conn().QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Errorf("foreign keys were not restored after failure: %d, %v", foreignKeys, err)
	}
}
