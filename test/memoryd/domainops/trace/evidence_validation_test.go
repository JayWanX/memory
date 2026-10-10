package main

import (
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestValidateGlobalCausationCountsOnlyFederationHops(t *testing.T) {
	root := eventEvidence{ID: "event:root", Digest: agency.Sum([]byte("root")).String()}
	imported := eventEvidence{ID: "event:imported", Digest: agency.Sum([]byte("imported")).String(),
		CausalDepth: 1, Causation: []eventRefWire{{ID: root.ID, Digest: root.Digest}}}
	response := eventEvidence{ID: "event:response", Digest: agency.Sum([]byte("response")).String(),
		CausalDepth: 1, Causation: []eventRefWire{{ID: imported.ID, Digest: imported.Digest}}}
	global := map[string]eventEvidence{root.ID: root, imported.ID: imported, response.ID: response}
	if err := validateGlobalCausation(global); err != nil {
		t.Fatalf("equal-depth local response rejected: %v", err)
	}

	response.CausalDepth = 0
	global[response.ID] = response
	if err := validateGlobalCausation(global); err == nil {
		t.Fatal("causal-depth regression accepted")
	}
}

func TestValidateEvolutionEvidenceBindsLaterEventToExactBoundaryHead(t *testing.T) {
	proof := validEvolutionEvidence()
	if err := validateEvolutionEvidence(proof); err != nil {
		t.Fatalf("validateEvolutionEvidence() error = %v", err)
	}

	proof.Report.Protocol.Evolution.Boundary.Nodes[2].ConsolidationAfterSequence = 2
	if err := validateEvolutionEvidence(proof); err == nil {
		t.Fatal("validateEvolutionEvidence() accepted a Reference published before consolidation")
	}
	proof.Report.Protocol.Evolution.Boundary.Nodes[2].ConsolidationAfterSequence = 1

	proof.Report.Protocol.Evolution.Effects[2].Matches[0].ReferenceDigest =
		agency.Sum([]byte("different head")).String()
	if err := validateEvolutionEvidence(proof); err == nil {
		t.Fatal("validateEvolutionEvidence() accepted a non-exact Reference edge")
	}
}

func TestValidateEvolutionEvidenceRejectsDeletedBoundaryObservation(t *testing.T) {
	proof := validEvolutionEvidence()
	clearEvolutionObservation(&proof.Report)
	if err := validateEvolutionEvidence(proof); err == nil {
		t.Fatal("validateEvolutionEvidence() accepted a real Reference boundary downgraded to N/A")
	}
}

func TestValidateEvolutionEvidenceRejectsRewrittenBoundaryCoordinates(t *testing.T) {
	proof := validEvolutionEvidence()
	clearEvolutionObservation(&proof.Report)
	leadBoundary := &proof.Report.Protocol.Evolution.Boundary.Nodes[2]
	leadBoundary.ConsolidationAfterSequence = 0
	leadBoundary.MaxOriginSequence = 0
	proof.Report.Protocol.Evolution.Effects[2].BoundarySequence = 0
	if err := validateEvolutionEvidence(proof); err == nil {
		t.Fatal("validateEvolutionEvidence() accepted report-owned replacement boundary coordinates")
	}
}

func TestValidateEvolutionEvidenceReplaysBoundaryBeforeLaterRetraction(t *testing.T) {
	proof := validEvolutionEvidence()
	retraction := eventEvidence{Node: "lead", ID: "event:retraction",
		Digest: agency.Sum([]byte("later retraction")).String(), OriginSequence: 4,
		ReferenceKey: "reference:playbook", ReferenceHead: "event:reference"}
	proof.Nodes[2].Events = append(proof.Nodes[2].Events, retraction)
	proof.Nodes[2].References = append(proof.Nodes[2].References, referenceEvidence{
		Node: "lead", EventID: retraction.ID, PreviousEventID: "event:reference",
		State: "retracted",
	})
	if err := validateEvolutionEvidence(proof); err != nil {
		t.Fatalf("validateEvolutionEvidence() used final instead of boundary Reference state: %v", err)
	}
}

func validEvolutionEvidence() evidence {
	report := validReport()
	before := eventEvidence{Node: "lead", ID: "event:before",
		Digest: agency.Sum([]byte("before consolidation")).String(), OriginSequence: 1}
	referenceDigest := agency.Sum([]byte("retained Reference Event")).String()
	reference := eventEvidence{Node: "lead", ID: "event:reference", Digest: referenceDigest,
		OriginSequence: 2, ReferenceKey: "reference:playbook"}
	later := eventEvidence{Node: "lead", ID: "event:evolution",
		Digest: agency.Sum([]byte("later Event")).String(), OriginSequence: 3,
		Causation: []eventRefWire{{ID: reference.ID, Digest: reference.Digest}}}
	proof := evidence{Report: report}
	for _, role := range domainRoles {
		node, consolidation, boundary := nodeEvidence{Role: role},
			nodeEvidence{Role: role}, nodeEvidence{Role: role}
		if role == "lead" {
			node.Events = []eventEvidence{before, reference, later}
			node.References = []referenceEvidence{{Node: role, EventID: reference.ID,
				State: "active", ArtifactDigest: agency.Sum([]byte("guide")).String()}}
			consolidation.Events = []eventEvidence{before}
			boundary.Events = []eventEvidence{before, reference}
			boundary.References = append([]referenceEvidence(nil), node.References...)
		}
		proof.Nodes = append(proof.Nodes, node)
		proof.ConsolidationNodes = append(proof.ConsolidationNodes, consolidation)
		proof.BoundaryNodes = append(proof.BoundaryNodes, boundary)
	}
	return proof
}

func TestValidateEvolutionEvidenceAcceptsNoEvolutionObservation(t *testing.T) {
	report := validReport()
	clearEvolutionObservation(&report)
	proof := evidence{Report: report}
	for _, role := range domainRoles {
		node, consolidation, boundary := nodeEvidence{Role: role},
			nodeEvidence{Role: role}, nodeEvidence{Role: role}
		if role == "lead" {
			node.Events = []eventEvidence{
				{Node: role, ID: "event:before", Digest: agency.Sum([]byte("before")).String(),
					OriginSequence: 1},
				{Node: role, ID: "event:boundary", Digest: agency.Sum([]byte("boundary")).String(),
					OriginSequence: 2},
			}
			consolidation.Events = append(consolidation.Events, node.Events[0])
			boundary.Events = append(boundary.Events, node.Events...)
		}
		proof.Nodes = append(proof.Nodes, node)
		proof.ConsolidationNodes = append(proof.ConsolidationNodes, consolidation)
		proof.BoundaryNodes = append(proof.BoundaryNodes, boundary)
	}
	if err := validateEvolutionEvidence(proof); err != nil {
		t.Fatalf("validateEvolutionEvidence() rejected an absent evolution claim: %v", err)
	}

	proof.Report.Protocol.Evolution.Demonstrated = true
	if err := validateEvolutionEvidence(proof); err == nil {
		t.Fatal("validateEvolutionEvidence() accepted an unproved evolution claim")
	}
}

func TestValidateTurnEventBindingsRequireExactStoppedOperation(t *testing.T) {
	digest := agency.Sum([]byte("turn Event")).String()
	turns := []turnSummary{{Role: "lead", Turn: "turn-a", AcceptedEvents: []acceptedEventSummary{{
		ID: "event:turn-a", Digest: digest,
	}}}}
	nodes := []nodeEvidence{{Role: "lead",
		Events: []eventEvidence{{Node: "lead", ID: "event:turn-a", Digest: digest}},
		Operations: []operationEvidence{{Node: "lead", Outcome: "accepted",
			EventID: "event:turn-a", EventDigest: digest}},
	}}
	if err := validateTurnEventBindings(turns, nodes); err != nil {
		t.Fatalf("exact Runtime turn/Event binding: %v", err)
	}

	turns[0].AcceptedEvents[0].Digest = agency.Sum([]byte("wrong")).String()
	if err := validateTurnEventBindings(turns, nodes); err == nil {
		t.Fatal("Runtime turn accepted an Event digest absent from stopped authority")
	}
	turns[0].AcceptedEvents[0].Digest = digest
	turns = append(turns, turns[0])
	if err := validateTurnEventBindings(turns, nodes); err == nil {
		t.Fatal("accepted Event was attributed to two Runtime turns")
	}
}
