//go:build !windows

package agency

import (
	"github.com/JayWanX/memory/internal/agency/client"
	"github.com/JayWanX/memory/internal/daemon"
	"github.com/spf13/cobra"
)

func runTerminal(command *cobra.Command, args []string) error {
	code := agencyclient.Run(command.Context(), append([]string{command.Name()}, args...),
		command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), daemon.Ensure)
	if code != 0 {
		// agencyclient has already emitted the bounded machine diagnostic.
		return commandFailure{code: code}
	}
	return nil
}
