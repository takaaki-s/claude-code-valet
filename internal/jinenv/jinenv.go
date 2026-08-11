// Package jinenv assembles the part of a spawned process's environment that
// names the jind-ai that spawned it.
//
// None of it can be left to ordinary inheritance, and not for one reason.
// JIN_SOCKET and JIN_DEBUG cannot because a tmux pane inherits the tmux
// *server's* environment, which may predate the daemon or belong to an older
// one; JIN_BIN cannot because a `jin` found on PATH may be an older install
// than the daemon the child is calling back into. Getting either wrong is
// silent — a `jin hook` that resolves the wrong socket exits 0 and writes
// nothing.
package jinenv

import (
	"os"
	"strings"
)

// EnvDepth names the chain depth a plugin run carries. It is spelled here
// rather than beside the guard that reads it, in internal/plugin, because that
// package imports this one and the reverse would not compile.
const EnvDepth = "JIN_PLUGIN_DEPTH"

// inheritedEnvKeys is the minimal set of parent-process variables forwarded to
// a child jind-ai starts — a worktree post-create hook, or a plugin's dispatch
// and build runs. It covers what interpreters and toolchains (pnpm / mise /
// node …) need to bootstrap, without leaking arbitrary caller or daemon state.
var inheritedEnvKeys = map[string]bool{
	"PATH":  true,
	"HOME":  true,
	"USER":  true,
	"SHELL": true,
	"LANG":  true,
	"TERM":  true,
}

// InheritedEnv returns the allowlisted subset of this process's environment,
// as KEY=VALUE entries. Callers append their own JIN_* variables afterwards.
func InheritedEnv() []string {
	env := make([]string, 0, 16)
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if inheritedEnvKeys[key] || strings.HasPrefix(key, "LC_") {
			env = append(env, kv)
		}
	}
	return env
}

// Identity names the jin a spawned process should call back into: which daemon
// to talk to, which binary to re-enter, and whether to record what it does.
type Identity struct {
	// SocketPath is the daemon's listening socket — what a child's `jin`
	// resolves instead of falling back to the default path.
	SocketPath string
	// BinPath is the jin binary a child should re-enter to call back. It must
	// stay valid, and stay matched to the daemon serving the socket above, for
	// as long as that child may call back — the whole life of a session. The
	// daemon's own executable does not meet that; see
	// session.EstablishHookBinary.
	BinPath string
	// Debug is whether the child should write its own debug log. Callers pass
	// what debug.Enabled() reports rather than re-reading the variable, so a
	// child cannot end up disagreeing with its parent about what "on" means.
	Debug bool
}

// Environ renders this identity as KEY=VALUE assignments, in the form os/exec
// takes. A caller splicing them into a shell command line can quote each whole
// assignment: `env` splits its argument at the first `=`, so quoting the word
// and quoting only the value are equivalent.
//
// Use it only where jind-ai builds the child's environment outright, so that a
// skipped key is simply absent. Anything tmux starts — including a shell `env`
// prefix tmux is handed, since `env` without -i adds to what it was given —
// needs TmuxEnviron instead.
func (i Identity) Environ() []string {
	pairs := i.pairs()
	env := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		if kv[1] != "" {
			env = append(env, kv[0]+"="+kv[1])
		}
	}
	return env
}

// pairs is the identity as ordered key/value pairs, shared by both renderers so
// that whether an empty value is emitted stays their only difference.
func (i Identity) pairs() [][2]string {
	debugFlag := ""
	if i.Debug {
		debugFlag = "1"
	}
	return [][2]string{
		{"JIN_SOCKET", i.SocketPath},
		{"JIN_BIN", i.BinPath},
		{"JIN_DEBUG", debugFlag},
	}
}

// TmuxEnviron renders this identity, plus the session a pane's work belongs to,
// for a destination whose environment jind-ai did not build. tmux takes these
// one per -e; the shell `env` prefix the agent's spawn line is built from takes
// them as words.
//
// Every key is emitted, empty when unknown, because a key left out of tmux's -e
// is taken from the tmux *server* — which holds whatever process forked it,
// down to another session's id when the server was forked from inside a pane.
// There is no third choice: `-e VAR` without a value is ignored and `-e VAR=`
// sets it empty, both measured.
//
// JIN_DEBUG is emitted empty rather than "0", which a plugin testing
// `[ -n "$JIN_DEBUG" ]` would read as on. EnvDepth is emitted empty always: a
// depth inherited from the tmux server makes every `jin plugin run` from a pane
// refuse itself as a chain, invisibly, since a plugin key binding fires through
// `run-shell -b` with its output discarded. An empty value reads back through
// strconv.Atoi as 0, so a run from a pane still begins at depth 1.
func (i Identity) TmuxEnviron(sessionID string) []string {
	pairs := i.pairs()
	env := make([]string, 0, len(pairs)+2)
	for _, kv := range pairs {
		env = append(env, kv[0]+"="+kv[1])
	}
	return append(env, "JIN_SESSION_ID="+sessionID, EnvDepth+"=")
}
