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

// EnvDepth names the chain depth a plugin run carries. Spelled once and
// exported because the parties have to agree on it exactly and none of them
// fails when they do not: internal/plugin's buildEnv writes the depth a plugin
// runs at, `jin plugin run` reads it back to tell the daemon how deep its
// caller was, and TmuxEnviron below writes it empty so a value stranded in a
// tmux server cannot be mistaken for a caller's. A typo in any one of those is
// silent — a run refused with its output discarded, or a chain guard that never
// sees a depth.
//
// It lives here rather than beside the guard that reads it, in internal/plugin,
// because that package imports this one and the reverse would not compile. The
// layering says the same thing the name does: a key has to be spelled where the
// environment is assembled, and this is that place.
const EnvDepth = "JIN_PLUGIN_DEPTH"

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
	// BinPath is the jin binary a child should re-enter to call back. The
	// requirement is not "some jin": it must stay valid, and stay matched to the
	// daemon serving the socket above, for as long as that child may call back —
	// which is the whole life of a session, long after the caller has stopped
	// watching. The daemon's own executable does not meet that;
	// session.EstablishHookBinary says what goes wrong and how often, and why a
	// spawn site must never work this out for itself.
	//
	// A binary a process re-launches itself as is a different question with a
	// different answer: internal/tui's popups run this build's own executable on
	// purpose, because what they open is more of this UI rather than a callback.
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
//
// This renders pairs() and nothing else, which is what keeps EnvDepth out of a
// plugin's environment. internal/plugin writes that key itself, from a depth
// only it knows, and appends this identity afterwards; a second assignment from
// here would land after that one and win. Keeping the key out of pairs() means
// the collision cannot be reintroduced by changing what an empty value renders
// as, which is the only rule this function has.
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

// pairs is the identity as ordered key/value pairs, and is the only place these
// three key names and their order live. The two renderers below differ in one
// thing — whether an empty value is emitted — and sharing the table is what
// keeps that the only difference. Adding a field to one renderer and not the
// other would otherwise be invisible: the reader that misses it is a pane, which
// quietly takes the tmux server's value instead of failing.
//
// What TmuxEnviron appends past this table — the session a pane's work belongs
// to, and the plugin chain depth — are not fields of an identity and are not
// rendered by Environ at all. They are named there rather than here for that
// reason, and in EnvDepth's case the separation is load-bearing: see Environ.
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
// them as words. The session id is an argument rather than a field because it
// answers a different question than the three above: not which jin, but which
// of its sessions.
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
//
// EnvDepth is emitted too, and unlike the others it is never anything but
// empty. A depth belongs to a plugin's own process, so nothing tmux starts here
// is continuing one; what a pane can inherit is only an accident. A tmux server
// forked by a process that carried a depth hands it to every pane, and each
// `jin plugin run` from those panes is then refused as a chain — measured 3 of
// 3, and invisible, because a plugin key binding fires through `run-shell -b`
// with its output discarded. Stating "not in a chain" beats inheriting that.
//
// It does not bound a chain and is not meant to. A run started from a pane
// still begins at depth 1: an empty assignment reads back through strconv.Atoi
// as 0, exactly as the absent one it replaces did. README's "Loop residual
// risk" says a chain started from a popup is unbounded and the plugin author
// must stop it, and that contract is unchanged — what this closes is the
// inverse, a depth arriving where no plugin put one, which internal/plugin's
// maxDepth doc separates from the other for the same reason.
func (i Identity) TmuxEnviron(sessionID string) []string {
	pairs := i.pairs()
	env := make([]string, 0, len(pairs)+2)
	for _, kv := range pairs {
		env = append(env, kv[0]+"="+kv[1])
	}
	return append(env, "JIN_SESSION_ID="+sessionID, EnvDepth+"=")
}
