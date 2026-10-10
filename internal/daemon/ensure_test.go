package daemon

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureReturnsWhenProvisionedDaemonIsAlreadyReady(t *testing.T) {
	state := provisionEnsureState(t)
	runtime, serveErrors := startEnsureRuntime(t, state)
	defer stopEnsureRuntime(t, runtime, serveErrors)

	var resolves atomic.Int32
	var starts atomic.Int32
	err := ensure(context.Background(), state, ensureDependencies{
		resolveExecutable: func() (string, error) {
			resolves.Add(1)
			return "", errors.New("ready path resolved an executable")
		},
		start: func(string, string) (ensureChild, error) {
			starts.Add(1)
			return nil, errors.New("ready path started an agency daemon")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolves.Load() != 0 || starts.Load() != 0 {
		t.Fatalf("ready Ensure side effects: resolves=%d starts=%d",
			resolves.Load(), starts.Load())
	}
}

func TestConcurrentEnsureStartsOneProvisionedDaemon(t *testing.T) {
	state := provisionEnsureState(t)
	var starts atomic.Int32
	var startedMu sync.Mutex
	var startedRuntime *Runtime
	var startedErrors chan error
	deps := ensureDependencies{
		resolveExecutable: func() (string, error) { return "/test/memory", nil },
		start: func(_ string, gotState string) (ensureChild, error) {
			starts.Add(1)
			runtime, err := OpenProvisioned(context.Background(), gotState)
			if err != nil {
				return nil, err
			}
			serveErrors := make(chan error, 1)
			go func() { serveErrors <- runtime.Serve(context.Background()) }()
			startedMu.Lock()
			startedRuntime = runtime
			startedErrors = serveErrors
			startedMu.Unlock()
			return &testEnsureChild{runtime: runtime, serveErrors: serveErrors}, nil
		},
	}

	const callers = 8
	errorsSeen := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- ensure(context.Background(), state, deps)
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Ensure: %v", err)
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("daemon starts = %d, want 1", starts.Load())
	}
	startedMu.Lock()
	runtime, serveErrors := startedRuntime, startedErrors
	startedMu.Unlock()
	if runtime == nil || serveErrors == nil {
		t.Fatal("successful Ensure did not retain its started daemon")
	}
	stopEnsureRuntime(t, runtime, serveErrors)
}

func TestEnsureKillsItsChildWhenReadinessContextEnds(t *testing.T) {
	state := provisionEnsureState(t)
	child := &testEnsureChild{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()
	err := ensure(ctx, state, ensureDependencies{
		resolveExecutable: func() (string, error) { return "/test/memory", nil },
		start: func(string, string) (ensureChild, error) {
			close(started)
			return child, nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure cancellation = %v", err)
	}
	if child.kills.Load() != 1 || child.releases.Load() != 0 {
		t.Fatalf("failed child lifecycle: kills=%d releases=%d",
			child.kills.Load(), child.releases.Load())
	}
}

func TestEnsureSettlesAChildThatExitsBeforeReadiness(t *testing.T) {
	state := provisionEnsureState(t)
	child := &testEnsureChild{exited: true, status: "exit 2"}
	err := ensure(context.Background(), state, ensureDependencies{
		resolveExecutable: func() (string, error) { return "/test/memory", nil },
		start:             func(string, string) (ensureChild, error) { return child, nil },
	})
	if err == nil || child.kills.Load() != 1 || child.releases.Load() != 0 {
		t.Fatalf("early child exit = error %v kills=%d releases=%d", err,
			child.kills.Load(), child.releases.Load())
	}
}

func TestEnsureRejectsMissingStartupLockWithoutRepair(t *testing.T) {
	state := provisionEnsureState(t)
	lock := filepath.Join(state, ensureLockFile)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	err := ensure(context.Background(), state, ensureDependencies{
		resolveExecutable: func() (string, error) { return "/test/memory", nil },
		start: func(string, string) (ensureChild, error) {
			starts.Add(1)
			return &testEnsureChild{}, nil
		},
	})
	if err == nil {
		t.Fatal("Ensure accepted an unprovisioned startup lock")
	}
	if starts.Load() != 0 {
		t.Fatalf("Ensure started %d child processes for invalid state", starts.Load())
	}
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Ensure repaired its missing startup lock: %v", err)
	}
}

func TestEnsureRejectsMalformedActiveStatusWithoutStarting(t *testing.T) {
	state := provisionEnsureState(t)
	listener, err := listenOwnerUnix(filepath.Join(state, controlSocketName))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{}\n"))
	})}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("malformed status server: %v", err)
		}
	})

	var starts atomic.Int32
	err = ensure(context.Background(), state, ensureDependencies{
		resolveExecutable: func() (string, error) { return "/test/memory", nil },
		start: func(string, string) (ensureChild, error) {
			starts.Add(1)
			return &testEnsureChild{}, nil
		},
	})
	if err == nil {
		t.Fatal("Ensure accepted a malformed active status endpoint")
	}
	if starts.Load() != 0 {
		t.Fatalf("Ensure started %d child processes over an active endpoint", starts.Load())
	}
}

func TestStartMemoryAgencyReachesTheRealReadyEndpoint(t *testing.T) {
	state := provisionEnsureState(t)
	buildDirectory := canonicalTempDir(t)
	executable := filepath.Join(buildDirectory, "memory")
	build := exec.Command("go", "build", "-o", executable, "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real memory: %v\n%s", err, output)
	}
	child, err := startMemoryAgency(executable, state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := child.KillAndWait(); err != nil {
			t.Errorf("settle real memory agency: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ready, probeErr := probeDaemonStatus(context.Background(), state)
		if probeErr == nil && ready {
			return
		}
		exited, status, childErr := child.Exited()
		if childErr != nil || exited {
			t.Fatalf("real memory agency stopped before readiness: status=%q error=%v probe=%v",
				status, childErr, probeErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("real memory agency did not become ready")
}

func TestExecutableAcceptsOnlyCurrentOrRootOwner(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		owner     uint32
		effective uint32
		want      bool
	}{
		{name: "current user", owner: 501, effective: 501, want: true},
		{name: "root package manager", owner: 0, effective: 501, want: true},
		{name: "different user", owner: 502, effective: 501, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := trustedExecutableOwner(test.owner, test.effective); got != test.want {
				t.Fatalf("trustedExecutableOwner(%d, %d) = %v, want %v",
					test.owner, test.effective, got, test.want)
			}
		})
	}
}

func TestPhysicalMemoryExecutableResolvesOnlyAProtectedCurrentBinary(t *testing.T) {
	t.Parallel()
	directory := canonicalTempDir(t)
	executable := filepath.Join(directory, "memory-physical")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "memory")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := physicalMemoryExecutable(link)
	if err != nil || resolved != executable {
		t.Fatalf("physicalMemoryExecutable(symlink) = (%q, %v), want %q", resolved, err,
			executable)
	}
	if err := os.Chmod(executable, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := physicalMemoryExecutable(link); err == nil {
		t.Fatal("physicalMemoryExecutable accepted a group-writable binary")
	}
}

type testEnsureChild struct {
	runtime     *Runtime
	serveErrors chan error
	kills       atomic.Int32
	releases    atomic.Int32
	exited      bool
	status      string
}

func (child *testEnsureChild) Exited() (bool, string, error) {
	if child.exited || child.serveErrors == nil {
		return child.exited, child.status, nil
	}
	select {
	case err := <-child.serveErrors:
		child.exited = true
		child.status = "stopped"
		if err != nil {
			child.status = err.Error()
		}
	default:
	}
	return child.exited, child.status, nil
}

func (child *testEnsureChild) KillAndWait() error {
	child.kills.Add(1)
	if child.runtime == nil {
		return nil
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closeErr := child.runtime.Close(closeContext)
	if !child.exited && child.serveErrors != nil {
		serveErr := <-child.serveErrors
		child.exited = true
		return errors.Join(closeErr, serveErr)
	}
	return closeErr
}

func (child *testEnsureChild) Release() error {
	child.releases.Add(1)
	return nil
}

func provisionEnsureState(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(base) > 32 {
		base, err = filepath.EvalSymlinks("/tmp")
		if err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.MkdirTemp(base, "m-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return result.StateDirectory()
}

func startEnsureRuntime(t *testing.T, state string) (*Runtime, chan error) {
	t.Helper()
	runtime, err := OpenProvisioned(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- runtime.Serve(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	var lastProbeErr error
	for time.Now().Before(deadline) {
		ready, probeErr := probeDaemonStatus(context.Background(), state)
		if probeErr == nil && ready {
			return runtime, serveErrors
		}
		if probeErr != nil {
			lastProbeErr = probeErr
		}
		select {
		case serveErr := <-serveErrors:
			t.Fatalf("started daemon exited before readiness: serve=%v last_probe=%v",
				serveErr, lastProbeErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopEnsureRuntime(t, runtime, serveErrors)
	t.Fatalf("started daemon did not become ready before deadline: last_probe=%v", lastProbeErr)
	return nil, nil
}

func stopEnsureRuntime(t *testing.T, runtime *Runtime, serveErrors chan error) {
	t.Helper()
	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErrors; err != nil {
		t.Fatal(err)
	}
}
