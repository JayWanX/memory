package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

// domainActorKind is the closed set of verified actor contexts that may enter
// the domain admission spine. It is deliberately unrelated to the open Event
// kind label and is not an extension registry.
type domainActorKind uint8

const (
	domainActorLocal domainActorKind = iota + 1
	domainActorPeer
)

// domainAdmissionCandidate carries exactly one already-authenticated input.
// Actor-specific authentication, replay, rejection, and Receipt settlement
// remain outside this value; only accepted facts share the spine below.
type domainAdmissionCandidate struct {
	actor      domainActorKind
	local      agency.BoundIntent
	peer       agency.VerifiedPeerDelivery
	peerEffect decidedPeerEffect
}

// decidedPeerEffect is one receiver-local decision over an authenticated peer
// candidate. It is ephemeral transaction input, not another domain object.
// Consequence, target set, and successor cardinality therefore have one source.
type decidedPeerEffect struct {
	consequence agency.Consequence
	targets     []agency.ResolvedTarget
}

func localDomainAdmission(request agency.BoundIntent) domainAdmissionCandidate {
	return domainAdmissionCandidate{actor: domainActorLocal, local: request}
}

func peerDomainAdmission(verified agency.VerifiedPeerDelivery,
	effect decidedPeerEffect,
) domainAdmissionCandidate {
	effect.targets = append([]agency.ResolvedTarget(nil), effect.targets...)
	return domainAdmissionCandidate{actor: domainActorPeer, peer: verified, peerEffect: effect}
}

func decidePeerEffect(verified agency.VerifiedPeerDelivery) (decidedPeerEffect, error) {
	delivery := verified.Delivery()
	if delivery.ID().IsZero() || verified.LocalTarget().IsZero() {
		return decidedPeerEffect{}, errors.New("admit PeerDelivery: incomplete verified candidate")
	}
	if !delivery.RequiresTerminalReplyMatch() {
		requested, err := agency.AliasTarget(delivery.TargetAlias())
		if err != nil {
			return decidedPeerEffect{}, fmt.Errorf("admit PeerDelivery: resolve target alias: %w", err)
		}
		target, err := agency.ResolveLocalTarget(requested, verified.LocalTarget())
		if err != nil {
			return decidedPeerEffect{}, fmt.Errorf("admit PeerDelivery: resolve local target: %w", err)
		}
		return decidedPeerEffect{consequence: agency.ConsequenceCreateHandlings,
			targets: []agency.ResolvedTarget{target}}, nil
	}

	var consequence agency.Consequence
	switch delivery.OriginConsequence() {
	case agency.ConsequenceResolveCompleted:
		if len(verified.Artifacts()) == 0 {
			return decidedPeerEffect{}, errors.New(
				"admit PeerDelivery: completed reply requires a verified Artifact")
		}
		consequence = agency.ConsequenceObserveCompleted
	case agency.ConsequenceResolveDeclined:
		consequence = agency.ConsequenceObserveDeclined
	case agency.ConsequenceResolveUnresolved:
		consequence = agency.ConsequenceObserveUnresolved
	default:
		return decidedPeerEffect{}, errors.New("admit PeerDelivery: invalid terminal reply consequence")
	}
	return decidedPeerEffect{consequence: consequence}, nil
}

// commitDomainAdmissionTx is the single fact-producing path for accepted
// local and peer inputs. The caller must persist the actor-specific Receipt or
// inbox settlement in this same transaction before committing it.
func commitDomainAdmissionTx(ctx context.Context, tx *sql.Tx,
	candidate domainAdmissionCandidate, eventID agency.EventID,
	handlingIDs []agency.HandlingID, now time.Time,
) (agency.Event, error) {
	event, claimAttachment, err := newDomainEventTx(ctx, tx, candidate, eventID, now)
	if err != nil {
		return agency.Event{}, err
	}
	if err := insertEventTx(ctx, tx, event); err != nil {
		return agency.Event{}, err
	}
	if err := applyDomainEffectTx(ctx, tx, event, claimAttachment, handlingIDs); err != nil {
		return agency.Event{}, err
	}
	if err := insertPeerDeliveriesTx(ctx, tx, event, handlingIDs, now); err != nil {
		return agency.Event{}, err
	}
	return event, nil
}

func newDomainEventTx(ctx context.Context, tx *sql.Tx,
	candidate domainAdmissionCandidate, eventID agency.EventID, now time.Time,
) (agency.Event, agency.AttachmentID, error) {
	sequence, err := nextOriginSequenceTx(ctx, tx)
	if err != nil {
		return agency.Event{}, agency.AttachmentID{}, err
	}
	stamp := agency.EventStamp{ID: eventID, AcceptedAt: now, OriginSequence: sequence}
	switch candidate.actor {
	case domainActorLocal:
		depth, err := deriveLocalEventCausalDepthTx(ctx, tx, candidate.local)
		if err != nil {
			return agency.Event{}, agency.AttachmentID{}, err
		}
		stamp.CausalDepth = depth
		event, err := agency.NewEvent(candidate.local, stamp)
		if err != nil {
			return agency.Event{}, agency.AttachmentID{},
				fmt.Errorf("admit Intent: construct Event: %w", err)
		}
		return event, candidate.local.Attachment().ID(), nil
	case domainActorPeer:
		stamp.CausalDepth = candidate.peer.Delivery().CausalDepth()
		event, err := agency.NewPeerEvent(candidate.peer, stamp,
			candidate.peerEffect.consequence, candidate.peerEffect.targets)
		if err != nil {
			return agency.Event{}, agency.AttachmentID{},
				fmt.Errorf("admit PeerDelivery: construct local Event: %w", err)
		}
		return event, agency.AttachmentID{}, nil
	default:
		return agency.Event{}, agency.AttachmentID{}, errors.New("admit domain: unknown actor context")
	}
}
