package codex

import (
	"fmt"
	"strings"

	"github.com/takaaki-s/jind-ai/internal/agentdocs"
)

// managedEvents is the Codex hook event set jind-ai injects on every spawn.
// SpawnCommand emits one -c per entry, and status.go's Interpret must
// recognise the same set.
var managedEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PermissionRequest",
	"Stop",
}

// sessionStartEvent names the one managed event whose hook command also asks
// `jin hook` to print the agent-facing context. Kept as its own constant
// rather than indexing managedEvents so reordering that slice cannot silently
// move the flag onto a different event.
const sessionStartEvent = "SessionStart"

// hookTimeoutMillis is the Codex-side per-hook execution budget. The value
// is generous because `jin hook` needs to reach the daemon socket and wait
// for a response — the median case is <100 ms, but a transiently busy
// daemon can push into the seconds.
const hookTimeoutMillis = 10000

// HookArgs builds the `--enable hooks` flag plus one `-c 'hooks.X=[...]'` pair
// per managedEvent, ready to be appended to `codex` when spawning a jind-ai
// session.
//
// The returned slice is joined with spaces and spliced into
// SpawnPlan.Command. Each -c value is wrapped in shell single quotes so the
// space in `"execPath hook"` cannot leak out into argv splitting; Manager's
// outer escape then handles the wrapping-around-wrapping. Single quotes are
// encoded as a TOML unicode escape so they never reach the shell parser.
//
// Returns nil when execPath is empty — SpawnCommand then spawns `codex` without
// hooks, so status follows only pane death but the operator gets a prompt.
func HookArgs(execPath string) []string {
	if execPath == "" {
		return nil
	}
	// Escaped first, then handed to HookCommand: the flag it appends contains no
	// character tomlEscapeForShell would have touched, so the command lands inside
	// the TOML basic string without disturbing the nesting this value lives in.
	//
	// SessionStart alone carries the context flag — Codex adds that hook's
	// additionalContext to the model's context, which is how a child learns
	// `jin docs` exists. See agentdocs.HookCommand.
	escaped := tomlEscapeForShell(execPath)
	args := []string{"--enable", "hooks"}
	for _, ev := range managedEvents {
		val := fmt.Sprintf(
			`hooks.%s=[{hooks=[{type="command",command="%s",timeout=%d}]}]`,
			ev, agentdocs.HookCommand(escaped, ev == sessionStartEvent), hookTimeoutMillis,
		)
		args = append(args, "-c", "'"+val+"'")
	}
	return args
}

// tomlEscapeForShell returns s in a form safe to embed inside a TOML basic
// string that itself lives inside a shell single-quoted context.
//
//   - single quote → its six-character TOML unicode escape. The shell never
//     sees a real single quote, so the outer `-c '...'` grouping stays intact
//     even for exotic install paths.
//   - the basic-string terminator and the escape lead-in are backslashed.
//
// Everything else — spaces, unicode, control chars — is left as-is: the outer
// single quotes group spaces, and unicode passes through both the shell (byte-
// transparent inside `'...'`) and TOML (UTF-8 basic strings verbatim).
func tomlEscapeForShell(s string) string {
	if !strings.ContainsAny(s, `'"\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\'':
			b.WriteString("\\u0027")
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
