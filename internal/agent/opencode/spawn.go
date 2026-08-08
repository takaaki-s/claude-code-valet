package opencode

import (
	"github.com/takaaki-s/jind-ai/internal/agent"
)

// sessionIDPrefix is the prefix opencode stamps on every session id
// (packages/opencode/src/id/id.ts: prefixes.session = "ses", joined with
// "_"). jind-ai pre-mints Session.AgentSessionID as a UUID, which can never
// collide with this, so the prefix is a reliable "has opencode told us its
// real id yet?" test. hasSessionIDPrefix in transcript.go asks exactly that and
// is what the resume decision below uses; isSessionID beside it is the stricter
// question, and the two are deliberately not the same — see its doc comment. isSessionID in transcript.go is that test; both the
// resume decision here and the transcript read go through it, so there is one
// answer to widen if opencode ever changes the format.
//
// This matters because startSessionTmux flips AgentSessionStarted to true
// before the process is even spawned, so that flag alone cannot distinguish
// "resumable" from "spawned once, never reported an id". The Codex adapter
// resolves the same ambiguity by betting that `codex resume <bad-uuid>`
// fails fast enough for the 10s quick-fail retry window to catch it; here
// the id format removes the need for that bet entirely.
const sessionIDPrefix = "ses_"

// configDirEnv is the env var that adds a directory to opencode's config
// search path. It is additive — the user's ~/.config/opencode and any
// project .opencode still load — so pointing it at jind-ai state is safe.
const configDirEnv = "OPENCODE_CONFIG_DIR"

// rootSessionEnv names the session the plugin should report on, and is set
// only when resuming. Resuming publishes no session.created, so without it
// the plugin would have to ask opencode to classify the id — which it can,
// but only once the server is answering. Naming the session up front makes
// the first status of a resumed session correct even before then, and saves
// the lookup afterwards.
//
// The name is adapter-scoped on purpose: unlike JIN_SESSION_ID, which
// Manager exports for every session of every kind, this is written by one
// adapter and read by one plugin.
const rootSessionEnv = "JIN_OPENCODE_ROOT_SESSION"

// sessionArgEnv carries the id that `--session` resumes, and exists so that id
// never reaches a shell as text.
//
// Manager splices Command into `$SHELL -ic '<cmd>'`. The single quotes stop the
// OUTER shell from touching it, but the inner shell is being handed a command
// to interpret — that is what -c means — so anything in Command that looks like
// substitution is substitution. Concatenating the id into the string made
// `ses_x$(...)`, `ses_x;...` and backticks all execute; measured, all three ran.
// Nothing about the escaping was broken: `'` is escaped correctly and an
// attacker does not need one.
//
// The id is not trustworthy. It arrives from a hook payload and is persisted
// unvalidated, so whatever a hook says becomes this value — and it is then run
// the next time the session resumes, which puts the execution at a different
// time and under a different trigger than the write.
//
// ExtraEnv is the channel Manager already quotes (see SpawnPlan), and a shell
// does not re-scan the result of a parameter expansion for substitutions, so
// `--session "$JIN_OPENCODE_SESSION"` receives the value verbatim however it is
// spelled. Deliberately NOT rootSessionEnv, though the value is the same on
// this path: that one exists for the plugin, and a change made for the plugin's
// sake should not be able to silently stop a resume — which is the failure this
// adapter is least able to afford, because it starts a new session and the
// operator's conversation is simply gone.
const sessionArgEnv = "JIN_OPENCODE_SESSION"

// SpawnCommand builds the `opencode ...` command line the daemon splices
// into its fixed shell wrapper. Manager owns cwd, JIN_SESSION_ID and the
// unconditional `env -u TMUX`; we only own the agent-specific pieces:
//
//   - `opencode` on the first spawn. opencode has no flag that assigns a
//     session id up front (`--session` only continues an existing one), so
//     we start fresh and let the plugin's session.created → SessionStart
//     event carry the real id back into Session.AgentSessionID via
//     HandleHookEvent's re-key path.
//   - `opencode --session <id>` once that re-key has happened, detected by
//     the ses_ prefix rather than by AgentSessionStarted alone.
//   - OPENCODE_CONFIG_DIR pointing at the directory Setup wrote the plugin
//     into, which is what makes status reporting work at all.
//
// configDir == "" means Setup never succeeded. We then emit a bare
// `opencode` with no env addition: the operator gets a working agent whose
// status is only tracked via pane death, which beats failing the spawn.
// This is the same fail-open posture the Codex adapter takes when it has no
// executable path to build hook arguments from.
func SpawnCommand(opts agent.SpawnOptions, configDir string) agent.SpawnPlan {
	resuming := opts.AgentSessionStarted && hasSessionIDPrefix(opts.AgentSessionID)

	cmd := "opencode"
	if resuming {
		// The id goes through the environment, never into this string. See
		// sessionArgEnv.
		cmd = "opencode --session \"$" + sessionArgEnv + "\""
	}

	plan := agent.SpawnPlan{Command: cmd, ExtraEnv: map[string]string{}}
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
	if len(plan.ExtraEnv) == 0 {
		plan.ExtraEnv = nil
	}
	return plan
}
