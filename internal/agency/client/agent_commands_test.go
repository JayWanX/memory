package agencyclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestAgentSubmitReturnsBoundedInputDiagnostics(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		code    controlErrorCode
		message string
	}{
		{name: "syntax", input: "{", code: codeInvalidArgument,
			message: "exactly one JSON object"},
		{name: "required", input: `{"kind":"work","payload":"brief"}`,
			code: codeInvalidArgument, message: "include kind, payload, and consequence"},
		{name: "unknown", input: `{"kind":"work","payload":"brief","consequence":"handling.create","extra":true}`,
			code: codeInvalidArgument, message: "contains a non-canonical field"},
		{name: "unknown-successor-field",
			input: `{"kind":"work","payload":"brief","consequence":"handling.create","successors":[{"self":true,"kind":"nested"}]}`,
			code:  codeInvalidArgument, message: "successors may contain only self:true or one View-offered alias"},
		{name: "unknown-artifact-field",
			input: `{"kind":"work","payload":"brief","consequence":"reference.publish","reference_key":"knowledge.current","artifacts":[{"kind":"candidate","handle":"artifact:one","digest":"forged"}]}`,
			code:  codeInvalidArgument, message: "Artifacts may contain only kind and handle"},
		{name: "duplicate", input: `{"kind":"work","kind":"work","payload":"brief","consequence":"handling.create"}`,
			code: codeInvalidArgument, message: "contains a duplicate JSON field"},
		{name: "shape", input: `{"kind":"work","payload":"brief","consequence":"not.closed"}`,
			code: codeInvalidArgument, message: "copied exactly from the current View allowed_intents"},
		{name: "root-shape", input: `{"kind":"work","payload":"brief","consequence":"handling.create"}`,
			code: codeInvalidArgument, message: "requires at least one View-offered successor"},
		{name: "subject-shape", input: `{"kind":"work","payload":"brief","consequence":"handling.advance"}`,
			code: codeInvalidArgument, message: "requires current.facts.handle as subject_handling"},
		{name: "reference-publish-shape",
			input: `{"kind":"work","payload":"brief","consequence":"reference.publish","reference_key":"knowledge.current"}`,
			code:  codeInvalidArgument, message: "requires one new reference_key, exactly one Artifact"},
		{name: "reference-supersede-shape",
			input: `{"kind":"work","payload":"brief","consequence":"reference.supersede","reference_head":"reference:head"}`,
			code:  codeInvalidArgument, message: "requires one View-offered reference_head, exactly one Artifact"},
		{name: "reference-retract-shape",
			input: `{"kind":"work","payload":"brief","consequence":"reference.retract","reference_head":"reference:head","artifacts":[{"kind":"view_handle","handle":"artifact:offered"}]}`,
			code:  codeInvalidArgument, message: "requires one View-offered reference_head, no Artifact"},
		{name: "target-shape",
			input: `{"kind":"work","payload":"brief","consequence":"handling.create","successors":[{"self":true,"alias":"target:peer"}]}`,
			code:  codeInvalidArgument, message: "exactly one of self:true or one View-offered alias"},
		{name: "artifact-kind",
			input: `{"kind":"work","payload":"brief","consequence":"reference.publish","reference_key":"knowledge.current","artifacts":[{"kind":"other","handle":"artifact:candidate"}]}`,
			code:  codeInvalidArgument, message: "Artifact kind must be exactly candidate or view_handle"},
		{name: "field-bound",
			input: `{"kind":"work","payload":"` + strings.Repeat("x", agency.MaxSemanticPayloadBytes+1) +
				`","consequence":"handling.create"}`,
			code: codeContentTooLarge, message: "exceeds a closed field or collection bound"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAppFixture(t)
			fixture.attach(t)
			var output bytes.Buffer
			exit := fixture.app(strings.NewReader(test.input), &output).
				run(context.Background(), []string{"agent", "submit", "--json"})
			if exit != test.code.exitStatus() ||
				!strings.Contains(output.String(), `"code":"`+string(test.code)+`"`) ||
				!strings.Contains(output.String(), test.message) {
				t.Fatalf("submit diagnostic = exit %d output %q", exit, output.String())
			}
			fixture.client.mu.Lock()
			submitCalls := len(fixture.client.submitOperations)
			fixture.client.mu.Unlock()
			if submitCalls != 0 {
				t.Fatalf("invalid input reached authority %d times", submitCalls)
			}
		})
	}
}

func TestAgentSubmitCommandAndStdinDiagnosticsAreActionable(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	for _, test := range []struct {
		name    string
		args    []string
		message string
	}{
		{name: "json passed as argument",
			args:    []string{"agent", "submit", "--json", `{}`},
			message: "use exactly memory agency agent submit --json and provide Intent JSON on stdin"},
		{name: "empty stdin", args: []string{"agent", "submit", "--json"},
			message: "provide exactly one Intent JSON object on stdin with a quoted heredoc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			exit := fixture.app(strings.NewReader(""), &output).run(context.Background(), test.args)
			if exit != codeInvalidArgument.exitStatus() && exit != codeContentRequired.exitStatus() {
				t.Fatalf("submit diagnostic exit = %d output %q", exit, output.String())
			}
			if !strings.Contains(output.String(), test.message) {
				t.Fatalf("submit diagnostic output = %q, want %q", output.String(), test.message)
			}
		})
	}
}

func TestAgentSubmitReportsUncapturedCandidateAsArtifactInput(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	if exit := fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("Current exit = %d", exit)
	}

	var output bytes.Buffer
	exit := fixture.app(bytes.NewReader(candidateRootIntent(t, "artifact:not-captured")), &output).
		run(context.Background(), []string{"agent", "submit", "--json"})
	if exit != codeArtifactInvalid.exitStatus() ||
		!strings.Contains(output.String(), `"code":"artifact_invalid"`) ||
		!strings.Contains(output.String(), "not returned by capture") ||
		!strings.Contains(output.String(), "use view_handle") ||
		strings.Contains(output.String(), string(codeAuthenticationFailed)) {
		t.Fatalf("uncaptured candidate diagnostic = exit %d output %q", exit, output.String())
	}
	fixture.client.mu.Lock()
	submitCalls := len(fixture.client.submitOperations)
	fixture.client.mu.Unlock()
	if submitCalls != 0 {
		t.Fatalf("uncaptured candidate reached authority %d times", submitCalls)
	}
	if exit := fixture.app(strings.NewReader("captured after correction"), io.Discard).
		run(context.Background(), []string{"artifact", "capture", "--json"}); exit != 0 {
		t.Fatalf("capture after candidate diagnostic exit = %d", exit)
	}
	output.Reset()
	exit = fixture.app(bytes.NewReader(candidateRootIntent(t, "artifact:test-candidate")), &output).
		run(context.Background(), []string{"agent", "submit", "--json"})
	if exit != 0 || !strings.Contains(output.String(), `"outcome":"accepted"`) {
		t.Fatalf("corrected candidate submit = exit %d output %q", exit, output.String())
	}
}

func TestIntentInputDiagnosticDoesNotEchoUnknownValidationText(t *testing.T) {
	err := &agency.ValidationError{Category: agency.ErrInvariant,
		Field: "secret-field-sentinel", Problem: "secret-problem-sentinel"}
	diagnostic := intentInputControlError(err)
	if strings.Contains(diagnostic.Message, "secret-field-sentinel") ||
		strings.Contains(diagnostic.Message, "secret-problem-sentinel") ||
		diagnostic.Code != codeInvalidArgument ||
		!strings.Contains(diagnostic.Message, "invalid canonical field or structural shape") {
		t.Fatalf("unknown validation diagnostic = %#v", diagnostic)
	}
}

func TestCaptureKeepsDigestPrivateAndSubmitReplaysAfterPresentationLoss(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	var current bytes.Buffer
	exit := fixture.app(strings.NewReader(""), &current).
		run(context.Background(), []string{"agent", "current", "--json"})
	if exit != 0 {
		t.Fatalf("current exit = %d, output %s", exit, current.String())
	}

	content := "verified artifact bytes"
	var capture bytes.Buffer
	exit = fixture.app(strings.NewReader(content), &capture).
		run(context.Background(), []string{"artifact", "capture", "--json"})
	digest := nodeDigestForTest(content)
	if exit != 0 || !strings.Contains(capture.String(), `"handle":"artifact:test-candidate"`) ||
		strings.Contains(capture.String(), digest) {
		t.Fatalf("capture exit/output = %d / %q", exit, capture.String())
	}

	intent := candidateRootIntent(t, "artifact:test-candidate")
	failing := &failWriter{}
	exit = fixture.app(bytes.NewReader(intent), failing).
		run(context.Background(), []string{"agent", "submit", "--json"})
	if exit != 1 {
		t.Fatalf("presentation-loss submit exit = %d", exit)
	}
	store := newJournalStore(fixture.nodeState, bytes.NewReader(make([]byte, 64)))
	if exists, err := store.exists(); err != nil || !exists {
		t.Fatalf("terminal replay journal exists = %v, %v", exists, err)
	}

	var replay bytes.Buffer
	exit = fixture.app(bytes.NewReader(intent), &replay).
		run(context.Background(), []string{"agent", "submit", "--json"})
	fixture.client.mu.Lock()
	operations := append([]string(nil), fixture.client.submitOperations...)
	bindings := append([][]candidateBinding(nil), fixture.client.submitCandidates...)
	endCalls := fixture.client.endCalls
	fixture.client.mu.Unlock()
	if exit != 0 || len(operations) != 2 || operations[0] == "" || operations[0] != operations[1] ||
		len(bindings) != 2 || len(bindings[0]) != 1 || bindings[0][0].Digest != digest ||
		!strings.Contains(replay.String(), `"replayed":true`) || endCalls != 1 {
		t.Fatalf("submit replay = exit %d operations %#v bindings %#v output %q",
			exit, operations, bindings, replay.String())
	}
	if exists, err := store.exists(); err != nil || exists {
		t.Fatalf("presented journal exists = %v, %v", exists, err)
	}
}

func TestAcceptedHandlingReceiptRetainsReplayUntilBoundaryEndCommits(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	if exit := fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("Current exit = %d", exit)
	}
	fixture.client.endFailures = 1
	intent := candidateFreeRootIntent(t, "boundary-end-replay")
	var first bytes.Buffer
	if exit := fixture.app(bytes.NewReader(intent), &first).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 0 {
		t.Fatalf("accepted submit with lost end = exit %d output %q", exit, first.String())
	}
	terminal := loadJournalForTest(t, fixture.nodeState)
	if !validTerminalName(terminal.fileName) || !terminal.CurrentOperation.IsZero() {
		terminal.clear()
		t.Fatalf("presented boundary end journal = file %q current %q",
			terminal.fileName, terminal.CurrentOperation.String())
	}
	terminal.clear()

	var next bytes.Buffer
	if exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x22)), &next).
		run(context.Background(), []string{"hook", "attach", "--json"}); exit != 0 {
		t.Fatalf("new boundary recovery = exit %d output %q", exit, next.String())
	}
	journal := loadJournalForTest(t, fixture.nodeState)
	if journal.fileName != journalActiveName ||
		journal.BoundaryDigest != agency.Sum(bytes.Repeat([]byte{0x22}, 32)) {
		journal.clear()
		t.Fatalf("recovered new boundary journal = file %q boundary %s",
			journal.fileName, journal.BoundaryDigest.String())
	}
	journal.clear()
	fixture.client.mu.Lock()
	endCalls := fixture.client.endCalls
	attachCalls := fixture.client.attachCalls
	submitCalls := len(fixture.client.submitOperations)
	fixture.client.mu.Unlock()
	if endCalls != 2 || attachCalls != 2 || submitCalls != 1 {
		t.Fatalf("boundary recovery calls = ends %d attaches %d submits %d, want 2/2/1",
			endCalls, attachCalls, submitCalls)
	}
}

func TestHookEndFinishesPresentedHandlingWithoutIntentReplay(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	if exit := fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("Current exit = %d", exit)
	}
	fixture.client.endFailures = 1
	intent := candidateFreeRootIntent(t, "hook-end-recovery")
	if exit := fixture.app(bytes.NewReader(intent), io.Discard).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 0 {
		t.Fatalf("accepted submit exit = %d", exit)
	}
	fixture.endBoundary(t, 0x21)
	store := newJournalStore(fixture.nodeState, bytes.NewReader(make([]byte, 64)))
	if exists, err := store.exists(); err != nil || exists {
		t.Fatalf("Hook-end recovered journal exists = %t, %v", exists, err)
	}
	fixture.client.mu.Lock()
	endCalls := fixture.client.endCalls
	submitCalls := len(fixture.client.submitOperations)
	fixture.client.mu.Unlock()
	if endCalls != 2 || submitCalls != 1 {
		t.Fatalf("Hook-end recovery calls = ends %d submits %d, want 2/1",
			endCalls, submitCalls)
	}
}

func TestSameHookAttachFinishesPresentedHandlingButNeverReturnsReady(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	if exit := fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("Current exit = %d", exit)
	}
	fixture.client.endFailures = 1
	intent := candidateFreeRootIntent(t, "same-boundary-recovery")
	if exit := fixture.app(bytes.NewReader(intent), io.Discard).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 0 {
		t.Fatalf("accepted submit exit = %d", exit)
	}
	// Model End reaching authority while the subsequent journal removal is
	// lost. The same-boundary Hook retry must finish cleanup, never report ready.
	presented := loadJournalForTest(t, fixture.nodeState)
	if apiErr := fixture.client.End(context.Background(), presented.Attachment); apiErr != nil {
		presented.clear()
		t.Fatal(apiErr)
	}
	presented.clear()
	var output bytes.Buffer
	exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x21)), &output).
		run(context.Background(), []string{"hook", "attach", "--json"})
	if exit != codeContextStale.exitStatus() ||
		!strings.Contains(output.String(), string(codeContextStale)) ||
		strings.Contains(output.String(), `"status":"ready"`) {
		t.Fatalf("completed same boundary = exit %d output %q", exit, output.String())
	}
	store := newJournalStore(fixture.nodeState, bytes.NewReader(make([]byte, 64)))
	if exists, err := store.exists(); err != nil || exists {
		t.Fatalf("completed same-boundary journal exists = %t, %v", exists, err)
	}
	fixture.client.mu.Lock()
	endCalls := fixture.client.endCalls
	attachCalls := fixture.client.attachCalls
	submitCalls := len(fixture.client.submitOperations)
	fixture.client.mu.Unlock()
	if endCalls != 3 || attachCalls != 1 || submitCalls != 1 {
		t.Fatalf("same-boundary recovery calls = ends %d attaches %d submits %d, want 3/1/1",
			endCalls, attachCalls, submitCalls)
	}
}

func TestHookEndCannotDestroyUnpresentedReceiptReplay(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	if exit := fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("Current exit = %d", exit)
	}
	intent := candidateFreeRootIntent(t, "unpresented-hook-end")
	if exit := fixture.app(bytes.NewReader(intent), &failWriter{}).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 1 {
		t.Fatalf("failed presentation exit = %d, want 1", exit)
	}
	var endOutput bytes.Buffer
	exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x21)), &endOutput).
		run(context.Background(), []string{"hook", "end", "--json"})
	if exit != codeOperationPending.exitStatus() ||
		!strings.Contains(endOutput.String(), string(codeOperationPending)) {
		t.Fatalf("unpresented Hook end = exit %d output %q", exit, endOutput.String())
	}
	fixture.client.mu.Lock()
	endCalls := fixture.client.endCalls
	fixture.client.mu.Unlock()
	if endCalls != 0 {
		t.Fatalf("unpresented Hook end calls = %d, want 0", endCalls)
	}
	var replay bytes.Buffer
	if exit := fixture.app(bytes.NewReader(intent), &replay).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 0 ||
		!strings.Contains(replay.String(), `"replayed":true`) {
		t.Fatalf("receipt replay after refused Hook end = exit %d output %q", exit, replay.String())
	}
}

func TestAcceptedReferenceEndsAttachmentAndRejectsFurtherMutation(t *testing.T) {
	fixture := newAppFixture(t)
	intent := prepareReferenceSubmit(t, fixture)
	var receipt bytes.Buffer
	if exit := fixture.app(bytes.NewReader(intent), &receipt).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 0 {
		t.Fatalf("Reference submit exit = %d output %q", exit, receipt.String())
	}
	store := newJournalStore(fixture.nodeState, bytes.NewReader(make([]byte, 64)))
	if exists, err := store.exists(); err != nil || exists {
		t.Fatalf("accepted Reference journal exists = %t, %v", exists, err)
	}
	var next bytes.Buffer
	if exit := fixture.app(strings.NewReader(""), &next).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != codeContextRequired.exitStatus() ||
		!strings.Contains(next.String(), string(codeContextRequired)) {
		t.Fatalf("post-Reference Current = exit %d output %q", exit, next.String())
	}
	var submit bytes.Buffer
	if exit := fixture.app(bytes.NewReader(intent), &submit).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != codeContextRequired.exitStatus() ||
		!strings.Contains(submit.String(), string(codeContextRequired)) {
		t.Fatalf("post-Reference submit = exit %d output %q", exit, submit.String())
	}
	fixture.client.mu.Lock()
	attachCalls := fixture.client.attachCalls
	endCalls := fixture.client.endCalls
	operations := append([]string(nil), fixture.client.currentOperations...)
	submitCalls := len(fixture.client.submitOperations)
	fixture.client.mu.Unlock()
	if attachCalls != 1 || endCalls != 1 || len(operations) != 1 || submitCalls != 1 {
		t.Fatalf("closed Reference boundary = attaches %d ends %d currents %#v submits %d",
			attachCalls, endCalls, operations, submitCalls)
	}
}

func TestReferencePresentationLossRetainsExactReplayBeforeBoundaryEnd(t *testing.T) {
	fixture := newAppFixture(t)
	intent := leaveUnpresentedReference(t, fixture)
	terminal := loadJournalForTest(t, fixture.nodeState)
	if !validTerminalName(terminal.fileName) || terminal.CurrentOperation.IsZero() ||
		len(terminal.Candidates) != 1 {
		terminal.clear()
		t.Fatalf("unpresented terminal = file %q current %q candidates %d",
			terminal.fileName, terminal.CurrentOperation.String(), len(terminal.Candidates))
	}
	terminal.clear()

	var replay bytes.Buffer
	if exit := fixture.app(bytes.NewReader(intent), &replay).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 0 ||
		!strings.Contains(replay.String(), `"replayed":true`) {
		t.Fatalf("Reference replay exit/output = %d / %q", exit, replay.String())
	}
	fixture.client.mu.Lock()
	operations := append([]string(nil), fixture.client.submitOperations...)
	fixture.client.mu.Unlock()
	if len(operations) != 2 || operations[0] != operations[1] {
		t.Fatalf("Reference replay operations = %#v", operations)
	}
	store := newJournalStore(fixture.nodeState, bytes.NewReader(make([]byte, 64)))
	if exists, err := store.exists(); err != nil || exists {
		t.Fatalf("replayed Reference journal exists = %t, %v", exists, err)
	}
}

func TestPresentedTerminalCannotReactivateAfterBoundaryEndFailure(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	if exit := fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("Current exit = %d", exit)
	}
	fixture.client.endFailures = 1
	intent := candidateFreeRootIntent(t, "end failure stays terminal")
	if exit := fixture.app(bytes.NewReader(intent), io.Discard).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 0 {
		t.Fatalf("accepted submit exit = %d", exit)
	}
	presented := loadJournalForTest(t, fixture.nodeState)
	if !validTerminalName(presented.fileName) || !presented.CurrentOperation.IsZero() {
		presented.clear()
		t.Fatalf("presented phase = file %q current %q",
			presented.fileName, presented.CurrentOperation.String())
	}
	presented.clear()

	var output bytes.Buffer
	if exit := fixture.app(strings.NewReader(""), &output).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != codeOperationPending.exitStatus() ||
		!strings.Contains(output.String(), string(codeOperationPending)) {
		t.Fatalf("presented terminal Current = exit %d output %q", exit, output.String())
	}
	var submit bytes.Buffer
	if exit := fixture.app(bytes.NewReader(intent), &submit).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != codeContextRequired.exitStatus() ||
		!strings.Contains(submit.String(), string(codeContextRequired)) {
		t.Fatalf("presented terminal submit = exit %d output %q", exit, submit.String())
	}
	fixture.client.mu.Lock()
	currentCalls := len(fixture.client.currentOperations)
	submitCalls := len(fixture.client.submitOperations)
	endCalls := fixture.client.endCalls
	fixture.client.mu.Unlock()
	if currentCalls != 1 || submitCalls != 1 || endCalls != 1 {
		t.Fatalf("presented terminal calls = current %d submit %d end %d, want 1/1/1",
			currentCalls, submitCalls, endCalls)
	}
}

func TestTerminalReplayRejectsChangedIntentLocally(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	_ = fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"})
	original := candidateFreeRootIntent(t, "first")
	exit := fixture.app(bytes.NewReader(original), &failWriter{}).
		run(context.Background(), []string{"agent", "submit", "--json"})
	if exit != 1 {
		t.Fatalf("first submit exit = %d", exit)
	}
	var output bytes.Buffer
	exit = fixture.app(bytes.NewReader(candidateFreeRootIntent(t, "changed")), &output).
		run(context.Background(), []string{"agent", "submit", "--json"})
	if exit != codeOperationMismatch.exitStatus() ||
		!strings.Contains(output.String(), string(codeOperationMismatch)) {
		t.Fatalf("changed terminal Intent exit/output = %d / %q", exit, output.String())
	}
	fixture.client.mu.Lock()
	calls := len(fixture.client.submitOperations)
	fixture.client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Submit calls = %d, want 1", calls)
	}
}

func TestHookNeverOverwritesExpiredTerminalReplayJournal(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	_ = fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"})
	intent := candidateFreeRootIntent(t, "terminal")
	exit := fixture.app(bytes.NewReader(intent), &failWriter{}).
		run(context.Background(), []string{"agent", "submit", "--json"})
	if exit != 1 {
		t.Fatalf("terminal setup exit = %d", exit)
	}
	fixture.now = fixture.now.AddDate(2, 0, 0)
	var rotated bytes.Buffer
	exit = fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x25)), &rotated).
		run(context.Background(), []string{"hook", "attach", "--json"})
	if exit != codeOperationPending.exitStatus() {
		t.Fatalf("new boundary over terminal journal = exit %d output %q", exit, rotated.String())
	}
	fixture.client.mu.Lock()
	calls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Attach calls with terminal journal = %d, want 1", calls)
	}

	var current bytes.Buffer
	exit = fixture.app(strings.NewReader(""), &current).
		run(context.Background(), []string{"agent", "current", "--json"})
	if exit != codeOperationPending.exitStatus() {
		t.Fatalf("terminal current = exit %d output %s", exit, current.String())
	}
}

func TestExpiredJournalStillUsesR7ControlPath(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	fixture.now = fixture.now.AddDate(2, 0, 0)
	var output bytes.Buffer
	exit := fixture.app(strings.NewReader(""), &output).
		run(context.Background(), []string{"agent", "current", "--json"})
	fixture.client.mu.Lock()
	currentCalls := len(fixture.client.currentOperations)
	fixture.client.mu.Unlock()
	if exit != 0 || output.Len() == 0 || currentCalls != 1 || fixture.ensure.Load() != 2 {
		t.Fatalf("expired current = exit %d output %q", exit, output.String())
	}
}

func TestUnsafeJournalFailsClosedBeforeEnsure(t *testing.T) {
	for _, args := range [][]string{
		{"hook", "attach", "--json"},
		{"agent", "current", "--json"},
		{"agent", "submit", "--json"},
		{"artifact", "capture", "--json"},
	} {
		t.Run(strings.Join(args[:2], "_"), func(t *testing.T) {
			fixture := newAppFixture(t)
			if err := os.Symlink(t.TempDir(),
				filepath.Join(fixture.nodeState, journalDirectoryName)); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			input := io.Reader(strings.NewReader(""))
			if args[0] == "hook" {
				input = bytes.NewReader(testBoundaryEnvelope(t, 0x21))
			}
			exit := fixture.app(input, &output).run(context.Background(), args)
			if exit != codeAuthenticationFailed.exitStatus() ||
				!strings.Contains(output.String(), string(codeAuthenticationFailed)) ||
				fixture.ensure.Load() != 0 {
				t.Fatalf("unsafe journal = exit %d output %q ensure %d",
					exit, output.String(), fixture.ensure.Load())
			}
		})
	}
}

func TestInterruptedJournalStageIsRecovered(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	directory := filepath.Join(fixture.nodeState, journalDirectoryName)
	if err := os.Remove(filepath.Join(directory, journalActiveName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, journalStageName), []byte("partial"), ownerFileMode); err != nil {
		t.Fatal(err)
	}
	fixture.attach(t)
	if _, err := os.Lstat(filepath.Join(directory, journalStageName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage after recovery = %v", err)
	}
}

func TestEnsureFailurePreventsPrivateClientCall(t *testing.T) {
	fixture := newAppFixture(t)
	var output bytes.Buffer
	app := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x21)), &output)
	app.deps.ensureDaemon = func(context.Context, string) error {
		return errors.New("private daemon failure detail")
	}
	exit := app.run(context.Background(), []string{"hook", "attach", "--json"})
	fixture.client.mu.Lock()
	calls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if exit != codeMemorydUnavailable.exitStatus() || calls != 0 ||
		strings.Contains(output.String(), "private daemon failure detail") {
		t.Fatalf("ensure failure = exit %d Attach calls %d output %q", exit, calls, output.String())
	}
}

func candidateRootIntent(t *testing.T, handleValue string) []byte {
	t.Helper()
	handle, err := agency.NewOpaqueHandle(handleValue)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := agency.NewArtifactCandidate(handle)
	if err != nil {
		t.Fatal(err)
	}
	return rootIntent(t, "with-artifact", []agency.ArtifactInput{artifact})
}

func candidateFreeRootIntent(t *testing.T, payloadValue string) []byte {
	t.Helper()
	return rootIntent(t, payloadValue, nil)
}

func prepareReferenceSubmit(t *testing.T, fixture *appFixture) []byte {
	t.Helper()
	fixture.client.currentView = subjectViewForTest()
	fixture.attach(t)
	if exit := fixture.app(strings.NewReader(""), io.Discard).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("Reference Current exit = %d", exit)
	}
	if exit := fixture.app(strings.NewReader("review playbook"), io.Discard).
		run(context.Background(), []string{"artifact", "capture", "--json"}); exit != 0 {
		t.Fatalf("Reference capture exit = %d", exit)
	}
	return referencePublishIntent(t, "artifact:test-candidate")
}

func leaveUnpresentedReference(t *testing.T, fixture *appFixture) []byte {
	t.Helper()
	intent := prepareReferenceSubmit(t, fixture)
	if exit := fixture.app(bytes.NewReader(intent), &failWriter{}).
		run(context.Background(), []string{"agent", "submit", "--json"}); exit != 1 {
		t.Fatalf("presentation-loss Reference exit = %d", exit)
	}
	return intent
}

func referencePublishIntent(t *testing.T, artifactHandle string) []byte {
	t.Helper()
	kind, err := agency.NewSemanticLabel("knowledge.playbook")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := agency.NewSemanticPayload("publish a reusable review playbook")
	if err != nil {
		t.Fatal(err)
	}
	key, err := agency.NewReferenceKey("playbook.review")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := agency.NewOpaqueHandle(artifactHandle)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := agency.NewArtifactCandidate(handle)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := agency.NewAgentIntent(agency.IntentSpec{Kind: kind, Payload: payload,
		Consequence: agency.ConsequencePublishReference, ReferenceKey: key,
		Artifacts: []agency.ArtifactInput{artifact}})
	if err != nil {
		t.Fatal(err)
	}
	return intent.CanonicalJSON()
}

func subjectViewForTest() []byte {
	return []byte(`{"schema":"memory.agent.view","version":8,` +
		`"view":"view:test","current":{"facts":{"handle":"r7:subject:test","reply_to":"r7:subject:test","reply_required":false,"reply_observation_pending":false},` +
		`"semantic":{"kind":"review.request","payload":"review"}},` +
		`"outstanding":{"open_total":1,"related_total":0,"related_projected":0,"truncated":false},` +
		`"allowed_intents":[]}`)
}

func loadJournalForTest(t *testing.T, nodeState string) clientJournal {
	t.Helper()
	store := newJournalStore(nodeState, bytes.NewReader(make([]byte, 64)))
	var result clientJournal
	if err := store.withLock(false, func(directory *lockedJournalDirectory) error {
		journal, err := directory.load()
		if err != nil {
			return err
		}
		result = journal
		result.Attachment.Credential = append([]byte(nil), journal.Attachment.Credential...)
		journal.clear()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func rootIntent(t *testing.T, payloadValue string, artifacts []agency.ArtifactInput) []byte {
	t.Helper()
	kind, err := agency.NewSemanticLabel("test.request")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := agency.NewSemanticPayload(payloadValue)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := agency.NewAgentIntent(agency.IntentSpec{Kind: kind, Payload: payload,
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget()}, Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	return intent.CanonicalJSON()
}

func nodeDigestForTest(content string) string {
	return agency.Sum([]byte(content)).String()
}

type failWriter struct{}

func (*failWriter) Write([]byte) (int, error) { return 0, errors.New("presentation lost") }
