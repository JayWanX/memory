//go:build windows

package agency

import (
	"errors"

	"github.com/spf13/cobra"
)

const platformAgencyNotice = " Agency operations are not supported on Windows."

var errUnsupported = errors.New("memory agency is not supported on Windows")

func runSetup(*cobra.Command, []string) error       { return errUnsupported }
func runPeerPrepare(*cobra.Command, []string) error { return errUnsupported }
func runPeerEnroll(*cobra.Command, []string) error  { return errUnsupported }
func runServe(*cobra.Command, []string) error       { return errUnsupported }
func runTerminal(*cobra.Command, []string) error    { return errUnsupported }
