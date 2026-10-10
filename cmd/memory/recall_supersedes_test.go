package memory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/model"
	"github.com/JayWanX/memory/internal/memory/store"
)

func TestRecallPreservesSupersededInEverySmartProjection(t *testing.T) {
	oldDir, oldStore, oldReadOnly := dataDir, storeName, readOnly
	oldBasic, oldBrief, oldVerbose, oldLimit := recBasic, recBrief, recVerbose, recLimit
	oldCategory, oldSource, oldIntent, oldExcerpt := recCategory, recSource, recIntent, recExcerpt
	t.Cleanup(func() {
		dataDir, storeName, readOnly = oldDir, oldStore, oldReadOnly
		recBasic, recBrief, recVerbose, recLimit = oldBasic, oldBrief, oldVerbose, oldLimit
		recCategory, recSource, recIntent, recExcerpt = oldCategory, oldSource, oldIntent, oldExcerpt
	})
	t.Setenv("MEMORY_EMBED_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("MEMORY_EMBED_PROTOCOL", "ollama")
	dataDir, storeName, readOnly = t.TempDir(), "supersedes-projection", true
	recBasic, recLimit = false, 5
	recCategory, recSource, recIntent, recExcerpt = "", "", "GENERAL", 240
	db, err := store.Open(store.StoreDir(dataDir, storeName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	insertTestInsight(t, db, "stale", "release cache ttl defaults to thirty days", "test", "2026-01-01T00:00:00Z")
	insertTestInsight(t, db, "fresh", "release cache ttl was corrected: use seven days", "test", "2026-01-02T00:00:00Z")
	if err := db.InsertEdge(&model.Edge{
		SourceID: "fresh", TargetID: "stale", EdgeType: model.EdgeSupersedes,
		Weight: 1, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"compact", "verbose", "brief"} {
		t.Run(mode, func(t *testing.T) {
			recBrief, recVerbose = mode == "brief", mode == "verbose"
			var runErr error
			out := captureStdout(t, func() { runErr = recallCmd.RunE(recallCmd, []string{"release cache ttl thirty days"}) })
			if runErr != nil {
				t.Fatal(runErr)
			}
			var response struct {
				Results []struct {
					ID         string `json:"id"`
					Insight    model.Insight
					Superseded *bool `json:"superseded"`
				}
			}
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, result := range response.Results {
				id := result.ID
				if mode == "verbose" {
					id = result.Insight.ID
				}
				seen[id] = true
				if id == "stale" && (result.Superseded == nil || !*result.Superseded) {
					t.Errorf("superseded marker missing from stale result: %s", out)
				}
				if id == "fresh" && result.Superseded != nil {
					t.Errorf("current insight must omit superseded: %s", out)
				}
			}
			if !seen["stale"] || !seen["fresh"] {
				t.Fatalf("both current and superseded memories must remain retrievable: %s", out)
			}
		})
	}
}
