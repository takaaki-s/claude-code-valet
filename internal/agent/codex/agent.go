// Package codex is the OpenAI Codex CLI adapter. It owns every Codex-specific
// concern jind-ai used to inline into internal/session — hook injection via
// per-invocation -c overrides, the SessionStart write-back path Codex needs
// because it has no `--session-id` equivalent, the shell command shape, and
// the rollout-derived Layer C-transcript enhancer.
//
// The type-name Agent implements session.Agent (via the aliases exposed in
// internal/agent). Register instances via the internal/agent/register
// blank-import package so the daemon can Lookup("codex") them at start-up.
package codex

import (
	"os"
	"sync"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// Agent is the process-wide Codex adapter state. All fields are set once via
// Setup and read from every subsequent SpawnCommand call, so we protect the
// write side with a sync.Once and expose the read side without locking.
//
//   - execPath is os.Executable() captured from the first Setup and reused
//     by hook_args.go to build the `-c 'hooks.X=[...]'` payloads. It never
//     changes for the lifetime of the daemon.
//   - locator is built once, from os.UserHomeDir() resolved eagerly at
//     construction (invariant for a running daemon; tests set CODEX_HOME
//     directly), and shared by enhancer and every TranscriptReader
//     Transcript() hands out, so a rollout path either one resolves is
//     cached for both — see rollout.go's Locator cache.
//   - enhancer and statusSrc are cached instances so hot-path calls to
//     Description() / StatusSource() don't reallocate on every hook.
type Agent struct {
	setupOnce sync.Once
	execPath  string
	locator   *Locator
	enhancer  *DescriptionEnhancer
	statusSrc *HookStatusSource
}

// New returns a fully-wired Codex adapter. Home dir is resolved eagerly; if
// the lookup fails, locator falls back to a relative path (which will simply
// never find any rollout — TryGenerate then returns false and the session
// keeps whatever description Layer A/B provided).
func New() *Agent {
	home, _ := os.UserHomeDir()
	loc := NewLocator(home)
	return &Agent{
		locator:   loc,
		enhancer:  NewDescriptionEnhancer(loc),
		statusSrc: NewHookStatusSource(),
	}
}

// Kind is the identifier jind-ai persists in Session.AgentKind.
func (a *Agent) Kind() string { return "codex" }

// Setup captures os.Executable() so SpawnCommand can wire the `-c` hook
// payload back to `jin hook`. Unlike the Claude adapter, Setup writes no
// files: Codex hooks are injected per-invocation on the command line, so
// ~/.codex/hooks.json and config.toml both stay untouched. See "Agent
// Adapters" in docs/architecture.md for the design principle behind this.
func (a *Agent) Setup(ctx agent.SetupContext) error {
	a.setupOnce.Do(func() {
		a.execPath = ctx.ExecPath
	})
	return nil
}

// SpawnCommand delegates to the package-level builder with the captured
// execPath. When Setup has not run yet — an edge case the interface
// contract does not forbid — execPath is the zero value, and HookArgs
// gracefully falls back to a hook-less `codex` invocation.
//
// A resume (isResume(opts)) evicts AgentSessionID from locator's cache
// first. Whether `codex resume` can retarget a UUID onto a different rollout
// file is unverified (see rollout.go), but if it does, the next Find must
// re-glob rather than keep answering with whatever the cache resolved before
// the resume.
func (a *Agent) SpawnCommand(opts agent.SpawnOptions) agent.SpawnPlan {
	if isResume(opts) {
		a.locator.invalidate(opts.AgentSessionID)
	}
	return SpawnCommand(opts, a.execPath)
}

// StatusSource returns the cached hook-event interpreter.
func (a *Agent) StatusSource() agent.StatusSource { return a.statusSrc }

// Description returns the cached Layer C-transcript enhancer that pulls
// the first genuine user prompt out of the rollout JSONL.
func (a *Agent) Description() agent.DescriptionSource { return a.enhancer }

// Transcript returns the rollout reader. It is a partial view of what Claude
// Code's reader gives — Codex records no token usage per message, no error
// flag on a tool result, and one tool name for every call — and transcript.go
// documents each gap where the mapping loses something.
//
// The TranscriptReader wrapper is still built fresh per call, unlike enhancer
// and statusSrc above: it holds nothing but a Locator pointer, and
// constructing it measured at 240ns against a read that takes milliseconds.
// What it wraps is not fresh, though — it shares locator with enhancer, so a
// `jin session result` call and a hook-driven description attempt for the
// same session hit the same uuid->path cache instead of each re-globbing
// every day shard.
func (a *Agent) Transcript() agent.TranscriptSource { return NewTranscriptReader(a.locator) }

// ClearInputKeys returns the tmux key sequence Manager.SendPrompt sends
// before each attempt to wipe Codex's input line to empty, preventing
// residual text from concatenating with the new prompt. C-u is the standard
// readline kill-line binding and Codex's TUI honours it. Adapters may
// return nil to opt out; empty here would mean "opt out" and disable the
// residual-concat protection for codex sessions.
func (a *Agent) ClearInputKeys() []string { return []string{"C-u"} }

// PastePlaceholder returns "": Codex receives prompts as keystrokes because
// at 16KB the keystroke path verifies in 1.7s, so trading the tail match
// for a placeholder count buys nothing. Its placeholder does carry an exact
// byte count ("[Pasted Content N chars]"), so this could be revisited if a
// size shows up that typing cannot reach.
func (a *Agent) PastePlaceholder(string) string { return "" }

// DismissOverlayKeys returns nil: Codex's completion behaviour has not been
// measured, so there is nothing to base a key on.
//
// Claude Code was found to leave a completion overlay open for prompts that
// end in an in-progress token, which then eats the Enter that SendPrompt
// presses. Codex has slash commands too, so it may well share the defect —
// but the run that would have settled it could not be made: the account hit
// its API usage limit, and the pane dies before a prompt can be typed.
//
// Opting out keeps this adapter byte-identical to its pre-fix behaviour.
// Guessing a key here would be the same mistake in the other direction:
// Escape is destructive on a running turn, and nothing yet says Codex needs
// it. Measure first, then return keys.
func (a *Agent) DismissOverlayKeys(string) []string { return nil }
