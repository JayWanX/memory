//go:build windows

package agency

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestWindowsAgencyKeepsHelpAndVersionDiscoverable(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"--help"}, want: "Agency operations are not supported on Windows."},
		{args: []string{"--version"}, want: "memory agency version test-version\n"},
		{args: []string{"peer", "prepare", "--help"}, want: "--advertise"},
	} {
		stdout, stderr, exit := executeWindowsAgency(test.args, "test-version")
		if exit != 0 || stderr != "" || !strings.Contains(stdout, test.want) {
			t.Fatalf("Run(%q) = exit %d stdout %q stderr %q", test.args, exit, stdout, stderr)
		}
	}
}

func TestWindowsAgencyRejectsEveryOperationalCommand(t *testing.T) {
	for _, args := range [][]string{
		{"setup"},
		{"peer", "prepare"},
		{"peer", "enroll"},
		{"serve"},
		{"hook", "attach"},
		{"agent", "current"},
		{"artifact", "read"},
	} {
		stdout, stderr, exit := executeWindowsAgency(args, "dev")
		if exit != 2 || stdout != "" || stderr != errUnsupported.Error()+"\n" {
			t.Errorf("Run(%q) = exit %d stdout %q stderr %q", args, exit, stdout, stderr)
		}
	}
}

func executeWindowsAgency(args []string, version string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	root := &cobra.Command{Use: "memory", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(New(version))
	root.SetArgs(append([]string{"agency"}, args...))
	root.SetIn(strings.NewReader(""))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	_, err := root.ExecuteContextC(context.Background())
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	if err.Error() != "" {
		_, _ = fmt.Fprintln(&stderr, err)
	}
	if code, ok := ExitCode(err); ok {
		return stdout.String(), stderr.String(), code
	}
	return stdout.String(), stderr.String(), 2
}
