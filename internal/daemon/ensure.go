package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/JayWanX/memory/internal/agency/authority"
	"golang.org/x/sys/unix"
)

const (
	ensureBudget       = 3 * time.Second
	ensureProbeBudget  = 100 * time.Millisecond
	ensurePollInterval = 20 * time.Millisecond
	ensureReapBudget   = time.Second
	ensureStatusBytes  = 128
)

var (
	ErrEnsure              = errors.New("daemon: ensure R7 authority")
	expectedStatusResponse = []byte(`{"schema":"memory.agency.status","status":"ready","version":1}` + "\n")
)

type ensureChild interface {
	Exited() (bool, string, error)
	KillAndWait() error
	Release() error
}

type ensureDependencies struct {
	resolveExecutable func() (string, error)
	start             func(string, string) (ensureChild, error)
}

// Ensure makes one already-provisioned local authority reachable. It does not
// provision, repair, register, or persist process state, and it never starts an
// Agent turn.
func Ensure(ctx context.Context, stateDirectory string) error {
	return ensure(ctx, stateDirectory, ensureDependencies{
		resolveExecutable: currentMemoryExecutable,
		start:             startMemoryAgency,
	})
}

func ensure(ctx context.Context, stateDirectory string, deps ensureDependencies) (err error) {
	if ctx == nil || deps.resolveExecutable == nil || deps.start == nil {
		return fmt.Errorf("%w: context and process dependencies are required", ErrEnsure)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ensureContext, cancel := context.WithTimeout(ctx, ensureBudget)
	defer cancel()
	ready, err := probeDaemonStatus(ensureContext, stateDirectory)
	if err != nil {
		return fmt.Errorf("%w: initial status: %w", ErrEnsure, err)
	}
	if ready {
		return nil
	}
	lock, err := acquireExistingEnsureLock(ensureContext, stateDirectory)
	if err != nil {
		return fmt.Errorf("%w: acquire startup lock: %w", ErrEnsure, err)
	}
	defer func() { err = errors.Join(err, releaseProvisionLock(lock)) }()
	ready, err = probeDaemonStatus(ensureContext, stateDirectory)
	if err != nil {
		return fmt.Errorf("%w: locked status: %w", ErrEnsure, err)
	}
	if ready {
		return nil
	}

	runtime, err := OpenProvisioned(ensureContext, stateDirectory)
	if err != nil {
		if errors.Is(err, authority.ErrWriterActive) {
			return waitForExistingDaemon(ensureContext, stateDirectory)
		}
		return fmt.Errorf("%w: strict preflight: %w", ErrEnsure, err)
	}
	closeContext, closeCancel := context.WithTimeout(ensureContext, shutdownBudget)
	closeErr := runtime.Close(closeContext)
	closeCancel()
	if closeErr != nil {
		return fmt.Errorf("%w: release preflight writer: %w", ErrEnsure, closeErr)
	}
	executable, err := deps.resolveExecutable()
	if err != nil {
		return fmt.Errorf("%w: resolve current executable: %w", ErrEnsure, err)
	}
	child, err := deps.start(executable, stateDirectory)
	if err != nil {
		return fmt.Errorf("%w: start agency daemon: %w", ErrEnsure, err)
	}
	return waitForStartedDaemon(ensureContext, stateDirectory, child)
}

func waitForExistingDaemon(ctx context.Context, stateDirectory string) error {
	poll := time.NewTicker(ensurePollInterval)
	defer poll.Stop()
	for {
		ready, err := probeDaemonStatus(ctx, stateDirectory)
		if err != nil {
			return fmt.Errorf("%w: existing daemon status: %w", ErrEnsure, err)
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: existing daemon readiness: %w", ErrEnsure, ctx.Err())
		case <-poll.C:
		}
	}
}

func waitForStartedDaemon(ctx context.Context, stateDirectory string,
	child ensureChild,
) (err error) {
	if child == nil {
		return fmt.Errorf("%w: started agency daemon is unavailable", ErrEnsure)
	}
	released := false
	defer func() {
		if !released {
			err = errors.Join(err, child.KillAndWait())
		}
	}()
	poll := time.NewTicker(ensurePollInterval)
	defer poll.Stop()
	for {
		ready, probeErr := probeDaemonStatus(ctx, stateDirectory)
		if probeErr != nil {
			return fmt.Errorf("%w: started daemon status: %w", ErrEnsure, probeErr)
		}
		if ready {
			exited, _, childErr := child.Exited()
			if childErr != nil {
				return fmt.Errorf("%w: inspect ready daemon child: %w", ErrEnsure, childErr)
			}
			if exited {
				released = true
				return nil
			}
			if err := child.Release(); err != nil {
				return fmt.Errorf("%w: release started daemon: %w", ErrEnsure, err)
			}
			released = true
			return nil
		}
		exited, status, childErr := child.Exited()
		if childErr != nil {
			return fmt.Errorf("%w: inspect started daemon: %w", ErrEnsure, childErr)
		}
		if exited {
			return fmt.Errorf("%w: started daemon exited before readiness (%s)", ErrEnsure, status)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: started daemon readiness: %w", ErrEnsure, ctx.Err())
		case <-poll.C:
		}
	}
}

func probeDaemonStatus(ctx context.Context, stateDirectory string) (bool, error) {
	if ctx == nil {
		return false, errors.New("status context is required")
	}
	if err := requireOwnerDirectory(stateDirectory); err != nil {
		return false, err
	}
	ownerInfo, err := os.Lstat(stateDirectory)
	if err != nil {
		return false, err
	}
	ownerUID, err := fileOwnerUID(ownerInfo)
	if err != nil {
		return false, err
	}
	socket := filepath.Join(stateDirectory, controlSocketName)
	identity, present, err := ownerSocketInfo(socket, ownerUID)
	if err != nil || !present {
		return false, err
	}
	probeContext, cancel := context.WithTimeout(ctx, ensureProbeBudget)
	defer cancel()
	client, transport := ownerStatusClient(socket, ownerUID, identity)
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(probeContext, http.MethodGet,
		"http://memoryd"+routeStatus, nil)
	if err != nil {
		return false, err
	}
	request.Close = true
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, err
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, ensureStatusBytes+1))
	if readErr != nil || len(raw) > ensureStatusBytes || response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "application/json" ||
		!bytes.Equal(raw, expectedStatusResponse) {
		return false, errors.New("status response is not the exact R7 ready projection")
	}
	return true, nil
}

func ownerStatusClient(socket string, ownerUID uint32,
	identity os.FileInfo,
) (*http.Client, *http.Transport) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		MaxResponseHeaderBytes: 4 << 10}
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		unixConnection, ok := connection.(*net.UnixConn)
		if !ok {
			_ = connection.Close()
			return nil, errors.New("status connection is not Unix")
		}
		peer, err := peerUID(unixConnection)
		current, present, inspectErr := ownerSocketInfo(socket, ownerUID)
		if err != nil || inspectErr != nil || !present || peer != ownerUID ||
			!os.SameFile(identity, current) {
			_ = connection.Close()
			return nil, errors.New("status socket authority changed while connecting")
		}
		return connection, nil
	}
	return &http.Client{Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		transport
}

func currentMemoryExecutable() (string, error) {
	current, err := os.Executable()
	if err != nil {
		return "", err
	}
	return physicalMemoryExecutable(current)
}

func physicalMemoryExecutable(current string) (string, error) {
	if current == "" || !filepath.IsAbs(current) || filepath.Clean(current) != current {
		return "", errors.New("current memory executable path is not absolute and clean")
	}
	physicalCurrent, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("resolve physical memory executable: %w", err)
	}
	if !filepath.IsAbs(physicalCurrent) || filepath.Clean(physicalCurrent) != physicalCurrent {
		return "", errors.New("physical memory executable path is not absolute and clean")
	}
	info, err := os.Lstat(physicalCurrent)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("current memory executable must be a protected regular file")
	}
	owner, ownerErr := fileOwnerUID(info)
	if ownerErr != nil || !trustedExecutableOwner(owner, uint32(os.Geteuid())) {
		return "", errors.New("current memory executable has the wrong owner")
	}
	return physicalCurrent, nil
}

func trustedExecutableOwner(owner, effectiveUser uint32) bool {
	return owner == effectiveUser || owner == 0
}

func startMemoryAgency(executable, stateDirectory string) (ensureChild, error) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	process, startErr := os.StartProcess(executable,
		[]string{executable, "agency", "serve", "--state-dir", stateDirectory}, &os.ProcAttr{
			Dir: stateDirectory, Env: []string{}, Files: []*os.File{null, null, null},
			Sys: &syscall.SysProcAttr{Setsid: true},
		})
	closeErr := null.Close()
	if startErr != nil || closeErr != nil {
		if process != nil {
			child := &systemEnsureChild{process: process}
			_ = child.KillAndWait()
		}
		return nil, errors.Join(startErr, closeErr)
	}
	return &systemEnsureChild{process: process}, nil
}

type systemEnsureChild struct {
	process  *os.Process
	settled  bool
	released bool
}

func (child *systemEnsureChild) Exited() (bool, string, error) {
	if child == nil || child.process == nil {
		return false, "", errors.New("started process is unavailable")
	}
	if child.settled {
		return true, "settled", nil
	}
	if child.released {
		return false, "", errors.New("started process was already released")
	}
	var status unix.WaitStatus
	pid, err := unix.Wait4(child.process.Pid, &status, unix.WNOHANG, nil)
	if errors.Is(err, syscall.EINTR) || (pid == 0 && err == nil) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	child.settled = true
	_ = child.process.Release()
	return true, fmt.Sprintf("wait_status=%d", status), nil
}

func (child *systemEnsureChild) KillAndWait() error {
	if child == nil || child.process == nil || child.settled || child.released {
		return nil
	}
	killErr := child.process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	deadline := time.Now().Add(ensureReapBudget)
	for time.Now().Before(deadline) {
		exited, _, waitErr := child.Exited()
		if waitErr != nil {
			return errors.Join(killErr, waitErr)
		}
		if exited {
			return killErr
		}
		time.Sleep(ensurePollInterval)
	}
	child.released = true
	return errors.Join(killErr, child.process.Release(),
		errors.New("started daemon was not reaped within its bound"))
}

func (child *systemEnsureChild) Release() error {
	if child == nil || child.process == nil || child.settled || child.released {
		return errors.New("started process cannot be released")
	}
	if err := child.process.Release(); err != nil {
		return err
	}
	child.released = true
	return nil
}
