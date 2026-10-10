package memory

import (
	"encoding/json"
	"testing"

	"github.com/JayWanX/memory/internal/memory/store"
)

// gc is where an operator reads the ceiling back, so it must report the
// configured value and not the built-in default -- otherwise raising the cap
// looks like it did nothing.
func TestGC_ReportsConfiguredMaxInsights(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want float64
	}{
		{"default", "", float64(store.MaxInsights)},
		{"raised", "5000", 5000},
		// Disabled is reported the way it is configured, not as the
		// internal sentinel.
		{"disabled", "0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MEMORY_MAX_INSIGHTS", tt.env)

			oldDataDir, oldStoreName, oldReadOnly := dataDir, storeName, readOnly
			oldThreshold, oldLimit, oldKeep := gcThreshold, gcLimit, gcKeepID
			t.Cleanup(func() {
				dataDir, storeName, readOnly = oldDataDir, oldStoreName, oldReadOnly
				gcThreshold, gcLimit, gcKeepID = oldThreshold, oldLimit, oldKeep
			})
			dataDir = t.TempDir()
			storeName = ""
			readOnly = false
			gcThreshold, gcLimit, gcKeepID = 0.5, 20, ""

			var runErr error
			out := captureStdout(t, func() {
				runErr = gcCmd.RunE(gcCmd, nil)
			})
			if runErr != nil {
				t.Fatalf("gc: %v", runErr)
			}

			var got map[string]interface{}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("decode gc output: %v (output %q)", err, out)
			}
			if got["max_insights"] != tt.want {
				t.Errorf("max_insights = %v, want %v", got["max_insights"], tt.want)
			}
		})
	}
}
