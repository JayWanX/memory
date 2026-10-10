package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

func TestDaemonServesCompleteLocalLoopOverOwnerUnix(t *testing.T) {
	state, principal := provisionDaemonState(t)
	daemon, err := Open(context.Background(), state, principal)
	if err != nil {
		t.Fatal(err)
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- daemon.Serve(context.Background()) }()
	socket := filepath.Join(state, controlSocketName)
	waitForSocket(t, socket)
	client := unixHTTPClient(socket)
	exerciseLocalControlLoop(t, client)

	closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	if err := daemon.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveErrors:
		if err != nil {
			t.Fatalf("Serve() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not join Close")
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control socket survived Close: %v", err)
	}
	if reopened, err := Open(context.Background(), state, principal); err != nil {
		t.Fatalf("authority writer was not released: %v", err)
	} else if err := reopened.Close(closeContext); err != nil {
		t.Fatalf("close reopened daemon: %v", err)
	}
}

func TestAttachmentBeginResponseExactlyReplaysAcrossDaemonRestart(t *testing.T) {
	state, principal := provisionDaemonState(t)
	body := testAttachmentBeginBody("lost-attach-response-and-journal")
	var first []byte
	for cycle := 0; cycle < 2; cycle++ {
		runtime, err := Open(context.Background(), state, principal)
		if err != nil {
			t.Fatal(err)
		}
		serveErrors := make(chan error, 1)
		go func() { serveErrors <- runtime.Serve(context.Background()) }()
		socket := filepath.Join(state, controlSocketName)
		waitForSocket(t, socket)
		response := controlRequest(t, unixHTTPClient(socket), http.MethodPost,
			routeAttachments, body, nil, http.StatusOK)
		if cycle == 0 {
			first = append([]byte(nil), response...)
		} else if !bytes.Equal(response, first) {
			t.Fatalf("attachment begin replay changed response:\nfirst  %s\nreplay %s",
				first, response)
		}
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := runtime.Close(closeContext); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		if err := <-serveErrors; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDaemonRecoversOnlyAStaleOwnerSocketAfterTakingWriter(t *testing.T) {
	state, principal := provisionDaemonState(t)
	runtime, err := Open(context.Background(), state, principal)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(state, controlSocketName)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(socket, ownerSocketMode); err != nil {
		_ = stale.Close()
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- runtime.Serve(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case serveErr := <-serveErrors:
			t.Fatalf("daemon serve exited before replacing stale socket: %v", serveErr)
		default:
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("daemon did not accept connections after replacing stale socket: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	client := unixHTTPClient(socket)
	status := controlRequest(t, client, http.MethodGet, routeStatus, nil, nil, http.StatusOK)
	if string(status) != `{"schema":"memory.agency.status","status":"ready","version":1}` {
		t.Fatalf("recovered daemon status = %s", status)
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErrors; err != nil {
		t.Fatal(err)
	}
}

func exerciseLocalControlLoop(t *testing.T, client *http.Client) {
	t.Helper()
	status := controlRequest(t, client, http.MethodGet, routeStatus, nil, nil, http.StatusOK)
	if got, want := string(status), `{"schema":"memory.agency.status","status":"ready","version":1}`; got != want {
		t.Fatalf("status = %s, want %s", got, want)
	}
	authorityHeaders := attachAndReadCurrent(t, client)
	submitAndReplayRoot(t, client, authorityHeaders)
	captureAndClaimHandling(t, client, authorityHeaders)
}

func attachAndReadCurrent(t *testing.T, client *http.Client) http.Header {
	t.Helper()
	attached := controlRequest(t, client, http.MethodPost, routeAttachments,
		testAttachmentBeginBody("daemon-loop-initial"), nil,
		http.StatusOK)
	var attachmentResponse attachmentWire
	decodeTestWire(t, attached, &attachmentResponse)
	if attachmentResponse.Schema != attachmentSchema || attachmentResponse.Version != controlVersion {
		t.Fatalf("attachment schema = %#v", attachmentResponse)
	}
	authorityHeaders := make(http.Header)
	authorityHeaders.Set(headerAttachment, attachmentResponse.Attachment)
	authorityHeaders.Set(headerCredential, attachmentResponse.Credential)
	authorityHeaders.Set(headerCurrentOperation, "operation:current-1")
	view := controlRequest(t, client, http.MethodPost, routeCurrent, []byte(`{}`), authorityHeaders,
		http.StatusOK)
	if err := agency.ValidateAgentViewProjectionCanonicalJSON(view); err != nil {
		t.Fatalf("current View = %v\n%s", err, view)
	}
	return authorityHeaders
}

func submitAndReplayRoot(t *testing.T, client *http.Client, authorityHeaders http.Header) {
	t.Helper()
	kind, err := agency.NewSemanticLabel("work.request")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := agency.NewSemanticPayload("inspect the bounded change")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := agency.NewAgentIntent(agency.IntentSpec{Kind: kind, Payload: payload,
		Consequence: agency.ConsequenceCreateHandlings, Successors: []agency.TargetRef{agency.SelfTarget()}})
	if err != nil {
		t.Fatal(err)
	}
	submitBody, err := json.Marshal(submitWire{Intent: intent.CanonicalJSON()})
	if err != nil {
		t.Fatal(err)
	}
	submitHeaders := authorityHeaders.Clone()
	submitHeaders.Set(headerOperation, "operation:submit-1")
	accepted := controlRequest(t, client, http.MethodPost, routeSubmit, submitBody, submitHeaders,
		http.StatusOK)
	receipt, err := agency.ParseAgentReceiptProjectionCanonicalJSON(accepted)
	if err != nil || receipt.Outcome() != agency.ReceiptOutcomeAccepted || receipt.Replayed() {
		t.Fatalf("first Receipt = (%v, %v)\n%s", receipt.Outcome(), err, accepted)
	}
	replayed := controlRequest(t, client, http.MethodPost, routeSubmit, submitBody, submitHeaders,
		http.StatusOK)
	replayReceipt, err := agency.ParseAgentReceiptProjectionCanonicalJSON(replayed)
	if err != nil || replayReceipt.Outcome() != agency.ReceiptOutcomeAccepted || !replayReceipt.Replayed() {
		t.Fatalf("replayed Receipt = (%v, replay=%t, %v)\n%s",
			replayReceipt.Outcome(), replayReceipt.Replayed(), err, replayed)
	}
}

func captureAndClaimHandling(t *testing.T, client *http.Client, authorityHeaders http.Header) {
	t.Helper()
	artifactContent := []byte("immutable review evidence")
	artifactBody, err := json.Marshal(artifactRequestWire{
		Content: base64.RawStdEncoding.EncodeToString(artifactContent),
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactRaw := controlRequest(t, client, http.MethodPost, routeArtifacts, artifactBody, nil,
		http.StatusOK)
	var artifact artifactResponseWire
	decodeTestWire(t, artifactRaw, &artifact)
	if artifact.Schema != artifactSchema || artifact.Version != controlVersion ||
		artifact.Digest != agency.Sum(artifactContent).String() ||
		artifact.ByteSize != int64(len(artifactContent)) || artifact.Handle == "" {
		t.Fatalf("Artifact capture = %#v", artifact)
	}

	secondHeaders := authorityHeaders.Clone()
	secondHeaders.Set(headerCurrentOperation, "operation:current-2")
	claimed := controlRequest(t, client, http.MethodPost, routeCurrent, []byte(`{}`), secondHeaders,
		http.StatusOK)
	if err := agency.ValidateAgentViewProjectionCanonicalJSON(claimed); err != nil ||
		!bytes.Contains(claimed, []byte(`"current":{`)) {
		t.Fatalf("claimed View = %v\n%s", err, claimed)
	}

	endHeaders := make(http.Header)
	endHeaders.Set(headerAttachment, authorityHeaders.Get(headerAttachment))
	endHeaders.Set(headerCredential, authorityHeaders.Get(headerCredential))
	endedRaw := controlRequest(t, client, http.MethodPost, routeAttachmentEnd, []byte(`{}`),
		endHeaders, http.StatusOK)
	var ended attachmentEndWire
	decodeTestWire(t, endedRaw, &ended)
	if ended.Schema != attachmentEndSchema || ended.Version != controlVersion ||
		ended.Status != "ended" || ended.Replayed || !ended.ReleasedClaim {
		t.Fatalf("first attachment end = %#v", ended)
	}
	replayedRaw := controlRequest(t, client, http.MethodPost, routeAttachmentEnd, []byte(`{}`),
		endHeaders, http.StatusOK)
	var replayed attachmentEndWire
	decodeTestWire(t, replayedRaw, &replayed)
	if !replayed.Replayed || !replayed.ReleasedClaim {
		t.Fatalf("replayed attachment end = %#v", replayed)
	}

	replacement := controlRequest(t, client, http.MethodPost, routeAttachments,
		testAttachmentBeginBody("daemon-loop-replacement"), nil,
		http.StatusOK)
	var replacementAttachment attachmentWire
	decodeTestWire(t, replacement, &replacementAttachment)
	reclaimHeaders := make(http.Header)
	reclaimHeaders.Set(headerAttachment, replacementAttachment.Attachment)
	reclaimHeaders.Set(headerCredential, replacementAttachment.Credential)
	reclaimHeaders.Set(headerCurrentOperation, "operation:current-3")
	reclaimed := controlRequest(t, client, http.MethodPost, routeCurrent, []byte(`{}`), reclaimHeaders,
		http.StatusOK)
	if err := agency.ValidateAgentViewProjectionCanonicalJSON(reclaimed); err != nil ||
		!bytes.Contains(reclaimed, []byte(`"current":{`)) {
		t.Fatalf("reclaimed View = %v\n%s", err, reclaimed)
	}
}

func testAttachmentBeginBody(label string) []byte {
	return []byte(`{"boundary_digest":"` + agency.Sum([]byte(label)).String() + `"}`)
}

func TestDaemonControlRejectsNonClosedInputAndForeignMetadata(t *testing.T) {
	state, principal := provisionDaemonState(t)
	daemon, err := Open(context.Background(), state, principal)
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- daemon.Serve(serveContext) }()
	socket := filepath.Join(state, controlSocketName)
	waitForSocket(t, socket)
	client := unixHTTPClient(socket)

	for name, request := range map[string]func() []byte{
		"unknown field": func() []byte {
			return controlRequest(t, client, http.MethodPost, routeAttachments,
				[]byte(`{"extra":true}`), nil, http.StatusBadRequest)
		},
		"noncanonical whitespace": func() []byte {
			return controlRequest(t, client, http.MethodPost, routeAttachments,
				[]byte(`{ }`), nil, http.StatusBadRequest)
		},
		"unknown route": func() []byte {
			return controlRequest(t, client, http.MethodPost, "/v1/agency/unknown",
				[]byte(`{}`), nil, http.StatusBadRequest)
		},
		"R5 header": func() []byte {
			header := make(http.Header)
			header.Set("Authorization", "forbidden")
			return controlRequest(t, client, http.MethodPost, routeAttachments,
				[]byte(`{}`), header, http.StatusBadRequest)
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := request()
			var response controlError
			decodeTestWire(t, raw, &response)
			if response.Status != "error" || !response.Code.valid() || response.OperationID != nil ||
				response.Replayed {
				t.Fatalf("control error = %#v", response)
			}
		})
	}

	cancelServe()
	select {
	case err := <-serveErrors:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Serve did not join")
	}
}

func TestOpenRequiresProvisionedPrivateStateAndOneWriter(t *testing.T) {
	principal, err := agency.NewAgentPrincipalID("principal:strict")
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(canonicalTempDir(t), "missing")
	if opened, err := Open(context.Background(), missing, principal); err == nil || opened != nil {
		t.Fatalf("Open(missing state) = (%v, %v)", opened, err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created missing state: %v", err)
	}

	state := canonicalTempDir(t)
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(context.Background(), state, principal); err == nil || opened != nil {
		t.Fatalf("Open(missing CAS) = (%v, %v)", opened, err)
	}
	if _, err := os.Lstat(filepath.Join(state, authorityFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created authority: %v", err)
	}

	state, enrolled := provisionDaemonState(t)
	first, err := Open(context.Background(), state, enrolled)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), state, enrolled)
	if second != nil || !errors.Is(err, authority.ErrWriterActive) {
		t.Fatalf("second Open = (%v, %v)", second, err)
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := first.Close(closeContext); err != nil {
		t.Fatal(err)
	}
}

func provisionDaemonState(t *testing.T) (string, agency.AgentPrincipalID) {
	t.Helper()
	state := canonicalTempDir(t)
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	objectsRoot := filepath.Join(state, "objects", "sha256")
	if err := os.MkdirAll(objectsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(state, "objects"), objectsRoot} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := artifact.Open(objectsRoot); err != nil {
		t.Fatal(err)
	}
	principal, err := agency.NewAgentPrincipalID("principal:daemon-test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := authority.Open(context.Background(), filepath.Join(state, authorityFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollPrincipal(context.Background(), principal); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return state, principal
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(base, "mnd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func unixHTTPClient(socket string) *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
	return &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func controlRequest(t *testing.T, client *http.Client, method, route string, body []byte,
	header http.Header, wantStatus int,
) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(context.Background(), method,
		"http://memoryd"+route, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, values := range header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxArtifactRequestBody+1))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus || response.Header.Get("Content-Type") != "application/json" ||
		len(raw) < 3 || raw[len(raw)-1] != '\n' {
		t.Fatalf("response = status %d, content-type %q, body %q", response.StatusCode,
			response.Header.Get("Content-Type"), raw)
	}
	return append([]byte(nil), raw[:len(raw)-1]...)
}

func decodeTestWire(t *testing.T, raw []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
	if rebuilt, err := json.Marshal(target); err != nil || !bytes.Equal(rebuilt, raw) {
		t.Fatalf("wire is not closed: %v\nraw=%s\nrebuilt=%s", err, raw, rebuilt)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("unix", path, 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		lastErr = err
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("control socket %s did not accept connections: %v", path, lastErr)
}

func TestControlWireContainsNoCaseSpecificKind(t *testing.T) {
	for _, value := range []string{routeAttachments, routeAttachmentEnd, routeCurrent, routeSubmit, routeArtifacts,
		routeArtifactRead, routeStatus, attachmentSchema, artifactSchema, statusSchema} {
		for _, forbidden := range []string{"review", "teamwork", "contract-net", "blackboard", "memory.wiki"} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("control value %q contains case kind %q", value, forbidden)
			}
		}
	}
}
