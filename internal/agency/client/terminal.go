// Package agencyclient implements the owner-local Agent action terminal.
//
// The terminal holds only private client mechanics: one
// attachment proof, one Current operation, and captured candidate bindings.
//
// It does not interpret semantic Event kinds or duplicate Agency admission
// policy.
package agencyclient

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

type dependencies struct {
	workingDirectory func() (string, error)
	ensureDaemon     ensureDaemonFunc
	newClient        func(string) (agencyClient, error)
	random           io.Reader
	clock            func() time.Time
}

// ensureDaemonFunc is the narrow composition seam to bounded local daemon
// readiness. It may block until readiness or context cancellation and returns
// no diagnostic content to the Agent terminal.
type ensureDaemonFunc func(context.Context, string) error

// terminal is the Agent Action Terminal. Every invocation either executes one
// closed command or emits one bounded control error.
type terminal struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	deps   dependencies
}

func newTerminal(stdin io.Reader, stdout, stderr io.Writer, ensure ensureDaemonFunc) *terminal {
	return &terminal{stdin: stdin, stdout: stdout, stderr: stderr, deps: dependencies{
		workingDirectory: os.Getwd,
		ensureDaemon:     ensure,
		newClient: func(nodeState string) (agencyClient, error) {
			return newControlClient(nodeState)
		},
		random: cryptorand.Reader,
		clock:  time.Now,
	}}
}

// Run executes one private Agent action command. The ensure function is the
// only process-composition dependency; command routing and daemon ownership
// remain outside this package.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer,
	ensure func(context.Context, string) error,
) int {
	return newTerminal(stdin, stdout, stderr, ensure).run(ctx, args)
}

// run executes one exact JSON command. Unknown and malformed commands fail
// closed instead of being offered to another command surface.
func (app *terminal) run(ctx context.Context, args []string) int {
	if app == nil || app.stdin == nil || app.stdout == nil || app.stderr == nil || ctx == nil {
		return 1
	}
	if !app.available(ctx) {
		return app.writeError(newControlError(codeInternal,
			"R7 Agent terminal is unavailable"))
	}
	command := classify(args)
	if !validArguments(command, args) {
		if command == commandSubmit {
			return app.writeError(newControlError(codeInvalidArgument,
				"use exactly memory agency agent submit --json and provide Intent JSON on stdin"))
		}
		return app.writeError(newControlError(codeInvalidArgument,
			"R7 Agent command requires its exact --json form"))
	}
	var boundary agency.Digest
	if command == commandAttach || command == commandEnd {
		var apiErr *controlError
		boundary, apiErr = readHookBoundary(app.stdin)
		if apiErr != nil {
			return app.writeError(apiErr)
		}
	}
	prepared, apiErr := app.prepare(ctx, command)
	if apiErr != nil {
		return app.writeError(apiErr)
	}

	switch command {
	case commandAttach:
		return app.runAttach(ctx, prepared.store, prepared.client, boundary)
	case commandEnd:
		return app.runEnd(ctx, prepared.store, prepared.client, boundary)
	case commandCurrent:
		return app.runCurrent(ctx, prepared.store, prepared.client)
	case commandSubmit:
		return app.runSubmit(ctx, prepared.store, prepared.client)
	case commandCapture:
		return app.runCapture(ctx, prepared.store, prepared.client)
	case commandReadArtifact:
		return app.runReadArtifact(ctx, prepared.store, prepared.client, args[2])
	default:
		return app.writeError(newControlError(codeInvalidArgument,
			"R7 Agent command is not supported"))
	}
}

type preparedRun struct {
	store  *journalStore
	client agencyClient
}

func (app *terminal) available(ctx context.Context) bool {
	return app != nil && ctx != nil && app.deps.workingDirectory != nil && app.deps.ensureDaemon != nil &&
		app.deps.newClient != nil && app.deps.random != nil && app.deps.clock != nil
}

func (app *terminal) prepare(ctx context.Context, command commandKind) (preparedRun, *controlError) {
	nodeState, err := resolveWorkspace(app.deps.workingDirectory)
	if err != nil {
		return preparedRun{}, newControlError(codeMemorydUnavailable,
			"Memory Agency is not set up in this workspace")
	}
	store := newJournalStore(nodeState, app.deps.random)
	if apiErr := preflightJournal(command, store); apiErr != nil {
		return preparedRun{}, apiErr
	}
	if err := app.deps.ensureDaemon(ctx, nodeState); err != nil {
		return preparedRun{}, newControlError(codeMemorydUnavailable,
			"Memory Agency local control is unavailable")
	}
	client, err := app.deps.newClient(nodeState)
	if err != nil {
		return preparedRun{}, classifyClientConstruction(err)
	}
	return preparedRun{store: store, client: client}, nil
}

func preflightJournal(command commandKind, store *journalStore) *controlError {
	exists, err := store.exists()
	if err != nil {
		return clientStateError()
	}
	if !exists && command != commandAttach && command != commandEnd {
		return newControlError(codeContextRequired,
			"hook attach must establish an Agent context before this command")
	}
	return nil
}

type commandKind uint8

const (
	commandOther commandKind = iota
	commandAttach
	commandEnd
	commandCurrent
	commandSubmit
	commandCapture
	commandReadArtifact
)

func classify(args []string) commandKind {
	if len(args) < 2 {
		return commandOther
	}
	switch args[0] + "\x00" + args[1] {
	case "hook\x00attach":
		return commandAttach
	case "hook\x00end":
		return commandEnd
	case "agent\x00current":
		return commandCurrent
	case "agent\x00submit":
		return commandSubmit
	case "artifact\x00capture":
		return commandCapture
	case "artifact\x00read":
		return commandReadArtifact
	default:
		return commandOther
	}
}

func validArguments(command commandKind, args []string) bool {
	if command == commandReadArtifact {
		if len(args) != 3 {
			return false
		}
		handle, err := agency.NewOpaqueHandle(args[2])
		return err == nil && !handle.IsZero()
	}
	return command != commandOther && len(args) == 3 && args[2] == "--json"
}

func resolveWorkspace(getwd func() (string, error)) (string, error) {
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	current, err := filepath.Abs(physical)
	if err != nil {
		return "", err
	}
	for {
		nodeState := filepath.Join(current, ".memory", "agency")
		info, statErr := os.Lstat(nodeState)
		if statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return nodeState, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("no configured Memory Agency workspace")
		}
		current = parent
	}
}

func classifyClientConstruction(err error) *controlError {
	if errors.Is(err, errUnsafeClientState) {
		return clientStateError()
	}
	return newControlError(codeMemorydUnavailable,
		"Memory Agency local control is unavailable")
}

func clientStateError() *controlError {
	return newControlError(codeAuthenticationFailed,
		"R7 owner-private client state is unsafe")
}
