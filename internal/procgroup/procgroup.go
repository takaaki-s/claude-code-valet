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
//
// Build commands with CommandContext, not exec.CommandContext. The first
// version of this package offered only KillOnCancel, to be remembered after
// building the command — and a fourth call site was missed, because forgetting
// it produces a process that works. The linter forbids exec.CommandContext
// outside this package so that the safe form is the one you get by default;
// see .golangci.yml.
package procgroup

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// CommandContext is exec.CommandContext with KillOnCancel already applied. It
// is the way to build a child process in this project.
//
// The returned command is ready for the caller to set Dir, Env, Stdin, Stdout
// and Stderr on before Start — none of which this package touches. It writes
// only SysProcAttr, WaitDelay and Cancel, and a caller that needs its own
// SysProcAttr fields (Credential, Pdeathsig) must add them to the struct this
// installed rather than replacing it.
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...) //nolint:forbidigo // the one sanctioned call
	KillOnCancel(cmd)
	return cmd
}

// GracePeriod is how long a process group has to exit after SIGTERM before
// KillOnCancel escalates to a group-wide SIGKILL. Long enough for a shell trap
// to run its cleanup, short enough that a caller waiting on the process is not
// left wondering.
const GracePeriod = 5 * time.Second

// waitMargin is how much longer than GracePeriod Wait will hold on before it
// stops waiting for the child's I/O and returns anyway. It has to be the
// larger of the two: the SIGKILL escalation is the thing that normally ends
// this, and cutting Wait short of it would report a timeout while the kill
// that was about to work had not yet fired.
const waitMargin = 2 * time.Second

// TeardownBudget is the longest Wait can take after a context is cancelled:
// the grace the group gets, plus the extra Wait holds on for I/O before giving
// up. Exported because it is the number a caller has to add to its own timeout
// to know when it really gets control back — GracePeriod alone understates it,
// and a caller that budgets against the smaller figure is 2 seconds optimistic
// about the worst case.
const TeardownBudget = GracePeriod + waitMargin

// KillOnCancel places cmd in a new process group and wires context
// cancellation to signal that whole group: SIGTERM first, then SIGKILL after
// GracePeriod for anything that ignored it.
//
// Prefer CommandContext, which calls this for you. Reach for KillOnCancel
// directly only when the command comes from somewhere else already built.
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
//
// WaitDelay is the second half, and it is not redundant with the first. When
// Stdout or Stderr is anything other than an *os.File, os/exec builds a pipe
// and a copying goroutine, and Wait does not return until every descendant
// holding the write end has closed it. A descendant that leaves the group —
// setsid does exactly that — is out of reach of the signals above while still
// holding that pipe, so Wait blocks with the process already dead. Measured:
// with a group kill and no WaitDelay, Run had not returned after 15 seconds
// against a 1-second context; with WaitDelay it returned as soon as the delay
// elapsed. A caller that set a timeout is entitled to get control back.
func KillOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = TeardownBudget
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		time.AfterFunc(GracePeriod, func() {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
}
