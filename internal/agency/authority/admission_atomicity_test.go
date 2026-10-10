package authority

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestBoundIntentFaultMatrixCommitsWholeOriginOutcomeOrNone(t *testing.T) {
	faults := []struct {
		name    string
		trigger string
	}{
		{
			name: "origin sequence",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER UPDATE ON authority_clock
				BEGIN SELECT RAISE(ABORT, 'fault: origin sequence'); END`,
		},
		{
			name: "Event",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON events
				BEGIN SELECT RAISE(ABORT, 'fault: Event'); END`,
		},
		{
			name: "Artifact pin",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON event_artifacts
				BEGIN SELECT RAISE(ABORT, 'fault: Artifact pin'); END`,
		},
		{
			name: "local Handling",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON handlings
				BEGIN SELECT RAISE(ABORT, 'fault: local Handling'); END`,
		},
		{
			name: "operation Receipt",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON operations
				BEGIN SELECT RAISE(ABORT, 'fault: operation Receipt'); END`,
		},
	}

	for index, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fixture := newAuthorityFixture(t, fmt.Sprintf("principal:origin-atomic-%d", index))
			firstRoute := peerRouteSpec(t, fixture.principal, "origin-first")
			secondRoute := peerRouteSpec(t, fixture.principal, "origin-second")
			mustEnrollPeerRoute(t, fixture, firstRoute)
			mustEnrollPeerRoute(t, fixture, secondRoute)

			firstRemote, err := agency.AliasTarget(firstRoute.PublicAlias)
			if err != nil {
				t.Fatal(err)
			}
			secondRemote, err := agency.AliasTarget(secondRoute.PublicAlias)
			if err != nil {
				t.Fatal(err)
			}
			digest := fixture.catalog(t, "origin atomic Artifact")
			operation := mustOperation(t, "operation:origin-atomic")
			candidateHandle := mustHandle(t, "candidate:origin-atomic")
			artifactInput, err := agency.NewArtifactCandidate(candidateHandle)
			if err != nil {
				t.Fatal(err)
			}
			intent := mustIntent(t, agency.IntentSpec{
				Kind:        mustLabel(t, "work.atomic-origin"),
				Payload:     mustPayload(t, "commit one complete origin outcome"),
				Consequence: agency.ConsequenceCreateHandlings,
				Successors:  []agency.TargetRef{agency.SelfTarget(), firstRemote, secondRemote},
				Artifacts:   []agency.ArtifactInput{artifactInput},
			})
			candidate, err := agency.NewCapturedCandidate(operation, artifactInput, digest)
			if err != nil {
				t.Fatal(err)
			}
			request, err := fixture.current(t).Bind(intent, operation,
				[]agency.CapturedCandidate{candidate})
			if err != nil {
				t.Fatal(err)
			}

			before := snapshotP05Authority(t, fixture.store)
			drop := installP05Fault(t, fixture.store, fault.trigger)
			if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, request); err == nil {
				t.Fatal("faulted origin admission unexpectedly succeeded")
			}
			requireP05Snapshot(t, fixture.store, before)
			drop()
			requireExactAdmissionReplay(t, fixture, request)
		})
	}
}

func TestBoundIntentFaultAfterSubjectMutationRestoresExistingHandling(t *testing.T) {
	faults := []struct {
		name    string
		trigger string
	}{
		{
			name: "subject update",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER UPDATE ON handlings
				BEGIN SELECT RAISE(ABORT, 'fault: subject update'); END`,
		},
		{
			name: "second PeerDelivery",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON peer_outbox
				WHEN (SELECT COUNT(*) FROM peer_outbox) = 2
				BEGIN SELECT RAISE(ABORT, 'fault: second PeerDelivery'); END`,
		},
	}

	for index, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fixture := newAuthorityFixture(t, fmt.Sprintf("principal:subject-atomic-%d", index))
			firstRoute := peerRouteSpec(t, fixture.principal, "subject-first")
			secondRoute := peerRouteSpec(t, fixture.principal, "subject-second")
			mustEnrollPeerRoute(t, fixture, firstRoute)
			mustEnrollPeerRoute(t, fixture, secondRoute)

			root := rootRequest(t, fixture.current(t), "operation:subject-atomic-root",
				"retain the claimed responsibility")
			if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
				t.Fatal(err)
			} else {
				requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
			}
			view := fixture.current(t)
			firstRemote, err := agency.AliasTarget(firstRoute.PublicAlias)
			if err != nil {
				t.Fatal(err)
			}
			secondRemote, err := agency.AliasTarget(secondRoute.PublicAlias)
			if err != nil {
				t.Fatal(err)
			}
			intent := mustIntent(t, agency.IntentSpec{
				Kind:            mustLabel(t, "work.atomic-subject"),
				Payload:         mustPayload(t, "resolve and leave successor responsibility"),
				Consequence:     agency.ConsequenceResolveUnresolved,
				SubjectHandling: currentSubjectHandle(t, view),
				Successors:      []agency.TargetRef{agency.SelfTarget(), firstRemote, secondRemote},
			})
			request, err := view.Bind(intent, mustOperation(t, "operation:subject-atomic"), nil)
			if err != nil {
				t.Fatal(err)
			}

			before := snapshotP05Authority(t, fixture.store)
			drop := installP05Fault(t, fixture.store, fault.trigger)
			if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, request); err == nil {
				t.Fatal("fault after subject mutation unexpectedly succeeded")
			}
			requireP05Snapshot(t, fixture.store, before)
			drop()
			requireExactAdmissionReplay(t, fixture, request)
		})
	}
}

func TestReferenceFaultMatrixCommitsLineageAndHeadAtomically(t *testing.T) {
	faults := []struct {
		name    string
		trigger string
	}{
		{
			name: "lineage",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON reference_lineage
				BEGIN SELECT RAISE(ABORT, 'fault: Reference lineage'); END`,
		},
		{
			name: "active head",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON active_references
				BEGIN SELECT RAISE(ABORT, 'fault: Reference head'); END`,
		},
		{
			name: "late operation Receipt",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON operations
				BEGIN SELECT RAISE(ABORT, 'fault: Reference operation Receipt'); END`,
		},
	}

	for index, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fixture := newAuthorityFixture(t, fmt.Sprintf("principal:reference-atomic-%d", index))
			digest := fixture.catalog(t, "Reference atomic playbook")
			request := referenceRequest(t, fixture.current(t), "operation:reference-atomic",
				agency.ConsequencePublishReference, "playbook.atomic", &digest)
			before := snapshotP05Authority(t, fixture.store)
			drop := installP05Fault(t, fixture.store, fault.trigger)
			if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, request); err == nil {
				t.Fatal("faulted Reference publish unexpectedly succeeded")
			}
			requireP05Snapshot(t, fixture.store, before)
			drop()
			requireExactAdmissionReplay(t, fixture, request)
		})
	}
}

func TestReferenceSupersedeFaultRestoresPreviousHead(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-supersede-atomic")
	first := fixture.catalog(t, "Reference version one")
	publish := referenceRequest(t, fixture.current(t), "operation:reference-first",
		agency.ConsequencePublishReference, "playbook.supersede-atomic", &first)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, publish); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	second := fixture.catalog(t, "Reference version two")
	supersede := referenceRequest(t, fixture.current(t), "operation:reference-second",
		agency.ConsequenceSupersedeReference, "playbook.supersede-atomic", &second)

	before := snapshotP05Authority(t, fixture.store)
	drop := installP05Fault(t, fixture.store, `CREATE TEMP TRIGGER p05_fault
		AFTER UPDATE ON active_references
		BEGIN SELECT RAISE(ABORT, 'fault: Reference head update'); END`)
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, supersede); err == nil {
		t.Fatal("faulted Reference supersede unexpectedly succeeded")
	}
	requireP05Snapshot(t, fixture.store, before)
	drop()
	requireExactAdmissionReplay(t, fixture, supersede)
}

func TestVerifiedPeerDeliveryFaultMatrixCommitsWholeReceiverOutcomeOrNone(t *testing.T) {
	faults := []struct {
		name    string
		trigger string
	}{
		{
			name: "origin sequence",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER UPDATE ON authority_clock
				BEGIN SELECT RAISE(ABORT, 'fault: receiver origin sequence'); END`,
		},
		{
			name: "local Event",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON events
				BEGIN SELECT RAISE(ABORT, 'fault: receiver Event'); END`,
		},
		{
			name: "Artifact pin",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON event_artifacts
				BEGIN SELECT RAISE(ABORT, 'fault: receiver Artifact pin'); END`,
		},
		{
			name: "local Handling",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER INSERT ON handlings
				BEGIN SELECT RAISE(ABORT, 'fault: receiver Handling'); END`,
		},
		{
			name: "inbox Receipt settlement",
			trigger: `CREATE TEMP TRIGGER p05_fault AFTER UPDATE ON peer_inbox
				WHEN OLD.state = 'staged' AND NEW.state = 'settled'
				BEGIN SELECT RAISE(ABORT, 'fault: receiver inbox settlement'); END`,
		},
	}

	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fixture := newPeerRoundTripFixture(t)
			delivery := fixture.admitOrigin(t)
			signature := ed25519.Sign(fixture.originPrivate, delivery.SigningMessage())
			staged, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
				fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
			if err != nil || staged.State() != PeerAdmissionStateStaged {
				t.Fatalf("StagePeerDelivery() = %#v, %v", staged, err)
			}
			if got := fixture.receiver.catalog(t, fixture.content); got != fixture.digest {
				t.Fatalf("receiver Artifact digest = %s, want %s", got.String(), fixture.digest.String())
			}

			before := snapshotP05Authority(t, fixture.receiver.store)
			drop := installP05Fault(t, fixture.receiver.store, fault.trigger)
			if _, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx,
				delivery.ID()); err == nil {
				t.Fatal("faulted receiver admission unexpectedly succeeded")
			}
			requireP05Snapshot(t, fixture.receiver.store, before)
			drop()

			accepted, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx,
				delivery.ID())
			if err != nil || accepted.State() != PeerAdmissionStateAccepted || accepted.Replayed() {
				t.Fatalf("receiver retry = %#v, %v", accepted, err)
			}
			receipt, ok := accepted.Receipt()
			if !ok || receipt.Outcome() != agency.PeerAdmissionOutcomeAccepted {
				t.Fatalf("receiver retry Receipt = %#v", receipt)
			}
			afterAccepted := snapshotP05Authority(t, fixture.receiver.store)
			replayed, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx,
				delivery.ID())
			if err != nil || !replayed.Replayed() || replayed.State() != PeerAdmissionStateAccepted {
				t.Fatalf("receiver replay = %#v, %v", replayed, err)
			}
			replayedReceipt, ok := replayed.Receipt()
			if !ok || replayedReceipt.Digest() != receipt.Digest() ||
				!bytes.Equal(replayedReceipt.CanonicalJSON(), receipt.CanonicalJSON()) {
				t.Fatal("receiver replay changed the accepted Receipt")
			}
			requireP05Snapshot(t, fixture.receiver.store, afterAccepted)
		})
	}
}

func requireExactAdmissionReplay(t *testing.T, fixture *authorityFixture,
	request agency.BoundIntent,
) {
	t.Helper()
	accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, accepted, agency.ReceiptOutcomeAccepted)
	if accepted.Replayed() {
		t.Fatal("retry after rolled-back fault was classified as replay")
	}
	afterAccepted := snapshotP05Authority(t, fixture.store)
	replayed, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed() || replayed.ReceiptDigest() != accepted.ReceiptDigest() ||
		!bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()) {
		t.Fatal("exact replay changed the accepted operation outcome")
	}
	requireP05Snapshot(t, fixture.store, afterAccepted)
}

func installP05Fault(t *testing.T, store *Store, trigger string) func() {
	t.Helper()
	if _, err := store.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	active := true
	drop := func() {
		t.Helper()
		if !active {
			return
		}
		if _, err := store.db.Exec(`DROP TRIGGER IF EXISTS p05_fault`); err != nil {
			t.Fatal(err)
		}
		active = false
	}
	t.Cleanup(drop)
	return drop
}

type p05Snapshot map[string][]byte

func snapshotP05Authority(t *testing.T, store *Store) p05Snapshot {
	t.Helper()
	tables := []string{
		"authority_clock",
		"verified_artifacts",
		"events",
		"event_artifacts",
		"operations",
		"current_operations",
		"handlings",
		"claim_dispositions",
		"active_references",
		"reference_lineage",
		"peer_outbox",
		"peer_inbox",
	}
	snapshot := make(p05Snapshot, len(tables))
	for _, table := range tables {
		snapshot[table] = rawP05Table(t, store, table)
	}
	return snapshot
}

func requireP05Snapshot(t *testing.T, store *Store, want p05Snapshot) {
	t.Helper()
	tables := make([]string, 0, len(want))
	for table := range want {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		got := rawP05Table(t, store, table)
		if !bytes.Equal(got, want[table]) {
			t.Fatalf("raw table %s changed across an atomicity boundary: before=%s after=%s",
				table, agency.Sum(want[table]).String(), agency.Sum(got).String())
		}
	}
}

func rawP05Table(t *testing.T, store *Store, table string) []byte {
	t.Helper()
	query, ok := p05SnapshotQuery(table)
	if !ok {
		t.Fatalf("P-05 snapshot requested unknown table %q", table)
	}
	rows, err := store.db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot bytes.Buffer
	for _, column := range columns {
		fmt.Fprintf(&snapshot, "%d:%s|", len(column), column)
	}
	snapshot.WriteByte('\n')
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			writeP05SQLValue(&snapshot, value)
		}
		snapshot.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot.Bytes()
}

func writeP05SQLValue(target *bytes.Buffer, value any) {
	switch typed := value.(type) {
	case nil:
		target.WriteString("null|")
	case int64:
		fmt.Fprintf(target, "int:%d|", typed)
	case float64:
		fmt.Fprintf(target, "float:%016x|", math.Float64bits(typed))
	case bool:
		fmt.Fprintf(target, "bool:%t|", typed)
	case string:
		fmt.Fprintf(target, "text:%d:%s|", len(typed), typed)
	case []byte:
		fmt.Fprintf(target, "blob:%d:%x|", len(typed), typed)
	default:
		fmt.Fprintf(target, "%T:%v|", typed, typed)
	}
}

func p05SnapshotQuery(table string) (string, bool) {
	queries := map[string]string{
		"authority_clock":    `SELECT * FROM authority_clock ORDER BY singleton`,
		"verified_artifacts": `SELECT * FROM verified_artifacts ORDER BY digest`,
		"events":             `SELECT * FROM events ORDER BY event_id`,
		"event_artifacts":    `SELECT * FROM event_artifacts ORDER BY event_id, artifact_digest`,
		"operations": `SELECT * FROM operations
			ORDER BY actor_principal_id, operation_key`,
		"current_operations": `SELECT * FROM current_operations
			ORDER BY attachment_id, operation_key`,
		"handlings":          `SELECT * FROM handlings ORDER BY handling_id`,
		"claim_dispositions": `SELECT * FROM claim_dispositions ORDER BY disposition_key`,
		"active_references":  `SELECT * FROM active_references ORDER BY reference_key`,
		"reference_lineage":  `SELECT * FROM reference_lineage ORDER BY event_id`,
		"peer_outbox":        `SELECT * FROM peer_outbox ORDER BY delivery_id`,
		"peer_inbox":         `SELECT * FROM peer_inbox ORDER BY delivery_id`,
	}
	query, ok := queries[table]
	return query, ok
}
