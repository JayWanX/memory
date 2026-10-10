package agencyclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const testCredentialText = "private-credential-never-project"

type fakeAgencyClient struct {
	mu                sync.Mutex
	attachCalls       int
	endCalls          int
	endAttachments    []string
	endFailures       int
	currentOperations []string
	submitOperations  []string
	submitCandidates  [][]candidateBinding
	captureCalls      int
	readCalls         []string
	readContent       []byte
	currentView       []byte
	currentFailures   int
	attachBlock       chan struct{}
	attachExpiresAt   time.Time
	attachDivergeAt   int
}

func (client *fakeAgencyClient) Attach(_ context.Context,
	_ agency.Digest,
) (attachment, *controlError) {
	client.mu.Lock()
	client.attachCalls++
	call := client.attachCalls
	diverge := call == client.attachDivergeAt
	block := client.attachBlock
	client.mu.Unlock()
	if block != nil {
		<-block
	}
	expiresAt := client.attachExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	}
	credential := make([]byte, journalCredentialBytes)
	copy(credential, testCredentialText)
	if diverge {
		credential[0] ^= 0xff
	}
	return attachment{ID: "attachment:test", Credential: credential,
		ExpiresAt: expiresAt}, nil
}

func (client *fakeAgencyClient) End(_ context.Context, value attachment) *controlError {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.endCalls++
	client.endAttachments = append(client.endAttachments, value.ID)
	if client.endFailures > 0 {
		client.endFailures--
		return newControlError(codeMemorydUnavailable, "test boundary end loss")
	}
	return nil
}

func (client *fakeAgencyClient) Current(_ context.Context, _ attachment,
	operation string,
) ([]byte, *controlError) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.currentOperations = append(client.currentOperations, operation)
	if client.currentFailures > 0 {
		client.currentFailures--
		return nil, newControlError(codeMemorydUnavailable, "test transport loss")
	}
	if len(client.currentView) > 0 {
		return append([]byte(nil), client.currentView...), nil
	}
	return []byte(`{"schema":"memory.agent.view","version":8,"view":"view:test","outstanding":{"open_total":0,"related_total":0,"related_projected":0,"truncated":false},"allowed_intents":[]}`), nil
}

func (client *fakeAgencyClient) Submit(_ context.Context, _ attachment,
	_, operation string, _ []byte, candidates []candidateBinding,
) ([]byte, *controlError) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.submitOperations = append(client.submitOperations, operation)
	client.submitCandidates = append(client.submitCandidates,
		append([]candidateBinding(nil), candidates...))
	replayed := len(client.submitOperations) > 1
	if replayed {
		return []byte(`{"schema":"memory.agent.receipt","version":1,"outcome":"accepted","replayed":true}`), nil
	}
	return []byte(`{"schema":"memory.agent.receipt","version":1,"outcome":"accepted","replayed":false}`), nil
}

func (client *fakeAgencyClient) Capture(_ context.Context,
	content []byte,
) (artifactCapture, *controlError) {
	client.mu.Lock()
	client.captureCalls++
	client.mu.Unlock()
	return artifactCapture{Handle: "artifact:test-candidate",
		Digest: agency.Sum(content).String(), ByteSize: int64(len(content))}, nil
}

func (client *fakeAgencyClient) ReadArtifact(_ context.Context, _ attachment,
	_ string, handle string,
) ([]byte, *controlError) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.readCalls = append(client.readCalls, handle)
	return append([]byte(nil), client.readContent...), nil
}

type appFixture struct {
	root      string
	nodeState string
	client    *fakeAgencyClient
	ensure    atomic.Int32
	now       time.Time
}

func newAppFixture(t *testing.T) *appFixture {
	t.Helper()
	root := t.TempDir()
	nodeState := filepath.Join(root, ".memory", "agency")
	if err := os.MkdirAll(nodeState, ownerDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nodeState, ownerDirectoryMode); err != nil {
		t.Fatal(err)
	}
	return &appFixture{root: root, nodeState: nodeState, client: &fakeAgencyClient{},
		now: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (fixture *appFixture) app(stdin io.Reader, stdout io.Writer) *terminal {
	app := newTerminal(stdin, stdout, io.Discard, func(context.Context, string) error {
		fixture.ensure.Add(1)
		return nil
	})
	app.deps.workingDirectory = func() (string, error) { return fixture.root, nil }
	app.deps.newClient = func(string) (agencyClient, error) { return fixture.client, nil }
	app.deps.random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 4096))
	app.deps.clock = func() time.Time { return fixture.now }
	return app
}

func (fixture *appFixture) attach(t *testing.T) string {
	return fixture.attachBoundary(t, 0x21)
}

func (fixture *appFixture) attachBoundary(t *testing.T, fill byte) string {
	t.Helper()
	var output bytes.Buffer
	exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, fill)), &output).
		run(context.Background(), []string{"hook", "attach", "--json"})
	if exit != 0 {
		t.Fatalf("hook attach = exit %d output %s", exit, output.String())
	}
	return output.String()
}

func (fixture *appFixture) endBoundary(t *testing.T, fill byte) string {
	t.Helper()
	var output bytes.Buffer
	exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, fill)), &output).
		run(context.Background(), []string{"hook", "end", "--json"})
	if exit != 0 {
		t.Fatalf("hook end = exit %d output %s", exit, output.String())
	}
	return output.String()
}

func testBoundaryEnvelope(t *testing.T, fill byte) []byte {
	t.Helper()
	value := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	raw, err := json.Marshal(map[string]any{
		"boundary": value, "schema": "memory.hook.boundary", "version": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCommandsRequireContextWithoutEnsuringDaemon(t *testing.T) {
	fixture := newAppFixture(t)
	for _, args := range [][]string{
		{"agent", "current", "--json"},
		{"agent", "submit", "--json"},
		{"artifact", "capture", "--json"},
		{"artifact", "read", "artifact:offered"},
	} {
		var output bytes.Buffer
		exit := fixture.app(strings.NewReader(""), &output).run(context.Background(), args)
		if exit != codeContextRequired.exitStatus() ||
			!strings.Contains(output.String(), `"code":"context_required"`) {
			t.Fatalf("absent journal for %q = exit %d output %q", args, exit, output.String())
		}
	}
	if fixture.ensure.Load() != 0 {
		t.Fatalf("Ensure calls without an Agent context = %d, want 0", fixture.ensure.Load())
	}
}

func TestArtifactReadRequiresCurrentAndWritesExactBytes(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	var absent bytes.Buffer
	exit := fixture.app(strings.NewReader(""), &absent).
		run(context.Background(), []string{"artifact", "read", "artifact:offered"})
	if exit != codeContextRequired.exitStatus() ||
		!strings.Contains(absent.String(), `"code":"context_required"`) {
		t.Fatalf("read without Current = exit %d output %q", exit, absent.String())
	}
	var view bytes.Buffer
	if exit := fixture.app(strings.NewReader(""), &view).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("current = exit %d output %q", exit, view.String())
	}
	fixture.client.readContent = []byte("exact Artifact bytes\nwithout an added delimiter")
	var output bytes.Buffer
	exit = fixture.app(strings.NewReader(""), &output).
		run(context.Background(), []string{"artifact", "read", "artifact:offered"})
	fixture.client.mu.Lock()
	calls := append([]string(nil), fixture.client.readCalls...)
	fixture.client.mu.Unlock()
	if exit != 0 || !bytes.Equal(output.Bytes(), fixture.client.readContent) ||
		len(calls) != 1 || calls[0] != "artifact:offered" {
		t.Fatalf("Artifact read = exit %d calls %#v output %q", exit, calls, output.Bytes())
	}
}

func TestAgentCurrentReadsViewAfterAttach(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	var output bytes.Buffer
	exit := fixture.app(strings.NewReader(""), &output).
		run(context.Background(), []string{"agent", "current", "--json"})
	if exit != 0 || output.String() !=
		`{"schema":"memory.agent.view","version":8,"view":"view:test","outstanding":{"open_total":0,"related_total":0,"related_projected":0,"truncated":false},"allowed_intents":[]}`+"\n" {
		t.Fatalf("R7 current = exit %d output %q", exit, output.String())
	}
}

func TestAgentCurrentWithoutSetupIsUnavailableWithoutEnsuringDaemon(t *testing.T) {
	fixture := newAppFixture(t)
	unconfigured := t.TempDir()
	app := fixture.app(strings.NewReader(""), &bytes.Buffer{})
	app.deps.workingDirectory = func() (string, error) { return unconfigured, nil }
	var output bytes.Buffer
	app.stdout = &output
	exit := app.run(context.Background(), []string{"agent", "current", "--json"})
	if exit != codeMemorydUnavailable.exitStatus() ||
		!strings.Contains(output.String(), `"code":"memoryd_unavailable"`) || fixture.ensure.Load() != 0 {
		t.Fatalf("unconfigured current = exit %d output %q ensure %d",
			exit, output.String(), fixture.ensure.Load())
	}
}

func TestUnsupportedCommandsFailClosedWithoutEnsuringDaemon(t *testing.T) {
	fixture := newAppFixture(t)
	for _, args := range [][]string{
		{"agency", "status", "--json"},
		{"teamwork", "list", "--json"},
		{"hook", "attach"},
	} {
		var output bytes.Buffer
		exit := fixture.app(strings.NewReader(""), &output).run(context.Background(), args)
		if exit != codeInvalidArgument.exitStatus() ||
			!strings.Contains(output.String(), `"code":"invalid_argument"`) {
			t.Fatalf("unsupported command %q = exit %d output %q", args, exit, output.String())
		}
	}
	if fixture.ensure.Load() != 0 {
		t.Fatalf("Ensure calls for unsupported commands = %d, want 0", fixture.ensure.Load())
	}
}

func TestHookAttachRequiresPrivateBoundaryEnvelope(t *testing.T) {
	fixture := newAppFixture(t)
	var output bytes.Buffer
	exit := fixture.app(strings.NewReader(""), &output).
		run(context.Background(), []string{"hook", "attach", "--json"})
	if exit != codeContentRequired.exitStatus() ||
		!strings.Contains(output.String(), string(codeContentRequired)) || fixture.ensure.Load() != 0 {
		t.Fatalf("attach without envelope = exit %d output %q ensure %d",
			exit, output.String(), fixture.ensure.Load())
	}
}

func TestHookAttachProjectsNoPrivateAuthorityAndReusesJournal(t *testing.T) {
	fixture := newAppFixture(t)
	first := fixture.attach(t)
	second := fixture.attach(t)
	if first != second || first != `{"schema":"memory.hook.attach","status":"ready","version":1}`+"\n" {
		t.Fatalf("hook outputs = %q / %q", first, second)
	}
	fixture.client.mu.Lock()
	attachCalls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if attachCalls != 2 || strings.Contains(first, "attachment:test") ||
		strings.Contains(first, testCredentialText) {
		t.Fatalf("attach calls/output = %d / %q", attachCalls, first)
	}
	assertMode(t, filepath.Join(fixture.nodeState, journalDirectoryName), ownerDirectoryMode)
	assertMode(t, filepath.Join(fixture.nodeState, journalDirectoryName, journalLockName), ownerFileMode)
	assertMode(t, filepath.Join(fixture.nodeState, journalDirectoryName, journalActiveName), ownerFileMode)
}

func TestHookAttachSameBoundaryRejectsDivergentAuthorityReplay(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	before := loadJournalForTest(t, fixture.nodeState)
	digest := before.fileDigest
	before.clear()
	fixture.client.attachDivergeAt = 2

	var output bytes.Buffer
	exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x21)), &output).
		run(context.Background(), []string{"hook", "attach", "--json"})
	if exit != codeAuthenticationFailed.exitStatus() ||
		!strings.Contains(output.String(), string(codeAuthenticationFailed)) ||
		strings.Contains(output.String(), `"status":"ready"`) {
		t.Fatalf("divergent same-boundary replay = exit %d output %q", exit, output.String())
	}
	after := loadJournalForTest(t, fixture.nodeState)
	defer after.clear()
	if after.fileDigest != digest {
		t.Fatal("divergent authority replay changed the private journal")
	}
}

func TestHookAttachMissingJournalDelegatesNewBoundaryReplacementToAuthority(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	if err := os.Remove(filepath.Join(fixture.nodeState, journalDirectoryName, journalActiveName)); err != nil {
		t.Fatal(err)
	}
	fixture.attachBoundary(t, 0x22)
	journal := loadJournalForTest(t, fixture.nodeState)
	defer journal.clear()
	if journal.BoundaryDigest != agency.Sum(bytes.Repeat([]byte{0x22}, 32)) {
		t.Fatalf("replacement boundary = %s", journal.BoundaryDigest.String())
	}
	fixture.client.mu.Lock()
	attachCalls, endCalls := fixture.client.attachCalls, fixture.client.endCalls
	fixture.client.mu.Unlock()
	if attachCalls != 2 || endCalls != 0 {
		t.Fatalf("journal-free replacement calls = attach %d end %d, want 2/0",
			attachCalls, endCalls)
	}
}

func TestHookAttachNewBoundaryEndsPredecessorAndRotatesEmptyCurrent(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	var view bytes.Buffer
	if exit := fixture.app(strings.NewReader(""), &view).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("empty current = exit %d output %q", exit, view.String())
	}
	fixture.attachBoundary(t, 0x22)
	fixture.client.mu.Lock()
	attachCalls := fixture.client.attachCalls
	endCalls := fixture.client.endCalls
	fixture.client.mu.Unlock()
	if attachCalls != 2 || endCalls != 1 {
		t.Fatalf("new boundary calls = attach %d end %d, want 2/1", attachCalls, endCalls)
	}
}

func TestHookAttachKeepsCurrentThatOwnsAHandling(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.client.currentView = []byte(`{"schema":"memory.agent.view","version":8,` +
		`"view":"view:test","current":{"facts":{"handle":"r7:subject:test","reply_to":"r7:subject:test","reply_required":false,"reply_observation_pending":false},` +
		`"semantic":{"kind":"review.request","payload":"review"}},` +
		`"outstanding":{"open_total":1,"related_total":0,"related_projected":0,"truncated":false},` +
		`"allowed_intents":[]}`)
	fixture.attach(t)
	var view bytes.Buffer
	if exit := fixture.app(strings.NewReader(""), &view).
		run(context.Background(), []string{"agent", "current", "--json"}); exit != 0 {
		t.Fatalf("subject current = exit %d output %q", exit, view.String())
	}
	fixture.attach(t)
	fixture.client.mu.Lock()
	attachCalls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if attachCalls != 2 {
		t.Fatalf("Attach calls after claimed Current = %d, want 2", attachCalls)
	}
}

func TestHookAttachEndFailurePreservesPriorJournal(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	before := loadJournalForTest(t, fixture.nodeState)
	beforeDigest := before.fileDigest
	before.clear()
	fixture.client.endFailures = 1
	var output bytes.Buffer
	exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x23)), &output).
		run(context.Background(), []string{"hook", "attach", "--json"})
	if exit != codeMemorydUnavailable.exitStatus() {
		t.Fatalf("failed predecessor end = exit %d output %q", exit, output.String())
	}
	after := loadJournalForTest(t, fixture.nodeState)
	defer after.clear()
	fixture.client.mu.Lock()
	attachCalls, endCalls := fixture.client.attachCalls, fixture.client.endCalls
	fixture.client.mu.Unlock()
	if after.fileDigest != beforeDigest || attachCalls != 1 || endCalls != 1 {
		t.Fatalf("failed end changed journal/calls: digest_equal=%t attach=%d end=%d",
			after.fileDigest == beforeDigest, attachCalls, endCalls)
	}
}

func TestHookEndReleasesMatchingBoundaryAndIgnoresStaleBoundary(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	fixture.endBoundary(t, 0x22)
	if _, err := os.Lstat(filepath.Join(fixture.nodeState, journalDirectoryName, journalActiveName)); err != nil {
		t.Fatalf("stale hook end removed active journal: %v", err)
	}
	fixture.endBoundary(t, 0x21)
	store := newJournalStore(fixture.nodeState, bytes.NewReader(make([]byte, 64)))
	if exists, err := store.exists(); err != nil || exists {
		t.Fatalf("matching hook end journal exists = %t, %v", exists, err)
	}
	fixture.client.mu.Lock()
	endCalls := fixture.client.endCalls
	fixture.client.mu.Unlock()
	if endCalls != 1 {
		t.Fatalf("hook End calls = %d, want 1", endCalls)
	}
}

func TestCurrentPersistsOperationBeforeTransportAndReplaysIt(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attach(t)
	fixture.client.currentFailures = 1
	var first bytes.Buffer
	firstExit := fixture.app(strings.NewReader(""), &first).
		run(context.Background(), []string{"agent", "current", "--json"})
	if firstExit != codeMemorydUnavailable.exitStatus() {
		t.Fatalf("first current exit/output = %d / %s", firstExit, first.String())
	}
	fixture.attach(t)
	var second bytes.Buffer
	secondExit := fixture.app(strings.NewReader(""), &second).
		run(context.Background(), []string{"agent", "current", "--json"})
	fixture.client.mu.Lock()
	operations := append([]string(nil), fixture.client.currentOperations...)
	fixture.client.mu.Unlock()
	fixture.client.mu.Lock()
	attachCalls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if secondExit != 0 || attachCalls != 2 || len(operations) != 2 || operations[0] == "" ||
		operations[0] != operations[1] {
		t.Fatalf("current replay = exit %d operations %#v output %q", secondExit, operations, second.String())
	}
}

func TestHookAttachSerializesAndRevalidatesConcurrentSameBoundary(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.client.attachBlock = make(chan struct{})
	const callers = 12
	results := make(chan int, callers)
	for range callers {
		go func() {
			exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x21)), io.Discard).
				run(context.Background(), []string{"hook", "attach", "--json"})
			results <- exit
		}()
	}
	for {
		fixture.client.mu.Lock()
		calls := fixture.client.attachCalls
		fixture.client.mu.Unlock()
		if calls == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(fixture.client.attachBlock)
	for range callers {
		if exit := <-results; exit != 0 {
			t.Fatalf("concurrent attach exit = %d", exit)
		}
	}
	fixture.client.mu.Lock()
	calls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if calls != callers {
		t.Fatalf("Attach calls = %d, want %d", calls, callers)
	}
}

func TestHookAttachRenewsOnlyExpiredActiveJournal(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.attachBoundary(t, 0x24)
	fixture.now = time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture.client.attachExpiresAt = time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture.attach(t)
	fixture.client.mu.Lock()
	calls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if calls != 2 {
		t.Fatalf("Attach calls after expiry = %d, want 2", calls)
	}

	var output bytes.Buffer
	exit := fixture.app(strings.NewReader(""), &output).
		run(context.Background(), []string{"agent", "current", "--json"})
	if exit != 0 {
		t.Fatalf("renewed current = exit %d output %s", exit, output.String())
	}
}

func TestHookAttachRejectsExpiredAuthorityOutcomeBeforeJournalCommit(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.now = time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture.client.attachExpiresAt = fixture.now.Add(-time.Second)
	var output bytes.Buffer
	exit := fixture.app(bytes.NewReader(testBoundaryEnvelope(t, 0x26)), &output).
		run(context.Background(), []string{"hook", "attach", "--json"})
	if exit != codeContextStale.exitStatus() ||
		!strings.Contains(output.String(), string(codeContextStale)) {
		t.Fatalf("expired authority outcome = exit %d output %q", exit, output.String())
	}
	store := newJournalStore(fixture.nodeState, bytes.NewReader(make([]byte, 64)))
	if exists, err := store.exists(); err != nil || exists {
		t.Fatalf("expired authority outcome journal exists = %t, %v", exists, err)
	}
	fixture.client.mu.Lock()
	attachCalls := fixture.client.attachCalls
	fixture.client.mu.Unlock()
	if attachCalls != 1 {
		t.Fatalf("expired authority outcome Attach calls = %d, want 1", attachCalls)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != want || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("mode %s = %v / %v, want %04o", path, info, err, want)
	}
}
