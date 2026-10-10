package daemon

import (
	"crypto/ed25519"
	"errors"
	"strings"

	"github.com/JayWanX/memory/internal/agency"
)

const defaultPrincipalDomain = "memory.r7.default-agent-principal.v1"

// DefaultAgentPrincipal deterministically derives the one T0 local governed
// actor from the transport public key in an independent domain. No Principal
// configuration is persisted beside the durable node identity.
func DefaultAgentPrincipal(publicKey ed25519.PublicKey) (agency.AgentPrincipalID, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return agency.AgentPrincipalID{}, errors.New("derive default Principal: invalid Ed25519 public key")
	}
	digest := agency.Sum(append([]byte(defaultPrincipalDomain+"\x00"), publicKey...))
	return agency.NewAgentPrincipalID("principal:r7:" +
		strings.TrimPrefix(digest.String(), "sha256:"))
}
