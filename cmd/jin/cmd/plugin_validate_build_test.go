package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/pkg/plugin/manifest"
)

func buildManifest(cmds ...string) *manifest.Manifest {
	return &manifest.Manifest{
		Install: manifest.Install{
			Source: &manifest.SourceInstall{Build: cmds, Entrypoint: "x"},
		},
	}
}

// TestBuildTimeout covers the floor rule on its own, which is only possible
// because runBuildChecks takes the budget rather than deriving it: the same
// cases through runBuildChecks would each cost a real subprocess and a real
// wait.
func TestBuildTimeout(t *testing.T) {
	for _, tt := range []struct {
		name     string
		declared time.Duration
		want     time.Duration
	}{
		{"unset takes the default, since DefaultTimeout is under the floor", 0, buildTimeoutDefault},
		{"under the floor is replaced, not raised", 10 * time.Second, buildTimeoutDefault},
		{"at the floor is kept", buildTimeoutFloor, buildTimeoutFloor},
		{"over the floor is kept", 2 * time.Minute, 2 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := buildManifest("true")
			m.Timeout = tt.declared
			if got := buildTimeout(m); got != tt.want {
				t.Errorf("buildTimeout(%v) = %v, want %v", tt.declared, got, tt.want)
			}
		})
	}
}

// TestRunBuildChecks_TimeoutTakesDownWhatTheScriptStarted is the end-to-end
// proof that this call site builds its command through procgroup.
//
// A build command is a shell script and shell scripts start things. Cancelling
// a plain exec.CommandContext signals only the process it launched, so a
// timeout used to leave the descendants running: the script here spawns a child
// that would create a file after the deadline, and that file existing is proof
// the kill stopped at the leader.
func TestRunBuildChecks_TimeoutTakesDownWhatTheScriptStarted(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "STARTED")
	survivor := filepath.Join(dir, "SURVIVED")

	// The inner sh outlives the deadline; the trailing sleep keeps the leader
	// alive so the context is what ends the run, not the script finishing.
	m := buildManifest("touch " + started + "; sh -c 'sleep 1.5; touch " + survivor + "' & sleep 30")

	var out bytes.Buffer
	findings := runBuildChecks(&out, m, dir, 300*time.Millisecond)

	if len(findings) == 0 {
		t.Fatal("a build killed by its timeout should be reported as a finding")
	}
	if findings[0].Rule != manifest.RuleBuildExec {
		t.Errorf("Rule = %v, want %v", findings[0].Rule, manifest.RuleBuildExec)
	}
	// Without this the test passes when the script never ran at all, which is
	// the same evidence as the descendant having been killed.
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("the build script never ran, so the check below proves nothing: %v", err)
	}

	// Past when the descendant would have fired had it lived.
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(survivor); err == nil {
		t.Error("the process the build script started outlived the timeout — the kill reached only the leader")
	}
}

// TestRunBuildChecks_CleanBuildReportsNothing keeps the timeout plumbing from
// being satisfied by a version that fails everything, and pins the echo, which
// is the only progress a long build shows.
func TestRunBuildChecks_CleanBuildReportsNothing(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer

	if f := runBuildChecks(&out, buildManifest("true", "true"), dir, 10*time.Second); len(f) != 0 {
		t.Errorf("findings = %v, want none for a build that succeeds", f)
	}
	if !bytes.Contains(out.Bytes(), []byte("[build] true")) {
		t.Errorf("build commands should be echoed; got %q", out.String())
	}
}
