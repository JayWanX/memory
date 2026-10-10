package memory

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/JayWanX/memory/internal/memory/store"
	"github.com/spf13/cobra"
)

var (
	gcThreshold float64
	gcLimit     int
	gcKeepID    string
	gcCompact   bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Review memory retention and suggest cleanup",
	Long: `Garbage collection for memory insights. Two modes:

Suggest mode (default):
  memory gc [--threshold 0.5] [--limit 20]
  Lists non-immune insights with effective_importance below threshold.
  Immune insights (importance >= 4 or access_count >= 3) are never listed.

Keep mode:
	memory gc --keep <id>
  Boosts an insight's retention (access_count +3, refreshes timestamp).

Compact mode:
  memory gc --compact
  Rewrites the database with VACUUM. Recall bumps access counters on every
  hit, which scatters table pages across the file and makes cold recalls
  slow; compacting restores sequential layout. Run it weekly or so.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requirePositiveLimit("--limit", gcLimit); err != nil {
			return err
		}
		if err := requireNonNegativeFloat("--threshold", gcThreshold); err != nil {
			return err
		}

		db, err := openDB()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()

		if gcCompact {
			if err := requireWritableDB(db, "gc --compact"); err != nil {
				return err
			}
			before, after, err := db.Compact()
			if err != nil {
				return err
			}
			db.LogOp("gc_compact", "", fmt.Sprintf("bytes %d -> %d", before, after))
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]interface{}{
				"status":       "compacted",
				"bytes_before": before,
				"bytes_after":  after,
			})
		}

		// Keep mode: boost retention for a specific insight
		if gcKeepID != "" {
			if err := requireWritableDB(db, "gc --keep"); err != nil {
				return err
			}
			ins, err := db.GetInsightByID(gcKeepID)
			if err != nil || ins == nil {
				return fmt.Errorf("insight %s not found", gcKeepID)
			}
			if err := db.BoostRetention(gcKeepID); err != nil {
				return fmt.Errorf("boost retention: %w", err)
			}
			ei, _ := db.RefreshEffectiveImportance(gcKeepID)
			db.LogOp("gc_keep", gcKeepID, ins.Content)

			output := map[string]interface{}{
				"status":               "retained",
				"id":                   gcKeepID,
				"content":              ins.Content,
				"new_access":           ins.AccessCount + 3,
				"effective_importance": ei,
				"immune":               store.IsImmune(ins.Importance, ins.AccessCount+3),
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(output)
		}

		// Suggest mode: find low effective_importance non-immune candidates
		candidates, total, err := db.GetRetentionCandidates(gcThreshold, gcLimit)
		if err != nil {
			return fmt.Errorf("get retention candidates: %w", err)
		}

		db.LogOp("gc", "", fmt.Sprintf("threshold=%.2f found=%d total=%d", gcThreshold, len(candidates), total))

		// Report the ceiling the way it is configured rather than the sentinel
		// AutoPrune consumes: 0 is how MEMORY_MAX_INSIGHTS spells "no cap".
		maxInsights := store.MaxInsightsLimit()
		if maxInsights == store.MaxInsightsUnlimited {
			maxInsights = 0
		}

		output := map[string]interface{}{
			"total_insights":   total,
			"threshold":        gcThreshold,
			"candidates_found": len(candidates),
			"candidates":       candidates,
			"max_insights":     maxInsights,
			"actions": map[string]string{
				"purge": "memory forget <id>",
				"keep":  "memory gc --keep <id>",
			},
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	},
}

func init() {
	gcCmd.Flags().Float64Var(&gcThreshold, "threshold", 0.5, "effective_importance threshold (insights below this are candidates)")
	gcCmd.Flags().IntVar(&gcLimit, "limit", 20, "max candidates to return")
	gcCmd.Flags().StringVar(&gcKeepID, "keep", "", "boost retention for this insight ID")
	gcCmd.Flags().BoolVar(&gcCompact, "compact", false, "rewrite the database with VACUUM to speed up cold recalls")
	rootCmd.AddCommand(gcCmd)
}
