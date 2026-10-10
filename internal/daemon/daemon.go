package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

const (
	controlSocketName = "control.sock"
	authorityFileName = "agency.db"
	shutdownBudget    = 2 * time.Second
)

type daemonState uint8

const (
	stateOpen daemonState = iota + 1
	stateServing
	stateClosing
	stateClosed
)

// Runtime owns one authority writer and one control-server lifecycle. It is a
// one-shot process boundary: once Serve ends or Close starts it cannot serve
// again.
type Runtime struct {
	mu              sync.Mutex
	state           daemonState
	store           *authority.Store
	service         *localService
	handler         http.Handler
	socket          string
	exchange        *exchangeRuntime
	exchangeSession *exchangeSession
	server          *http.Server
	cancel          context.CancelFunc
	serveDone       chan struct{}
	closeDone       chan struct{}
	closeErr        error
	requests        requestTracker
}

// Open strictly adopts already-provisioned state. It creates no database,
// Principal, Artifact root, socket directory, peer route, or setup state.
func Open(ctx context.Context, stateDirectory string,
	principal agency.AgentPrincipalID,
) (_ *Runtime, err error) {
	return openRuntime(ctx, stateDirectory, principal, nil)
}

// OpenWithExchange strictly adopts local state and enables the optional peer
// plane when at least one active route exists. A prepared node with no route
// remains local-only, so interrupted offline enrollment cannot block N=1.
// Remote reachability is never a startup dependency.
func OpenWithExchange(ctx context.Context, stateDirectory string,
	principal agency.AgentPrincipalID, options ExchangeOptions,
) (_ *Runtime, err error) {
	return openRuntime(ctx, stateDirectory, principal, &options)
}

func openRuntime(ctx context.Context, stateDirectory string,
	principal agency.AgentPrincipalID, exchangeOptions *ExchangeOptions,
) (_ *Runtime, err error) {
	if ctx == nil || principal.IsZero() {
		return nil, errors.New("daemon open: context and Principal are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireOwnerDirectory(stateDirectory); err != nil {
		return nil, fmt.Errorf("daemon open state: %w", err)
	}
	objectsRoot := filepath.Join(stateDirectory, "objects", "sha256")
	if err := requireOwnerDirectory(objectsRoot); err != nil {
		return nil, fmt.Errorf("daemon open Artifact store: %w", err)
	}
	objects, err := artifact.OpenExisting(objectsRoot)
	if err != nil {
		return nil, fmt.Errorf("daemon open Artifact store: %w", err)
	}
	now := time.Now
	store, err := authority.OpenExistingWithArtifactVerifierAndClock(ctx,
		filepath.Join(stateDirectory, authorityFileName), objects, now)
	if err != nil {
		return nil, fmt.Errorf("daemon open authority: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, store.Close())
		}
	}()
	if err := store.RequirePrincipal(ctx, principal); err != nil {
		return nil, fmt.Errorf("daemon verify Principal: %w", err)
	}
	service, err := newLocalService(principal, store, objects, now)
	if err != nil {
		return nil, err
	}
	control, err := newControlServer(service)
	if err != nil {
		return nil, err
	}
	var exchange *exchangeRuntime
	if exchangeOptions != nil {
		routes, routeErr := store.PeerRoutes(ctx)
		if routeErr != nil {
			return nil, fmt.Errorf("daemon inspect exchange routes: %w", routeErr)
		}
		for _, route := range routes {
			if !route.Active() {
				continue
			}
			exchange, err = newExchangeRuntime(ctx, stateDirectory, store, objects, now,
				*exchangeOptions)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	return &Runtime{state: stateOpen, store: store, service: service, handler: control,
		socket: filepath.Join(stateDirectory, controlSocketName), exchange: exchange}, nil
}

// Serve binds the fixed owner-only Unix socket and blocks until cancellation,
// Close, or a server failure. Every termination path joins shutdown and closes
// the authority writer before returning.
func (daemon *Runtime) Serve(ctx context.Context) error {
	if daemon == nil || ctx == nil {
		return errors.New("daemon serve: daemon and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	daemon.mu.Lock()
	if daemon.state != stateOpen || daemon.store == nil || daemon.handler == nil {
		daemon.mu.Unlock()
		return errors.New("daemon serve: daemon is not open")
	}
	if err := removeStaleOwnerSocket(daemon.socket); err != nil {
		daemon.mu.Unlock()
		return err
	}
	listener, err := listenOwnerUnix(daemon.socket)
	if err != nil {
		daemon.mu.Unlock()
		return err
	}
	runContext, cancel := context.WithCancel(ctx)
	var exchangeSession *exchangeSession
	if daemon.exchange != nil {
		exchangeSession, err = daemon.exchange.start(runContext)
		if err != nil {
			cancel()
			_ = listener.Close()
			daemon.mu.Unlock()
			return err
		}
	}
	serveDone := make(chan struct{})
	server := &http.Server{
		Handler:           daemon.requests.wrap(daemon.handler),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext: func(net.Listener) context.Context {
			return runContext
		},
	}
	server.SetKeepAlivesEnabled(false)
	daemon.state = stateServing
	daemon.server = server
	daemon.cancel = cancel
	daemon.serveDone = serveDone
	daemon.exchangeSession = exchangeSession
	daemon.mu.Unlock()

	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		if exchangeSession == nil {
			select {
			case <-ctx.Done():
				daemon.beginClose()
			case <-serveDone:
			}
			return
		}
		select {
		case <-ctx.Done():
			daemon.beginClose()
		case <-exchangeSession.Done():
			daemon.beginClose()
		case <-serveDone:
		}
	}()

	serveErr := server.Serve(listener)
	_ = listener.Close()
	close(serveDone)
	<-watchDone
	daemon.beginClose()
	closeErr := daemon.waitClosed(context.Background())
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		serveErr = errors.Join(serveErr, ctxErr)
	}
	return errors.Join(serveErr, closeErr)
}

// Close starts exactly one bounded graceful shutdown and waits until ctx ends
// or all requests, the server, and the authority writer are joined. If ctx
// ends first, owned cleanup continues and a later Close may wait for it.
func (daemon *Runtime) Close(ctx context.Context) error {
	if daemon == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("daemon close: context is required")
	}
	daemon.beginClose()
	return daemon.waitClosed(ctx)
}

func (daemon *Runtime) beginClose() {
	if daemon == nil {
		return
	}
	daemon.mu.Lock()
	if daemon.state == stateClosing || daemon.state == stateClosed {
		daemon.mu.Unlock()
		return
	}
	daemon.state = stateClosing
	daemon.closeDone = make(chan struct{})
	server, cancel := daemon.server, daemon.cancel
	exchangeSession := daemon.exchangeSession
	serveDone, store := daemon.serveDone, daemon.store
	waitRequests := daemon.requests.stop()
	daemon.mu.Unlock()

	go daemon.closeOwned(server, cancel, exchangeSession, serveDone, waitRequests, store)
}

func (daemon *Runtime) closeOwned(server *http.Server, cancel context.CancelFunc,
	exchangeSession *exchangeSession, serveDone, waitRequests <-chan struct{}, store *authority.Store,
) {
	var result error
	if cancel != nil {
		cancel()
	}
	if exchangeSession != nil {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), peerShutdownBudget)
		result = errors.Join(result, exchangeSession.Close(shutdownContext))
		shutdownCancel()
	}
	if server != nil {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), shutdownBudget)
		if err := server.Shutdown(shutdownContext); err != nil {
			result = errors.Join(result, err)
		}
		shutdownCancel()
	}
	if server != nil {
		if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, err)
		}
	}
	if serveDone != nil {
		<-serveDone
	}
	if waitRequests != nil {
		<-waitRequests
	}
	if store != nil {
		result = errors.Join(result, store.Close())
	}

	daemon.mu.Lock()
	daemon.closeErr = result
	daemon.state = stateClosed
	daemon.store = nil
	daemon.service = nil
	daemon.handler = nil
	daemon.server = nil
	daemon.cancel = nil
	daemon.serveDone = nil
	daemon.exchangeSession = nil
	done := daemon.closeDone
	daemon.mu.Unlock()
	close(done)
}

func (daemon *Runtime) waitClosed(ctx context.Context) error {
	daemon.mu.Lock()
	if daemon.state == stateClosed {
		err := daemon.closeErr
		daemon.mu.Unlock()
		return err
	}
	done := daemon.closeDone
	daemon.mu.Unlock()
	if done == nil {
		return errors.New("daemon close: close was not started")
	}
	select {
	case <-done:
		daemon.mu.Lock()
		err := daemon.closeErr
		daemon.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
