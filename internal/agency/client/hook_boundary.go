package agencyclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	hookBoundarySchema        = "memory.hook.boundary"
	hookBoundaryVersion       = 1
	hookBoundaryEntropyBytes  = 32
	maxHookBoundaryInputBytes = 256
)

type hookBoundaryWire struct {
	Boundary string `json:"boundary"`
	Schema   string `json:"schema"`
	Version  int    `json:"version"`
}

// readHookBoundary consumes Host-private lifecycle material before daemon
// setup. Only its digest is returned for owner-private journal binding.
func readHookBoundary(reader io.Reader) (agency.Digest, *controlError) {
	raw, apiErr := readBoundedInput(reader, maxHookBoundaryInputBytes,
		codeContentTooLarge, "Hook boundary input exceeds its closed byte bound")
	if apiErr != nil {
		return agency.Digest{}, apiErr
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire hookBoundaryWire
	if err := decoder.Decode(&wire); err != nil {
		return agency.Digest{}, newControlError(codeInvalidArgument,
			"Hook boundary input is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return agency.Digest{}, newControlError(codeInvalidArgument,
			"Hook boundary input is invalid")
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) || wire.Schema != hookBoundarySchema ||
		wire.Version != hookBoundaryVersion {
		return agency.Digest{}, newControlError(codeInvalidArgument,
			"Hook boundary input is invalid")
	}
	boundary, err := base64.RawURLEncoding.Strict().DecodeString(wire.Boundary)
	if err != nil || len(boundary) != hookBoundaryEntropyBytes ||
		base64.RawURLEncoding.EncodeToString(boundary) != wire.Boundary {
		clear(boundary)
		return agency.Digest{}, newControlError(codeInvalidArgument,
			"Hook boundary input is invalid")
	}
	digest := agency.Sum(boundary)
	clear(boundary)
	return digest, nil
}
