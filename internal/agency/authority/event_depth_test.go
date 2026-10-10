package authority

import (
	"encoding/json"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestLocalEventsPersistCanonicalPeerHopDepthWithoutIncrement(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:event-depth")
	root := rootRequest(t, fixture.current(t), "operation:depth-root", "start locally")
	rootResult, err := fixture.store.Admit(fixture.ctx, fixture.proof, root)
	if err != nil || rootResult.Outcome() != agency.ReceiptOutcomeAccepted {
		t.Fatalf("root admission = %#v, %v", rootResult, err)
	}
	assertPersistedEventDepth(t, fixture, rootResult, 0)

	advance := subjectRequest(t, fixture.current(t), "operation:depth-advance",
		agency.ConsequenceAdvanceHandling, "continue locally", nil)
	advanceResult, err := fixture.store.Admit(fixture.ctx, fixture.proof, advance)
	if err != nil || advanceResult.Outcome() != agency.ReceiptOutcomeAccepted {
		t.Fatalf("advance admission = %#v, %v", advanceResult, err)
	}
	assertPersistedEventDepth(t, fixture, advanceResult, 0)
}

func TestLocalCausalDepthInheritsEveryAcceptedInputWithoutAddingHop(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:event-depth-inputs")
	attachment := fixture.current(t).authority.Attachment()
	predecessor := insertSyntheticDepthEvent(t, fixture, attachment, 5)
	self, err := agency.ResolveLocalTarget(agency.SelfTarget(), fixture.principal)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("subject", func(t *testing.T) {
		handle := mustHandle(t, "subject:depth")
		if _, err := fixture.store.db.Exec(`INSERT INTO handlings(
			handling_id, target_principal_id, head_event_id, state, created_sequence)
			VALUES('handling:depth', ?, ?, 'open', 100)`, fixture.principal.String(),
			predecessor.ID().String()); err != nil {
			t.Fatal(err)
		}
		subject, err := agency.NewSubjectBinding(handle, mustHandling(t, "handling:depth"),
			predecessor, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		view := mustMachineView(t, agency.MachineViewSpec{Attachment: attachment,
			Consequences: []agency.Consequence{agency.ConsequenceAdvanceHandling},
			Subjects:     []agency.SubjectBinding{subject}})
		intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "depth.subject"),
			Consequence: agency.ConsequenceAdvanceHandling, SubjectHandling: handle})
		request, err := bindIntent(view, intent,
			mustOperation(t, "operation:depth-subject"), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertDerivedDepth(t, fixture, request, 5)
	})

	t.Run("reference", func(t *testing.T) {
		handle := mustHandle(t, "reference:depth")
		expected, err := agency.ExpectReferenceHead(handle, mustReferenceKey(t, "depth-reference"),
			predecessor)
		if err != nil {
			t.Fatal(err)
		}
		view := mustMachineView(t, agency.MachineViewSpec{Attachment: attachment,
			Consequences: []agency.Consequence{agency.ConsequenceRetractReference},
			References:   []agency.ReferenceExpectation{expected}})
		intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "depth.reference"),
			Consequence: agency.ConsequenceRetractReference, ReferenceHead: handle})
		request, err := bindIntent(view, intent,
			mustOperation(t, "operation:depth-reference"), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertDerivedDepth(t, fixture, request, 5)
	})

	for _, test := range []struct {
		name        string
		correlation bool
	}{
		{name: "causation"},
		{name: "correlation", correlation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handle := mustHandle(t, "provenance:"+test.name)
			offer, err := agency.NewProvenanceOffer(handle, predecessor)
			if err != nil {
				t.Fatal(err)
			}
			view := mustMachineView(t, agency.MachineViewSpec{Attachment: attachment,
				Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
				Targets:      []agency.ResolvedTarget{self}, Provenance: []agency.ProvenanceOffer{offer}})
			spec := agency.IntentSpec{Kind: mustLabel(t, "depth."+test.name),
				Consequence: agency.ConsequenceCreateHandlings,
				Successors:  []agency.TargetRef{agency.SelfTarget()}}
			if test.correlation {
				spec.CorrelationHandle = handle
			} else {
				spec.CausationHandles = []agency.OpaqueHandle{handle}
			}
			intent := mustIntent(t, spec)
			request, err := bindIntent(view, intent,
				mustOperation(t, "operation:depth-"+test.name), nil)
			if err != nil {
				t.Fatal(err)
			}
			assertDerivedDepth(t, fixture, request, 5)
		})
	}
}

func insertSyntheticDepthEvent(t *testing.T, fixture *authorityFixture,
	attachment agency.Attachment, depth uint16,
) agency.EventRef {
	t.Helper()
	self, err := agency.ResolveLocalTarget(agency.SelfTarget(), attachment.Principal())
	if err != nil {
		t.Fatal(err)
	}
	view := mustMachineView(t, agency.MachineViewSpec{Attachment: attachment,
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{self}})
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "depth.synthetic"),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget()}})
	request, err := bindIntent(view, intent,
		mustOperation(t, "operation:depth-synthetic"), nil)
	if err != nil {
		t.Fatal(err)
	}
	event, err := agency.NewEvent(request, agency.EventStamp{ID: mustEventID(t, "event:depth-synthetic"),
		AcceptedAt: *fixture.now, OriginSequence: 100, CausalDepth: depth})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := insertEventTx(fixture.ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return event.Ref()
}

func assertDerivedDepth(t *testing.T, fixture *authorityFixture, request agency.BoundIntent,
	want uint16,
) {
	t.Helper()
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := deriveLocalEventCausalDepthTx(fixture.ctx, tx, request)
	if err != nil || got != want {
		t.Fatalf("deriveLocalEventCausalDepthTx() = %d, %v; want %d", got, err, want)
	}
}

func assertPersistedEventDepth(t *testing.T, fixture *authorityFixture,
	result AdmissionResult, want uint16,
) {
	t.Helper()
	var receipt struct {
		Event struct {
			ID string `json:"id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(result.ReceiptJSON(), &receipt); err != nil {
		t.Fatal(err)
	}
	var depth uint16
	var canonical []byte
	if err := fixture.store.db.QueryRow(`SELECT causal_depth, canonical_json FROM events WHERE event_id = ?`,
		receipt.Event.ID).Scan(&depth, &canonical); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		SchemaVersion int `json:"schema_version"`
		Machine       struct {
			CausalDepth uint16 `json:"causal_depth"`
		} `json:"machine"`
	}
	if err := json.Unmarshal(canonical, &wire); err != nil {
		t.Fatal(err)
	}
	if depth != want || wire.SchemaVersion != 3 || wire.Machine.CausalDepth != want {
		t.Fatalf("persisted Event depth = column %d wire(v%d) %d; want %d",
			depth, wire.SchemaVersion, wire.Machine.CausalDepth, want)
	}
}

func mustMachineView(t *testing.T, spec agency.MachineViewSpec) agency.ViewAuthority {
	t.Helper()
	view, err := agency.NewViewAuthority(spec)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func mustHandling(t *testing.T, value string) agency.HandlingID {
	t.Helper()
	handling, err := agency.NewHandlingID(value)
	if err != nil {
		t.Fatal(err)
	}
	return handling
}

func mustEventID(t *testing.T, value string) agency.EventID {
	t.Helper()
	event, err := agency.NewEventID(value)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestCausalDepthRejectsMissingOrMismatchedAcceptedEvent(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:event-depth-invalid")
	attachment := fixture.current(t).authority.Attachment()
	predecessor := insertSyntheticDepthEvent(t, fixture, attachment, 2)
	wrong, err := agency.NewEventRef(predecessor.ID(), agency.Sum([]byte("wrong")))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := exactEventDepthTx(fixture.ctx, tx, wrong); err == nil {
		t.Fatal("mismatched causal Event digest unexpectedly accepted")
	}
	missing, err := agency.NewEventRef(mustEventID(t, "event:missing"), agency.Sum([]byte("missing")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exactEventDepthTx(fixture.ctx, tx, missing); err == nil {
		t.Fatalf("missing causal Event error = %v", err)
	}
}

func TestCausalDepthRejectsColumnCanonicalDivergence(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:event-depth-divergence")
	predecessor := insertSyntheticDepthEvent(t, fixture,
		fixture.current(t).authority.Attachment(), 5)
	result, err := fixture.store.db.Exec(`UPDATE events SET causal_depth = 4 WHERE event_id = ?`,
		predecessor.ID().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := requireOneRow(result, "tamper causal depth fixture"); err != nil {
		t.Fatal(err)
	}

	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := exactEventDepthTx(fixture.ctx, tx, predecessor); err == nil {
		t.Fatal("causal depth column diverging from canonical Event unexpectedly accepted")
	}
}
