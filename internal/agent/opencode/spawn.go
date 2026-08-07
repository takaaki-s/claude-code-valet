package opencode

import (
	"github.com/takaaki-s/jind-ai/internal/agent"
)

// sessionIDPrefix is the prefix opencode stamps on every session id
// (packages/opencode/src/id/id.ts: prefixes.session = "ses", joined with
// "_"). jind-ai pre-mints Session.AgentSessionID as a UUID, which can never
// collide with this, so the prefix is a reliable "has opencode told us its
// real id yet?" test. isSessionID in transcript.go is that test; both the
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
	resuming := opts.AgentSessionStarted && isSessionID(opts.AgentSessionID)

	cmd := "opencode"
	if resuming {
		cmd = "opencode --session " + opts.AgentSessionID
	}

	plan := agent.SpawnPlan{Command: cmd}
	if configDir != "" {
		// Handed over unescaped on purpose: Manager single-quotes every
		// ExtraEnv value, and the SpawnPlan contract makes double-escaping
		// the adapter's bug, not Manager's.
		plan.ExtraEnv = map[string]string{configDirEnv: configDir}
		if resuming {
			// Only on resume: on a fresh spawn AgentSessionID is still the
			// pre-minted UUID, which names no opencode session at all.
			plan.ExtraEnv[rootSessionEnv] = opts.AgentSessionID
		}
	}
	return plan
}
