package attach

import (
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	guideAsset            = "assets/memoryd.md"
	cueAsset              = "assets/hook-cue.txt"
	extensionAsset        = "assets/pi/memoryd.ts"
	currentExtensionAsset = "assets/pi/memoryd-current.ts"

	MaxGuideBytes     = 4 << 10
	MaxCueBytes       = 160
	MaxExtensionBytes = 8 << 10
)

//go:embed assets/memoryd.md assets/hook-cue.txt assets/pi/memoryd.ts assets/pi/memoryd-current.ts
var projectionFS embed.FS

// Projection is the complete, immutable Pi-facing R7 surface. It contains no
// runtime state; callers receive copies of all mutable byte slices.
type Projection struct {
	guide            []byte
	cue              string
	extension        []byte
	currentExtension []byte
}

// Load verifies and returns the embedded, pattern-neutral R7 assets.
func Load() (Projection, error) {
	guide, err := projectionFS.ReadFile(guideAsset)
	if err != nil {
		return Projection{}, err
	}
	cueBytes, err := projectionFS.ReadFile(cueAsset)
	if err != nil {
		return Projection{}, err
	}
	extension, err := projectionFS.ReadFile(extensionAsset)
	if err != nil {
		return Projection{}, err
	}
	currentExtension, err := projectionFS.ReadFile(currentExtensionAsset)
	if err != nil {
		return Projection{}, err
	}
	if err := validateText("guide", guide, MaxGuideBytes); err != nil {
		return Projection{}, err
	}
	if err := validateText("cue", cueBytes, MaxCueBytes); err != nil {
		return Projection{}, err
	}
	if err := validateText("Pi extension", extension, MaxExtensionBytes); err != nil {
		return Projection{}, err
	}
	if err := validateText("Pi current extension", currentExtension, MaxExtensionBytes); err != nil {
		return Projection{}, err
	}
	cue := strings.TrimSuffix(string(cueBytes), "\n")
	if cue == "" || strings.ContainsAny(cue, "\r\n") {
		return Projection{}, errors.New("attach: cue must be one nonempty line")
	}
	if err := validateNeutralProjection(guide, cue, extension, currentExtension); err != nil {
		return Projection{}, err
	}
	return Projection{guide: clone(guide), cue: cue, extension: clone(extension),
		currentExtension: clone(currentExtension)}, nil
}

func (projection Projection) Guide() []byte { return clone(projection.guide) }

func (projection Projection) HookCue() string { return projection.cue }

func (projection Projection) PiExtension() []byte { return clone(projection.extension) }

func (projection Projection) PiCurrentExtension() []byte {
	return clone(projection.currentExtension)
}

func validateText(name string, content []byte, maximum int) error {
	if len(content) == 0 || len(content) > maximum || !utf8.Valid(content) ||
		strings.IndexByte(string(content), 0) >= 0 {
		return fmt.Errorf("attach: %s is not bounded UTF-8", name)
	}
	return nil
}

func validateNeutralProjection(guide []byte, cue string, extension, currentExtension []byte) error {
	base := strings.ToLower(string(guide) + "\n" + cue + "\n" + string(extension))
	all := base + "\n" + strings.ToLower(string(currentExtension))
	for _, forbidden := range []string{
		"--event-id", "--operation-id", "--principal", "--fence", "--peer-id",
		"deepseek", "api_key", "api-key", "authorization:", "bearer ", "sk-",
	} {
		if strings.Contains(all, forbidden) {
			return fmt.Errorf("attach: projection contains forbidden surface %q", forbidden)
		}
	}
	source := string(extension)
	if strings.Count(source, "content: HOOK_CUE") != 1 ||
		strings.Count(source, "function parseOutput(") != 1 ||
		strings.Count(source, "JSON.parse(raw)") != 1 ||
		!strings.Contains(source, "const HOOK_CUE = "+strconv.Quote(cue)+";") ||
		!strings.Contains(source, `pi.on("before_agent_start"`) ||
		!strings.Contains(source, `pi.on("agent_settled"`) ||
		!strings.Contains(source, `pi.on("session_shutdown"`) ||
		!strings.Contains(source, `execFileSync("memory", ["agency", ...args]`) ||
		!strings.Contains(source,
			`execFile("memory", ["agency", "agent", "submit", "--json"]`) ||
		!strings.Contains(source, `receipt.schema !== "memory.agent.receipt"`) {
		return errors.New("attach: Pi extension does not have fixed lifecycle, command, cue, and Receipt boundaries")
	}
	for _, forbidden := range []string{
		"process.env",
		"content: output", "content: result", "text: raw", "text: output",
		"text: result", "event_id", "eventid", "payload",
		"transcript", "credential", "console.", "--socket", "setactivetools",
		"getactivetools", `pi.on("tool_call"`, `pi.on("turn_start"`, "ctx.abort",
	} {
		if strings.Contains(strings.ToLower(source), forbidden) {
			return fmt.Errorf("attach: Pi extension carries runtime data %q", forbidden)
		}
	}
	currentSource := string(currentExtension)
	for _, required := range []string{
		`name: CURRENT_TOOL`, `execFile("memory", ["agency", "agent", "current", "--json"]`,
		`shell: false`, `setTimeout(interrupt, CURRENT_TIMEOUT_MS)`,
		`CURRENT_SHUTDOWN_GRACE_MS`, `child.kill(signal)`, `"SIGTERM"`, `"SIGKILL"`,
		`removeEventListener("abort", interrupt)`, `maxBuffer: MAX_CURRENT_OUTPUT_BYTES`,
		`value = JSON.parse(raw)`, `details: { schema: "memory.pi.current", version: 1, status }`,
	} {
		if !strings.Contains(currentSource, required) {
			return fmt.Errorf("attach: Pi current extension lacks %q", required)
		}
	}
	return nil
}

func clone(content []byte) []byte { return append([]byte(nil), content...) }
