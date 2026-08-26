package opencode

import (
	"github.com/takaaki-s/jind-ai/internal/agent"
)

// sessionIDPrefix is the prefix opencode stamps on every session id
// (packages/opencode/src/id/id.ts: prefixes.session = "ses", joined with "_").
// jind-ai pre-mints Session.AgentSessionID as a UUID, which can never collide
// with this, so the prefix is a reliable "has opencode told us its real id
// yet?" test — which is what the resume decision below uses, through
// hasSessionIDPrefix.
//
// It matters because startSessionTmux flips AgentSessionStarted to true before
// the process is even spawned, so that flag alone cannot distinguish
// "resumable" from "spawned once, never reported an id".
const sessionIDPrefix = "ses_"

// configDirEnv is the env var that adds a directory to opencode's config
// search path. It is additive — the user's ~/.config/opencode and any
// project .opencode still load — so pointing it at jind-ai state is safe.
const configDirEnv = "OPENCODE_CONFIG_DIR"

// rootSessionEnv names the session the plugin should report on, and is set only
// when resuming. Resuming publishes no session.created, so without it the
// plugin could only classify the id once the server is answering. Naming the
// session up front makes the first status of a resumed session correct even
// before then.
const rootSessionEnv = "JIN_OPENCODE_ROOT_SESSION"

// sessionArgEnv carries the id that `--session` resumes, and exists so that id
// never reaches a shell as text. The rule is the shell-safety contract on
// session.SpawnPlan; this adapter is where it was learned. Concatenating the id
// into Command made `ses_x$(...)`, `ses_x;...` and backticks all execute, all
// three measured. Nothing about the escaping was broken: `'` is escaped
// correctly and an attacker does not need one.
//
// The id is not trustworthy. It arrives from a hook payload and is persisted
// unvalidated, so whatever a hook says is run the next time the session
// resumes — a different time and trigger than the write.
//
// Deliberately NOT rootSessionEnv, though the value is the same on this path:
// that one exists for the plugin, and a change made for the plugin's sake must
// not be able to silently stop a resume.
const sessionArgEnv = "JIN_OPENCODE_SESSION"

// modelArgEnv keeps the model out of Command for the reason the paragraphs
// above keep the id out; SpawnOptions.Model states it.
const modelArgEnv = "JIN_OPENCODE_MODEL"

// nestedEnv is the mark the plugin sets on everything the agent starts, so it
// can decline to report from inside such a process. Spelled here as well as in
// the plugin because a spawn must clear it, and pinned to the plugin's own
// spelling by a test in this package.
//
// Clearing it is not housekeeping: an agent that runs `jin daemon start` hands
// the mark to jind-ai's own processes, and a pane that inherited it would run an
// opencode reporting no status for any session and no error either. The path it
// travels is in docs/gotchas.md.
const nestedEnv = "JIN_OPENCODE_NESTED"

// SpawnCommand builds the `opencode ...` command line the daemon splices into
// its fixed shell wrapper. Manager owns cwd, JIN_SESSION_ID and the
// unconditional `env -u TMUX`; this owns the agent-specific pieces:
//
//   - `opencode` on the first spawn. opencode has no flag that assigns a
//     session id up front (`--session` only continues an existing one), so we
//     start fresh and let the plugin's session.created → SessionStart carry the
//     real id back through HandleHookEvent's re-key path.
//   - `opencode --session <id>` once that re-key has happened, detected by the
//     ses_ prefix rather than by AgentSessionStarted alone.
//   - OPENCODE_CONFIG_DIR pointing at the directory Setup wrote the plugin
//     into, which is what makes status reporting work at all.
//   - `--model "$JIN_OPENCODE_MODEL"` when the session names one. opencode
//     spells this `provider/model`; jind-ai does not check that, and omits the
//     flag entirely when no model was named.
//
// configDir == "" means Setup never succeeded: contribute none of the pieces
// that depend on it — no OPENCODE_CONFIG_DIR, so nothing points opencode at the
// plugin — and the operator gets a working agent whose status is tracked only by
// pane death. What the operator asked for directly still stands on that path,
// model included. Same fail-open
// posture the Codex adapter takes when it has no executable path to build hook
// arguments from.
//
// UnsetEnv clears the plugin's nested mark; see nestedEnv for why a spawn must.
func SpawnCommand(opts agent.SpawnOptions, configDir string) agent.SpawnPlan {
	resuming := opts.AgentSessionStarted && hasSessionIDPrefix(opts.AgentSessionID)

	cmd := "opencode"
	if resuming {
		// The id goes through the environment, never into this string. See
		// sessionArgEnv.
		cmd = "opencode --session \"$" + sessionArgEnv + "\""
	}

	plan := agent.SpawnPlan{Command: cmd, ExtraEnv: map[string]string{}, Resumed: resuming}
	if resuming {
		// Set whatever else is true: the command above names this variable, so
		// leaving it out would resume nothing. On a fresh spawn AgentSessionID
		// is still the pre-minted UUID, which names no opencode session at all.
		plan.ExtraEnv[sessionArgEnv] = opts.AgentSessionID
	}
	if configDir != "" {
		// Handed over unescaped on purpose: Manager single-quotes every
		// ExtraEnv value, and the SpawnPlan contract makes double-escaping
		// the adapter's bug, not Manager's.
		plan.ExtraEnv[configDirEnv] = configDir
		if resuming {
			plan.ExtraEnv[rootSessionEnv] = opts.AgentSessionID
		}
	}
	if opts.Model != "" {
		plan.Command += ` --model "$` + modelArgEnv + `"`
		plan.ExtraEnv[modelArgEnv] = opts.Model
	}
	if len(plan.ExtraEnv) == 0 {
		plan.ExtraEnv = nil
	}
	plan.UnsetEnv = []string{nestedEnv}
	return plan
}
