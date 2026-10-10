package memory

import (
	"encoding/json"
	"testing"

	"github.com/JayWanX/memory/internal/memory/search"
	"github.com/JayWanX/memory/internal/memory/store"
)

func configureRememberDiffTest(t *testing.T) {
	t.Helper()
	configureRememberTest(t)
	storeName = store.DefaultStoreName
	remNoDiff = false
	remImportance = 5
	t.Setenv("MEMORY_MAX_INSIGHTS", "1000")
}

type rememberDiffOutput struct {
	ID             string                `json:"id"`
	Action         string                `json:"action"`
	DiffSuggestion search.DiffSuggestion `json:"diff_suggestion"`
	ReplacedID     string                `json:"replaced_id"`
}

func rememberForDiffTest(t *testing.T, content string) rememberDiffOutput {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		runErr = rememberCmd.RunE(rememberCmd, []string{content})
	})
	if runErr != nil {
		t.Fatalf("remember: %v", runErr)
	}
	var result rememberDiffOutput
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode remember output: %v\n%s", err, out)
	}
	return result
}

func assertActiveRememberContents(t *testing.T, want map[string]string) {
	t.Helper()
	db, err := store.Open(store.StoreDir(dataDir, store.DefaultStoreName))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer db.Close()
	active, err := db.GetAllActiveInsights()
	if err != nil {
		t.Fatalf("get active insights: %v", err)
	}
	if len(active) != len(want) {
		t.Errorf("active insight count = %d, want %d", len(active), len(want))
	}
	for _, insight := range active {
		if content, ok := want[insight.ID]; !ok || insight.Content != content {
			t.Errorf("unexpected active insight %s: %q", insight.ID, insight.Content)
		}
	}
}

func TestRememberPreservesDistinctContent(t *testing.T) {
	const alpha = "Project Alpha uses PostgreSQL database for persistent application storage"
	const details = " with indexed customer records, transaction history, audit events, replication, backups, failover, monitoring, access controls, migrations, connection pooling, and disaster recovery"
	tests := []struct {
		name       string
		first      string
		second     string
		suggestion search.DiffSuggestion
	}{
		{
			name:       "different subject same property",
			first:      alpha,
			second:     "Project Beta uses PostgreSQL database for persistent application storage",
			suggestion: search.DiffUpdate,
		},
		{
			name:       "same subject changed value",
			first:      alpha,
			second:     "Project Alpha uses SQLite database for persistent application storage",
			suggestion: search.DiffUpdate,
		},
		{
			name:       "different subject above duplicate threshold",
			first:      alpha + details,
			second:     "Project Beta uses PostgreSQL database for persistent application storage" + details,
			suggestion: search.DiffDuplicate,
		},
		{
			name:       "same tokens different relationship",
			first:      "Project Alpha exports records to Project Beta",
			second:     "Project Beta exports records to Project Alpha",
			suggestion: search.DiffDuplicate,
		},
		{
			name:       "conflict remains advisory",
			first:      alpha,
			second:     "Project Alpha no longer uses PostgreSQL database for persistent application storage",
			suggestion: search.DiffConflict,
		},
		{
			name:       "near duplicate negation remains advisory",
			first:      "Production deployment is allowed",
			second:     "Production deployment is not allowed",
			suggestion: search.DiffConflict,
		},
		{
			name:       "removing negation remains advisory",
			first:      "Production deployment is not allowed",
			second:     "Production deployment is allowed",
			suggestion: search.DiffConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configureRememberDiffTest(t)
			first := rememberForDiffTest(t, tt.first)
			second := rememberForDiffTest(t, tt.second)
			if first.Action != "added" || second.Action != "added" {
				t.Errorf("actions = (%q, %q), want (added, added)", first.Action, second.Action)
			}
			if second.ReplacedID != "" {
				t.Errorf("preserved content reports replaced_id = %q", second.ReplacedID)
			}
			if second.DiffSuggestion != tt.suggestion {
				t.Errorf("diff_suggestion = %q, want %q", second.DiffSuggestion, tt.suggestion)
			}
			assertActiveRememberContents(t, map[string]string{first.ID: tt.first, second.ID: tt.second})
		})
	}
}

func TestRememberSkipsExactDuplicate(t *testing.T) {
	for _, content := range []string{
		"Project Alpha uses PostgreSQL database for persistent application storage",
		"!!!", // Exact identity must not depend on searchable tokens.
	} {
		t.Run(content, func(t *testing.T) {
			configureRememberDiffTest(t)
			first := rememberForDiffTest(t, content)
			repeat := rememberForDiffTest(t, content)
			if repeat.Action != "skipped" || repeat.DiffSuggestion != search.DiffDuplicate || repeat.ReplacedID != first.ID {
				t.Errorf("repeat = %+v, want skipped DUPLICATE of %s", repeat, first.ID)
			}
			assertActiveRememberContents(t, map[string]string{first.ID: content})
		})
	}
}

func TestRememberExactDuplicateOutsideDiffCandidates(t *testing.T) {
	configureRememberDiffTest(t)
	const content = "Project Alpha uses PostgreSQL database for persistent application storage"
	remImportance = 1
	first := rememberForDiffTest(t, content)
	want := map[string]string{first.ID: content}
	remImportance = 5
	remNoDiff = true
	// Higher-importance keyword ties fill the five diff candidate slots.
	for _, detail := range []string{"backups", "replication", "indexes", "migrations", "monitoring", "transactions"} {
		extended := content + " with " + detail
		result := rememberForDiffTest(t, extended)
		want[result.ID] = extended
	}
	remNoDiff = false
	repeat := rememberForDiffTest(t, content)
	if repeat.Action != "skipped" || repeat.DiffSuggestion != search.DiffDuplicate || repeat.ReplacedID != first.ID {
		t.Errorf("repeat = %+v, want skipped DUPLICATE of %s", repeat, first.ID)
	}
	assertActiveRememberContents(t, want)
}

func TestRememberNoDiffStoresExactRepeat(t *testing.T) {
	configureRememberDiffTest(t)
	remNoDiff = true
	const content = "Project Alpha uses PostgreSQL database for persistent application storage"
	first := rememberForDiffTest(t, content)
	repeat := rememberForDiffTest(t, content)
	if repeat.Action != "added" || repeat.DiffSuggestion != search.DiffAdd || repeat.ReplacedID != "" {
		t.Errorf("repeat with --no-diff = %+v, want added ADD without replaced_id", repeat)
	}
	assertActiveRememberContents(t, map[string]string{first.ID: content, repeat.ID: content})
}
