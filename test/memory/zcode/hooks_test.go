package zcode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/memory/setup"
	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

var stopCases = []struct {
	name, input string
	block       bool
}{
	{"ordinary", `{"stop_hook_active":false,"last_assistant_message":"Done."}`, true},
	{"ordinary Chinese", `{"last_assistant_message":"中文任务完成。"}`, true},
	{"active", `{"stop_hook_active":true}`, false},
	{"active Chinese", `{"stop_hook_active":true,"last_assistant_message":"中"}`, false},
	{"active escaped Unicode", `{"stop_hook_active":true,"last_assistant_message":"中文\"引号\"，路径 C:\\工作 😀"}`, false},
	{"English evaluation", `{"last_assistant_message":"No Durable Memory is needed."}`, false},
	{"language neutral marker", `{"last_assistant_message":"[Memory] 本轮无需保存。"}`, false},
	{"Chinese no memory", `{"last_assistant_message":"无需写入持久记忆"}`, false},
	{"Chinese memory saved", `{"last_assistant_message":"已写入记忆"}`, false},
	// Preserve the existing reminder behavior for missing or malformed payloads.
	{"empty", "", true},
	{"empty object", `{}`, true},
	{"malformed", `{"stop_hook_active":true,`, true},
}

func TestStopShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows installs PowerShell hooks")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	script := writeFile(t, t.TempDir(), "stop.sh", assets.ZCodeStopHook)
	for _, tc := range stopCases {
		t.Run(tc.name, func(t *testing.T) {
			out := run(t, bash, []string{script}, tc.input, os.Environ())
			assertStop(t, out, tc.block)
		})
	}
}

func TestPowerShellHooks(t *testing.T) {
	powershell := os.Getenv("MEMORY_TEST_POWERSHELL")
	if runtime.GOOS == "windows" {
		// Never silently substitute PowerShell 7 for the affected Windows host.
		var err error
		powershell, err = exec.LookPath("powershell.exe")
		if err != nil {
			t.Fatalf("Windows PowerShell 5.1 is required: %v", err)
		}
	} else if powershell == "" {
		t.Skip("set MEMORY_TEST_POWERSHELL for a portable PowerShell probe; Windows runs 5.1")
	}

	home := filepath.Join(t.TempDir(), "ZCode 测试 home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := productBinary(t)
	env := isolatedEnv(home, bin)
	version := run(t, powershell, []string{"-NoProfile", "-NonInteractive", "-Command",
		`[ordered]@{ major = $PSVersionTable.PSVersion.Major; minor = $PSVersionTable.PSVersion.Minor; ansi = [Text.Encoding]::Default.CodePage } | ConvertTo-Json -Compress`}, "", env)
	var host struct{ Major, Minor, ANSI int }
	if err := json.Unmarshal(version, &host); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && (host.Major != 5 || host.Minor != 1) {
		t.Fatalf("expected Windows PowerShell 5.1, got %s", version)
	}
	t.Logf("PowerShell host: %s", bytes.TrimSpace(version))
	run(t, bin, []string{"setup", "--target", "zcode", "--global", "--yes"}, "", env)
	hooks := installedHooks(t, home)
	writeFile(t, filepath.Join(home, ".memory", "prompt"), "guide.md", []byte("ZCode test guide."))
	wrapper := writeFile(t, home, "invoke.ps1", []byte(`
[Console]::InputEncoding = [Text.Encoding]::GetEncoding([int]$env:MEMORY_TEST_CODEPAGE)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
& $env:MEMORY_TEST_HOOK
`))
	legacy, err := filepath.Abs("../../../testdata/memory/zcode/stop-before-142.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, codepage := range []int{65001, 936} {
		t.Run("codepage="+strconv.Itoa(codepage), func(t *testing.T) {
			invoke := func(t *testing.T, script, input string) []byte {
				t.Helper()
				hookEnv := append(append([]string{}, env...), "MEMORY_TEST_CODEPAGE="+strconv.Itoa(codepage), "MEMORY_TEST_HOOK="+script)
				return run(t, powershell, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", wrapper}, input, hookEnv)
			}
			// Execute the old asset too: the CP936 case loses the active guard,
			// and the Chinese evaluation is missed even with correct UTF-8 input.
			t.Run("legacy active guard control", func(t *testing.T) {
				assertStop(t, invoke(t, legacy, `{"stop_hook_active":true,"last_assistant_message":"中"}`), codepage == 936)
			})
			t.Run("legacy Chinese evaluation control", func(t *testing.T) {
				assertStop(t, invoke(t, legacy, `{"last_assistant_message":"无需写入持久记忆"}`), true)
			})
			t.Run("null PowerShell payload", func(t *testing.T) {
				assertStop(t, invoke(t, hooks["stop.ps1"], `null`), true)
			})
			for _, tc := range stopCases {
				t.Run(tc.name, func(t *testing.T) {
					out := invoke(t, hooks["stop.ps1"], tc.input)
					assertStop(t, out, tc.block)
					if tc.block && !bytes.Contains(out, []byte("Include [memory]")) {
						t.Fatal("reminder must explain the completion marker")
					}
				})
			}
			for _, tc := range []struct{ script, event, context string }{
				{"user_prompt.ps1", "UserPromptSubmit", "[memory] Evaluate: recall needed? After responding, evaluate: remember needed?"},
				{"prime.ps1", "SessionStart", "[memory] Memory active (0 insights, 0 edges).\nZCode test guide."},
			} {
				t.Run(tc.event, func(t *testing.T) {
					out := invoke(t, hooks[tc.script], `{"prompt":"中文输入 😀"}`)
					var result struct {
						HookSpecificOutput struct{ HookEventName, AdditionalContext string }
					}
					if err := json.Unmarshal(out, &result); err != nil {
						t.Fatalf("decode hook output %q: %v", out, err)
					}
					if result.HookSpecificOutput.HookEventName != tc.event || result.HookSpecificOutput.AdditionalContext != tc.context {
						t.Fatalf("unexpected hook output: %s", out)
					}
				})
			}
		})
	}
}

func installedHooks(t *testing.T, home string) map[string]string {
	t.Helper()
	config := filepath.Join(home, ".zcode")
	hooks := map[string]string{}
	for _, hook := range []struct {
		name string
		body []byte
	}{
		{"stop.ps1", assets.ZCodeStopHookPowerShell},
		{"prime.ps1", assets.ZCodePrimeHookPowerShell},
		{"user_prompt.ps1", assets.ZCodeUserPromptHookPowerShell},
	} {
		// A portable probe uses the production writer because setup on Unix
		// selects shell scripts. Windows checks the actual setup CLI output.
		if runtime.GOOS != "windows" {
			if _, err := setup.ZCodeWriteHook(config, hook.name, hook.body); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(config, "hooks", "memory", hook.name)
		installed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(installed, hook.body) {
			t.Fatalf("installed %s differs from its embedded asset", hook.name)
		}
		if hook.name == "stop.ps1" && !bytes.HasPrefix(installed, []byte{0xef, 0xbb, 0xbf}) {
			t.Fatal("installed Stop script must have a UTF-8 BOM for PowerShell 5.1")
		}
		hooks[hook.name] = path
	}
	return hooks
}

func productBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("MEMORY_TEST_BIN"); bin != "" {
		abs, err := filepath.Abs(bin)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	bin := filepath.Join(t.TempDir(), "memory")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "../../..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build product: %v\n%s", err, out)
	}
	return bin
}

func isolatedEnv(home, bin string) []string {
	var env []string
	for _, value := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(value, "=", 2)[0])
		if strings.HasPrefix(key, "MEMORY_") || key == "HOME" || key == "USERPROFILE" || key == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, value)
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"MEMORY_DATA_DIR="+filepath.Join(home, ".memory"), "MEMORY_BIN="+bin, "MEMORY_EMBED_ENDPOINT=http://127.0.0.1:1")
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func run(t *testing.T, executable string, args []string, input string, env []string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("process failed: %v\nstdout: %s\nstderr: %s", err, out, &stderr)
	}
	return out
}

func assertStop(t *testing.T, out []byte, block bool) {
	t.Helper()
	if !block {
		if len(bytes.TrimSpace(out)) != 0 {
			t.Fatalf("expected silent exit, got %s", out)
		}
		return
	}
	var result struct{ Decision, Reason string }
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode stop output %q: %v", out, err)
	}
	if result.Decision != "block" || result.Reason == "" {
		t.Fatalf("expected one blocking reminder, got %s", out)
	}
}
