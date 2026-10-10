package agency

import (
	"errors"

	"github.com/spf13/cobra"
)

type commandFailure struct {
	code int
	err  error
}

func (failure commandFailure) Error() string {
	if failure.err == nil {
		return ""
	}
	return failure.err.Error()
}

// ExitCode reports the process status carried by an Agency command failure.
// Ordinary Cobra validation errors are intentionally not classified here.
func ExitCode(err error) (int, bool) {
	var failure commandFailure
	if !errors.As(err, &failure) {
		return 0, false
	}
	return failure.code, true
}

func newCommand(version string) *cobra.Command {
	command := &cobra.Command{
		Use:     "agency",
		Short:   "Manage durable Agent work and peer collaboration",
		Long:    "Memory Agency adds durable project-local responsibility and admitted effects to an existing Agent Runtime." + platformAgencyNotice,
		Version: version,
		Args:    cobra.NoArgs,
		RunE:    showCommandHelp,
	}
	command.SetVersionTemplate("memory agency version {{.Version}}\n")
	return command
}

func showCommandHelp(command *cobra.Command, _ []string) error {
	if err := command.Help(); err != nil {
		return commandFailure{code: 1, err: err}
	}
	return nil
}
