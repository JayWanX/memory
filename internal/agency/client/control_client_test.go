package agencyclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestControlClientRoundTripsFrozenAgencyWire(t *testing.T) {
	credential := bytes.Repeat([]byte{0x2a}, attachmentSecretBytes)
	expiry := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
	content := []byte("captured bytes")
	requests := make(chan capturedControlRequest, 5)
	handler := roundTripControlHandler(requests, credential, expiry, content)
	nodeState, stop := serveControlSocket(t, handler)
	defer stop()
	client := mustControlClient(t, nodeState)
	boundary := agency.Sum([]byte("control-client-boundary"))

	attached, apiErr := client.Attach(context.Background(), boundary)
	requireAttachment(t, attached, apiErr, credential, expiry)
	current := "current:test"
	view, apiErr := client.Current(context.Background(), attached, current)
	requireProjection(t, "Current", view, apiErr,
		`{"schema":"memory.agent.view","version":8,"view":"view:test","outstanding":{"open_total":0,"related_total":0,"related_projected":0,"truncated":false},"allowed_intents":[]}`)
	intent := controlTestIntent(t)
	receipt, apiErr := client.Submit(context.Background(), attached, current,
		"admit:test", intent, nil)
	requireProjectionContains(t, "Submit", receipt, apiErr, `"outcome":"accepted"`)
	capture, apiErr := client.Capture(context.Background(), content)
	requireCapture(t, capture, apiErr, content)
	read, apiErr := client.ReadArtifact(context.Background(), attached, current, "artifact:test")
	if apiErr != nil || !bytes.Equal(read, content) {
		t.Fatalf("ReadArtifact() = (%q, %#v)", read, apiErr)
	}

	assertAttachRequest(t, <-requests, boundary)
	assertCurrentRequest(t, <-requests, attached, credential, current)
	assertSubmitRequest(t, <-requests, intent)
	assertArtifactRequest(t, <-requests, content)
	assertArtifactReadRequest(t, <-requests, attached, credential, current)
}

func roundTripControlHandler(requests chan<- capturedControlRequest, credential []byte,
	expiry time.Time, content []byte,
) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- capturedControlRequest{Method: request.Method, Path: request.URL.Path,
			Header: request.Header.Clone(), Body: body}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case routeAttachments:
			fmt.Fprintf(writer, `{"attachment":"attachment:test","credential":"%s","expires_at":"%s","schema":"%s","version":1}`+"\n",
				base64.RawURLEncoding.EncodeToString(credential), expiry.Format(timeWireLayout), attachmentSchema)
		case routeCurrent:
			_, _ = io.WriteString(writer, `{"schema":"memory.agent.view","version":8,"view":"view:test","outstanding":{"open_total":0,"related_total":0,"related_projected":0,"truncated":false},"allowed_intents":[]}`+"\n")
		case routeSubmit:
			_, _ = io.WriteString(writer, `{"schema":"memory.agent.receipt","version":1,"outcome":"accepted","replayed":false}`+"\n")
		case routeArtifacts:
			fmt.Fprintf(writer, `{"byte_size":%d,"digest":"%s","handle":"artifact:test","schema":"%s","version":1}`+"\n",
				len(content), agency.Sum(content).String(), artifactSchema)
		case routeArtifactRead:
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set(headerArtifactDigest, agency.Sum(content).String())
			writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
			_, _ = writer.Write(content)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})
}

func mustControlClient(t *testing.T, nodeState string) *controlClient {
	t.Helper()
	client, err := newControlClient(nodeState)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func requireAttachment(t *testing.T, attached attachment, apiErr *controlError,
	credential []byte, expiry time.Time,
) {
	t.Helper()
	if apiErr != nil || attached.ID != "attachment:test" ||
		!bytes.Equal(attached.Credential, credential) || !attached.ExpiresAt.Equal(expiry) {
		t.Fatalf("Attach() = (%#v, %#v)", attached, apiErr)
	}
}

func requireProjection(t *testing.T, name string, raw []byte, apiErr *controlError, want string) {
	t.Helper()
	if apiErr != nil || string(raw) != want {
		t.Fatalf("%s() = (%s, %#v)", name, raw, apiErr)
	}
}

func requireProjectionContains(t *testing.T, name string, raw []byte,
	apiErr *controlError, want string,
) {
	t.Helper()
	if apiErr != nil || !bytes.Contains(raw, []byte(want)) {
		t.Fatalf("%s() = (%s, %#v)", name, raw, apiErr)
	}
}

func requireCapture(t *testing.T, capture artifactCapture, apiErr *controlError, content []byte) {
	t.Helper()
	if apiErr != nil || capture.Handle != "artifact:test" ||
		capture.Digest != agency.Sum(content).String() || capture.ByteSize != int64(len(content)) {
		t.Fatalf("Capture() = (%#v, %#v)", capture, apiErr)
	}
}

func assertAttachRequest(t *testing.T, request capturedControlRequest,
	boundary agency.Digest,
) {
	t.Helper()
	if request.Method != http.MethodPost || request.Path != routeAttachments ||
		string(request.Body) != `{"boundary_digest":"`+boundary.String()+`"}` {
		t.Fatalf("attachment request = %#v", request)
	}
}

func assertCurrentRequest(t *testing.T, request capturedControlRequest, attached attachment,
	credential []byte, current string,
) {
	t.Helper()
	if request.Header.Get(headerAttachment) != attached.ID ||
		request.Header.Get(headerCredential) != base64.RawURLEncoding.EncodeToString(credential) ||
		request.Header.Get(headerCurrentOperation) != current || string(request.Body) != `{}` {
		t.Fatalf("Current request = %#v", request)
	}
}

func assertSubmitRequest(t *testing.T, request capturedControlRequest, intent []byte) {
	t.Helper()
	if request.Header.Get(headerOperation) != "admit:test" ||
		string(request.Body) != `{"intent":`+string(intent)+`}` {
		t.Fatalf("Submit request = %#v", request)
	}
}

func assertArtifactRequest(t *testing.T, request capturedControlRequest, content []byte) {
	t.Helper()
	encodedContent := base64.RawStdEncoding.EncodeToString(content)
	if request.Path != routeArtifacts ||
		string(request.Body) != `{"content_base64":"`+encodedContent+`"}` {
		t.Fatalf("Artifact request = %#v", request)
	}
}

func assertArtifactReadRequest(t *testing.T, request capturedControlRequest,
	attached attachment, credential []byte, current string,
) {
	t.Helper()
	if request.Method != http.MethodPost || request.Path != routeArtifactRead ||
		request.Header.Get(headerAttachment) != attached.ID ||
		request.Header.Get(headerCredential) != base64.RawURLEncoding.EncodeToString(credential) ||
		request.Header.Get(headerCurrentOperation) != current ||
		string(request.Body) != `{"handle":"artifact:test"}` {
		t.Fatalf("Artifact read request = %#v", request)
	}
}

func TestControlClientRejectsInvalidProjectionAndRemoteErrorEnvelope(t *testing.T) {
	credential := bytes.Repeat([]byte{0x31}, attachmentSecretBytes)
	authority := attachment{ID: "attachment:test", Credential: credential,
		ExpiresAt: time.Now().Add(time.Hour)}
	for name, response := range map[string]struct {
		Status int
		Body   string
	}{
		"duplicate projection": {Status: http.StatusOK,
			Body: `{"schema":"memory.agent.view","schema":"memory.agent.view","version":8}`},
		"wrong projection schema": {Status: http.StatusOK,
			Body: `{"schema":"memory.agent.receipt","version":1}`},
		"status mismatch": {Status: http.StatusUnauthorized,
			Body: `{"code":"memoryd_unavailable","message":"not ready","operation_id":null,"replayed":false,"retryable":true,"schema_version":1,"status":"error"}`},
	} {
		t.Run(name, func(t *testing.T) {
			nodeState, stop := serveControlSocket(t, http.HandlerFunc(func(writer http.ResponseWriter,
				_ *http.Request,
			) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(response.Status)
				_, _ = io.WriteString(writer, response.Body+"\n")
			}))
			defer stop()
			client, err := newControlClient(nodeState)
			if err != nil {
				t.Fatal(err)
			}
			if _, apiErr := client.Current(context.Background(), authority,
				"current:test"); apiErr == nil || apiErr.Code != codeInternal {
				t.Fatalf("Current() error = %#v", apiErr)
			}
		})
	}
}

func TestControlClientRejectsUnverifiedArtifactResponses(t *testing.T) {
	content := []byte("exact bytes")
	validDigest := agency.Sum(content).String()
	for name, mutate := range map[string]func(*http.Response){
		"missing digest": func(response *http.Response) {
			response.Header.Del(headerArtifactDigest)
		},
		"duplicate digest": func(response *http.Response) {
			response.Header.Add(headerArtifactDigest, validDigest)
		},
		"mismatched digest": func(response *http.Response) {
			response.Header.Set(headerArtifactDigest, agency.Sum([]byte("other")).String())
		},
		"mismatched length": func(response *http.Response) {
			response.ContentLength++
		},
		"wrong media type": func(response *http.Response) {
			response.Header.Set("Content-Type", "application/json")
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": {"application/octet-stream"},
					headerArtifactDigest: {validDigest}},
				Body: io.NopCloser(bytes.NewReader(content)), ContentLength: int64(len(content))}
			mutate(response)
			if raw, apiErr := readArtifactResponse(response); raw != nil ||
				apiErr == nil || apiErr.Code != codeInternal {
				t.Fatalf("readArtifactResponse() = (%q, %#v)", raw, apiErr)
			}
		})
	}
}

func TestControlClientKeepsBoundsAndOwnerChecksLocal(t *testing.T) {
	nodeState := t.TempDir()
	if err := os.Chmod(nodeState, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newControlClient(nodeState); !errors.Is(err, errUnsafeClientState) {
		t.Fatalf("unsafe NewControlClient() error = %v", err)
	}

	safeState := t.TempDir()
	if err := os.Chmod(safeState, ownerDirectoryMode); err != nil {
		t.Fatal(err)
	}
	client, err := newControlClient(safeState)
	if err != nil {
		t.Fatal(err)
	}
	if _, apiErr := client.Capture(context.Background(), make([]byte, maxArtifactInputBytes+1)); apiErr == nil || apiErr.Code != codeArtifactTooLarge {
		t.Fatalf("oversized Capture() error = %#v", apiErr)
	}
	if _, apiErr := client.Current(context.Background(), attachment{}, "current:test"); apiErr == nil || apiErr.Code != codeAuthenticationFailed ||
		strings.Contains(apiErr.Message, "current:test") {
		t.Fatalf("authority-free Current() error = %#v", apiErr)
	}
}

type capturedControlRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

func controlTestIntent(t *testing.T) []byte {
	t.Helper()
	kind, _ := agency.NewSemanticLabel("test.request")
	payload, _ := agency.NewSemanticPayload("payload")
	intent, err := agency.NewAgentIntent(agency.IntentSpec{Kind: kind, Payload: payload,
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget()}})
	if err != nil {
		t.Fatal(err)
	}
	return intent.CanonicalJSON()
}

func serveControlSocket(t *testing.T, handler http.Handler) (string, func()) {
	t.Helper()
	nodeState, err := os.MkdirTemp("/tmp", "memory-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(nodeState) })
	if err := os.Chmod(nodeState, ownerDirectoryMode); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nodeState, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, ownerSocketMode); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	return nodeState, func() {
		_ = server.Close()
		_ = listener.Close()
		<-done
	}
}
