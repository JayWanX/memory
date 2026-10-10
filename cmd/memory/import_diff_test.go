package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/importdraft"
	"github.com/JayWanX/memory/internal/memory/model"
	"github.com/JayWanX/memory/internal/memory/store"
)

func configureImportDiffTest(t *testing.T) {
	t.Helper()
	configureRememberDiffTest(t)
	oldNoDiff, oldDryRun := importNoDiff, importDryRun
	t.Cleanup(func() { importNoDiff, importDryRun = oldNoDiff, oldDryRun })
	importNoDiff, importDryRun = false, false
}

type importDiffOutput struct {
	Imported int `json:"imported"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
	Errors   int `json:"errors"`
	Results  []struct {
		Index   int    `json:"index"`
		ID      string `json:"id"`
		Content string `json:"content"`
		Action  string `json:"action"`
	} `json:"results"`
}

func importForDiffTest(t *testing.T, contents []string, edges []importdraft.DraftEdge) importDiffOutput {
	t.Helper()
	draft := importdraft.MemoryDraft{SchemaVersion: "1", Edges: edges}
	for _, content := range contents {
		draft.Insights = append(draft.Insights, importdraft.DraftInsight{
			Content: content, Category: "fact", Importance: 5,
		})
	}
	data, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "draft.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() { runErr = importCmd.RunE(importCmd, []string{path}) })
	if runErr != nil {
		t.Fatalf("import: %v", runErr)
	}
	var result importDiffOutput
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode import output: %v\n%s", err, out)
	}
	if result.Errors != 0 || len(result.Results) != len(contents) {
		t.Fatalf("incomplete import: %+v", result)
	}
	return result
}

func TestImportPreservesDistinctContent(t *testing.T) {
	const alpha = "Project Alpha uses PostgreSQL database for persistent application storage"
	const details = " with indexed customer records, transaction history, audit events, replication, backups, failover, monitoring, access controls, migrations, connection pooling, and disaster recovery"
	tests := []struct{ name, first, second string }{
		{"different subject", alpha, "Project Beta uses PostgreSQL database for persistent application storage"},
		{"changed value", alpha, "Project Alpha uses SQLite database for persistent application storage"},
		{"near duplicate", alpha + details, "Project Beta uses PostgreSQL database for persistent application storage" + details},
		{"conflict", alpha, "Project Alpha no longer uses PostgreSQL database for persistent application storage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configureImportDiffTest(t)
			result := importForDiffTest(t, []string{tt.first, tt.second}, nil)
			if result.Imported != 2 || result.Updated != 0 || result.Skipped != 0 {
				t.Errorf("import = %+v, want two added insights", result)
			}
			assertActiveRememberContents(t, map[string]string{
				result.Results[0].ID: tt.first, result.Results[1].ID: tt.second,
			})
		})
	}
}

func TestImportPreservesExistingFact(t *testing.T) {
	configureImportDiffTest(t)
	const alpha = "Project Alpha uses PostgreSQL database for persistent application storage"
	const beta = "Project Beta uses PostgreSQL database for persistent application storage"
	first := rememberForDiffTest(t, alpha)
	result := importForDiffTest(t, []string{beta}, nil)
	if result.Imported != 1 || result.Updated != 0 || result.Skipped != 0 {
		t.Errorf("import = %+v, want one added insight", result)
	}
	assertActiveRememberContents(t, map[string]string{first.ID: alpha, result.Results[0].ID: beta})
}

func TestImportExactDuplicateRetainsIndexAndEdgeMapping(t *testing.T) {
	configureImportDiffTest(t)
	const content = "Project Alpha uses PostgreSQL database for persistent application storage"
	remImportance = 1
	first := rememberForDiffTest(t, content)
	want := map[string]string{first.ID: content}
	remImportance, remNoDiff = 5, true
	for _, detail := range []string{"backups", "replication", "indexes", "migrations", "monitoring", "transactions"} {
		extended := content + " with " + detail
		result := rememberForDiffTest(t, extended)
		want[result.ID] = extended
	}
	const linked = "Zebra migration survey notes"
	result := importForDiffTest(t, []string{content, linked, linked}, []importdraft.DraftEdge{
		{SourceIndex: 0, TargetIndex: 2, EdgeType: "semantic", Weight: 0.75, Reason: "exact duplicate index mapping"},
	})
	if result.Imported != 1 || result.Updated != 0 || result.Skipped != 2 {
		t.Errorf("import = %+v, want one added insight and two exact duplicates", result)
	}
	if result.Results[0].ID != first.ID || result.Results[0].Action != "skipped" {
		t.Errorf("existing duplicate = %+v, want skipped %s", result.Results[0], first.ID)
	}
	linkedID := result.Results[1].ID
	if result.Results[2].ID != linkedID || result.Results[2].Action != "skipped" {
		t.Errorf("batch duplicate = %+v, want skipped %s", result.Results[2], linkedID)
	}
	want[linkedID] = linked
	assertActiveRememberContents(t, want)
	db, err := store.Open(store.StoreDir(dataDir, store.DefaultStoreName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	edges, err := db.GetEdgesBySourceAndType(first.ID, model.EdgeSemantic)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range edges {
		if edge.TargetID == linkedID && edge.Metadata["reason"] == "exact duplicate index mapping" {
			return
		}
	}
	t.Fatalf("explicit edge from existing duplicate %s to batch duplicate %s is missing", first.ID, linkedID)
}

func TestImportNoDiffStoresExactRepeats(t *testing.T) {
	configureImportDiffTest(t)
	importNoDiff = true
	const content = "Project Alpha uses PostgreSQL database for persistent application storage"
	result := importForDiffTest(t, []string{content, content}, nil)
	if result.Imported != 2 || result.Updated != 0 || result.Skipped != 0 {
		t.Errorf("import with --no-diff = %+v, want two added insights", result)
	}
	assertActiveRememberContents(t, map[string]string{
		result.Results[0].ID: content, result.Results[1].ID: content,
	})
}

// Mainline now preserves every non-identical fact, including UPDATE suggestions.
// Keep the conflict review's persistence and edge checks under that write policy.
func TestImportDiffWriteOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		existing    string
		newText     string
		noDiff      bool
		wantAction  string
		wantActive  int
		wantDeleted bool
	}{
		{"exact duplicate", "Production deployment is allowed", "Production deployment is allowed", false, "skipped", 1, false},
		{"negated correction", "Production deployment is allowed", "Production deployment is not allowed", false, "added", 2, false},
		{"existing conflict signal", "Production deployment supports Python services", "Production deployment no longer supports Python services", false, "added", 2, false},
		{"ordinary update is preserved", "Production deployment uses PostgreSQL for persistent storage", "Production deployment uses SQLite for persistent storage", false, "added", 2, false},
		{"no diff inserts duplicate", "Production deployment is allowed", "Production deployment is allowed", true, "added", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupImportDiffTest(t, tt.noDiff)
			insertTestInsight(t, db, "original", tt.existing, "original-source", "2026-01-01T00:00:00Z")
			summary := runImportDiffDraft(t, importdraft.MemoryDraft{
				SchemaVersion: "1",
				Insights:      []importdraft.DraftInsight{{Content: tt.newText, Importance: 5}},
			})
			if summary.Errors != 0 || len(summary.Results) != 1 || summary.Results[0].Action != tt.wantAction {
				t.Fatalf("import summary = %+v, want one %s result", summary, tt.wantAction)
			}
			counts := map[string]int{"added": summary.Imported, "updated": summary.Updated, "skipped": summary.Skipped}
			if counts[tt.wantAction] != 1 || summary.Imported+summary.Updated+summary.Skipped != 1 {
				t.Fatalf("import counts = %v, want one %s", counts, tt.wantAction)
			}
			active, err := db.GetAllActiveInsights()
			if err != nil || len(active) != tt.wantActive {
				t.Fatalf("active insights = %d, error = %v; want %d", len(active), err, tt.wantActive)
			}
			original, err := db.GetInsightByIDIncludeDeleted("original")
			if err != nil {
				t.Fatal(err)
			}
			if deleted := original.DeletedAt != nil; deleted != tt.wantDeleted {
				t.Fatalf("original deleted = %v, want %v", deleted, tt.wantDeleted)
			}
			resultID := summary.Results[0].ID
			if (resultID == "original") != (tt.wantAction == "skipped") {
				t.Fatalf("result ID = %q for action %s", resultID, tt.wantAction)
			}
			result, err := db.GetInsightByID(resultID)
			if err != nil || result.Content != tt.newText {
				t.Fatalf("imported insight = %+v, error = %v", result, err)
			}
		})
	}
}

func TestImportConflictAndDuplicateResolveExplicitEdges(t *testing.T) {
	db := setupImportDiffTest(t, false)
	insertTestInsight(t, db, "original", "Production deployment is allowed", "original-source", "2026-01-01T00:00:00Z")
	insertTestInsight(t, db, "context", "Security review approved the release plan", "review-source", "2026-01-01T00:00:00Z")
	if err := db.InsertEdge(&model.Edge{
		SourceID: "original", TargetID: "context", EdgeType: model.EdgeCausal,
		Weight: 0.7, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	summary := runImportDiffDraft(t, importdraft.MemoryDraft{
		SchemaVersion: "1",
		Insights: []importdraft.DraftInsight{
			{Content: "Production deployment is allowed", Importance: 5},
			{Content: "Production deployment is not allowed", Importance: 5},
		},
		Edges: []importdraft.DraftEdge{{SourceIndex: 1, TargetIndex: 0, EdgeType: "causal", Weight: 0.8}},
	})
	if summary.Errors != 0 || summary.Imported != 1 || summary.Updated != 0 || summary.Skipped != 1 || summary.EdgesInserted != 1 || len(summary.Results) != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.Results[0].ID != "original" || summary.Results[0].Action != "skipped" || summary.Results[1].Action != "added" {
		t.Fatalf("draft indices resolved incorrectly: %+v", summary.Results)
	}
	active, err := db.GetAllActiveInsights()
	if err != nil || len(active) != 3 {
		t.Fatalf("active insights = %d, error = %v; want 3", len(active), err)
	}
	for _, pair := range [][2]string{{summary.Results[1].ID, "original"}, {"original", "context"}} {
		edges, err := db.GetEdgesBySourceAndType(pair[0], model.EdgeCausal)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, edge := range edges {
			if edge.TargetID == pair[1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing causal edge %s -> %s", pair[0], pair[1])
		}
	}
}

type importDiffSummary struct {
	Imported      int            `json:"imported"`
	Updated       int            `json:"updated"`
	Skipped       int            `json:"skipped"`
	Errors        int            `json:"errors"`
	EdgesInserted int            `json:"edges_inserted"`
	Results       []importResult `json:"results"`
}

func setupImportDiffTest(t *testing.T, noDiff bool) *store.DB {
	t.Helper()
	t.Setenv("MEMORY_EMBED_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("MEMORY_EMBED_PROTOCOL", "ollama")
	t.Setenv("MEMORY_MAX_INSIGHTS", "1000")
	oldDataDir, oldStoreName, oldReadOnly := dataDir, storeName, readOnly
	oldImportNoDiff, oldImportDryRun := importNoDiff, importDryRun
	t.Cleanup(func() {
		dataDir, storeName, readOnly = oldDataDir, oldStoreName, oldReadOnly
		importNoDiff, importDryRun = oldImportNoDiff, oldImportDryRun
	})
	dataDir, storeName, readOnly = t.TempDir(), store.DefaultStoreName, false
	importNoDiff, importDryRun = noDiff, false
	db, err := store.Open(store.StoreDir(dataDir, storeName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func runImportDiffDraft(t *testing.T, draft importdraft.MemoryDraft) importDiffSummary {
	t.Helper()
	data, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(t.TempDir(), "draft.json")
	if err := os.WriteFile(draftPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := importCmd.RunE(importCmd, []string{draftPath}); err != nil {
			t.Fatal(err)
		}
	})
	var summary importDiffSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatalf("decode summary: %v\n%s", err, output)
	}
	return summary
}
