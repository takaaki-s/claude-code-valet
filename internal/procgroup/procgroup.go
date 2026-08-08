// Package procgroup runs a child process in a process group of its own, so
// cancelling its context takes down everything the child started rather than
// only the process jind-ai launched.
//
// It exists because the standard library's answer is not the one this project
// needs. exec.CommandContext kills the leader PID on cancellation, and
// Cmd.WaitDelay escalates to SIGKILL against that same PID — neither reaches a
// grandchild. Every process jind-ai spawns is a script or a CLI that spawns
// more (a plugin's interpreter, a worktree hook's bash, an agent's own tooling),
// so "the leader is gone" and "the work has stopped" are different statements.
//
// Three call sites arrived at this independently before it was a package. The
// point of having one now is that the reasoning below is stated once, and a
// fourth caller inherits it instead of rediscovering it.
package procgroup

import (
	"os/exec"
	"syscall"
	"time"
)

// GracePeriod is how long a process group has to exit after SIGTERM before
// KillOnCancel escalates to a group-wide SIGKILL. Long enough for a shell trap
// to run its cleanup, short enough that a caller waiting on the process is not
// left wondering.
const GracePeriod = 5 * time.Second

// KillOnCancel places cmd in a new process group and wires context
// cancellation to signal that whole group: SIGTERM first, then SIGKILL after
// GracePeriod for anything that ignored it.
//
// Call it before Start. cmd.Cancel is read by the context-watcher goroutine
// Start spawns, so setting it afterwards is a data race; the closure reads
// cmd.Process.Pid, which Start populates before that watcher can observe a
// cancellation.
//
// The escalation timer is fire-and-forget on purpose. Once Cancel has fired the
// SIGKILL is unconditional: a signal sent to an already-reaped group fails with
// ESRCH and costs nothing, which is cheaper than sharing a *Timer between the
// watcher goroutine and the caller in order to stop it.
//
// The negated PID is the whole mechanism — kill(2) treats -pid as "every
// process in the group led by pid", which is why Setpgid has to be set as well.
func KillOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		time.AfterFunc(GracePeriod, func() {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
}
