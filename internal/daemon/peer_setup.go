package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/JayWanX/memory/internal/agency/authority"
)

const (
	peerSetupVersion         = 1
	maxPeerSetupDocumentSize = 1024
)

var ErrPeerSetup = errors.New("daemon: prepare R7 peer")

func validateTCPAddress(value string, allowWildcard bool) error {
	if value == "" || len(value) > authority.MaxPeerTransportAddressBytes ||
		strings.TrimSpace(value) != value || strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) >= 0 {
		return errors.New("bounded host:port is required")
	}
	host, portValue, err := net.SplitHostPort(value)
	if err != nil || host == "" {
		return errors.New("canonical host:port is required")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portValue {
		return errors.New("port must be an integer in 1..65535")
	}
	if !allowWildcard {
		if address := net.ParseIP(host); address != nil && address.IsUnspecified() {
			return errors.New("advertised host must not be a wildcard address")
		}
	}
	return nil
}

func readOwnerDocument(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := requireOwnerRegularFile(info); err != nil {
		return nil, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, false, errors.New("owner document changed while opening")
	}
	if err := requireOwnerRegularFile(opened); err != nil {
		return nil, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPeerSetupDocumentSize+1))
	if err != nil || len(raw) == 0 || len(raw) > maxPeerSetupDocumentSize {
		return nil, false, errors.New("owner document exceeds its byte bound")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) {
		return nil, false, errors.New("owner document changed during read")
	}
	if err := requireOwnerRegularFile(after); err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON document has trailing content")
	}
	return nil
}
