// Package claude is the Claude Code adapter. It owns every CC-specific
// concern jind-ai used to inline into internal/session — hook language,
// hooks-settings.json generation, trust-dialog suppression, the shell
// command shape, and the transcript-derived description enhancer.
//
// The type-name Agent implements session.Agent (via the aliases exposed in
// internal/agent). Register instances via the internal/agent/register
// blank-import package so the daemon can Lookup("claude") them at start-up.
package claude

import (
	"strings"
	"sync"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/debug"
)

// claudeLog is shared across the whole adapter (agent / trust / hooks_settings)
// so debug output for CC-specific setup goes to a single logger instance.
var claudeLog = debug.NewLogger("daemon-debug.log")

// The Claude Code adapter caches a handful of pieces so multiple session
// starts don't repeatedly perform expensive or racy work:
//
//   - hooksOnce guards hooks-settings.json generation. The file describes how
//     Claude Code invokes `jin hook`; it must exist once per daemon process
//     and it is safe to reuse across every session (the content is
//     session-independent).
//   - enhancer is the Layer C description enhancer — it holds a transcript
//     reader whose only state is the ~/.claude directory path.
//   - statusSrc is stateless but held as a value so we don't allocate one per
//     hook event.
type Agent struct {
	hooksOnce sync.Once
	hooksPath string
	hooksErr  error

	enhancer  *CCDescriptionEnhancer
	statusSrc *HookStatusSource
}

// New returns a fully-wired Claude Code adapter.
func New() *Agent {
	return &Agent{
		enhancer:  NewCCDescriptionEnhancer(),
		statusSrc: NewHookStatusSource(),
	}
}

// Kind is the identifier jind-ai persists in Session.AgentKind.
func (a *Agent) Kind() string { return "claude" }

// StatusSource returns the CC hook interpreter.
func (a *Agent) StatusSource() agent.StatusSource { return a.statusSrc }

// Description returns the Layer C enhancer that mines the CC transcript for
// a better human-readable label.
func (a *Agent) Description() agent.DescriptionSource { return a.enhancer }

// Transcript returns the Claude Code transcript reader. It is the reference
// implementation of the interface — transcript.Reader's ReadEntries already
// has the signature TranscriptSource declares, so this hands it over as-is
// rather than wrapping it.
func (a *Agent) Transcript() agent.TranscriptSource { return NewTranscriptReader() }

// Setup writes the process-wide hooks-settings.json (exactly once) and the
// per-workDir trust flag. Both failures are logged but do not abort the
// session start — the historical behaviour is "warn and continue", matching
// what Claude Code itself tolerates.
//
// SpawnCommand consults a.hooksPath to decide whether to pass --settings; if
// the hooks file could not be written the flag is simply omitted.
func (a *Agent) Setup(ctx agent.SetupContext) error {
	a.hooksOnce.Do(func() {
		a.hooksPath, a.hooksErr = EnsureHooksSettingsFile(ctx.StateDir, ctx.ExecPath)
		if a.hooksErr != nil {
			claudeLog("[HOOKS] Warning: failed to generate hooks settings: %v", a.hooksErr)
		}
	})
	if err := EnsureTrustState(ctx.WorkDir); err != nil {
		claudeLog("[TRUST] Warning: failed to set trust state: %v", err)
	}
	return nil
}

// HooksSettingsPath returns the cached path written by Setup; empty means
// Setup either hasn't run yet or the write failed.
func (a *Agent) HooksSettingsPath() string { return a.hooksPath }

// ClearInputKeys returns the tmux key sequence Manager.SendPrompt sends
// before each attempt to wipe Claude Code's input line to empty, preventing
// residual text from concatenating with the new prompt. C-u is the standard
// readline kill-line binding and Claude Code's TUI honours it. Adapters may
// return nil to opt out; empty here would mean "opt out" and disable the
// residual-concat protection for claude sessions.
func (a *Agent) ClearInputKeys() []string { return []string{"C-u"} }

// PastePlaceholder returns "": Claude Code receives prompts as keystrokes because
// its placeholder numbers pastes ("#1", "#2", ...) rather than
// measuring them, so a fold would leave nothing to verify against — and at
// 32KB the keystroke path is fast enough that there is nothing to gain.
func (a *Agent) PastePlaceholder(string) string { return "" }

// DismissOverlayKeys returns Escape for prompts that leave a completion
// overlay open, and nil for every other prompt.
//
// Escape is the right key and it is not a free one. Measured on Claude Code
// 2.1.224 against a throwaway tmux server, 3/3 per row:
//
//	prompt                     without Escape                with Escape
//	list @internal/agent       Enter eaten; input left       submitted verbatim
//	                           holding "list
//	                           @internal/agentdocs/"
//	/<ambiguous-prefix>        ran a DIFFERENT command       submitted as sent
//	/<exact-command>           ran as sent                   ran as sent
//	say pong only              submitted                     submitted
//
// The third row is the one that justifies returning keys for bare slash
// commands even though nothing is drawn on screen: a burst
// `send-keys -l` write does not render the slash overlay at all — only
// character-by-character typing does (3/3 either way) — yet the selection is
// live, and SendPrompt's nudge key walks it onto a different entry. Measured
// with a prefix matching two commands: without the nudge the first entry ran,
// with it the second did. Escape discards that selection, so the prompt runs
// as the caller wrote it.
//
// Why not every prompt: Escape also interrupts a running turn (2/3, the
// third run's turn finished first). SendPrompt only sends while a session
// reads as idle, but jin is known to report idle while a sub-agent runs, so
// restricting the key to prompts that can actually open an overlay keeps
// that misfire away from ordinary sends.
func (a *Agent) DismissOverlayKeys(prompt string) []string {
	if !opensCompletionOverlay(prompt) {
		return nil
	}
	return []string{"Escape"}
}

// opensCompletionOverlay reports whether Claude Code still has a completion
// overlay open once prompt has been typed in full.
//
// The overlay survives only while the caret sits inside the token that opened
// it. Measured 3/3 per case: "list @internal/agent" keeps it open, "list
// @internal/agent and say ok" does not, and "explain the fix/send-deadlock
// branch" never opens one because the slash is not the first character of the
// input.
//
// Requiring the "@" at the START of the final token is measured, not assumed —
// a closing delimiter puts the caret outside the token and the overlay is
// gone. A backtick-wrapped path (3/3), a double-quoted one (2/2) and an email
// address mid-sentence (1/1) all drew no overlay and submitted verbatim, so
// returning false for them is right rather than a gap. See overlay_test.go for
// the exact prompts.
//
// Whitespace is dropped before the check, which errs toward returning true for
// a trailing space. That is the safe direction for the cases the two rules
// cover: an unnecessary Escape was measured harmless on a prompt with no
// overlay (3/3), while a missed one leaves the defect exactly as it was. It
// says nothing about a trigger neither rule names — that is missed, not
// caught.
//
// "#" was measured to draw no overlay (3/3) and is deliberately absent. What
// its Enter does was not measured: "#" opens a save-destination dialog, and
// finding out costs a write to a memory file.
func opensCompletionOverlay(prompt string) bool {
	// One split, read by both rules — they are two readings of the same
	// tokenization, not two independent parses. Fields splits on newlines
	// too, so the final token is the prompt's, not its first line's.
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return false
	}
	// A slash command is the whole input or it is not a command at all.
	if len(fields) == 1 && strings.HasPrefix(fields[0], "/") {
		return true
	}
	// A file reference keeps its picker open only while it is the last thing
	// typed.
	return strings.HasPrefix(fields[len(fields)-1], "@")
}
