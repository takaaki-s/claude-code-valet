package codex

import (
	"fmt"
	"strings"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// SpawnCommand builds the `codex ...` command line the daemon splices into
// its fixed shell wrapper. Manager handles cwd + JIN_SESSION_ID + env -u
// TMUX; we only own the agent-specific pieces:
//
//   - `codex` on the first spawn — Codex has no `--session-id`
//     equivalent, so we spawn fresh and let SessionStart's hook stdin
//     write the actual UUID back into Session.AgentSessionID. The
//     pre-mint UUID Manager set on Session.AgentSessionID is
//     intentionally ignored here — see "Codex adapter" in
//     docs/gotchas.md for why.
//   - `codex resume <UUID>` once AgentSessionStarted is true and
//     AgentSessionID has been re-keyed to the real Codex UUID. `codex
//     resume` fails fast on an unknown UUID (~3s in Codex 0.144.1, well
//     within the existing 10s quick-fail auto-recovery window), so a
//     stale UUID does not require a defensive glob check up front.
//   - Hook injection via `--enable hooks` + one `-c 'hooks.X=[...]'`
//     per managedEvent. See hook_args.go.
//   - The behaviour overrides in configArgs, injected per spawn rather
//     than written into the user's Codex config.
//
// UnsetEnv clears three Codex sandbox markers so a jind-ai session
// spawned from inside a Codex-created sandbox does not inherit "we're
// already inside a sandbox" state. The three variables mirror the
// [[cc-child-session-env]] discipline the Claude adapter follows;
// authentication vars (CODEX_API_KEY / CODEX_ACCESS_TOKEN /
// OPENAI_API_KEY) are intentionally left set so the spawned Codex can
// authenticate.
// configArgs returns the `-c` overrides jind-ai forces on every spawned
// Codex. They are passed per spawn — the user's own Codex config is never
// rewritten, so a Codex started outside jind-ai keeps its normal
// behaviour.
//
//   - disable_paste_burst=true turns off the input folding that replaces a
//     large paste with a "[Pasted Content N chars]" placeholder. Manager's
//     verify reads the prompt tail back out of capture-pane, and a folded
//     input hides it — the send then looks dropped even though it landed.
//     Trade-off: a human pasting into this pane also gets raw text instead
//     of the placeholder.
//   - check_for_update_on_startup=false suppresses the startup update
//     prompt. It steals the first keystrokes of a session, and answering it
//     by accident upgrades Codex out from under the user.
//
// Values are wrapped in single quotes to match HookArgs: SpawnPlan.Command
// is spliced into a shell wrapper, and quoting everything uniformly means
// no one has to work out which `-c` values happen to be shell-safe.
//
// Caveat: Codex ignores unknown `-c` keys silently rather than failing. If
// either key is renamed upstream this stops working with no error, so do
// not treat the paste-burst override as a reason to drop the chunking and
// inter-chunk delay in Manager.SendPrompt — those defend the same failure
// independently.
func configArgs() []string {
	return []string{
		"-c", "'disable_paste_burst=true'",
		"-c", "'check_for_update_on_startup=false'",
	}
}

func SpawnCommand(opts agent.SpawnOptions, execPath string) agent.SpawnPlan {
	base := "codex"
	if opts.AgentSessionID != "" && opts.AgentSessionStarted {
		base = fmt.Sprintf("codex resume %s", opts.AgentSessionID)
	}
	args := configArgs()
	args = append(args, HookArgs(execPath)...)
	cmd := base
	if len(args) > 0 {
		cmd = base + " " + strings.Join(args, " ")
	}
	return agent.SpawnPlan{
		Command: cmd,
		UnsetEnv: []string{
			"CODEX_SANDBOX",
			"CODEX_SANDBOX_NETWORK_DISABLED",
			"CODEX_CI",
		},
	}
}
