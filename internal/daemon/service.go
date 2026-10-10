package daemon

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

const candidateEntropyBytes = 16

type localService struct {
	principal agency.AgentPrincipalID
	authority *authority.Store
	artifacts *artifact.Store
	now       func() time.Time
	random    io.Reader
}

type attachment struct {
	id         agency.AttachmentID
	credential []byte
	expiresAt  time.Time
}

type attachmentEnd struct {
	replayed      bool
	releasedClaim bool
}

type capturedArtifact struct {
	handle   agency.OpaqueHandle
	digest   agency.Digest
	byteSize int64
}

type candidateBinding struct {
	handle agency.OpaqueHandle
	digest agency.Digest
}

func newLocalService(principal agency.AgentPrincipalID, store *authority.Store,
	objects *artifact.Store, now func() time.Time,
) (*localService, error) {
	if principal.IsZero() || store == nil || store.Path() == "" || objects == nil ||
		objects.Root() == "" || now == nil {
		return nil, errors.New("daemon: Principal, authority, and Artifact store are required")
	}
	return &localService{principal: principal, authority: store, artifacts: objects,
		now: now, random: cryptorand.Reader}, nil
}

func (service *localService) attach(ctx context.Context,
	boundary agency.Digest,
) (attachment, error) {
	if err := service.available(ctx); err != nil {
		return attachment{}, err
	}
	proof, err := service.authority.IssueInteractiveAttachment(ctx, service.principal, boundary)
	if err != nil {
		return attachment{}, fmt.Errorf("daemon attach: %w", err)
	}
	return attachment{id: proof.ID(), credential: proof.Credential(), expiresAt: proof.ExpiresAt()}, nil
}

func (service *localService) endAttachment(ctx context.Context,
	proof authority.AttachmentProof,
) (attachmentEnd, error) {
	if err := service.available(ctx); err != nil {
		return attachmentEnd{}, err
	}
	result, err := service.authority.EndInteractiveAttachment(ctx, proof)
	if err != nil {
		return attachmentEnd{}, fmt.Errorf("daemon end attachment: %w", err)
	}
	return attachmentEnd{replayed: result.Replayed(), releasedClaim: result.ReleasedClaim()}, nil
}

func (service *localService) current(ctx context.Context, proof authority.AttachmentProof,
	operation authority.CurrentOperation,
) (agency.AgentView, error) {
	if err := service.available(ctx); err != nil {
		return agency.AgentView{}, err
	}
	view, err := service.authority.Current(ctx, proof, operation)
	if err != nil {
		return agency.AgentView{}, fmt.Errorf("daemon current: %w", err)
	}
	return view.AgentView(), nil
}

func (service *localService) submit(ctx context.Context, proof authority.AttachmentProof,
	current authority.CurrentOperation, operation agency.OperationKey, intent agency.AgentIntent,
	bindings []candidateBinding,
) (agency.AgentReceipt, error) {
	if err := service.available(ctx); err != nil {
		return agency.AgentReceipt{}, err
	}
	view, err := service.authority.ReplayCurrent(ctx, proof, current)
	if err != nil {
		return agency.AgentReceipt{}, fmt.Errorf("daemon submit current: %w", err)
	}
	candidates, err := bindCandidates(operation, intent, bindings)
	if err != nil {
		return agency.AgentReceipt{}, err
	}
	bound, err := view.Bind(intent, operation, candidates)
	if err != nil {
		return agency.AgentReceipt{}, fmt.Errorf("daemon submit bind: %w", err)
	}
	result, err := service.authority.Admit(ctx, proof, bound)
	if err != nil {
		return agency.AgentReceipt{}, fmt.Errorf("daemon submit admit: %w", err)
	}
	receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
	if err != nil || receipt.Digest() != result.ReceiptDigest() || receipt.Outcome() != result.Outcome() {
		return agency.AgentReceipt{}, errors.New("daemon submit: authority returned corrupt Receipt")
	}
	projected, err := agency.ProjectAgentReceipt(receipt, result.Replayed())
	if err != nil {
		return agency.AgentReceipt{}, fmt.Errorf("daemon submit project Receipt: %w", err)
	}
	return projected, nil
}

func bindCandidates(operation agency.OperationKey, intent agency.AgentIntent,
	bindings []candidateBinding,
) ([]agency.CapturedCandidate, error) {
	if operation.IsZero() || len(bindings) > agency.MaxArtifactInputs {
		return nil, errors.New("daemon submit: invalid candidate bindings")
	}
	inputs := make(map[string]agency.ArtifactInput)
	for _, input := range intent.Artifacts() {
		if input.Kind() == agency.ArtifactInputCandidate {
			inputs[input.Handle().String()] = input
		}
	}
	result := make([]agency.CapturedCandidate, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		input, offered := inputs[binding.handle.String()]
		if binding.handle.IsZero() || binding.digest.IsZero() || !offered {
			return nil, errors.New("daemon submit: candidate is absent from Intent")
		}
		if _, duplicate := seen[binding.handle.String()]; duplicate {
			return nil, errors.New("daemon submit: duplicate candidate binding")
		}
		seen[binding.handle.String()] = struct{}{}
		candidate, err := agency.NewCapturedCandidate(operation, input, binding.digest)
		if err != nil {
			return nil, fmt.Errorf("daemon submit candidate: %w", err)
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (service *localService) capture(ctx context.Context, content []byte) (capturedArtifact, error) {
	if err := service.available(ctx); err != nil {
		return capturedArtifact{}, err
	}
	if len(content) > artifact.MaxObjectBytes {
		return capturedArtifact{}, fmt.Errorf("daemon capture: Artifact exceeds %d bytes", artifact.MaxObjectBytes)
	}
	digest := agency.Sum(content)
	stored, err := service.artifacts.Put(ctx, digest, content)
	if err != nil {
		return capturedArtifact{}, fmt.Errorf("daemon capture Artifact: %w", err)
	}
	verified, err := authority.VerifyArtifact(content, service.now().Round(0).UTC())
	if err != nil || verified.Digest() != stored.Digest || verified.ByteSize() != stored.Size {
		return capturedArtifact{}, errors.New("daemon capture: CAS verification diverged")
	}
	if err := service.authority.CatalogArtifact(ctx, verified); err != nil {
		return capturedArtifact{}, fmt.Errorf("daemon capture catalog: %w", err)
	}
	handle, err := service.newCandidateHandle()
	if err != nil {
		return capturedArtifact{}, err
	}
	return capturedArtifact{handle: handle, digest: stored.Digest, byteSize: stored.Size}, nil
}

func (service *localService) readArtifact(ctx context.Context, proof authority.AttachmentProof,
	current authority.CurrentOperation, handle agency.OpaqueHandle,
) ([]byte, agency.Digest, error) {
	if err := service.available(ctx); err != nil {
		return nil, agency.Digest{}, err
	}
	digest, byteSize, err := service.authority.ResolveCurrentArtifact(ctx, proof, current, handle)
	if err != nil {
		return nil, agency.Digest{}, fmt.Errorf("daemon read Artifact authority: %w", err)
	}
	if byteSize > agency.MaxAgentArtifactReadBytes {
		return nil, agency.Digest{}, fmt.Errorf("%w: Agent Artifact read exceeds %d bytes",
			agency.ErrLimit, agency.MaxAgentArtifactReadBytes)
	}
	content, err := service.artifacts.Read(ctx, digest, byteSize)
	if err != nil {
		return nil, agency.Digest{}, fmt.Errorf("daemon read Artifact bytes: %w", err)
	}
	if int64(len(content)) != byteSize || agency.Sum(content) != digest {
		clear(content)
		return nil, agency.Digest{}, errors.New("daemon read Artifact: verified CAS digest diverged")
	}
	if err := agency.ValidateAgentArtifactText(content); err != nil {
		clear(content)
		return nil, agency.Digest{}, fmt.Errorf("daemon read Artifact projection: %w", err)
	}
	return content, digest, nil
}

func (service *localService) available(ctx context.Context) error {
	if service == nil || service.authority == nil || service.artifacts == nil || service.now == nil ||
		service.random == nil || service.principal.IsZero() || ctx == nil {
		return errors.New("daemon: local service is unavailable")
	}
	return ctx.Err()
}

func (service *localService) newCandidateHandle() (agency.OpaqueHandle, error) {
	var entropy [candidateEntropyBytes]byte
	if _, err := io.ReadFull(service.random, entropy[:]); err != nil {
		return agency.OpaqueHandle{}, fmt.Errorf("daemon capture handle: %w", err)
	}
	handle, err := agency.NewOpaqueHandle("artifact:" +
		base64.RawURLEncoding.EncodeToString(entropy[:]))
	if err != nil {
		return agency.OpaqueHandle{}, fmt.Errorf("daemon capture handle: %w", err)
	}
	return handle, nil
}
