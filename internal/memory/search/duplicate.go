package search

import "github.com/JayWanX/memory/internal/memory/model"

// FindExactDuplicateID returns the ID of an insight with byte-identical content,
// or an empty string when none exists. Callers supply all active insights: fuzzy
// diff candidates and similarity scores cannot establish content identity.
func FindExactDuplicateID(insights []*model.Insight, content string) string {
	for _, insight := range insights {
		if insight.Content == content {
			return insight.ID
		}
	}
	return ""
}
