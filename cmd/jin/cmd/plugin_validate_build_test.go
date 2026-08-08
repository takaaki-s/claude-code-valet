package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/procgroup"
	"github.com/takaaki-s/jind-ai/pkg/plugin/manifest"
)

// shortBuildTimeout points runBuildChecks at a timeout a test can wait out.
// The production floor is a minute, which is the right budget for a compile and
// the wrong one for a test, so the two knobs are vars.
func shortBuildTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	floor, def := buildTimeoutFloor, buildTimeoutDefault
	buildTimeoutFloor, buildTimeoutDefault = d, d
	t.Cleanup(func() { buildTimeoutFloor, buildTimeoutDefault = floor, def })
}

// buildManifest sets Timeout below any floor a test installs, so the build
// budget comes from buildTimeoutDefault. Leaving it unset would take
// manifest.DefaultTimeout (30s) instead, which is above the floor and so is
// used as-is — a test that skipped this would sit through the real timeout and
// look like the fix had failed.
func buildManifest(cmds ...string) *manifest.Manifest {
	return &manifest.Manifest{
		Timeout: time.Millisecond,
		Install: manifest.Install{
			Source: &manifest.SourceInstall{Build: cmds, Entrypoint: "x"},
		},
	}
}

// TestRunBuildChecks_TimeoutTakesDownWhatTheScriptStarted is the property
// procgroup exists for, checked at this call site because the call site is what
// forgets it.
//
// A build command is a shell script and shell scripts start things.
// exec.CommandContext signals only the process it launched, so a timeout used
// to leave the descendants running: here the script spawns a child that would
// create a file shortly after the deadline, and its existence afterwards is
// proof the kill did not reach past the leader.
func TestRunBuildChecks_TimeoutTakesDownWhatTheScriptStarted(t *testing.T) {
	dir := t.TempDir()
	survivor := filepath.Join(dir, "SURVIVED")
	shortBuildTimeout(t, 300*time.Millisecond)

	// The inner sh outlives the deadline; the trailing sleep keeps the leader
	// alive so the context is what ends the run, not the script finishing.
	m := buildManifest("sh -c 'sleep 1.5; touch " + survivor + "' & sleep 30")

	var out bytes.Buffer
	findings := runBuildChecks(&out, m, dir)

	if len(findings) == 0 {
		t.Fatal("a build killed by its timeout should be reported as a finding")
	}
	if findings[0].Rule != manifest.RuleBuildExec {
		t.Errorf("Rule = %v, want %v", findings[0].Rule, manifest.RuleBuildExec)
	}

	// Past when the descendant would have fired had it lived.
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(survivor); err == nil {
		t.Error("the process the build script started outlived the timeout — the kill reached only the leader")
	}
}

// TestRunBuildChecks_TimeoutReturnsPromptly covers the other half of
// KillOnCancel, which the group kill alone does not give.
//
// out is an io.Writer rather than an *os.File, so os/exec puts a pipe between
// the build and this process. A descendant that has left the group still holds
// the write end, and Wait does not return while anything does — the caller that
// set a timeout would never get control back. WaitDelay bounds that.
func TestRunBuildChecks_TimeoutReturnsPromptly(t *testing.T) {
	dir := t.TempDir()
	shortBuildTimeout(t, 300*time.Millisecond)

	// setsid puts the child in a session of its own, out of reach of the group
	// signal, still holding the inherited pipe.
	m := buildManifest("setsid sleep 30 & sleep 30")

	var out bytes.Buffer
	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); runBuildChecks(&out, m, dir) }()

	// The budget is what procgroup promises a caller; anything beyond it means
	// Wait is blocked on a descendant rather than bounded by WaitDelay.
	select {
	case <-done:
		if el := time.Since(start); el > procgroup.TeardownBudget+3*time.Second {
			t.Errorf("returned after %v, want within the teardown budget (%v)", el, procgroup.TeardownBudget)
		}
	case <-time.After(procgroup.TeardownBudget + 10*time.Second):
		t.Fatal("runBuildChecks never returned — a descendant is holding the pipe open")
	}
}

// TestRunBuildChecks_CleanBuildReportsNothing keeps the timeout plumbing from
// being satisfied by a version that just fails everything.
func TestRunBuildChecks_CleanBuildReportsNothing(t *testing.T) {
	dir := t.TempDir()
	shortBuildTimeout(t, 10*time.Second)

	var out bytes.Buffer
	if f := runBuildChecks(&out, buildManifest("true", "true"), dir); len(f) != 0 {
		t.Errorf("findings = %v, want none for a build that succeeds", f)
	}
	if !bytes.Contains(out.Bytes(), []byte("[build] true")) {
		t.Errorf("build commands should be echoed; got %q", out.String())
	}
}
