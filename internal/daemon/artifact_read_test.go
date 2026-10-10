package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestDaemonReadsOnlyArtifactOfferedByExactCurrentView(t *testing.T) {
	environment := startArtifactReadDaemon(t)
	client := environment.client
	attached := controlRequest(t, client, http.MethodPost, routeAttachments,
		testAttachmentBeginBody("artifact-read"), nil,
		http.StatusOK)
	var attachmentResponse attachmentWire
	decodeTestWire(t, attached, &attachmentResponse)
	headers := make(http.Header)
	headers.Set(headerAttachment, attachmentResponse.Attachment)
	headers.Set(headerCredential, attachmentResponse.Credential)
	headers.Set(headerCurrentOperation, "operation:artifact-read-root")
	controlRequest(t, client, http.MethodPost, routeCurrent, []byte(`{}`), headers, http.StatusOK)

	content := []byte("bytes offered by one exact frozen View")
	captureBody, _ := json.Marshal(artifactRequestWire{
		Content: base64.RawStdEncoding.EncodeToString(content),
	})
	capturedRaw := controlRequest(t, client, http.MethodPost, routeArtifacts, captureBody, nil,
		http.StatusOK)
	var captured artifactResponseWire
	decodeTestWire(t, capturedRaw, &captured)

	kind, _ := agency.NewSemanticLabel("generic.request")
	payload, _ := agency.NewSemanticPayload("continue with the referenced bytes")
	candidate, _ := agency.NewArtifactCandidate(mustOpaqueHandle(t, captured.Handle))
	intent, err := agency.NewAgentIntent(agency.IntentSpec{Kind: kind, Payload: payload,
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget()},
		Artifacts:   []agency.ArtifactInput{candidate}})
	if err != nil {
		t.Fatal(err)
	}
	submitBody, _ := json.Marshal(submitWire{Candidates: []candidateWire{{
		Digest: captured.Digest, Handle: captured.Handle,
	}}, Intent: intent.CanonicalJSON()})
	submitHeaders := headers.Clone()
	submitHeaders.Set(headerOperation, "operation:artifact-read-submit")
	controlRequest(t, client, http.MethodPost, routeSubmit, submitBody, submitHeaders, http.StatusOK)

	readHeaders := headers.Clone()
	readHeaders.Set(headerCurrentOperation, "operation:artifact-read-current")
	viewRaw := controlRequest(t, client, http.MethodPost, routeCurrent, []byte(`{}`), readHeaders,
		http.StatusOK)
	offered := currentArtifactHandle(t, viewRaw)

	readBody, _ := json.Marshal(artifactReadRequestWire{Handle: offered})
	response := rawControlRequest(t, client, routeArtifactRead, readBody, readHeaders)
	assertArtifactContentResponse(t, response, captured.Digest, content)
	assertArtifactContentResponse(t,
		rawControlRequest(t, client, routeArtifactRead, readBody, readHeaders),
		captured.Digest, content)

	unofferedBody, _ := json.Marshal(artifactReadRequestWire{Handle: captured.Handle})
	rejected := rawControlRequest(t, client, routeArtifactRead, unofferedBody, readHeaders)
	if rejected.StatusCode != http.StatusConflict ||
		rejected.Header.Get("Content-Type") != "application/json" ||
		!bytes.Contains(rejected.Body, []byte(`"code":"action_not_allowed"`)) {
		t.Fatalf("unoffered Artifact response = status %d headers %#v body %q",
			rejected.StatusCode, rejected.Header, rejected.Body)
	}
	hexDigest := strings.TrimPrefix(captured.Digest, "sha256:")
	object := filepath.Join(environment.state, "objects", "sha256", hexDigest[:2], hexDigest)
	if err := os.WriteFile(object, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := rawControlRequest(t, client, routeArtifactRead, readBody, readHeaders)
	if corrupt.StatusCode != http.StatusBadRequest ||
		!bytes.Contains(corrupt.Body, []byte(`"code":"artifact_invalid"`)) {
		t.Fatalf("corrupt Artifact response = status %d body %q",
			corrupt.StatusCode, corrupt.Body)
	}
}

type artifactReadDaemon struct {
	client *http.Client
	state  string
}

func startArtifactReadDaemon(t *testing.T) artifactReadDaemon {
	t.Helper()
	state, principal := provisionDaemonState(t)
	runtime, err := Open(context.Background(), state, principal)
	if err != nil {
		t.Fatal(err)
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- runtime.Serve(context.Background()) }()
	waitForSocket(t, filepath.Join(state, controlSocketName))
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtime.Close(closeContext); err != nil {
			t.Error(err)
		}
		if err := <-serveErrors; err != nil {
			t.Error(err)
		}
	})
	return artifactReadDaemon{client: unixHTTPClient(filepath.Join(state, controlSocketName)),
		state: state}
}

func assertArtifactContentResponse(t *testing.T, response rawControlResponse,
	digest string, content []byte,
) {
	t.Helper()
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "application/octet-stream" ||
		response.Header.Get(headerArtifactDigest) != digest ||
		!bytes.Equal(response.Body, content) {
		t.Fatalf("Artifact response = status %d headers %#v body %q",
			response.StatusCode, response.Header, response.Body)
	}
}

func mustOpaqueHandle(t *testing.T, value string) agency.OpaqueHandle {
	t.Helper()
	handle, err := agency.NewOpaqueHandle(value)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func currentArtifactHandle(t *testing.T, raw []byte) string {
	t.Helper()
	var view struct {
		Current *struct {
			Facts struct {
				Artifacts []struct {
					Handle string `json:"handle"`
				} `json:"artifacts"`
			} `json:"facts"`
		} `json:"current"`
	}
	if err := json.Unmarshal(raw, &view); err != nil || view.Current == nil ||
		len(view.Current.Facts.Artifacts) != 1 {
		t.Fatalf("current View Artifact projection = %v\n%s", err, raw)
	}
	return view.Current.Facts.Artifacts[0].Handle
}

type rawControlResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func rawControlRequest(t *testing.T, client *http.Client, route string, body []byte,
	header http.Header,
) rawControlResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://memoryd"+route, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
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
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(maxArtifactRequestBody)+1))
	if err != nil {
		t.Fatal(err)
	}
	return rawControlResponse{StatusCode: response.StatusCode,
		Header: response.Header.Clone(), Body: append([]byte(nil), raw...)}
}
