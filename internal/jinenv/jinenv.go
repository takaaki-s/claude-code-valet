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
// One value serves every spawn site. Assembling it per site is what let the two
// disagree: the agent was pointed at the stable copy of the binary while a
// plugin was pointed at the daemon's live executable, which is not the same
// file for long — see BinPath.
type Identity struct {
	// SocketPath is the daemon's listening socket — what a child's `jin`
	// resolves instead of falling back to the default path.
	SocketPath string
	// BinPath is the jin binary a child should re-enter — the stable copy under
	// the state dir, not the daemon's live executable.
	//
	// The distinction is not cosmetic. `go build -o` over a running binary
	// unlinks it and creates a new file at the same path, so the daemon keeps
	// running the old inode while that path holds a different build: after one
	// `make build` a child pointed at the live path re-enters a jin the daemon
	// never was (3/3). Where the wire shape changed, the child's call fails
	// with the protocol-mismatch message (3/3) — recoverable, and the user is
	// told to restart; where it did not, the call succeeds against a binary
	// nobody chose. Delete the directory the daemon launched from instead and
	// the path is simply gone: callbacks exit 127 (3/3), and `${JIN_BIN:-jin}`
	// does not rescue them — `:-` substitutes only when unset or empty, and a
	// dead path is neither.
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
