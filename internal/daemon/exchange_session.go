package daemon

import (
	"context"
	"errors"
	"sync"

	"github.com/JayWanX/memory/internal/agency/peerlink"
)

type exchangeSession struct {
	cancel context.CancelFunc
	server *peerlink.Server
	done   chan struct{}

	mu  sync.Mutex
	err error
}

func (session *exchangeSession) supervise(lifetime context.Context,
	workerDone, peerDone <-chan error,
) {
	var result error
	workerRead, peerRead := false, false
	select {
	case err := <-workerDone:
		workerRead = true
		if lifetime.Err() == nil {
			if err == nil {
				err = errors.New("outbox worker exited before cancellation")
			}
			result = errors.Join(result, err)
		}
	case err := <-peerDone:
		peerRead = true
		if lifetime.Err() == nil {
			if err == nil {
				err = errors.New("peer listener exited before cancellation")
			}
			result = errors.Join(result, err)
		}
	}
	session.cancel()
	closeContext, cancel := context.WithTimeout(context.Background(), peerShutdownBudget)
	result = errors.Join(result, session.server.CloseContext(closeContext))
	cancel()
	if !workerRead {
		result = errors.Join(result, <-workerDone)
	}
	if !peerRead {
		result = errors.Join(result, <-peerDone)
	}
	session.mu.Lock()
	session.err = result
	session.mu.Unlock()
	close(session.done)
}

func (session *exchangeSession) Done() <-chan struct{} { return session.done }

func (session *exchangeSession) Err() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.err
}

func (session *exchangeSession) Close(ctx context.Context) error {
	if session == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("daemon exchange: close context is required")
	}
	session.cancel()
	select {
	case <-session.done:
		return session.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
