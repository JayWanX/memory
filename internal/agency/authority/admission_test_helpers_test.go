package authority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

var attachmentBoundarySequence atomic.Uint64

func nextAttachmentBoundary(t *testing.T) agency.Digest {
	t.Helper()
	return agency.Sum([]byte(fmt.Sprintf("%s/%d", t.Name(), attachmentBoundarySequence.Add(1))))
}

type memoryArtifactVerifier struct {
	mu      sync.Mutex
	content map[agency.Digest][]byte
	calls   int
}

func (verifier *memoryArtifactVerifier) VerifyArtifact(_ context.Context, digest agency.Digest,
	byteSize int64,
) error {
	verifier.mu.Lock()
	verifier.calls++
	content, exists := verifier.content[digest]
	verifier.mu.Unlock()
	if !exists || int64(len(content)) != byteSize || agency.Sum(content) != digest {
		return ErrArtifactUnavailable
	}
	return nil
}

type authorityFixture struct {
	ctx       context.Context
	store     *Store
	path      string
	now       *time.Time
	principal agency.AgentPrincipalID
	proof     AttachmentProof
	verifier  *memoryArtifactVerifier
	currentID int
}

func newAuthorityFixture(t *testing.T, principalValue string) *authorityFixture {
	t.Helper()
	path := testDatabasePath(t)
	now := time.Date(2026, 8, 3, 4, 5, 6, 7, time.UTC)
	verifier := &memoryArtifactVerifier{content: make(map[agency.Digest][]byte)}
	store, err := open(context.Background(), path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	store.artifactVerifier = verifier
	fixture := &authorityFixture{ctx: context.Background(), store: store, path: path,
		now: &now, principal: mustPrincipal(t, principalValue), verifier: verifier}
	if err := store.EnrollPrincipal(fixture.ctx, fixture.principal); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	fixture.proof, err = store.IssueInteractiveAttachment(fixture.ctx, fixture.principal,
		nextAttachmentBoundary(t))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.store.Close() })
	return fixture
}

func (fixture *authorityFixture) current(t *testing.T) BoundView {
	t.Helper()
	fixture.currentID++
	operation, err := NewCurrentOperation(mustOperation(t,
		fmt.Sprintf("operation:current-%d", fixture.currentID)))
	if err != nil {
		t.Fatal(err)
	}
	view, err := fixture.store.Current(fixture.ctx, fixture.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func (fixture *authorityFixture) catalog(t *testing.T, content string) agency.Digest {
	t.Helper()
	bytes := []byte(content)
	artifact, err := VerifyArtifact(bytes, *fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.CatalogArtifact(fixture.ctx, artifact); err != nil {
		t.Fatal(err)
	}
	fixture.verifier.mu.Lock()
	fixture.verifier.content[artifact.Digest()] = append([]byte(nil), bytes...)
	fixture.verifier.mu.Unlock()
	return artifact.Digest()
}

func rootRequest(t *testing.T, view BoundView, operationValue, payloadValue string) agency.BoundIntent {
	t.Helper()
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "work.request"),
		Payload: mustPayload(t, payloadValue), Consequence: agency.ConsequenceCreateHandlings,
		Successors: []agency.TargetRef{agency.SelfTarget()}})
	request, err := view.Bind(intent, mustOperation(t, operationValue), nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func subjectRequest(t *testing.T, view BoundView, operationValue string, consequence agency.Consequence,
	payloadValue string, artifact *agency.Digest,
) agency.BoundIntent {
	t.Helper()
	subject := currentSubjectHandle(t, view)
	spec := agency.IntentSpec{Kind: mustLabel(t, "work.progress"), Payload: mustPayload(t, payloadValue),
		Consequence: consequence, SubjectHandling: subject}
	var candidates []agency.CapturedCandidate
	if artifact != nil {
		handle := mustHandle(t, "candidate:"+operationValue)
		input, err := agency.NewArtifactCandidate(handle)
		if err != nil {
			t.Fatal(err)
		}
		spec.Artifacts = []agency.ArtifactInput{input}
		candidate, err := agency.NewCapturedCandidate(mustOperation(t, operationValue), input, *artifact)
		if err != nil {
			t.Fatal(err)
		}
		candidates = []agency.CapturedCandidate{candidate}
	}
	intent := mustIntent(t, spec)
	request, err := view.Bind(intent, mustOperation(t, operationValue), candidates)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func referenceRequest(t *testing.T, view BoundView, operationValue string,
	consequence agency.Consequence, keyValue string, artifact *agency.Digest,
) agency.BoundIntent {
	t.Helper()
	spec := agency.IntentSpec{Kind: mustLabel(t, "knowledge.playbook"),
		Payload: mustPayload(t, "review artifacts before completion"), Consequence: consequence}
	if consequence == agency.ConsequencePublishReference {
		spec.ReferenceKey = mustReferenceKey(t, keyValue)
	} else {
		spec.ReferenceHead = referenceHeadHandle(t, view, keyValue)
	}
	var candidates []agency.CapturedCandidate
	if artifact != nil {
		handle := mustHandle(t, "candidate:"+operationValue)
		input, err := agency.NewArtifactCandidate(handle)
		if err != nil {
			t.Fatal(err)
		}
		spec.Artifacts = []agency.ArtifactInput{input}
		candidate, err := agency.NewCapturedCandidate(mustOperation(t, operationValue), input, *artifact)
		if err != nil {
			t.Fatal(err)
		}
		candidates = []agency.CapturedCandidate{candidate}
	}
	request, err := view.Bind(mustIntent(t, spec), mustOperation(t, operationValue), candidates)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func publishTestReference(t *testing.T, fixture *authorityFixture, key, content string) {
	t.Helper()
	digest := fixture.catalog(t, content)
	request := referenceRequest(t, fixture.current(t), "operation:publish:"+key,
		agency.ConsequencePublishReference, key, &digest)
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
}

type publicViewWire struct {
	Current *struct {
		Facts struct {
			Handle string `json:"handle"`
		} `json:"facts"`
	} `json:"current"`
	References []struct {
		Facts struct {
			Head  string `json:"head"`
			Key   string `json:"key"`
			State string `json:"state"`
		} `json:"facts"`
	} `json:"references"`
}

func decodePublicView(t *testing.T, view BoundView) publicViewWire {
	t.Helper()
	var wire publicViewWire
	if err := json.Unmarshal(view.AgentView().CanonicalJSON(), &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

func currentSubjectHandle(t *testing.T, view BoundView) agency.OpaqueHandle {
	t.Helper()
	wire := decodePublicView(t, view)
	if wire.Current == nil {
		t.Fatal("View has no current subject")
	}
	return mustHandle(t, wire.Current.Facts.Handle)
}

func referenceHeadHandle(t *testing.T, view BoundView, key string) agency.OpaqueHandle {
	t.Helper()
	for _, reference := range decodePublicView(t, view).References {
		if reference.Facts.Key == key {
			return mustHandle(t, reference.Facts.Head)
		}
	}
	t.Fatalf("View has no Reference %q", key)
	return agency.OpaqueHandle{}
}

func mustIntent(t *testing.T, spec agency.IntentSpec) agency.AgentIntent {
	t.Helper()
	intent, err := agency.NewAgentIntent(spec)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func mustLabel(t *testing.T, value string) agency.SemanticLabel {
	t.Helper()
	label, err := agency.NewSemanticLabel(value)
	if err != nil {
		t.Fatal(err)
	}
	return label
}

func mustPayload(t *testing.T, value string) agency.SemanticPayload {
	t.Helper()
	payload, err := agency.NewSemanticPayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustOperation(t *testing.T, value string) agency.OperationKey {
	t.Helper()
	operation, err := agency.NewOperationKey(value)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func mustHandle(t *testing.T, value string) agency.OpaqueHandle {
	t.Helper()
	handle, err := agency.NewOpaqueHandle(value)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func mustReferenceKey(t *testing.T, value string) agency.ReferenceKey {
	t.Helper()
	key, err := agency.NewReferenceKey(value)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func requireOutcome(t *testing.T, result AdmissionResult, want agency.ReceiptOutcome) {
	t.Helper()
	if result.Outcome() != want || len(result.ReceiptJSON()) == 0 || result.ReceiptDigest().IsZero() {
		t.Fatalf("Admission result = outcome:%s bytes:%d digest:%s", result.Outcome().String(),
			len(result.ReceiptJSON()), result.ReceiptDigest().String())
	}
}

func countRows(t *testing.T, store *Store, table string) int {
	t.Helper()
	allowed := map[string]bool{"events": true, "operations": true, "handlings": true,
		"active_references": true, "reference_lineage": true, "claim_dispositions": true,
		"peer_outbox": true, "peer_inbox": true}
	if !allowed[table] {
		t.Fatal(errors.New("test requested unsafe table"))
	}
	var count int
	if err := store.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
