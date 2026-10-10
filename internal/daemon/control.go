package daemon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
)

const (
	routeAttachments   = "/v1/agency/attachments"
	routeAttachmentEnd = "/v1/agency/attachments/end"
	routeCurrent       = "/v1/agency/current"
	routeSubmit        = "/v1/agency/submit"
	routeArtifacts     = "/v1/agency/artifacts"
	routeArtifactRead  = "/v1/agency/artifacts/read"
	routeStatus        = "/v1/agency/status"

	headerAttachment       = "Memory-Agency-Attachment"
	headerCredential       = "Memory-Agency-Credential"
	headerCurrentOperation = "Memory-Agency-Current-Operation"
	headerOperation        = "Memory-Agency-Operation"
	headerArtifactDigest   = "Memory-Artifact-Digest"

	attachmentSchema    = "memory.agency.attachment"
	attachmentEndSchema = "memory.agency.attachment-end"
	artifactSchema      = "memory.agency.artifact"
	statusSchema        = "memory.agency.status"
	controlVersion      = 1
	timeWireLayout      = "2006-01-02T15:04:05.000000000Z"

	attachmentCredentialBytes = 32
	maxPrivateResponse        = 4 << 10
	maxControlDiagnostic      = 512
	maxArtifactRequestBody    = ((artifact.MaxObjectBytes + 2) / 3 * 4) + 256
)

var legacyHeaders = [...]string{
	"Authorization", "Memory-Operation-Key", "Memory-Claim-Context", "Memory-Run-Attachment",
}

type controlServer struct {
	service *localService
	handler http.Handler
}

func newControlServer(service *localService) (*controlServer, error) {
	if service == nil {
		return nil, errors.New("daemon control: local service is required")
	}
	server := &controlServer{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc(routeAttachments, server.handleAttach)
	mux.HandleFunc(routeAttachmentEnd, server.handleAttachmentEnd)
	mux.HandleFunc(routeCurrent, server.handleCurrent)
	mux.HandleFunc(routeSubmit, server.handleSubmit)
	mux.HandleFunc(routeArtifacts, server.handleArtifact)
	mux.HandleFunc(routeArtifactRead, server.handleArtifactRead)
	mux.HandleFunc(routeStatus, server.handleStatus)
	server.handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request == nil || !controlRoute(request.URL.Path) {
			writeControlError(writer, newControlError(codeInvalidArgument,
				"local agency route does not exist"))
			return
		}
		mux.ServeHTTP(writer, request)
	})
	return server, nil
}

func controlRoute(path string) bool {
	return path == routeAttachments || path == routeAttachmentEnd || path == routeCurrent || path == routeSubmit ||
		path == routeArtifacts || path == routeArtifactRead || path == routeStatus
}

func (server *controlServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if server == nil || server.handler == nil {
		writeControlError(writer, newControlError(codeInternal, "local agency server is unavailable"))
		return
	}
	server.handler.ServeHTTP(writer, request)
}

func (server *controlServer) handleAttach(writer http.ResponseWriter, request *http.Request) {
	if err := prepareControlRequest(request, false, false, false, false); err != nil {
		writeControlError(writer, err)
		return
	}
	var input attachmentBeginWire
	if err := decodeClosedRequest(request, &input, maxPrivateResponse); err != nil {
		writeControlError(writer, err)
		return
	}
	boundary, err := agency.ParseDigest(input.BoundaryDigest)
	if err != nil || boundary.IsZero() {
		writeControlError(writer, newControlError(codeInvalidArgument,
			"Host boundary operation is invalid"))
		return
	}
	result, err := server.service.attach(request.Context(), boundary)
	if err != nil {
		writeControlError(writer, classifyServiceError(err))
		return
	}
	defer clear(result.credential)
	if result.id.IsZero() || len(result.credential) != attachmentCredentialBytes || result.expiresAt.IsZero() {
		writeControlError(writer, newControlError(codeInternal,
			"Agency service returned invalid attachment"))
		return
	}
	writeControlJSON(writer, http.StatusOK, attachmentWire{
		Attachment: result.id.String(),
		Credential: base64.RawURLEncoding.EncodeToString(result.credential),
		ExpiresAt:  result.expiresAt.UTC().Format(timeWireLayout),
		Schema:     attachmentSchema,
		Version:    controlVersion,
	})
}

func (server *controlServer) handleAttachmentEnd(writer http.ResponseWriter, request *http.Request) {
	if err := prepareControlRequest(request, false, true, false, false); err != nil {
		writeControlError(writer, err)
		return
	}
	proof, errValue := parseAttachmentProof(request.Header)
	if errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	var input struct{}
	if err := decodeClosedRequest(request, &input, maxPrivateResponse); err != nil {
		writeControlError(writer, err)
		return
	}
	result, err := server.service.endAttachment(request.Context(), proof)
	if err != nil {
		writeControlError(writer, classifyServiceError(err))
		return
	}
	writeControlJSON(writer, http.StatusOK, attachmentEndWire{ReleasedClaim: result.releasedClaim,
		Replayed: result.replayed, Schema: attachmentEndSchema, Status: "ended", Version: controlVersion})
}

func (server *controlServer) handleCurrent(writer http.ResponseWriter, request *http.Request) {
	if err := prepareControlRequest(request, false, true, true, false); err != nil {
		writeControlError(writer, err)
		return
	}
	proof, current, errValue := parseControlAuthority(request.Header)
	if errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	var input struct{}
	if err := decodeClosedRequest(request, &input, maxPrivateResponse); err != nil {
		writeControlError(writer, err)
		return
	}
	view, err := server.service.current(request.Context(), proof, current)
	if err != nil {
		writeControlError(writer, classifyServiceError(err))
		return
	}
	writeProjection(writer, view.CanonicalJSON(), agency.MaxAgentViewCanonicalBytes)
}

func (server *controlServer) handleSubmit(writer http.ResponseWriter, request *http.Request) {
	if err := prepareControlRequest(request, false, true, true, true); err != nil {
		writeControlError(writer, err)
		return
	}
	proof, current, errValue := parseControlAuthority(request.Header)
	if errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	operationValue, errValue := singleHeader(request.Header, headerOperation,
		codeInvalidArgument, "Agency operation is required exactly once")
	if errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	operation, err := agency.NewOperationKey(operationValue)
	if err != nil {
		writeControlError(writer, newControlError(codeInvalidArgument, "Agency operation is invalid"))
		return
	}
	var input submitWire
	if errValue := decodeClosedRequest(request, &input,
		int64(agency.MaxIntentCanonicalBytes+maxPrivateResponse)); errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	intent, err := agency.ParseAgentIntentJSON(input.Intent)
	if err != nil || len(input.Candidates) > agency.MaxArtifactInputs {
		writeControlError(writer, newControlError(codeInvalidArgument,
			"Intent or candidate binding is invalid"))
		return
	}
	bindings, err := parseCandidateBindings(input.Candidates)
	if err != nil {
		writeControlError(writer, newControlError(codeInvalidArgument,
			"Intent or candidate binding is invalid"))
		return
	}
	receipt, err := server.service.submit(request.Context(), proof, current, operation, intent, bindings)
	if err != nil {
		writeControlError(writer, classifyServiceError(err))
		return
	}
	writeProjection(writer, receipt.CanonicalJSON(), agency.MaxAgentReceiptCanonicalBytes)
}

func (server *controlServer) handleArtifact(writer http.ResponseWriter, request *http.Request) {
	if err := prepareControlRequest(request, false, false, false, false); err != nil {
		writeControlError(writer, err)
		return
	}
	var input artifactRequestWire
	if err := decodeClosedRequest(request, &input, maxArtifactRequestBody); err != nil {
		writeControlError(writer, err)
		return
	}
	content, errValue := decodeArtifactContent(input.Content)
	if errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	result, err := server.service.capture(request.Context(), content)
	clear(content)
	if err != nil {
		writeControlError(writer, classifyServiceError(err))
		return
	}
	writeControlJSON(writer, http.StatusOK, artifactResponseWire{
		ByteSize: result.byteSize,
		Digest:   result.digest.String(),
		Handle:   result.handle.String(),
		Schema:   artifactSchema,
		Version:  controlVersion,
	})
}

func (server *controlServer) handleArtifactRead(writer http.ResponseWriter, request *http.Request) {
	if err := prepareControlRequest(request, false, true, true, false); err != nil {
		writeControlError(writer, err)
		return
	}
	proof, current, errValue := parseControlAuthority(request.Header)
	if errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	var input artifactReadRequestWire
	if errValue := decodeClosedRequest(request, &input, maxPrivateResponse); errValue != nil {
		writeControlError(writer, errValue)
		return
	}
	handle, err := agency.NewOpaqueHandle(input.Handle)
	if err != nil {
		writeControlError(writer, newControlError(codeInvalidArgument,
			"Artifact handle is invalid"))
		return
	}
	content, digest, err := server.service.readArtifact(request.Context(), proof, current, handle)
	if err != nil {
		writeControlError(writer, classifyServiceError(err))
		return
	}
	defer clear(content)
	writeArtifactContent(writer, content, digest)
}

func (server *controlServer) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if err := prepareControlRequest(request, true, false, false, false); err != nil {
		writeControlError(writer, err)
		return
	}
	if err := server.service.available(request.Context()); err != nil {
		writeControlError(writer, classifyServiceError(err))
		return
	}
	writeControlJSON(writer, http.StatusOK, statusWire{
		Schema: statusSchema, Status: "ready", Version: controlVersion,
	})
}

func writeProjection(writer http.ResponseWriter, raw []byte, maximum int) {
	if len(raw) < 2 || len(raw) > maximum || raw[0] != '{' || raw[len(raw)-1] != '}' {
		writeControlError(writer, newControlError(codeInternal,
			"Agency service returned invalid canonical JSON"))
		return
	}
	setControlHeaders(writer)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(append(append([]byte(nil), raw...), '\n'))
}

func writeArtifactContent(writer http.ResponseWriter, content []byte, digest agency.Digest) {
	if digest.IsZero() || agency.Sum(content) != digest ||
		agency.ValidateAgentArtifactText(content) != nil {
		writeControlError(writer, newControlError(codeInternal,
			"Agency service returned invalid Artifact content"))
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set(headerArtifactDigest, digest.String())
	writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func writeControlJSON(writer http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxPrivateResponse {
		writeControlError(writer, newControlError(codeInternal, "control response cannot be encoded"))
		return
	}
	setControlHeaders(writer)
	writer.WriteHeader(status)
	_, _ = writer.Write(append(raw, '\n'))
}

func setControlHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}
