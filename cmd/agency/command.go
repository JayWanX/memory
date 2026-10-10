package agency

import "github.com/spf13/cobra"

// New returns a fresh Agency command tree for the Memory product root.
func New(version string) *cobra.Command {
	command := newCommand(version)
	command.AddCommand(setupCommand(), peerCommand(), serveCommand())

	// These machine surfaces keep the exact grammar owned by agencyclient.
	for _, name := range []string{"hook", "agent", "artifact"} {
		command.AddCommand(&cobra.Command{
			Use:                name,
			Hidden:             true,
			DisableFlagParsing: true,
			RunE:               runTerminal,
		})
	}
	return command
}
