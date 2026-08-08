package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/pkg/plugin/manifest"
)

// buildManifest is a manifest that only carries build commands. Actions stays
// nil — normalize() runs on load, not on a struct built here — so
// runBuildChecks' entrypoint loop has nothing to walk and these tests see the
// build result alone. Rule #14 is covered end-to-end in plugin_validate_test.go.
func buildManifest(cmds ...string) *manifest.Manifest {
	return &manifest.Manifest{
		Install: manifest.Install{Source: &manifest.SourceInstall{Build: cmds}},
	}
}

// TestValidateBuildTimeout_IgnoresThisMachinesConfig pins both halves of where
// the budget comes from.
//
// It must track the shipped default rather than restate it, or the two commands
// drift apart again the moment that default moves. And it must not read the
// local plugins.build_timeout even though an install does: that setting belongs
// to whoever installs the plugin, so an author who raised their own would
// certify a build only their machine admits — the same defect as letting the
// author's environment decide whether the build succeeds.
//
// The config written here declares a budget nothing could build under, so a
// version that consults it cannot coincide with the default and pass anyway.
func TestValidateBuildTimeout_IgnoresThisMachinesConfig(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "jind-ai"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	writeFile(t, filepath.Join(cfgHome, "jind-ai", "config.yaml"), "plugins:\n  build_timeout: 1\n")

	want := config.DefaultPluginsConfig().BuildTimeoutDuration()
	if want <= 0 {
		t.Fatal("the shipped default must be a positive budget, or no build can finish")
	}
	if got := validateBuildTimeout(); got != want {
		t.Errorf("validateBuildTimeout() = %v, want the shipped install default %v", got, want)
	}
}

// TestPluginValidateRunBuildIgnoresManifestTimeout is the regression test for
// the second half of the divergence.
//
// The budget used to be derived from the manifest's timeout:, which bounds how
// long an action may take to answer a dispatch — not how long the plugin takes
// to build. A manifest declaring more than the install default therefore
// passed --run-build on a budget no default install would grant it, which is
// the same "validate passes, install fails" shape as the environment defect.
// The declaration below is deliberately far too small for any process to start
// in: if it reaches the build at all, the build times out.
func TestPluginValidateRunBuildIgnoresManifestTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "jind-ai-plugin.yaml"), `schema_version: 1
name: tiny-timeout
version: 0.1.0
description: declares a dispatch timeout far shorter than any build
jin: ">=0.0.0"
timeout: 1ms
install:
  source:
    build:
      - "true"
    entrypoint: ./run.sh
`)
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/usr/bin/env bash\nexit 0\n")

	out, err := runValidateCmd(t, "--skip-uniqueness", "--run-build", dir)
	if err != nil {
		t.Fatalf("a manifest's dispatch timeout must not become its build budget: %v\nout=%q", err, out)
	}
}

// TestRunBuildChecks_CleanBuildReportsNothing keeps the timeout plumbing from
// being satisfied by a version that fails everything, and checks the build was
// actually run rather than skipped. What the progress looks like belongs to
// plugin.RunBuilds and is pinned there.
func TestRunBuildChecks_CleanBuildReportsNothing(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer

	if f := runBuildChecks(&out, buildManifest("true", "true"), dir, 10*time.Second); len(f) != 0 {
		t.Errorf("findings = %v, want none for a build that succeeds", f)
	}
	if out.Len() == 0 {
		t.Error("a build that runs reports its progress; nothing was written")
	}
}

// TestRunBuildChecks_BuildEnvIsTheInstallEnv is the regression test for the
// defect this file's build path was rebuilt to close.
//
// --run-build exists to answer one question — will the build an install runs
// succeed? — and it can only answer it from the environment an install builds
// in. While this check inherited the caller's environment instead, a build
// leaning on a variable the author happened to have exported passed here and
// failed on the installing user's machine, and a build leaning on the
// supply-chain guard an install injects failed here and installed fine.
//
// The command below passes under exactly one environment: the caller's own
// variables absent, npm_config_ignore_scripts present.
func TestRunBuildChecks_BuildEnvIsTheInstallEnv(t *testing.T) {
	t.Setenv("JIN_BUILD_ENV_PROBE", "leaked")
	var out bytes.Buffer

	m := buildManifest(`test -z "$JIN_BUILD_ENV_PROBE" && [ "$npm_config_ignore_scripts" = true ]`)
	if f := runBuildChecks(&out, m, t.TempDir(), 30*time.Second); len(f) != 0 {
		t.Errorf("findings = %v, want none: validate must build in the same environment install does", f)
	}
}

// TestRunBuildChecks_FailureNamesTheStepAndNoLog pins how this layer turns a
// build failure into a finding: one ERROR against install.source.build,
// carrying what the builder said. validate writes no build log, so a log
// pointer in the message would name a file that does not exist.
func TestRunBuildChecks_FailureNamesTheStepAndNoLog(t *testing.T) {
	var out bytes.Buffer

	findings := runBuildChecks(&out, buildManifest("true", "exit 7"), t.TempDir(), 10*time.Second)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	f := findings[0]
	if f.Rule != manifest.RuleBuildExec || f.Severity != manifest.SeverityError || f.Field != "install.source.build" {
		t.Errorf("finding = %v, want an ERROR R%d against install.source.build", f, manifest.RuleBuildExec)
	}
	// The builder's own text has to survive the hand-off, or the author is told
	// only that "the build failed".
	if !strings.Contains(f.Message, `"exit 7"`) {
		t.Errorf("Message = %q, want it to name the step that failed", f.Message)
	}
	if strings.Contains(f.Message, "(log:") {
		t.Errorf("Message = %q, want no log pointer: validate keeps no build log", f.Message)
	}
}

// TestRunBuildChecks_ReleaseAssetSkipsTheBuild keeps the early return honest:
// a manifest with no install.source has no build commands to run, so nothing
// should be executed at all.
func TestRunBuildChecks_ReleaseAssetSkipsTheBuild(t *testing.T) {
	var out bytes.Buffer

	m := &manifest.Manifest{}
	if f := runBuildChecks(&out, m, t.TempDir(), 10*time.Second); f != nil {
		t.Errorf("findings = %v, want nil for a manifest with no install.source", f)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing run", out.String())
	}
}
