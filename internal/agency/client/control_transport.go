package agencyclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"github.com/JayWanX/memory/internal/agency"
)

func (client *controlClient) post(ctx context.Context, route string, input any,
	headers http.Header, response any, maximum int64,
) *controlError {
	if client == nil || client.http == nil || ctx == nil || !postRoute(route) ||
		response == nil || maximum <= 0 {
		return invalidControlResponse("local Agency client is unavailable")
	}
	request, apiErr := newPostRequest(ctx, route, input, headers)
	if apiErr != nil {
		return apiErr
	}
	return client.send(request, response, maximum)
}

func (client *controlClient) postProjection(ctx context.Context, route string, input any,
	headers http.Header, maximum int,
) ([]byte, *controlError) {
	if client == nil || client.http == nil || ctx == nil ||
		(route != routeCurrent && route != routeSubmit) || maximum <= 0 {
		return nil, invalidControlResponse("local Agency client is unavailable")
	}
	request, apiErr := newPostRequest(ctx, route, input, headers)
	if apiErr != nil {
		return nil, apiErr
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, newControlError(codeMemorydUnavailable, "Memory Agency local control is unavailable")
	}
	return readProjectionResponse(response, maximum)
}

func (client *controlClient) postArtifact(ctx context.Context, route string, input any,
	headers http.Header,
) ([]byte, *controlError) {
	if client == nil || client.http == nil || ctx == nil || route != routeArtifactRead {
		return nil, invalidControlResponse("local Agency client is unavailable")
	}
	request, apiErr := newPostRequest(ctx, route, input, headers)
	if apiErr != nil {
		return nil, apiErr
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, newControlError(codeMemorydUnavailable, "Memory Agency local control is unavailable")
	}
	return readArtifactResponse(response)
}

func newPostRequest(ctx context.Context, route string, input any,
	headers http.Header,
) (*http.Request, *controlError) {
	body, err := marshalClosedJSON(input)
	if err != nil || len(body) == 0 || body[0] != '{' || int64(len(body)) > maxArtifactRequestBody {
		return nil, newControlError(codeInvalidArgument, "Agency request cannot be encoded canonically")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://memoryd"+route, bytes.NewReader(body))
	if err != nil {
		return nil, invalidControlResponse("local Agency request cannot be created")
	}
	request.Header.Set("Content-Type", "application/json")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	return request, nil
}

func (client *controlClient) send(request *http.Request, response any, maximum int64) *controlError {
	if client == nil || client.http == nil || request == nil || response == nil || maximum <= 0 {
		return invalidControlResponse("local Agency client is unavailable")
	}
	httpResponse, err := client.http.Do(request)
	if err != nil {
		return newControlError(codeMemorydUnavailable, "Memory Agency local control is unavailable")
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, maximum+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > maximum {
		return invalidControlResponse("local control response exceeds its closed bound")
	}
	if httpResponse.Header.Get("Content-Type") != "application/json" {
		return invalidControlResponse("local control response has the wrong content type")
	}
	object, apiErr := canonicalResponseObject(raw)
	if apiErr != nil {
		return apiErr
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return decodeRemoteError(object, httpResponse.StatusCode)
	}
	if httpResponse.StatusCode != http.StatusOK || decodeClosedJSON(object, response) != nil {
		return invalidControlResponse("Memory Agency returned an invalid success envelope")
	}
	return nil
}

func readProjectionResponse(response *http.Response, maximum int) ([]byte, *controlError) {
	if response == nil || response.Body == nil {
		return nil, invalidControlResponse("Memory Agency returned no Agency projection")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(maximum)+2))
	if err != nil || maximum <= 0 || len(raw) < 3 || len(raw) > maximum+1 ||
		raw[len(raw)-1] != '\n' || response.Header.Get("Content-Type") != "application/json" {
		return nil, invalidControlResponse("Agency projection response exceeds its closed bound")
	}
	object := raw[:len(raw)-1]
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeRemoteError(object, response.StatusCode)
	}
	if response.StatusCode != http.StatusOK || len(object) < 2 || object[0] != '{' ||
		object[len(object)-1] != '}' {
		return nil, invalidControlResponse("Memory Agency returned an invalid Agency projection")
	}
	return append([]byte(nil), object...), nil
}

func readArtifactResponse(response *http.Response) ([]byte, *controlError) {
	if response == nil || response.Body == nil {
		return nil, invalidControlResponse("Memory Agency returned no Artifact content")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(agency.MaxAgentArtifactReadBytes)+1))
	if err != nil || len(raw) > agency.MaxAgentArtifactReadBytes {
		clear(raw)
		return nil, invalidControlResponse("Artifact response exceeds its closed bound")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.Header.Get("Content-Type") != "application/json" || len(raw) > maxPrivateResponse {
			clear(raw)
			return nil, invalidControlResponse("Memory Agency returned an invalid Artifact error")
		}
		object, apiErr := canonicalResponseObject(raw)
		if apiErr != nil {
			clear(raw)
			return nil, apiErr
		}
		remote := decodeRemoteError(object, response.StatusCode)
		clear(raw)
		return nil, remote
	}
	values := response.Header.Values(headerArtifactDigest)
	digest, digestErr := agency.ParseDigest(firstExact(values))
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/octet-stream" ||
		response.ContentLength != int64(len(raw)) || digestErr != nil || digest.IsZero() ||
		agency.Sum(raw) != digest || agency.ValidateAgentArtifactText(raw) != nil {
		clear(raw)
		return nil, invalidControlResponse("Memory Agency returned invalid Artifact content")
	}
	return raw, nil
}

func firstExact(values []string) string {
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func canonicalResponseObject(raw []byte) ([]byte, *controlError) {
	if len(raw) < 2 || raw[len(raw)-1] != '\n' || raw[len(raw)-2] == '\n' {
		return nil, invalidControlResponse("local control response is not canonical")
	}
	object := raw[:len(raw)-1]
	if len(object) < 2 || object[0] != '{' || object[len(object)-1] != '}' {
		return nil, invalidControlResponse("local control response is not canonical")
	}
	return object, nil
}

func decodeRemoteError(object []byte, status int) *controlError {
	var remote controlError
	if decodeClosedJSON(object, &remote) != nil || validateControlError(&remote) != nil ||
		httpStatusForError(&remote) != status {
		return invalidControlResponse("Memory Agency returned an invalid Agency error envelope")
	}
	return &remote
}

func postRoute(route string) bool {
	return route == routeAttachments || route == routeAttachmentEnd || route == routeCurrent || route == routeSubmit ||
		route == routeArtifacts || route == routeArtifactRead
}

func httpStatusForError(value *controlError) int {
	if value == nil {
		return http.StatusInternalServerError
	}
	switch value.Code {
	case codeAuthenticationFailed:
		return http.StatusUnauthorized
	case codeOperationPending, codeMemorydUnavailable:
		return http.StatusServiceUnavailable
	case codeContextStale, codeActionNotAllowed, codeOperationMismatch:
		return http.StatusConflict
	case codeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

func invalidControlResponse(message string) *controlError {
	return newControlError(codeInternal, message)
}

func (client *controlClient) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	identity, err := validateOwnerSocket(client.socket, client.ownerUID)
	if err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", client.socket)
	if err != nil {
		return nil, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("R7 Agency control connection is not Unix")
	}
	peer, err := controlPeerUID(unixConnection)
	if err != nil || peer != client.ownerUID {
		_ = connection.Close()
		return nil, errors.New("R7 Agency control peer owner mismatch")
	}
	current, err := validateOwnerSocket(client.socket, client.ownerUID)
	if err != nil || !os.SameFile(identity, current) {
		_ = connection.Close()
		return nil, errors.New("R7 Agency control socket changed while connecting")
	}
	return connection, nil
}

func validateOwnerSocket(path string, ownerUID uint32) (os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("R7 Agency control socket path is not canonical")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeType != os.ModeSocket || info.Mode().Perm() != ownerSocketMode {
		return nil, errors.New("R7 Agency control socket is not owner-only")
	}
	uid, err := ownerUIDOf(info)
	if err != nil || uid != ownerUID {
		return nil, errors.New("R7 Agency control socket has the wrong owner")
	}
	return info, nil
}

func ownerUIDOf(info os.FileInfo) (uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("R7 Agency filesystem owner metadata is unavailable")
	}
	return stat.Uid, nil
}
