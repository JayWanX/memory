package observer

import "testing"

func validateMetadataTokens(t *testing.T, sequence int, facts factsWire) {
	t.Helper()
	for _, value := range []string{facts.Code, facts.Episode, facts.GateID,
		facts.Role, facts.SemanticKind} {
		if value != "" && !validToken(value) {
			t.Fatalf("fact %d has invalid metadata token %q", sequence, value)
		}
	}
}
