// Package jinenv assembles the part of a spawned process's environment that
// names the jind-ai that spawned it.
//
// jind-ai starts children in more than one place — an agent in a tmux pane, a
// plugin from the dispatcher — and each place used to name these variables
// inline, where it happened to build its own environment. No place named all of
// them, and the two that mattered were missing from opposite sides: the agent
// was told whether to record what it does but not which daemon to reach, and a
// plugin was told which daemon and which binary but not whether to record.
//
// Both gaps were measured, and both are silent. A `jin hook` that resolves the
// wrong socket exits 0 and writes nothing; a callback that runs with the flag
// off simply leaves no line behind. Nothing fails, so nothing is noticed.
//
// There are two renderers, and which one a caller wants is decided by the
// destination rather than by the caller: Environ where jind-ai builds the
// child's environment outright, TmuxEnviron where something else does and
// jind-ai only adds to it. They differ only in what an unknown value renders
// as, and they have to, because leaving a key out means "absent" in the first
// case and "keep whatever was already there" in the second.
//
// Only the plugin dispatcher qualifies for the first today: it assigns
// cmd.Env from a curated allowlist that has no JIN_* in it, so a key it does
// not write is a key the plugin does not have. A shell `env` prefix does not
// qualify on its own — `env` without -i adds to what it was handed — and the
// one jind-ai writes is handed to tmux, which starts a pane from its server's
// environment. That is why the agent's spawn line uses TmuxEnviron too.
//
// The worktree post-create hook is a deliberate non-consumer. It runs inside
// provisioning, before the session's recorded working directory moves to the
// new worktree, so a hook that could call back would be asking about a session
// mid-creation; its documented contract is the six JIN_WORKTREE_*/JIN_SESSION_*
// variables and no callback at all. Wiring one is a product decision, not a
// gap to close.
package jinenv

// Identity names the jin a spawned process should call back into: which daemon
// to talk to, which binary to re-enter, and whether to record what it does.
//
// The three travel together because they answer one question — "how do I reach
// the jin that started me?" — and a child that gets an incomplete answer cannot
// tell. In particular none of them can be left to ordinary inheritance:
//
//   - JIN_SOCKET, because a tmux pane inherits the tmux *server's* environment,
//     not the daemon's. When the daemon happens to fork that server the value
//     arrives; when the server predates it the value is missing, and when an
//     earlier daemon forked it the value is that older daemon's socket. All
//     three were observed. Only the first is correct, and it is correct by
//     accident.
//   - JIN_DEBUG, because the flag reaches the daemon's own environment and
//     stops there, for the same reason.
//   - JIN_BIN, because a `jin` found on PATH may be an older install that
//     predates the subcommand the child is trying to call.
//
// One value serves every spawn site, and the whole of it travels together.
// Assembling it per site is what let the answers disagree — see BinPath.
type Identity struct {
	// SocketPath is the daemon's listening socket — what a child's `jin`
	// resolves instead of falling back to the default path.
	SocketPath string
	// BinPath is the jin binary a child should re-enter. The requirement is not
	// "some jin": it must stay valid, and stay matched to the daemon serving the
	// socket above, for as long as that child may call back — which is the whole
	// life of a session, long after the caller has stopped watching. The
	// daemon's own executable does not meet that; session.EstablishHookBinary
	// says what goes wrong and how often.
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
// A value that is not known is skipped rather than assigned empty. Every reader
// treats an empty JIN_SOCKET as no socket and an empty JIN_BIN as no binary, so
// an empty assignment carries no information the absence does not — it only
// makes a child's environment claim to know something it does not.
//
// That reasoning holds only where a skipped key is simply absent from the
// child, which is a property of the destination and not of this function. Where
// the child starts from an environment jind-ai did not build — anything tmux
// runs, including a shell `env` prefix tmux is given — use TmuxEnviron.
func (i Identity) Environ() []string {
	env := make([]string, 0, 3)
	if i.SocketPath != "" {
		env = append(env, "JIN_SOCKET="+i.SocketPath)
	}
	if i.BinPath != "" {
		env = append(env, "JIN_BIN="+i.BinPath)
	}
	if i.Debug {
		env = append(env, "JIN_DEBUG=1")
	}
	return env
}

// TmuxEnviron renders this identity, plus the session a pane's work belongs to,
// as the assignments tmux takes one per -e. The session id is an argument
// rather than a field because it answers a different question than the three
// above: not which jin, but which of its sessions.
//
// It emits every key, empty when the value is unknown, and that is the whole
// difference from Environ. The rule inverts because the destination does: a key
// left out of an exec.Cmd environment is absent from the child, but a key left
// out of tmux's -e is taken from the tmux *server* — which holds whatever
// process forked it. Four provenances were measured, 3 trials each: nothing at
// all when the daemon or a plain shell forked the server, the stale values when
// the forking environment carried some, and another session's id when the
// server was forked from inside an agent's pane (a `jin daemon start` run there
// is enough). The last is the one to fear, because a stale id is a plausible
// UUID and an absent one is not.
//
// There is no third choice to reach for: tmux has no unset form for -e. `-e
// VAR` without a value is ignored, and `-e VAR=` sets it empty — both measured.
//
// Emitting empty is safe because jind-ai's own readers already read empty as
// unknown: debug.Enabled() compares against "1", and an empty JIN_SOCKET or
// JIN_BIN means no socket and no binary, exactly as their absence does.
// `"${JIN_BIN:-jin}"`, the form the README recommends, substitutes on empty as
// well as on unset — unlike a stale path, which is neither, and which
// session.EstablishHookBinary records exiting 127 instead.
//
// JIN_DEBUG is emitted empty rather than "0" for the reason it is omitted from
// Environ rather than set to "0": a plugin testing `[ -n "$JIN_DEBUG" ]` would
// read "0" as on.
func (i Identity) TmuxEnviron(sessionID string) []string {
	debugFlag := ""
	if i.Debug {
		debugFlag = "1"
	}
	return []string{
		"JIN_SOCKET=" + i.SocketPath,
		"JIN_BIN=" + i.BinPath,
		"JIN_DEBUG=" + debugFlag,
		"JIN_SESSION_ID=" + sessionID,
	}
}
