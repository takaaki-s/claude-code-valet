package claude

import (
	"fmt"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// sessionArgEnv carries the id that --session-id / --resume names, so that id
// never reaches a shell as text. The rule and the reasoning behind it are the
// shell-safety contract on session.SpawnPlan; this is one adapter obeying it.
//
// Worth restating only for what is specific here: the id is not a value this
// adapter chooses. It is re-keyed from a hook payload, and while Manager
// validates one before recording it, a record written by an older jind-ai — or
// edited by hand — reaches SpawnCommand having passed no gate at all.
// TestSpawnCommand_NoAdapterPutsTheSessionIDInTheCommand (internal/agent/register)
// enforces the rule for every registered adapter.
const sessionArgEnv = "JIN_CLAUDE_SESSION"

// SpawnCommand builds the `claude ...` command line the daemon splices into
// its fixed shell wrapper. The wrapper handles cwd + JIN_SESSION_ID + env -u
// TMUX; we only own the agent-specific pieces:
//
//   - `--settings <path>` when opts.StateDir still holds a usable hooks file —
//     normally the one Setup wrote moments ago, sometimes one an earlier spawn
//     left there when this session's own write failed. Omitted otherwise so the
//     CLI still starts (with default settings) and the user gets a working
//     session, just without status hooks.
//   - `--session-id "$JIN_CLAUDE_SESSION"` on the very first spawn, or
//     `--resume "$JIN_CLAUDE_SESSION"` when the session has already been
//     started at least once. Falling through both branches (empty
//     AgentSessionID) yields a plain `claude` invocation, which is the
//     intended fallback for adapters that ever get invoked without a
//     pre-minted id. The id itself travels in ExtraEnv — see sessionArgEnv.
//
// UnsetEnv includes CLAUDECODE because Claude Code sets it when it runs jin
// via a hook, and we must strip it before spawning a *new* CC to avoid the
// child thinking it's already inside a CC session.
func (a *Agent) SpawnCommand(opts agent.SpawnOptions) agent.SpawnPlan {
	// Asked of the state directory itself rather than remembered from Setup.
	// Setup ran for this spawn moments ago and normally leaves a usable file
	// right here; when its write failed, this answers with whatever the
	// directory can still serve, and with "" when that is nothing — which is
	// what drops --settings below. See existingHooksSettings for what it will
	// and will not serve.
	hooksPath := existingHooksSettings(opts.StateDir)

	cmd := "claude"
	if hooksPath != "" {
		cmd = fmt.Sprintf("claude --settings %s", hooksPath)
	}

	var extraEnv map[string]string
	if opts.AgentSessionID != "" {
		flag := "--session-id"
		if opts.AgentSessionStarted {
			flag = "--resume"
		}
		// The id goes through the environment, never into this string, and
		// unescaped — see sessionArgEnv.
		cmd += " " + flag + ` "$` + sessionArgEnv + `"`
		extraEnv = map[string]string{sessionArgEnv: opts.AgentSessionID}
	}

	return agent.SpawnPlan{
		Command:  cmd,
		ExtraEnv: extraEnv,
		// Every Claude Code var that leaks in from a CC-parent environment
		// gets cleared here, so the spawned CC starts as a top-level
		// session with a fresh transcript. Missing any of these is not
		// just cosmetic: with CLAUDE_CODE_CHILD_SESSION=1, CC 2.x runs in
		// "child agent" mode and does not persist a .jsonl transcript,
		// which silently breaks the Layer C description enhancer (it
		// looks for that file). CLAUDECODE guards against nested tmux
		// self-detection; the CLAUDE_CODE_* group guards against session
		// inheritance from whatever process launched jind-ai's daemon or
		// tmux server.
		UnsetEnv: []string{
			"CLAUDECODE",
			"CLAUDE_CODE_CHILD_SESSION",
			"CLAUDE_CODE_SESSION_ID",
			"CLAUDE_CODE_ENTRYPOINT",
		},
	}
}
