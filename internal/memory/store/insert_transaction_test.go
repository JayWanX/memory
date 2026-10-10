package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/model"
)

func TestInsertStatementsFollowTransactionOutcome(t *testing.T) {
	db := testDB(t)
	abort := errors.New("abort batch")
	for _, rollback := range []bool{true, false} {
		err := db.InTransaction(func() error {
			for _, id := range []string{"a", "b"} {
				if err := db.InsertInsight(makeInsight(id, id, 3)); err != nil {
					return err
				}
			}
			for _, weight := range []float64{0.5, 0.9} {
				if err := db.InsertEdge(&model.Edge{
					SourceID: "a", TargetID: "b", EdgeType: model.EdgeSemantic,
					Weight: weight, CreatedAt: time.Now().UTC(),
				}); err != nil {
					return err
				}
			}
			if rollback {
				return abort
			}
			return nil
		})
		if rollback {
			if !errors.Is(err, abort) {
				t.Fatalf("rollback: %v", err)
			}
			if _, err := db.GetInsightByID("a"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("rolled-back insert persisted: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		edges, err := db.GetAllEdges()
		if err != nil {
			t.Fatal(err)
		}
		if rollback && len(edges) != 0 || !rollback && (len(edges) != 1 || edges[0].Weight != 0.9) {
			t.Fatalf("rollback=%v: unexpected edges %+v", rollback, edges)
		}
	}
	// A committed statement must not leak into the next transaction either.
	if err := db.InTransaction(func() error { return db.InsertInsight(makeInsight("c", "next transaction", 3)) }); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertInsight(makeInsight("d", "outside transaction", 3)); err != nil {
		t.Fatal(err)
	}
}

func TestInsertTransactionPanicReleasesConnection(t *testing.T) {
	db := testDB(t)
	func() {
		defer func() {
			if value := recover(); value != "abort" {
				t.Fatalf("unexpected panic: %v", value)
			}
		}()
		_ = db.InTransaction(func() error {
			if err := db.InsertInsight(makeInsight("panic", "rolled back", 3)); err != nil {
				t.Fatal(err)
			}
			panic("abort")
		})
	}()
	if inUse := db.Conn().Stats().InUse; inUse != 0 {
		t.Fatalf("transaction kept %d connections after panic", inUse)
	}
	if _, err := db.GetInsightByID("panic"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("panic insert persisted: %v", err)
	}
	if err := db.InTransaction(func() error { return db.InsertInsight(makeInsight("panic", "retry", 3)) }); err != nil {
		t.Fatal(err)
	}
}
