package main

import "testing"

func TestParseOptionsRequiresIndependentEvolutionSnapshotsForSuccess(t *testing.T) {
	base := []string{
		"--report", "/tmp/report.json",
		"--authority", "/tmp/final-authority",
		"--consolidation-authority", "/tmp/consolidation-authority",
		"--boundary-authority", "/tmp/boundary-authority",
		"--output", "/tmp/trace.json",
		"--scenario-root", "/tmp/scenario",
		"--candidate-binaries", "/tmp/binaries.sha256",
	}
	if _, err := parseOptions(base); err != nil {
		t.Fatalf("parse complete success evidence: %v", err)
	}
	for _, missing := range []string{"--consolidation-authority", "--boundary-authority"} {
		arguments := withoutOption(base, missing)
		if _, err := parseOptions(arguments); err == nil {
			t.Fatalf("accepted success evidence without %s", missing)
		}
	}
}

func TestParseOptionsRejectsEvolutionSnapshotsForFailure(t *testing.T) {
	base := []string{
		"--failure-report", "/tmp/failure.json",
		"--authority", "/tmp/final-authority",
		"--output", "/tmp/trace.json",
		"--scenario-root", "/tmp/scenario",
		"--candidate-binaries", "/tmp/binaries.sha256",
	}
	if _, err := parseOptions(base); err != nil {
		t.Fatalf("parse failure evidence: %v", err)
	}
	withSnapshot := append(append([]string{}, base...),
		"--boundary-authority", "/tmp/boundary-authority")
	if _, err := parseOptions(withSnapshot); err == nil {
		t.Fatal("accepted independent evolution snapshots for a failed run")
	}
}

func withoutOption(arguments []string, option string) []string {
	result := make([]string, 0, len(arguments)-2)
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == option {
			index++
			continue
		}
		result = append(result, arguments[index])
	}
	return result
}
