package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every build command in this file writes through an absolute path. A version
// of RunBuilds that ignored BuildOptions.Dir would otherwise scatter files into
// the package's own source directory instead of failing a test.

// resolved makes a path comparable to one a subprocess printed: t.TempDir can
// hand back a path through a symlinked temp root, and `pwd` reports the target.
func resolved(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return got
}

func readBuildFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

// TestRunBuilds_EnvIsCuratedNotInherited pins the property both build surfaces
// depend on: what a build sees is decided here, not by whoever invoked jin.
// Inheriting the caller's environment is what let one command pass a build the
// other rejected.
func TestRunBuilds_EnvIsCuratedNotInherited(t *testing.T) {
	t.Setenv("JIN_BUILD_ENV_PROBE", "leaked")
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.txt")
	guard := filepath.Join(dir, "guard.txt")
	path := filepath.Join(dir, "path.txt")

	// Each printenv exits non-zero for an unset name, so the trailing `true`
	// keeps the step's own exit status out of what this test asserts.
	err := RunBuilds([]string{
		"printenv JIN_BUILD_ENV_PROBE > " + probe + "; " +
			"printenv npm_config_ignore_scripts > " + guard + "; " +
			"printenv PATH > " + path + "; true",
	}, BuildOptions{Dir: dir, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("RunBuilds: %v", err)
	}

	if got := readBuildFile(t, probe); got != "" {
		t.Errorf("caller's own env var reached the build as %q; the build environment must not depend on who invoked jin", got)
	}
	if got := readBuildFile(t, guard); got != "true" {
		t.Errorf("npm_config_ignore_scripts = %q, want true", got)
	}
	// Without this, the assertion above is also satisfied by handing the build
	// an empty environment, which no real build survives.
	if got := readBuildFile(t, path); got == "" {
		t.Error("PATH did not reach the build; the environment is an allowlist, not an empty set")
	}
}

// TestRunBuilds_TimeoutBoundsWholeSequence distinguishes a per-step budget from
// a sequence budget: each step fits the timeout on its own, only their sum does
// not.
func TestRunBuilds_TimeoutBoundsWholeSequence(t *testing.T) {
	dir := t.TempDir()
	step1 := filepath.Join(dir, "step1")
	step2 := filepath.Join(dir, "step2")

	err := RunBuilds([]string{
		"sleep 1.2; touch " + step1,
		"sleep 1.2; touch " + step2,
	}, BuildOptions{Dir: dir, Timeout: 2 * time.Second})

	if err == nil {
		t.Fatal("a sequence that outruns its budget must fail")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it to name the timeout", err)
	}
	// Without this the assertion below is also satisfied by a build that never
	// started at all.
	if _, statErr := os.Stat(step1); statErr != nil {
		t.Fatalf("the first step never completed, so the check below proves nothing: %v", statErr)
	}
	if _, statErr := os.Stat(step2); statErr == nil {
		t.Error("the second step completed: the deadline covered one step at a time, not the sequence")
	}
}

// TestRunBuilds_TimeoutTakesDownWhatTheScriptStarted is the proof that the
// deadline reaches past the process this package launched.
//
// A build command is a shell script and shell scripts start things. Cancelling
// a plain exec.CommandContext signals only the process it launched, so a
// timeout would leave the descendants running: the script here spawns a child
// that would create a file after the deadline, and that file existing is proof
// the kill stopped at the leader. It lives beside RunBuilds because RunBuilds
// is what chooses procgroup — a version that dropped that choice would
// otherwise only be caught by another package's tests.
func TestRunBuilds_TimeoutTakesDownWhatTheScriptStarted(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "STARTED")
	survivor := filepath.Join(dir, "SURVIVED")

	// The inner sh outlives the deadline; the trailing sleep keeps the leader
	// alive so the context is what ends the run, not the script finishing.
	err := RunBuilds(
		[]string{"touch " + started + "; sh -c 'sleep 1.5; touch " + survivor + "' & sleep 30"},
		BuildOptions{Dir: dir, Timeout: 300 * time.Millisecond})

	if err == nil {
		t.Fatal("a build killed by its deadline must fail")
	}
	// Without this the test passes when the script never ran at all, which is
	// the same evidence as the descendant having been killed.
	if _, statErr := os.Stat(started); statErr != nil {
		t.Fatalf("the build script never ran, so the check below proves nothing: %v", statErr)
	}

	// Past when the descendant would have fired had it lived.
	time.Sleep(2 * time.Second)
	if _, statErr := os.Stat(survivor); statErr == nil {
		t.Error("the process the build script started outlived the timeout — the kill reached only the leader")
	}
}

// TestRunBuilds_RunsInDir reports the observed working directory through a path
// outside Dir, so a version that ignores Dir is caught by the comparison.
func TestRunBuilds_RunsInDir(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(t.TempDir(), "cwd.txt")

	if err := RunBuilds([]string{"pwd > " + report}, BuildOptions{Dir: dir, Timeout: 30 * time.Second}); err != nil {
		t.Fatalf("RunBuilds: %v", err)
	}
	if got, want := resolved(t, readBuildFile(t, report)), resolved(t, dir); got != want {
		t.Errorf("build ran in %s, want %s", got, want)
	}
}

// TestRunBuilds_StopsAtFirstFailure pins both halves of a failed step: the
// sequence does not continue past it, and the error names the command and its
// exit status rather than a bare "failed".
func TestRunBuilds_StopsAtFirstFailure(t *testing.T) {
	dir := t.TempDir()
	after := filepath.Join(dir, "after")

	err := RunBuilds([]string{"exit 3", "touch " + after}, BuildOptions{Dir: dir, Timeout: 30 * time.Second})
	if err == nil {
		t.Fatal("a step exiting non-zero must fail the sequence")
	}
	for _, want := range []string{`"exit 3"`, "exit status 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	if _, statErr := os.Stat(after); statErr == nil {
		t.Error("a step after the failing one ran")
	}
}

// TestRunBuilds_InstallSideKeepsALog covers the wrapper an install goes
// through. Both halves of its promise are load-bearing and neither shows up in
// a test that only asserts the plugin got installed: the log has to hold what
// the build printed, and the failure has to say where that log is — a build
// that failed on someone else's machine is diagnosed from nothing else.
func TestRunBuilds_InstallSideKeepsALog(t *testing.T) {
	staging, stateDir := t.TempDir(), t.TempDir()

	// Arithmetic, not a literal: see TestRunBuilds_HeadersNumberEveryStep.
	err := runStagedBuild(staging, stateDir, "notifier", []string{"echo $((21+21))", "exit 5"}, 30*time.Second)
	if err == nil {
		t.Fatal("a failing step must fail the install's build")
	}

	logPath := filepath.Join(stateDir, "plugin-logs", "notifier-build.log")
	if !strings.Contains(err.Error(), "(log: "+logPath+")") {
		t.Errorf("error = %q, want it to point at %s", err, logPath)
	}
	if !strings.Contains(err.Error(), "exit status 5") {
		t.Errorf("error = %q, want the wrapper to keep what RunBuilds reported", err)
	}
	if body := readBuildFile(t, logPath); !strings.Contains(body, "42") {
		t.Errorf("build log did not capture the build's output; got %q", body)
	}
}

// TestRunBuilds_HeadersNumberEveryStep pins the only progress a long build
// shows — which of how many steps is running — and that both of the step's own
// streams reach the same writer.
//
// The steps print arithmetic rather than a literal so the expected text cannot
// appear in the header that echoes the command. A marker that is also in the
// command is satisfied by the header alone, which lets a build whose output
// goes nowhere pass this test.
func TestRunBuilds_HeadersNumberEveryStep(t *testing.T) {
	var out bytes.Buffer

	if err := RunBuilds([]string{"true", "echo $((21+21)); echo $((300+39)) >&2"}, BuildOptions{
		Dir: t.TempDir(), Timeout: 30 * time.Second, Out: &out,
	}); err != nil {
		t.Fatalf("RunBuilds: %v", err)
	}
	for _, want := range []string{"build step 1/2: true", "build step 2/2:", "42", "339"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q; got %q", want, out.String())
		}
	}
}
