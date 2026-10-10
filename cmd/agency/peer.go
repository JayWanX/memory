//go:build !windows

package agency

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/JayWanX/memory/internal/daemon"
	"github.com/spf13/cobra"
)

const maxPeerCardInputBytes = 1025

func runPeerPrepare(command *cobra.Command, _ []string) error {
	listenAddress, err := command.Flags().GetString("listen")
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer prepare: %w", err)}
	}
	advertisedAddress, err := command.Flags().GetString("advertise")
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer prepare: %w", err)}
	}
	projectRoot, err := command.Flags().GetString("project-root")
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer prepare: %w", err)}
	}
	if strings.TrimSpace(listenAddress) == "" || strings.TrimSpace(advertisedAddress) == "" {
		return errors.New("memory agency peer prepare: requires --listen and --advertise")
	}
	if command.Flags().Changed("project-root") && strings.TrimSpace(projectRoot) == "" {
		return errors.New("memory agency peer prepare: --project-root must not be empty")
	}

	projectRoot, err = resolvePeerProjectRoot(projectRoot)
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer prepare: %w", err)}
	}
	provisioned, err := daemon.Provision(command.Context(), projectRoot)
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer prepare: %w", err)}
	}
	card, err := daemon.ConfigureExchange(command.Context(), provisioned.StateDirectory(),
		listenAddress, advertisedAddress)
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer prepare: %w", err)}
	}
	if _, err := command.OutOrStdout().Write(append(card.CanonicalJSON(), '\n')); err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer prepare: %w", err)}
	}
	return nil
}

func runPeerEnroll(command *cobra.Command, _ []string) error {
	alias, err := command.Flags().GetString("alias")
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer enroll: %w", err)}
	}
	projectRoot, err := command.Flags().GetString("project-root")
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer enroll: %w", err)}
	}
	if strings.TrimSpace(alias) == "" {
		return errors.New("memory agency peer enroll: requires --alias")
	}
	if command.Flags().Changed("project-root") && strings.TrimSpace(projectRoot) == "" {
		return errors.New("memory agency peer enroll: --project-root must not be empty")
	}
	card, err := readPeerCard(command.InOrStdin())
	if err != nil {
		return fmt.Errorf("memory agency peer enroll: %w", err)
	}
	projectRoot, err = resolvePeerProjectRoot(projectRoot)
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer enroll: %w", err)}
	}
	_, stateDirectory, err := daemon.ResolveProjectState(projectRoot)
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer enroll: %w", err)}
	}
	result, err := daemon.EnrollPeer(command.Context(), stateDirectory, alias, card)
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer enroll: %w", err)}
	}
	if _, err := command.OutOrStdout().Write(append(result.CanonicalJSON(), '\n')); err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency peer enroll: %w", err)}
	}
	return nil
}

func resolvePeerProjectRoot(requested string) (string, error) {
	if requested == "" {
		var err error
		requested, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	projectRoot, _, err := daemon.ResolveProjectState(requested)
	return projectRoot, err
}

func readPeerCard(input io.Reader) (daemon.PeerCard, error) {
	raw, err := io.ReadAll(io.LimitReader(input, maxPeerCardInputBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxPeerCardInputBytes {
		return daemon.PeerCard{}, errors.New("canonical Peer Card on stdin is required")
	}
	raw = bytes.TrimSuffix(raw, []byte{'\n'})
	card, err := daemon.ParsePeerCardCanonicalJSON(raw)
	if err != nil {
		return daemon.PeerCard{}, fmt.Errorf("parse Peer Card: %w", err)
	}
	return card, nil
}
