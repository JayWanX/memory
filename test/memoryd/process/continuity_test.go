package process_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/daemon"
)

const (
	commandBudget = 30 * time.Second
	buildBudget   = 2 * time.Minute
	processBudget = 10 * time.Second
)

// TestHandlingSurvivesProcessAndDaemonBoundaries is deliberately process
// based. Each terminal call is a fresh process, the private client journal is
// removed, and memoryd is stopped and started before a new attachment reads
// the durable responsibility. Text printed by an unrelated Runtime process
// has no control path and therefore cannot complete the Handling.
func TestHandlingSurvivesProcessAndDaemonBoundaries(t *testing.T) {
	fixture := newProcessFixture(t)

	runTerminal(t, fixture.binary, fixture.workspace, hostBoundaryEnvelope(t, 0x11),
		"hook", "attach", "--json")
	empty := runTerminal(t, fixture.binary, fixture.workspace, "", "agent", "current", "--json")
	assertEmptyView(t, empty)

	const kind = "probe.request"
	const payload = "continue after all process boundaries"
	intent := fmt.Sprintf(
		`{"kind":%q,"payload":%q,"consequence":"handling.create","successors":[{"self":true}]}`,
		kind, payload)
	receipt := runTerminal(t, fixture.binary, fixture.workspace, intent, "agent", "submit", "--json")
	assertAcceptedReceipt(t, receipt)

	// The Action Terminal journal is private convenience state. Removing it
	// cannot remove the accepted Event or the Handling owned by memoryd.
	journalDirectory := filepath.Join(fixture.state, "agency-client")
	if err := os.RemoveAll(journalDirectory); err != nil {
		t.Fatalf("remove exact temporary client journal %q: %v", journalDirectory, err)
	}
	if _, err := os.Lstat(journalDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("client journal still exists after fresh-Runtime reset: %v", err)
	}

	observations := runObservationProcess(t)
	assertObservationVocabulary(t, observations)
	fixture.restartDaemon(t)

	// This attachment and Current journal did not exist before the daemon
	// restart. Its View is reconstructed from durable authority state only.
	runTerminal(t, fixture.binary, fixture.workspace, hostBoundaryEnvelope(t, 0x12),
		"hook", "attach", "--json")
	current := runTerminal(t, fixture.binary, fixture.workspace, "", "agent", "current", "--json")
	handle := assertCurrentView(t, current, kind, payload)

	// A second unrelated process may claim completion in natural language,
	// report idle, or report provider success. None is an Intent or admission.
	moreObservations := runObservationProcess(t)
	assertObservationVocabulary(t, moreObservations)
	replayed := runTerminal(t, fixture.binary, fixture.workspace, "", "agent", "current", "--json")
	if !bytes.Equal(bytes.TrimSpace([]byte(current)), bytes.TrimSpace([]byte(replayed))) {
		t.Fatalf("Current changed after observation-only process\nfirst: %s\nnext:  %s", current, replayed)
	}
	if got := assertCurrentView(t, replayed, kind, payload); got != handle {
		t.Fatalf("Handling handle changed across Current replay: %q != %q", got, handle)
	}
}

func hostBoundaryEnvelope(t *testing.T, fill byte) string {
	t.Helper()
	wire := struct {
		Boundary string `json:"boundary"`
		Schema   string `json:"schema"`
		Version  int    `json:"version"`
	}{Boundary: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)),
		Schema: "memory.hook.boundary", Version: 1}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type processFixture struct {
	binary    string
	workspace string
	state     string
	active    *daemonProcess
}

func newProcessFixture(t *testing.T) *processFixture {
	t.Helper()
	root := moduleRoot(t)
	binDirectory := t.TempDir()
	fixture := &processFixture{
		binary:    filepath.Join(binDirectory, "memory"),
		workspace: shortWorkspace(t),
	}
	buildBinary(t, root, ".", fixture.binary)
	ctx, cancel := context.WithTimeout(context.Background(), commandBudget)
	result, err := daemon.Provision(ctx, fixture.workspace)
	cancel()
	if err != nil {
		t.Fatalf("provision R7 workspace: %v", err)
	}
	fixture.state = result.StateDirectory()
	fixture.active = startDaemon(t, fixture.binary, fixture.state)
	requireDaemonReady(t, fixture.active, fixture.state)
	t.Cleanup(func() {
		if fixture.active != nil {
			_ = fixture.active.stop()
		}
	})
	return fixture
}

func (fixture *processFixture) restartDaemon(t *testing.T) {
	t.Helper()
	if err := fixture.active.stop(); err != nil {
		t.Fatalf("stop memoryd before restart: %v", err)
	}
	waitForSocketRemoval(t, fixture.state)
	fixture.active = startDaemon(t, fixture.binary, fixture.state)
	requireDaemonReady(t, fixture.active, fixture.state)
}

func requireDaemonReady(t *testing.T, process *daemonProcess, stateDirectory string) {
	t.Helper()
	if err := waitForDaemon(stateDirectory); err != nil {
		_ = process.stop()
		t.Fatalf("memoryd readiness: %v\nstdout: %s\nstderr: %s", err,
			process.stdout.String(), process.stderr.String())
	}
}

// TestRuntimeObservationHelper is executed as a child test process. It models
// an opaque Runtime/provider surface whose output is observable text only.
func TestRuntimeObservationHelper(t *testing.T) {
	if os.Getenv("MEMORY_R7_OBSERVATION_HELPER") != "1" {
		t.Skip("child process helper")
	}
	fmt.Fprintln(os.Stdout, "final answer: completed")
	fmt.Fprintln(os.Stdout, "runtime status: idle")
	fmt.Fprintln(os.Stdout, "provider result: success")
}

type daemonProcess struct {
	command *exec.Cmd
	wait    chan error
	once    sync.Once
	err     error
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

func startDaemon(t *testing.T, binary, stateDirectory string) *daemonProcess {
	t.Helper()
	process := &daemonProcess{wait: make(chan error, 1)}
	process.command = exec.Command(binary, "agency", "serve", "--state-dir", stateDirectory)
	process.command.Dir = stateDirectory
	process.command.Stdout = &process.stdout
	process.command.Stderr = &process.stderr
	if err := process.command.Start(); err != nil {
		t.Fatalf("start memoryd: %v", err)
	}
	go func() { process.wait <- process.command.Wait() }()
	return process
}

func (process *daemonProcess) stop() error {
	process.once.Do(func() {
		if process == nil || process.command == nil || process.command.Process == nil {
			process.err = errors.New("memoryd process is unavailable")
			return
		}
		signalErr := process.command.Process.Signal(os.Interrupt)
		select {
		case waitErr := <-process.wait:
			if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
				process.err = signalErr
			}
			if waitErr != nil {
				var exitErr *exec.ExitError
				if !errors.As(waitErr, &exitErr) {
					process.err = errors.Join(process.err, waitErr)
				}
			}
		case <-time.After(processBudget):
			killErr := process.command.Process.Kill()
			waitErr := <-process.wait
			process.err = errors.Join(errors.New("memoryd exceeded shutdown bound"), killErr, waitErr)
		}
	})
	if process.err != nil {
		return fmt.Errorf("%w\nstdout: %s\nstderr: %s", process.err,
			process.stdout.String(), process.stderr.String())
	}
	return nil
}

func buildBinary(t *testing.T, root, target, output string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), buildBudget)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", output, target)
	command.Dir = root
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, combined)
	}
}

func runTerminal(t *testing.T, binary, workspace, stdin string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandBudget)
	defer cancel()
	commandArgs := append([]string{"agency"}, args...)
	command := exec.CommandContext(ctx, binary, commandArgs...)
	command.Dir = workspace
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("terminal %q: %v\nstdout: %s\nstderr: %s", args, err,
			stdout.String(), stderr.String())
	}
	return stdout.String()
}

func runObservationProcess(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandBudget)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeObservationHelper$")
	command.Env = append(os.Environ(), "MEMORY_R7_OBSERVATION_HELPER=1")
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("observation helper: %v\n%s", err, combined)
	}
	return string(combined)
}

func waitForDaemon(stateDirectory string) error {
	socket := filepath.Join(stateDirectory, "control.sock")
	deadline := time.Now().Add(processBudget)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("memoryd control socket %q did not become reachable", socket)
}

func waitForSocketRemoval(t *testing.T, stateDirectory string) {
	t.Helper()
	socket := filepath.Join(stateDirectory, "control.sock")
	deadline := time.Now().Add(processBudget)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(socket); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("memoryd control socket %q survived daemon shutdown", socket)
}

func assertEmptyView(t *testing.T, raw string) {
	t.Helper()
	view := parseView(t, raw)
	if view.Current != nil {
		t.Fatalf("initial View unexpectedly contains a Handling: %s", raw)
	}
}

func assertCurrentView(t *testing.T, raw, kind, payload string) string {
	t.Helper()
	view := parseView(t, raw)
	if view.Current == nil || view.Current.Facts.Handle == "" ||
		view.Current.Semantic.Kind != kind || view.Current.Semantic.Payload != payload {
		t.Fatalf("View does not contain the durable Handling %q/%q: %s", kind, payload, raw)
	}
	return view.Current.Facts.Handle
}

func parseView(t *testing.T, raw string) viewProjection {
	t.Helper()
	var view viewProjection
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode Agent View: %v\n%s", err, raw)
	}
	if view.Schema != "memory.agent.view" || view.Version != 8 || view.View == "" {
		t.Fatalf("invalid Agent View envelope: %#v", view)
	}
	return view
}

func assertAcceptedReceipt(t *testing.T, raw string) {
	t.Helper()
	var receipt struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil ||
		receipt.Schema != "memory.agent.receipt" || receipt.Version != 1 ||
		receipt.Outcome != "accepted" {
		t.Fatalf("root Intent was not accepted: %v / %s", err, raw)
	}
}

func assertObservationVocabulary(t *testing.T, raw string) {
	t.Helper()
	for _, expected := range []string{
		"final answer: completed", "runtime status: idle", "provider result: success",
	} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("observation helper omitted %q: %s", expected, raw)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve process test source path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../.."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolved repository root %q is invalid: %v", root, err)
	}
	return root
}

func shortWorkspace(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatalf("resolve short temporary base: %v", err)
	}
	workspace, err := os.MkdirTemp(base, "mnr7-")
	if err != nil {
		t.Fatalf("create short temporary workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	return workspace
}

type viewProjection struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	View    string `json:"view"`
	Current *struct {
		Facts struct {
			Handle string `json:"handle"`
		} `json:"facts"`
		Semantic struct {
			Kind    string `json:"kind"`
			Payload string `json:"payload"`
		} `json:"semantic"`
	} `json:"current"`
}
