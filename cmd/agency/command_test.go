//go:build !windows

package agency

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestAgencyHelpAndVersion(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "empty", want: "Available Commands:"},
		{name: "help", args: []string{"--help"}, want: "Available Commands:"},
		{name: "version flag", args: []string{"--version"}, want: "memory agency version test-version\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, exit := executeAgency(test.args, "", "test-version")
			if exit != 0 || stderr != "" || !strings.Contains(stdout, test.want) {
				t.Fatalf("Run(%q) = exit %d stdout %q stderr %q", test.args, exit, stdout, stderr)
			}
		})
	}
}

func TestAgencyCommandsDeclareTheirOwnHelp(t *testing.T) {
	for _, test := range []struct {
		args []string
		want []string
	}{
		{args: []string{"--help"}, want: []string{"peer", "serve", "setup"}},
		{args: []string{"setup", "--help"}, want: []string{"--project-root", "--runtime"}},
		{args: []string{"peer", "--help"}, want: []string{"enroll", "prepare"}},
		{args: []string{"peer", "prepare", "--help"}, want: []string{"--advertise", "--listen", "--project-root"}},
		{args: []string{"peer", "enroll", "--help"}, want: []string{"--alias", "--project-root"}},
		{args: []string{"serve", "--help"}, want: []string{"--state-dir"}},
	} {
		stdout, stderr, exit := executeAgency(test.args, "", "dev")
		if exit != 0 || stderr != "" {
			t.Fatalf("Run(%q) = exit %d stdout %q stderr %q", test.args, exit, stdout, stderr)
		}
		for _, text := range test.want {
			if !strings.Contains(stdout, text) {
				t.Errorf("Run(%q) help lacks %q\n%s", test.args, text, stdout)
			}
		}
	}
}

func TestAgencyHelpHidesMachineAndRetiredCommands(t *testing.T) {
	stdout, stderr, exit := executeAgency([]string{"--help"}, "", "dev")
	if exit != 0 || stderr != "" {
		t.Fatalf("help = exit %d stderr %q", exit, stderr)
	}
	lower := strings.ToLower(stdout)
	for _, forbidden := range []string{"hook", "artifact", "r5", "channel", "teamwork",
		"codex", "eject", "doctor", "status", "reset", "managed", "review", "workflow"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("help contains private or retired vocabulary %q", forbidden)
		}
	}
}

func TestAgencyRejectsUnknownCommandsAsUsageErrors(t *testing.T) {
	for _, command := range []string{"channel", "teamwork", "status", "doctor", "eject",
		"reset", "sync", "daemon", "unknown"} {
		stdout, stderr, exit := executeAgency([]string{command}, "", "dev")
		if exit != 2 || stdout != "" || !strings.Contains(stderr, "unknown command") {
			t.Errorf("unknown %q = exit %d stdout %q stderr %q", command, exit, stdout, stderr)
		}
	}
}

func TestHiddenTerminalKeepsItsExactGrammarAndExitStatus(t *testing.T) {
	for _, test := range []struct {
		args  []string
		input string
	}{
		{args: []string{"hook", "attach", "--json"}, input: "{}"},
		{args: []string{"agent", "current"}},
		{args: []string{"agent", "submit"}},
		{args: []string{"artifact", "capture"}},
		{args: []string{"artifact", "read", ""}},
	} {
		stdout, stderr, exit := executeAgency(test.args, test.input, "dev")
		if exit != 2 || stderr != "" || !strings.Contains(stdout, `"code":"invalid_argument"`) {
			t.Fatalf("hidden %q = exit %d stdout %q stderr %q", test.args, exit, stdout, stderr)
		}
	}
}

func executeAgency(args []string, input, version string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	root := &cobra.Command{Use: "memory", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(New(version))
	root.SetArgs(append([]string{"agency"}, args...))
	root.SetIn(strings.NewReader(input))
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
