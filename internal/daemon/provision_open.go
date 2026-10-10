package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/JayWanX/memory/internal/agency"
)

// OpenProvisioned derives the local Principal from an existing transport
// identity and strictly opens the matching authority. It never creates or
// repairs identity, lock, CAS, schema, Principal, or exchange state.
func OpenProvisioned(ctx context.Context, stateDirectory string) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("daemon open provisioned: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stateIdentity, err := snapshotOwnerDirectory(stateDirectory)
	if err != nil {
		return nil, fmt.Errorf("daemon open provisioned: %w", err)
	}
	lock, err := acquireExistingProvisionLock(ctx, stateDirectory)
	if err != nil {
		return nil, err
	}
	runtime, openErr := openProvisionedLocked(ctx, stateDirectory, stateIdentity)
	releaseErr := releaseProvisionLock(lock)
	if releaseErr != nil {
		return nil, errors.Join(openErr, releaseErr, closeProvisionedRuntime(runtime))
	}
	return runtime, openErr
}

func openProvisionedLocked(ctx context.Context, stateDirectory string,
	stateIdentity os.FileInfo,
) (*Runtime, error) {
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return nil, err
	}
	if err := requireProvisionedLayout(stateDirectory); err != nil {
		return nil, err
	}
	identity, err := loadTransportIdentity(stateDirectory)
	if err != nil {
		return nil, err
	}
	config, exchangePrepared, err := readExchangeConfigLocked(stateDirectory)
	if err != nil {
		return nil, err
	}
	principal, err := DefaultAgentPrincipal(identity.projection.PublicKey())
	if err != nil {
		return nil, err
	}
	runtime, err := openConfiguredRuntime(ctx, stateDirectory, principal,
		config, exchangePrepared)
	if err != nil {
		return nil, err
	}
	if err := runtime.store.RequireProvisionedPrincipalShape(ctx, principal); err != nil {
		return nil, errors.Join(err, closeProvisionedRuntime(runtime))
	}
	return runtime, nil
}

func openConfiguredRuntime(ctx context.Context, stateDirectory string,
	principal agency.AgentPrincipalID, config exchangeConfig, exchangePrepared bool,
) (*Runtime, error) {
	if !exchangePrepared {
		return Open(ctx, stateDirectory, principal)
	}
	return OpenWithExchange(ctx, stateDirectory, principal,
		ExchangeOptions{ListenAddress: config.listenAddress})
}

func closeProvisionedRuntime(runtime *Runtime) error {
	if runtime == nil {
		return nil
	}
	closeContext, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	return runtime.Close(closeContext)
}
