// Package opencode is the opencode (sst/opencode) adapter. opencode has no
// hook-command surface of its own — its extension point is a Bun-runtime
// TypeScript plugin — so this adapter carries a plugin in its binary,
// materialises it under jind-ai's own state directory, and points opencode
// at that directory with OPENCODE_CONFIG_DIR.
//
// OPENCODE_CONFIG_DIR is additive, not a replacement: opencode's
// ConfigPaths.directories() returns
// unique([~/.config/opencode, ...project .opencode dirs, ...$OPENCODE_CONFIG_DIR]),
// so the user's own agents / commands / plugins keep loading. That property
// is what lets jind-ai wire up status reporting without ever writing to
// ~/.config/opencode or to the user's repository — the same "adapters must
// not write to user-global config" rule the Claude and Codex adapters follow.
//
// The type-name Agent implements session.Agent (via the aliases exposed in
// internal/agent). Register instances via the internal/agent/register
// blank-import package so the daemon can Lookup("opencode") them at start-up.
package opencode

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/debug"
)

var opencodeLog = debug.NewLogger("daemon-debug.log")

// Agent is the process-wide opencode adapter state.
//
//   - configDir is the OPENCODE_CONFIG_DIR value Setup materialised the
//     plugin into. Empty means Setup has not run or the write failed; the
//     spawn path then omits the env var entirely and opencode starts
//     without the jind-ai plugin (see SpawnCommand for the fail-open
//     rationale).
//   - statusSrc is cached so hot-path StatusSource() calls on every hook
//     don't reallocate.
//
// setupMu guards configDir because Setup runs once per session start (not
// once per process): the plugin is rewritten whenever the jin executable
// path changes, so a sync.Once would pin a stale path after a reinstall.
type Agent struct {
	setupMu   sync.Mutex
	configDir string
	statusSrc *EventStatusSource
}

// New returns a fully-wired opencode adapter.
func New() *Agent {
	return &Agent{statusSrc: NewEventStatusSource()}
}

// Kind is the identifier jind-ai persists in Session.AgentKind.
func (a *Agent) Kind() string { return "opencode" }

// Setup materialises the bundled plugin under
// <StateDir>/opencode/plugin/jin.ts and records the directory SpawnCommand
// hands to opencode via OPENCODE_CONFIG_DIR.
//
// Failures are logged and swallowed: the session must still start. Losing
// the plugin costs live status reporting (the session falls back to
// pane-death detection), which is strictly better than refusing to launch
// the agent at all.
// A failure deliberately leaves the previously recorded directory in
// place. Setup is called once per session start, from a per-session
// goroutine, against one shared adapter — so clearing the field here
// would let a failure on one session silently disable status reporting
// for every other session already running. StateDir and ExecPath are
// invariant for the daemon's lifetime, so a directory that worked before
// is still valid now.
func (a *Agent) Setup(ctx agent.SetupContext) error {
	dir, err := WritePlugin(ctx.StateDir, ctx.ExecPath)
	if err != nil {
		opencodeLog("[OPENCODE] Warning: failed to write plugin: %v", err)
		return nil
	}
	// Fail-open, and separately from the plugin: status reporting is what
	// makes the session usable at all, whereas the context is a convenience
	// for the agent inside it. A session that starts without knowing about
	// `jin docs` is far better than one that does not start.
	if err := WriteAgentContext(dir); err != nil {
		opencodeLog("[OPENCODE] Warning: failed to write agent context: %v", err)
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.configDir = dir
	return nil
}

// SpawnCommand delegates to the package-level builder with the config dir
// captured by the most recent successful Setup.
func (a *Agent) SpawnCommand(opts agent.SpawnOptions) agent.SpawnPlan {
	a.setupMu.Lock()
	dir := a.configDir
	a.setupMu.Unlock()
	return SpawnCommand(opts, dir)
}

// StatusSource returns the cached interpreter for the canonical event names
// the bundled plugin normalises opencode's bus events into.
func (a *Agent) StatusSource() agent.StatusSource { return a.statusSrc }

// Description returns nil: the opencode adapter has no Layer C enhancer
// today, so sessions keep whatever description Layer A/B derived. The
// interface explicitly permits nil here.
func (a *Agent) Description() agent.DescriptionSource { return nil }

// ClearInputKeys returns the tmux key sequence Manager.SendPrompt sends
// before each attempt to wipe opencode's input line to empty, preventing
// residual text from concatenating with the new prompt. C-u empties the
// input line, verified against opencode 1.17.18. Adapters may return nil to
// opt out; empty here would mean "opt out" and disable the residual-concat
// protection for opencode sessions.
func (a *Agent) ClearInputKeys() []string { return []string{"C-u"} }

// PastePlaceholder opts opencode into SendPrompt's paste transport, and
// predicts the summary line opencode will render for this prompt.
//
// opencode is the one adapter where typing a prompt is genuinely expensive:
// it renders each character as if keyed, so an 8KB prompt takes 88s and grows
// RSS by 770MB, while the same prompt pasted settles in 0.41s with no growth.
// Past roughly 3KB the keystroke path cannot finish inside the verify budget
// at all, which put a ~2KB ceiling on what a session could be sent.
//
// The cost is a weaker check: a folded paste hides the text, so the line
// count is all there is to compare against. That is worth it here because a
// paste is one atomic write — the chunk-boundary losses the tail match was
// built to catch cannot happen on this path — and because opencode still
// inserts small pastes as plain text, where SendPrompt falls back to matching
// the tail as usual.
//
// The "~" opencode prints is decoration; the number is exact, checked against
// 1, 2, 3, 7, 33, 100, 200, 999, 1000 and 2500 lines over payloads from 900B
// to 62KB. It says "lines" even for one.
func (a *Agent) PastePlaceholder(prompt string) string {
	return fmt.Sprintf("[Pasted ~%d lines]", pasteLineCount(prompt))
}

// pasteLineCount counts prompt's lines the way opencode counts them when it
// summarises a paste. Two details are measured, not assumed, and getting
// either wrong would reject sends that in fact landed:
//
//   - CR is a line break in its own right, so CRLF breaks twice.
//     "a\r\nb\r\nc" is reported as 5 lines, not 3.
//   - Trailing blank or whitespace-only lines are dropped, however many.
//     "a\nb\nc\n", "a\nb\nc\n\n" and "a\nb\nc\n   " are all 3.
//
// Blank lines in the MIDDLE count, and a prompt with no break at all is 1.
// Verified as a prediction on cases the rule was not derived from (6/6).
func pasteLineCount(s string) int {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + strings.Count(s, "\r") + 1
}
