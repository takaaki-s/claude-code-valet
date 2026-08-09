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
// off simply leaves no line behind. Nothing fails, so nothing is noticed —
// which is why the fix is a shared answer rather than a third conditional at a
// third spawn site.
package jinenv

// Var is one environment assignment, kept split rather than pre-joined because
// the callers render it differently: one appends to an exec.Cmd.Env slice, the
// other writes a shell command line where the value has to be quoted and the
// key must not be.
type Var struct {
	Key   string
	Value string
}

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
type Identity struct {
	// SocketPath is the daemon's listening socket — what a child's `jin`
	// resolves instead of falling back to the default path.
	SocketPath string
	// BinPath is the jin binary a child should re-enter. Callers differ on
	// which one that is: a spawn whose environment outlives the daemon's own
	// executable wants the stable copy, a short-lived one can use the running
	// binary.
	BinPath string
	// Debug is whether the child should write its own debug log. Callers pass
	// what debug.Enabled() reports rather than re-reading the variable, so a
	// child cannot end up disagreeing with its parent about what "on" means.
	Debug bool
}

// Vars returns the assignments this identity implies, skipping any whose value
// is not known.
//
// Skipping rather than emitting an empty is deliberate: every reader treats an
// empty JIN_SOCKET as no socket and an empty JIN_BIN as no binary, so an empty
// assignment carries no information the absence does not — it only makes a
// child's environment claim to know something it does not.
func (i Identity) Vars() []Var {
	vars := make([]Var, 0, 3)
	if i.SocketPath != "" {
		vars = append(vars, Var{Key: "JIN_SOCKET", Value: i.SocketPath})
	}
	if i.BinPath != "" {
		vars = append(vars, Var{Key: "JIN_BIN", Value: i.BinPath})
	}
	if i.Debug {
		vars = append(vars, Var{Key: "JIN_DEBUG", Value: "1"})
	}
	return vars
}

// Environ renders Vars in the KEY=VALUE form os/exec expects.
func (i Identity) Environ() []string {
	vars := i.Vars()
	env := make([]string, 0, len(vars))
	for _, v := range vars {
		env = append(env, v.Key+"="+v.Value)
	}
	return env
}
