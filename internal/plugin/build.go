package plugin

// This file owns the one implementation of "run a plugin's
// install.source.build commands". Both surfaces that build a plugin go
// through it — `jin plugin install`/`update` (via InstallPlan.Commit) and
// `jin plugin validate --run-build` — so the build an author checks before
// publishing is the build the installing user actually gets.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/takaaki-s/jind-ai/internal/procgroup"
)

// BuildOptions carries the parts of a build sequence that legitimately differ
// per caller: where it runs, how long it may take, and where its output goes.
//
// Everything the outcome depends on — the environment, the shell, the process
// group, whether the deadline covers one step or all of them — is fixed inside
// RunBuilds and deliberately has no option here. A knob for any of those is how
// the two callers drifted apart in the first place.
type BuildOptions struct {
	// Dir is the working directory every step runs in: the staging clone for
	// an install, the plugin's own directory for a validate.
	Dir string
	// Timeout is the budget for the run. It has no useful zero value: a
	// non-positive one expires before the first step, so callers resolve their
	// own budget rather than leaving it out. What it covers is RunBuilds' to
	// say, not the caller's — see below.
	Timeout time.Duration
	// Out receives each step's header line and its stdout/stderr. A nil Out
	// discards them.
	Out io.Writer
}

// RunBuilds runs cmds through `bash -c` in opts.Dir, in order, stopping at the
// first failure — a broken step cannot produce a meaningful artifact for the
// steps after it.
//
// The environment is the curated base plus npm_config_ignore_scripts=true (a
// supply-chain guard the author can override inside their own build command).
// It is not the caller's environment, and that is the point: a build that only
// succeeds because the author had some toolchain variable exported has to fail
// here, rather than pass the author's check and fail on the installing user's
// machine.
//
// opts.Timeout bounds the entire sequence, so a wedged step cannot outlive the
// caller-supplied window. Each step gets its own process group so the escalated
// SIGKILL sweeps whatever the step's script started.
func RunBuilds(cmds []string, opts BuildOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	// curatedEnv() scans os.Environ() on every call, so the environment is
	// assembled once here rather than per step.
	env := append(curatedEnv(), "npm_config_ignore_scripts=true")

	for i, cmdStr := range cmds {
		fmt.Fprintf(out, "\n--- build step %d/%d: %s ---\n", i+1, len(cmds), cmdStr)

		cmd := procgroup.CommandContext(ctx, "bash", "-c", cmdStr)
		cmd.Dir = opts.Dir
		cmd.Env = env
		cmd.Stdout = out
		cmd.Stderr = out

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start build step %q: %w", cmdStr, err)
		}
		runErr := cmd.Wait()

		// The deadline is checked before runErr because a killed step reports
		// only "signal: killed", which says nothing about why it died.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("build timed out after %s", opts.Timeout)
		}
		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				return fmt.Errorf("build step %q failed: exit status %d", cmdStr, exitErr.ExitCode())
			}
			return fmt.Errorf("run build step %q: %w", cmdStr, runErr)
		}
	}
	return nil
}

// runStagedBuild is the install-side wrapper: it truncates the plugin's build
// log at <stateDir>/plugin-logs/<name>-build.log, tees it to stderr so an
// interactive install shows progress, leaves the running to RunBuilds, and
// points any failure at the log — which is all a user has to go on once the
// install has rolled the staging directory away.
func runStagedBuild(stagingDir, stateDir, name string, cmds []string, timeout time.Duration) error {
	logPath := filepath.Join(stateDir, "plugin-logs", name+"-build.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("mkdir plugin log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open build log: %w", err)
	}
	defer logFile.Close()

	if err := RunBuilds(cmds, BuildOptions{
		Dir:     stagingDir,
		Timeout: timeout,
		Out:     io.MultiWriter(logFile, os.Stderr),
	}); err != nil {
		return fmt.Errorf("%w (log: %s)", err, logPath)
	}
	return nil
}
