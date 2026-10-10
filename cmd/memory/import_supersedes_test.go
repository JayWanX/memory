package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/JayWanX/memory/internal/memory/store"
)

func TestImportDeduplicationCannotSupersedeItself(t *testing.T) {
	t.Setenv("MEMORY_EMBED_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("MEMORY_EMBED_PROTOCOL", "ollama")
	oldDir, oldStore, oldReadOnly := dataDir, storeName, readOnly
	oldNoDiff, oldDryRun := importNoDiff, importDryRun
	t.Cleanup(func() {
		dataDir, storeName, readOnly = oldDir, oldStore, oldReadOnly
		importNoDiff, importDryRun = oldNoDiff, oldDryRun
	})
	dataDir, storeName, readOnly = t.TempDir(), "dedup-supersedes", false
	importNoDiff, importDryRun = false, false
	path := filepath.Join(t.TempDir(), "draft.json")
	if err := os.WriteFile(path, []byte(`{
		"schema_version":"1",
		"insights":[{"content":"release cache ttl is seven days"},{"content":"release cache ttl is seven days"}],
		"edges":[{"source_index":1,"target_index":0,"edge_type":"supersedes","weight":1}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var runErr error
	output := captureStdout(t, func() { runErr = importCmd.RunE(importCmd, []string{path}) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	var summary struct {
		Imported      int `json:"imported"`
		Skipped       int `json:"skipped"`
		EdgesInserted int `json:"edges_inserted"`
	}
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Imported != 1 || summary.Skipped != 1 || summary.EdgesInserted != 0 {
		t.Fatalf("deduplicated import must not write a self supersedes edge: %s", output)
	}
	db, err := store.Open(store.StoreDir(dataDir, storeName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	active, err := db.GetAllActiveInsights()
	if err != nil || len(active) != 1 {
		t.Fatalf("expected one retained insight: %v, %v", active, err)
	}
	superseded, err := db.GetSupersededIDs([]string{active[0].ID})
	if err != nil || len(superseded) != 0 {
		t.Fatalf("the only current fact was superseded: %v, %v", superseded, err)
	}
}
