package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/takaaki-s/jind-ai/internal/agentdocs"
	"github.com/takaaki-s/jind-ai/internal/atomicfile"
)

// hooksSettingsTmpPattern names the in-flight temp file. Nothing scans
// stateDir by glob — every other name under it is built literally — so the
// only requirement is that it not collide with the published name. A crash
// between creating it and the rename leaves one behind, and nothing sweeps
// stateDir; the .jin- prefix is what makes such a file attributable, the same
// reasoning the other temp patterns in this package and in the opencode
// adapter give.
const hooksSettingsTmpPattern = ".jin-hooks-*.tmp"

// hooksSettingsFileName is the only spelling of the name in this package.
const hooksSettingsFileName = "hooks-settings.json"

func hooksSettingsPath(stateDir string) string {
	return filepath.Join(stateDir, hooksSettingsFileName)
}

// usableHooksSettings reports whether data can serve as a settings file. It is
// the package's one definition of "usable", shared by the fallback below and
// by the test that watches for a torn read.
//
// Which half catches what is not obvious: encoding/json validates a whole
// document before decoding any of it, so a syntax error leaves settings
// untouched and is caught by the length rather than by the error. What the
// error catches on its own is valid JSON that disagrees about a field's type.
func usableHooksSettings(data []byte) bool {
	var settings hooksSettings
	return json.Unmarshal(data, &settings) == nil && len(settings.Hooks) > 0
}

// existingHooksSettings returns the hooks file already sitting in stateDir, or
// "" if there is none or it cannot serve as one. It answers "what can THIS
// state directory still offer?", which is a different question from "what did
// the last successful Setup produce?" — the second can name a directory
// belonging to a different Manager, and the first cannot.
//
// It reads rather than stats because the caller is on a failure path, so what
// is on disk was left by some earlier write and a rename-published file is
// only one of the things it can be: a version that wrote in place could have
// been killed mid-truncate. Handing Claude Code a settings file it reads as
// empty is worse than handing it none, because none is a documented fallback
// and the other is silent. What parses is served as it stands, so a file an
// older jind-ai published is accepted.
func existingHooksSettings(stateDir string) string {
	path := hooksSettingsPath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil || !usableHooksSettings(data) {
		return ""
	}
	return path
}

// hooksEntry is a single hook command entry — one row inside a matcher's
// "hooks" array in ~/.claude/settings*.json.
type hooksEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// hooksMatcher is a hook event matcher plus its handler list. Matcher is
// only set for events that support one (Notification uses a "|"-joined
// regex-like string).
type hooksMatcher struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []hooksEntry `json:"hooks"`
}

// hooksSettings is the top-level shape of the file we write.
type hooksSettings struct {
	Hooks map[string][]hooksMatcher `json:"hooks"`
}

// EnsureHooksSettingsFile generates hooks-settings.json inside stateDir so
// Claude Code (started via `claude --settings <path>`) invokes `jin hook`
// on every event the daemon cares about.
//
// The file is written on every call — it's cheap, and always-write means the
// command path stays correct if the jin binary was upgraded / moved. Returns
// the absolute path to the generated file.
func EnsureHooksSettingsFile(stateDir, execPath string) (string, error) {
	entry := hooksEntry{
		Type:    "command",
		Command: agentdocs.HookCommand(execPath, false),
		// Claude Code kills the hook child at this many seconds, so this is the
		// real ceiling on what a wedged daemon costs a Claude session. Keep it
		// in step with the two other layers that bound the same exchange: the
		// client-side read deadline (daemon.hookRequestTimeout, 10s) and the
		// Codex per-hook budget (codex.hookTimeoutMillis, 10000ms).
		Timeout: 10,
	}
	// SessionStart is the one event whose hook also writes to stdout: Claude
	// Code adds that stdout to the session's context, which is how a child
	// learns `jin docs` exists. See agentdocs.HookCommand for why the flag,
	// rather than the event name, is what gates the output.
	contextEntry := entry
	contextEntry.Command = agentdocs.HookCommand(execPath, true)

	settings := hooksSettings{
		Hooks: map[string][]hooksMatcher{
			"UserPromptSubmit": {{Hooks: []hooksEntry{entry}}},
			"Stop":             {{Hooks: []hooksEntry{entry}}},
			"StopFailure":      {{Hooks: []hooksEntry{entry}}},
			"PostToolUse":      {{Hooks: []hooksEntry{entry}}},
			"CwdChanged":       {{Hooks: []hooksEntry{entry}}},
			"SessionStart":     {{Hooks: []hooksEntry{contextEntry}}},
			"SessionEnd":       {{Hooks: []hooksEntry{entry}}},
			"Notification": {{
				Matcher: "permission_prompt|elicitation_dialog|idle_prompt",
				Hooks:   []hooksEntry{entry},
			}},
		},
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal hooks settings: %w", err)
	}

	path := hooksSettingsPath(stateDir)
	// 0600: match trust state and session store; the file is single-user.
	//
	// Published atomically rather than written in place, and the reason is
	// what Setup now does: this runs at every session start, so a rewrite can
	// land while another session's Claude Code is starting up and reading the
	// same file through --settings. os.WriteFile opens with O_TRUNC, which
	// leaves the file at length zero for the middle of each write — measured
	// at 28-30 microseconds per rewrite, roughly half of the call. While the
	// write happened once per process no session was starting up alongside it.
	// (Whether a Claude Code already running re-reads the file is unmeasured,
	// so this says nothing about one; a tmux session outlives a daemon
	// restart, so such a reader does exist.) ~/.claude.json is temp-and-renamed
	// for exactly this reason (docs/gotchas.md), and hooks-settings.json has
	// now joined it. The rename replaces a symlink at this path rather than
	// following it — acceptable inside jind-ai's own state directory, unlike
	// ~/.claude.json, where EnsureTrustState resolves one first.
	if err := atomicfile.Write(path, data, 0600, hooksSettingsTmpPattern); err != nil {
		return "", fmt.Errorf("failed to write hooks settings file: %w", err)
	}

	claudeLog("[HOOKS] Wrote hooks settings to %s", path)
	return path, nil
}
