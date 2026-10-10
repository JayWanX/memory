//go:build !windows

package agency

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JayWanX/memory/internal/daemon"
	"github.com/spf13/cobra"
)

const gracefulShutdownBudget = 5 * time.Second

func runServe(command *cobra.Command, _ []string) error {
	stateDirectory, err := command.Flags().GetString("state-dir")
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency serve: %w", err)}
	}
	if strings.TrimSpace(stateDirectory) == "" {
		return errors.New("memory agency serve: requires --state-dir")
	}
	resolved, err := resolveStateDirectory(stateDirectory)
	if err == nil {
		lifetime, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = serveDaemon(lifetime, resolved)
	}
	if err != nil {
		return commandFailure{code: 1, err: fmt.Errorf("memory agency serve: %w", err)}
	}
	return nil
}

func serveDaemon(ctx context.Context, stateDirectory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := daemon.OpenProvisioned(ctx, stateDirectory)
	if err != nil {
		return err
	}
	serveErr := runtime.Serve(ctx)
	closeContext, cancel := context.WithTimeout(context.Background(), gracefulShutdownBudget)
	closeErr := runtime.Close(closeContext)
	cancel()
	return errors.Join(serveErr, closeErr)
}

func resolveStateDirectory(requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", errors.New("serve state directory is empty")
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", err)
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("serve state directory must be a real directory")
	}
	return filepath.Clean(resolved), nil
}
