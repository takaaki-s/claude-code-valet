package procgroup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists. Signal 0 performs the permission and
// existence checks without delivering anything.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitGone polls until pid is reaped or the budget runs out, and says which.
// Polling rather than sleeping keeps the test as fast as the kill actually is.
func waitGone(pid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// runWithGrandchild starts a shell that records a background grandchild's PID,
// cancels its context, and returns that PID.
//
// It deliberately does NOT wait for the command before returning. Waiting first
// would hide the very failure these tests exist to catch: with no escalation,
// Wait blocks until the grandchild finishes on its own, and by the time it
// returns the process is gone for the wrong reason. Reaping happens in the
// background so the assertion sees the process group's actual fate.
func runWithGrandchild(t *testing.T, script string) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "sh", "-c", strings.ReplaceAll(script, "{{PID_FILE}}", pidFile))
	KillOnCancel(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(GracePeriod + 5*time.Second):
			t.Error("the command never unblocked, so nothing took its process group down")
		}
	})

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("grandchild never reported a pid")
	}
	if !alive(pid) {
		t.Fatal("grandchild exited before the test could cancel anything")
	}

	cancel()
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// TestKillOnCancel_TakesTheGrandchildToo is the whole reason this package
// exists. exec.CommandContext signals the leader, so without a process group
// the `sleep` outlives the shell that started it — and outlives jind-ai's
// belief that the work stopped.
func TestKillOnCancel_TakesTheGrandchildToo(t *testing.T) {
	pid := runWithGrandchild(t, "sleep 120 & echo $! > {{PID_FILE}}\nwait\n")
	if !waitGone(pid, 2*time.Second) {
		t.Errorf("grandchild pid %d survived the group signal", pid)
	}
}

// TestKillOnCancel_EscalatesPastAnIgnoredSIGTERM covers the second half: a
// process that ignores SIGTERM has to be taken down anyway, which is what
// GracePeriod buys and what Cmd.WaitDelay would only do to the leader.
//
// The grandchild sleeps far longer than the budget here, so "it went away"
// cannot be it finishing on its own — and the first assertion proves the
// signal really was ignored, so a platform where it is not fails loudly rather
// than passing without testing anything.
func TestKillOnCancel_EscalatesPastAnIgnoredSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out GracePeriod")
	}
	pid := runWithGrandchild(t, "trap '' TERM\nsleep 120 & echo $! > {{PID_FILE}}\nwait\n")

	// SIG_IGN is inherited across fork and exec, so the sleep ignores TERM too.
	if waitGone(pid, 1*time.Second) {
		t.Fatal("the grandchild died on SIGTERM, so this run never exercised the escalation")
	}
	if !waitGone(pid, GracePeriod+3*time.Second) {
		t.Errorf("grandchild pid %d outlived GracePeriod (%s); the SIGKILL escalation did not reach the group", pid, GracePeriod)
	}
}

// TestKillOnCancel_LeavesAFinishedRunAlone checks the ordinary path is
// untouched: wiring the teardown must not change what a process that exits on
// its own returns.
func TestKillOnCancel_LeavesAFinishedRunAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "exit 3")
	KillOnCancel(cmd)

	var exitErr *exec.ExitError
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected the exit status to survive")
	}
	if !asExitError(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("err = %v, want exit status 3", err)
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
