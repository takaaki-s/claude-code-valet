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
// adapter chooses. It is re-keyed from a hook payload, and a record written by
// an older jind-ai — or edited by hand — reaches SpawnCommand having passed no
// gate at all.
const sessionArgEnv = "JIN_CLAUDE_SESSION"

// modelArgEnv keeps the model out of Command for the reason sessionArgEnv keeps
// the id out; SpawnOptions.Model states it.
const modelArgEnv = "JIN_CLAUDE_MODEL"

// SpawnCommand builds the `claude ...` command line the daemon splices into
// its fixed shell wrapper. The wrapper handles cwd + JIN_SESSION_ID + env -u
// TMUX; this owns the agent-specific pieces:
//
//   - `--settings <path>` when opts.StateDir still holds a usable hooks file.
//     Omitted otherwise so the CLI still starts with default settings, just
//     without status hooks.
//   - `--session-id "$JIN_CLAUDE_SESSION"` on the very first spawn, or
//     `--resume` once the session has been started at least once. An empty
//     AgentSessionID falls through both branches and yields a plain `claude`.
//     The id itself travels in ExtraEnv — see sessionArgEnv.
//   - `--model "$JIN_CLAUDE_MODEL"` when the session names one. Omitted
//     entirely when it does not, so the CLI picks its own default.
//
// UnsetEnv includes CLAUDECODE because Claude Code sets it when it runs jin
// via a hook, and a *new* CC must not think it is already inside a CC session.
func (a *Agent) SpawnCommand(opts agent.SpawnOptions) agent.SpawnPlan {
	// Asked of the state directory itself rather than remembered from Setup:
	// when this spawn's write failed, this answers with whatever the directory
	// can still serve, and with "" when that is nothing — which is what drops
	// --settings below.
	hooksPath := existingHooksSettings(opts.StateDir)

	cmd := "claude"
	if hooksPath != "" {
		cmd = fmt.Sprintf("claude --settings %s", hooksPath)
	}

	resuming := opts.AgentSessionID != "" && opts.AgentSessionStarted

	var extraEnv map[string]string
	if opts.AgentSessionID != "" {
		flag := "--session-id"
		if resuming {
			flag = "--resume"
		}
		// The id goes through the environment, never into this string, and
		// unescaped — see sessionArgEnv.
		cmd += " " + flag + ` "$` + sessionArgEnv + `"`
		extraEnv = map[string]string{sessionArgEnv: opts.AgentSessionID}
	}

	if opts.Model != "" {
		cmd += ` --model "$` + modelArgEnv + `"`
		if extraEnv == nil {
			extraEnv = map[string]string{}
		}
		extraEnv[modelArgEnv] = opts.Model
	}

	return agent.SpawnPlan{
		Command:  cmd,
		ExtraEnv: extraEnv,
		Resumed:  resuming,
		// Every Claude Code var that leaks in from a CC-parent environment is
		// cleared here, so the spawned CC starts as a top-level session with a
		// fresh transcript. Not just cosmetic: with CLAUDE_CODE_CHILD_SESSION=1,
		// CC 2.x runs in "child agent" mode and persists no .jsonl transcript,
		// which silently breaks the Layer C description enhancer.
		UnsetEnv: []string{
			"CLAUDECODE",
			"CLAUDE_CODE_CHILD_SESSION",
			"CLAUDE_CODE_SESSION_ID",
			"CLAUDE_CODE_ENTRYPOINT",
		},
	}
}
