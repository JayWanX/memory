package observer

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTraceVersionMatchesBrowser(t *testing.T) {
	html := string(readFile(t, "index.html"))
	want := fmt.Sprintf("const TRACE_VERSION = %d;", traceVersion)
	if !strings.Contains(html, want) {
		t.Fatalf("browser trace version does not contain %q", want)
	}
}

func TestBrowserVocabularyMatchesGo(t *testing.T) {
	html := string(readFile(t, "index.html"))
	assertSameStrings(t, "browser fact fields",
		javascriptStringArray(t, html, "const FACT_FIELDS"), jsonFields(reflect.TypeOf(factsWire{})))
	assertSameStrings(t, "browser source classes",
		javascriptStringArray(t, html, "const SOURCE_CLASSES"), sourceClasses)
	assertSameStrings(t, "browser truth classes",
		javascriptStringArray(t, html, "const TRUTH_CLASSES"), truthClasses)
}

func TestObserverKeepsScenarioSemanticsInTraceData(t *testing.T) {
	html := string(readFile(t, "index.html"))
	for _, forbidden := range []string{
		"domain-ops", "payment", "ledger", "gateway", "blackboard", "contract-net",
	} {
		if strings.Contains(strings.ToLower(html), forbidden) {
			t.Fatalf("observer hard-codes scenario vocabulary %q", forbidden)
		}
	}
	for _, required := range []string{
		`laneKey(fact.source.node, fact.agent)`,
		`standaloneRuntimeIDs.has(fact.id)`,
		`collaborationPriority(right) - collaborationPriority(left)`,
		`fact.facts.semantic_kind`,
		`fact.facts.action`,
		`fact.facts.attempt_count`,
		`fact.facts.code`,
		`fact.facts.targets`,
		`fact.facts.outcome`,
		`fact.facts.state`,
		`record.causes.length !== 0`,
		`record.facts.attempt_count < 1`,
		`INTENT_DENIAL_CODES.includes(record.facts.code)`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("observer is missing generic evidence projection %q", required)
		}
	}
}

func TestFinalAttentionSemanticsMatchBrowser(t *testing.T) {
	html := string(readFile(t, "index.html"))
	for _, required := range []string{
		`record.kind === "test.attention.outcome"`,
		`!record.facts.goal_satisfied || record.facts.occupied_claims !== 0`,
		`record.kind === "test.attention.exhausted"`,
		`record.facts.goal_satisfied || record.facts.occupied_claims !== 0`,
		`record.kind === "test.attention.quiescent"`,
		`record.facts.open_unclaimed !== 0`,
		`record.kind === "test.attention.occupied"`,
		`record.facts.goal_digest !== undefined`,
		`must precede and omit external goal evidence`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("browser attention validator is missing %q", required)
		}
	}
}

func TestGateSettlementSemanticsMatchBrowser(t *testing.T) {
	html := string(readFile(t, "index.html"))
	for _, required := range []string{
		`gateIDs.has(gate.id)`,
		`gate.evidence.length === 0`,
		`gate.status === "unknown" && gate.evidence.length !== 0`,
		`assertion.id !== gate.id || assertion.status !== gate.status`,
		`if (!hasPass) fail`,
		`record.gates.some(gate => gate.status === "fail" || gate.status === "unknown")`,
		`record.status === "failed" && !hasFailure`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("browser gate settlement validator is missing %q", required)
		}
	}
}

func TestRuntimeIntentDenialVocabularyMatchesBrowser(t *testing.T) {
	html := string(readFile(t, "index.html"))
	assertSameStrings(t, "browser Intent denial codes",
		javascriptStringArray(t, html, "const INTENT_DENIAL_CODES"), intentDenialCodes)
}

func jsonFields(value reflect.Type) []string {
	fields := make([]string, 0, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		name, _, _ := strings.Cut(value.Field(index).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	return fields
}
