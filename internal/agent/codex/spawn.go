package codex

import (
	"strings"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// sessionArgEnv carries the id that `codex resume` names, so that id never
// reaches a shell as text. The rule and the reasoning behind it are the
// shell-safety contract on session.SpawnPlan; this is one adapter obeying it.
//
// Codex has the weakest claim of the three adapters to trust this value: with
// no --session-id equivalent, every id it resumes arrived from a hook payload
// rather than from jind-ai, and a record written by an older jind-ai — or
// edited by hand — reaches SpawnCommand having passed no gate.
const sessionArgEnv = "JIN_CODEX_SESSION"

// modelArgEnv keeps the model out of Command for the reason sessionArgEnv keeps
// the id out; SpawnOptions.Model states it.
const modelArgEnv = "JIN_CODEX_MODEL"

// configArgs returns the `-c` overrides jind-ai forces on every spawned Codex.
// They are passed per spawn — the user's own Codex config is never rewritten,
// so a Codex started outside jind-ai keeps its normal behaviour.
//
//   - disable_paste_burst=true turns off the input folding that replaces a
//     large paste with a "[Pasted Content N chars]" placeholder. Manager's
//     verify reads the prompt tail back out of capture-pane, and a folded
//     input hides it — the send then looks dropped even though it landed.
//     Trade-off: a human pasting into this pane also gets raw text.
//   - check_for_update_on_startup=false suppresses the startup update prompt,
//     which steals the first keystrokes of a session and, answered by
//     accident, upgrades Codex out from under the user.
//
// Values are wrapped in single quotes to match HookArgs, so no one has to work
// out which `-c` values happen to be shell-safe.
//
// Caveat: Codex ignores unknown `-c` keys silently rather than failing. If
// either key is renamed upstream this stops working with no error, so do not
// treat the paste-burst override as a reason to drop the chunking and
// inter-chunk delay in Manager.SendPrompt.
func configArgs() []string {
	return []string{
		"-c", "'disable_paste_burst=true'",
		"-c", "'check_for_update_on_startup=false'",
	}
}

// isResume reports whether opts calls for `codex resume <UUID>` rather than a
// fresh `codex` spawn — see SpawnCommand's doc comment for the two cases.
// Exported as its own predicate rather than inlined so Agent.SpawnCommand
// (agent.go) can check the identical condition without the two silently
// drifting apart.
func isResume(opts agent.SpawnOptions) bool {
	return opts.AgentSessionID != "" && opts.AgentSessionStarted
}

// SpawnCommand builds the `codex ...` command line the daemon splices into its
// fixed shell wrapper. Manager handles cwd + JIN_SESSION_ID + env -u TMUX;
// this owns the agent-specific pieces:
//
//   - `codex` on the first spawn — Codex has no `--session-id` equivalent, so
//     we spawn fresh and let SessionStart's hook stdin write the actual UUID
//     back into Session.AgentSessionID. The pre-minted UUID is intentionally
//     ignored here; see "Codex adapter" in docs/gotchas.md for why.
//   - `codex resume "$JIN_CODEX_SESSION"` once AgentSessionStarted is true and
//     AgentSessionID has been re-keyed to the real Codex UUID. `codex resume`
//     fails fast on an unknown UUID (~3s in Codex 0.144.1, well within the 10s
//     quick-fail auto-recovery window), so a stale UUID does not require a
//     defensive glob check up front. The UUID travels in ExtraEnv.
//   - Hook injection via `--enable hooks` + one `-c 'hooks.X=[...]'` per
//     managedEvent (see hook_args.go), plus the overrides in configArgs.
//   - `--model "$JIN_CODEX_MODEL"` on both lines alike when the session names
//     one. Omitted entirely when it does not, so Codex picks its own default.
//
// UnsetEnv clears three Codex sandbox markers so a jind-ai session spawned
// from inside a Codex-created sandbox does not inherit that state.
// Authentication vars (CODEX_API_KEY / CODEX_ACCESS_TOKEN / OPENAI_API_KEY)
// are intentionally left set so the spawned Codex can authenticate.
func SpawnCommand(opts agent.SpawnOptions) agent.SpawnPlan {
	base := "codex"
	var extraEnv map[string]string
	if isResume(opts) {
		// The id goes through the environment, never into this string, and
		// unescaped — see sessionArgEnv.
		base = `codex resume "$` + sessionArgEnv + `"`
		extraEnv = map[string]string{sessionArgEnv: opts.AgentSessionID}
	}
	args := configArgs()
	if opts.Model != "" {
		// Appended to the shared args rather than to `base`, which is what puts
		// it on the resume line too — `codex resume` takes --model as well.
		args = append(args, "--model", `"$`+modelArgEnv+`"`)
		if extraEnv == nil {
			extraEnv = map[string]string{}
		}
		extraEnv[modelArgEnv] = opts.Model
	}
	// Straight from the options rather than from anything Setup kept — this
	// adapter carries nothing between the two calls (see the type's doc, which
	// also names the rollout cache it does keep, for its own unrelated reason).
	// An empty ExecPath is reachable in production, and HookArgs answers it
	// with a hook-less invocation.
	args = append(args, HookArgs(opts.ExecPath)...)
	cmd := base
	if len(args) > 0 {
		cmd = base + " " + strings.Join(args, " ")
	}
	return agent.SpawnPlan{
		Command:  cmd,
		ExtraEnv: extraEnv,
		UnsetEnv: []string{
			"CODEX_SANDBOX",
			"CODEX_SANDBOX_NETWORK_DISABLED",
			"CODEX_CI",
		},
	}
}
