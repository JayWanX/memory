package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/test/memoryd/observer"
)

func TestDomainEffectsExposeSuccessorHandlingCreation(t *testing.T) {
	started := time.Date(2026, 8, 6, 4, 0, 0, 0, time.UTC)
	advance := effectTestEvent("event:advance", "handling.advance", "handling:subject-a", 1,
		started.Add(time.Second))
	resolve := effectTestEvent("event:resolve", "handling.resolve.completed", "handling:subject-b", 2,
		started.Add(2*time.Second))
	node := nodeEvidence{Role: "lead", Handlings: []handlingEvidence{
		{Node: "lead", ID: "handling:successor-a", TargetPrincipal: "principal:lead",
			HeadEventID: advance.ID, State: "open", CreatedSequence: advance.OriginSequence},
		{Node: "lead", ID: "handling:successor-b", TargetPrincipal: "principal:lead",
			HeadEventID: resolve.ID, State: "open", CreatedSequence: resolve.OriginSequence},
	}}
	var output bytes.Buffer
	writer, err := observer.NewWriter(&output, observer.Run{ID: "successor-observer",
		Scenario: observer.Scenario{ID: "successor-test",
			Digest: agency.Sum([]byte("successor scenario")).String()},
		StartedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	eventFacts := make(map[string]string, 2)
	for _, event := range []eventEvidence{advance, resolve} {
		factID := hashedFactID("test-event", event.ID)
		if _, err := writer.Append(observer.Fact{ID: factID, CapturedAt: event.AcceptedAt,
			Source: observer.Source{Class: observer.SourceR7Authority, Node: event.Node},
			Kind:   "r7.event.accepted", Truth: observer.TruthAcceptedLocalFact,
			References: observer.References{Event: event.ID, EventDigest: event.Digest},
			Fields: observer.FactFields{SemanticKind: event.SemanticKind,
				Consequence: event.Consequence}}); err != nil {
			t.Fatal(err)
		}
		eventFacts[event.ID] = factID
	}
	if err := appendDomainEffectFacts(writer, []nodeEvidence{node}, eventFacts,
		[]eventEvidence{advance, resolve}); err != nil {
		t.Fatal(err)
	}
	trace := output.String()
	for _, expected := range []string{
		`"kind":"r7.handling.advanced"`,
		`"kind":"r7.handling.resolved"`,
	} {
		if !strings.Contains(trace, expected) {
			t.Fatalf("trace omitted %s:\n%s", expected, trace)
		}
	}
	created := createdHandlingIDs(t, trace)
	if len(created) != 2 || created["handling:successor-a"] != 1 ||
		created["handling:successor-b"] != 1 {
		t.Fatalf("created Handling set = %#v, want each successor exactly once", created)
	}
	for _, subject := range []string{advance.SubjectHandling, resolve.SubjectHandling} {
		if created[subject] != 0 {
			t.Fatalf("subject Handling %q appeared in successor creation set %#v", subject, created)
		}
	}
}

func TestDomainEffectsRejectOrphanedHandlingCreation(t *testing.T) {
	started := time.Date(2026, 8, 6, 4, 0, 0, 0, time.UTC)
	event := effectTestEvent("event:create", "handling.create", "", 1,
		started.Add(time.Second))
	node := nodeEvidence{Role: "lead", Handlings: []handlingEvidence{{
		Node: "lead", ID: "handling:orphan", TargetPrincipal: "principal:lead",
		HeadEventID: event.ID, State: "open", CreatedSequence: 2,
	}}}
	var output bytes.Buffer
	writer, err := observer.NewWriter(&output, observer.Run{ID: "orphan-observer",
		Scenario: observer.Scenario{ID: "orphan-test",
			Digest: agency.Sum([]byte("orphan scenario")).String()},
		StartedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	err = appendDomainEffectFacts(writer, []nodeEvidence{node},
		map[string]string{event.ID: hashedFactID("test-event", event.ID)},
		[]eventEvidence{event})
	if err == nil || !strings.Contains(err.Error(), "has no observable creating Event") {
		t.Fatalf("appendDomainEffectFacts() error = %v, want orphaned creation rejection", err)
	}
}

func createdHandlingIDs(t *testing.T, trace string) map[string]int {
	t.Helper()
	created := make(map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(trace), "\n") {
		var record struct {
			Kind       string `json:"kind"`
			References struct {
				Handling string `json:"handling"`
			} `json:"refs"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode trace record: %v", err)
		}
		if record.Kind == "r7.handling.created" {
			created[record.References.Handling]++
		}
	}
	return created
}

func effectTestEvent(id, consequence, subject string, sequence uint64,
	acceptedAt time.Time,
) eventEvidence {
	return eventEvidence{Node: "lead", ID: id,
		Digest: agency.Sum([]byte(id)).String(), AcceptedAt: acceptedAt,
		OriginSequence: sequence, SourcePrincipal: "principal:lead",
		SemanticKind: "work.test", Consequence: consequence, SubjectHandling: subject}
}
