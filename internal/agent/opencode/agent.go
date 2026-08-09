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
// setupMu guards configDir on both sides because the directory belongs to the
// Manager whose SetupContext named it, while this adapter belongs to the
// process. Setup runs per spawn, from a per-session goroutine, and a process
// hosting two Managers — the e2e suite builds a daemon per test — writes two
// different directories into this one field while SpawnCommand reads it.
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

// RecognizesSessionID answers with hasSessionIDPrefix — the LOOSE predicate,
// not isSessionID beside it — and the choice is the point.
//
// This adapter is the one that cannot afford a wrong refusal. opencode mints
// its own id and reports it through the plugin, and a resume that does not
// happen starts a new session with nothing saying so: the operator's
// conversation is simply absent. Answering with the same predicate
// SpawnCommand already gates the resume on makes the two sets identical, so
// every id this accepts is an id that would be resumed, and every id this
// refuses is one the resume path would have ignored anyway. The gate therefore
// adds no silent failure that was not already there.
//
// isSessionID would break that. Its base62 alphabet is evidence about today's
// ids rather than a rule opencode promised, and using it here would make an
// upstream alphabet change lose conversations quietly — the exact trade its own
// doc comment refuses on the resume path. Injection is not the reason to reach
// for the strict test: Manager's safeAgentSessionID has already refused every
// shell metacharacter before this is called.
func (a *Agent) RecognizesSessionID(id string) bool { return hasSessionIDPrefix(id) }

// Setup materialises the bundled plugin under
// <StateDir>/opencode/plugin/jin.ts and records the directory SpawnCommand
// hands to opencode via OPENCODE_CONFIG_DIR.
//
// Failures are logged and swallowed: the session must still start. Losing
// the plugin costs live status reporting (the session falls back to
// pane-death detection), which is strictly better than refusing to launch
// the agent at all.
// A failure deliberately leaves the previously recorded directory in place.
// Setup is called once per spawn, from a per-session goroutine, against one
// shared adapter — so clearing the field here would let a failure on one
// session silently disable status reporting for every other session already
// running.
//
// That directory belongs to the Manager that last succeeded, and the adapter
// outlives any one of them (see the contract on session.Agent.Setup), so on a
// failure it can name a state directory this ctx does not own. This adapter
// keeps it even so, which TestAgent_SetupFailure_KeepsPreviousConfigDir pins;
// the Claude Code adapter answers the other way, re-deriving inside the ctx's
// own state directory. What either answer costs opencode has not been measured
// here, so the difference is a gap left alone rather than a considered
// asymmetry.
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

// Transcript returns the reader that asks opencode to print the session.
//
// Never nil. Whether the read can actually happen — whether `opencode` is on
// the daemon's PATH — is decided per call, and the answer when it is not is an
// error naming the reason. Returning nil here instead would report "this
// adapter has no transcript reader", which is a different and untrue thing.
//
// Built per call rather than cached, like the Codex adapter: the reader is one
// word wide and the allocation is noise next to spawning a process.
func (a *Agent) Transcript() agent.TranscriptSource { return NewTranscriptReader() }

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

// DismissOverlayKeys returns nil: opencode's completion behaviour has not
// been measured, so there is nothing to base a key on.
//
// Claude Code was found to leave a completion overlay open for prompts that
// end in an in-progress token, which then eats the Enter that SendPrompt
// presses. Whether opencode does the same is unknown — the run that would
// have settled it could not be made, because the local install has no
// provider configured and never reaches a usable prompt.
//
// This adapter also takes the paste transport, where the whole prompt
// arrives as one bracketed write rather than as typing, so it is not even
// clear that a completion would be triggered. Both reasons point the same
// way: opt out, stay byte-identical to the pre-fix behaviour, and measure
// before sending a key that is destructive when it misfires.
func (a *Agent) DismissOverlayKeys(string) []string { return nil }

// DetectBlock reports BlockNone always. Unlike the Codex adapter next to it,
// this one opts out with the measurements already in hand — they are written
// down here so that finishing the job is a coding task rather than another
// round of measuring.
//
// Measured against opencode 1.18.4 with `permission: {bash: "ask"}`, on a
// throwaway tmux server. The dialog is a horizontal button row, not a
// numbered list:
//
//	△ Permission required
//	  # Shell command
//	$ sha256sum /etc/hostname
//	 Allow once   Allow always   Reject    ctrl+f fullscreen  ⇆ select  enter confirm
//
//	key / input          effect
//	C-u                  inert (2/2)
//	typed prose          not drawn, not buffered — but an `h` or `l` INSIDE
//	                     it moves the selection (2/2)
//	a digit              inert (1/1) — there are no numbers to address
//	Down, Tab            inert (1/1, 2/2)
//	Right / l            selection moves right (2/2)
//	h                    selection moves left (2/2)
//	bracketed paste      inert; not drawn, not buffered (1/1). This is the
//	                     transport PastePlaceholder above selects, so it is
//	                     the one that would actually be used
//	Escape               dismisses the dialog (3/3)
//	at either end        the selection WRAPS: `h` on "Allow once" lands on
//	                     "Reject" (1/1)
//	initial selection    "Allow once" (3/3)
//
// Two of those decide the shape of any implementation. Because the selection
// wraps there is no home position, so a fixed number of moves cannot address
// a button — the current one has to be read first. It can be: the highlight
// is an SGR background in `capture-pane -e` output and was read correctly
// 5/5. So the sequence is read, move, read again to confirm the intended
// label is lit, then Enter — verify-by-capture pointed at the selection
// rather than at typed text, since typed text is never drawn here.
//
// It is left unimplemented because that sequence has a failure mode the
// Claude path does not: its correctness rests on reading a position, and a
// misread moves to a different button and confirms it. On a permission
// dialog that means approving something nobody approved. Claude Code's
// digit is absolute and needs no position at all, so shipping both together
// would put two very different risks behind one flag.
func (a *Agent) DetectBlock(string) agent.BlockKind { return agent.BlockNone }

// AnswerBlockKeys refuses always, for the reason DetectBlock returns
// BlockNone. See there for the measurements an implementation would use.
func (a *Agent) AnswerBlockKeys(agent.BlockKind, string, agent.BlockAnswer) ([]agent.KeyStep, error) {
	return nil, fmt.Errorf("opencode sessions cannot be answered by jin yet: " +
		"its permission dialog is driven by moving a selection rather than by typing a choice. " +
		"Attach the session and answer it directly")
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
