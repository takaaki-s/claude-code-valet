package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agentdocs"
)

// initEnv isolates one `jin init` run: its own HOME, its own config dir, and a
// PATH containing only the fake agent executables the test asks for.
type initEnv struct {
	home string
}

func newInitEnv(t *testing.T, agents ...string) initEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	binDir := t.TempDir()
	for _, a := range agents {
		p := filepath.Join(binDir, a)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", a, err)
		}
	}
	t.Setenv("PATH", binDir)

	return initEnv{home: home}
}

func (e initEnv) claudeSkill() string {
	return filepath.Join(e.home, ".claude", "skills", "jind-ai", "SKILL.md")
}

func (e initEnv) agentsSkill() string {
	return filepath.Join(e.home, ".agents", "skills", "jind-ai", "SKILL.md")
}

func (e initEnv) openCodeSkill() string {
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "skills", "jind-ai", "SKILL.md")
}

// files lists every regular file under HOME, so a test can assert that a
// declined install left nothing at all behind.
func (e initEnv) files(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(e.home, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", e.home, err)
	}
	return out
}

// skillFiles lists every SKILL.md that was installed, across both roots the
// targets can land in: HOME for the claude and codex locations, and
// XDG_CONFIG_HOME for opencode's own. Filtering by name keeps config.yaml,
// which also lives under XDG_CONFIG_HOME, out of the count.
func (e initEnv) skillFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, root := range []string{e.home, os.Getenv("XDG_CONFIG_HOME")} {
		if root == "" {
			continue
		}
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && filepath.Base(path) == "SKILL.md" {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

// dirs lists every directory under HOME. Consent has to gate directory
// creation too: an empty ~/.agents/skills/jind-ai/ left by a refused prompt is
// still something the user did not agree to.
func (e initEnv) dirs(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(e.home, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && path != e.home {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", e.home, err)
	}
	return out
}

// runInitCmd runs `jin init` with the given flags and stdin, resetting every
// package-level flag variable on both sides so cobra's retained state cannot
// leak between tests.
func runInitCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	// Clearing a flag needs Changed reset as well as the value: cobra's
	// MarkFlagsMutuallyExclusive tests Changed, not the value, so setting
	// --skill and --no-skill back to "false" between runs would still read as
	// "both were given" and fail every later invocation.
	reset := func() {
		forceInit, initSkill, initNoSkill, initDryRun = false, false, false, false
		initSkillDir = ""
		for _, name := range []string{"force", "skill", "no-skill", "dry-run", "skill-dir"} {
			f := initCmd.Flags().Lookup(name)
			if f == nil {
				t.Fatalf("init has no --%s flag", name)
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	}
	reset()
	t.Cleanup(reset)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetIn(strings.NewReader(stdin))
	rootCmd.SetArgs(append([]string{"init"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// withTerminal makes the interactive branch reachable under `go test`, where
// stdin is always a pipe.
func withTerminal(t *testing.T, isTTY bool) {
	t.Helper()
	prev := initStdinIsTerminal
	initStdinIsTerminal = func() bool { return isTTY }
	t.Cleanup(func() { initStdinIsTerminal = prev })
}

func TestInitSkip_NoAgentOnPath(t *testing.T) {
	env := newInitEnv(t) // no agents
	withTerminal(t, true)

	out, err := runInitCmd(t, "y\n")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "No agent CLI found on PATH") {
		t.Errorf("no explanation for skipping:\n%s", out)
	}
	// Nothing may be created in HOME — not even an empty directory.
	if files := env.files(t); len(files) != 0 {
		t.Errorf("files created with no agent installed: %v", files)
	}
	if dirs := env.dirs(t); len(dirs) != 0 {
		t.Errorf("directories created with no agent installed: %v", dirs)
	}
}

func TestInitSkillTargetsPerKind(t *testing.T) {
	tests := []struct {
		name   string
		agents []string
		want   func(initEnv) []string
	}{
		{"claude only", []string{"claude"}, func(e initEnv) []string { return []string{e.claudeSkill()} }},
		{"codex only", []string{"codex"}, func(e initEnv) []string { return []string{e.agentsSkill()} }},
		{"opencode only", []string{"opencode"}, func(e initEnv) []string { return []string{e.openCodeSkill()} }},
		{"claude and codex", []string{"claude", "codex"}, func(e initEnv) []string {
			return []string{e.claudeSkill(), e.agentsSkill()}
		}},
		// opencode reads ~/.claude and ~/.agents as well, so a machine with
		// all three still gets exactly two files.
		{"all three", []string{"claude", "codex", "opencode"}, func(e initEnv) []string {
			return []string{e.claudeSkill(), e.agentsSkill()}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newInitEnv(t, tc.agents...)
			if _, err := runInitCmd(t, "", "--skill"); err != nil {
				t.Fatalf("init --skill: %v", err)
			}

			want := tc.want(env)
			for _, p := range want {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("expected skill at %s: %v", p, err)
				}
			}
			if got := env.skillFiles(t); len(got) != len(want) {
				t.Errorf("wrote %d skills (%v), want %d (%v)", len(got), got, len(want), want)
			}
		})
	}
}

func TestInitSkillDeclined(t *testing.T) {
	// A bare Enter must count as no: the prompt says [y/N], and a user who
	// hits Enter to get past it has not agreed to anything.
	for _, answer := range []string{"n\n", "\n", "no\n", "q\n", "yes please\n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			env := newInitEnv(t, "claude")
			withTerminal(t, true)

			out, err := runInitCmd(t, answer)
			if err != nil {
				t.Fatalf("init: %v", err)
			}
			if !strings.Contains(out, "Skipped") {
				t.Errorf("declining did not report a skip:\n%s", out)
			}
			if _, err := os.Stat(env.claudeSkill()); err == nil {
				t.Error("skill was written despite being declined")
			}
			if dirs := env.dirs(t); len(dirs) != 0 {
				t.Errorf("declining still created directories: %v", dirs)
			}
		})
	}
}

func TestInitSkillAccepted(t *testing.T) {
	env := newInitEnv(t, "claude")
	withTerminal(t, true)

	out, err := runInitCmd(t, "y\n")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	body, err := os.ReadFile(env.claudeSkill())
	if err != nil {
		t.Fatalf("skill not written: %v", err)
	}
	if len(body) == 0 {
		t.Error("skill file is empty")
	}
	// The full text and the destination must both appear before the prompt,
	// or the consent is not informed.
	promptAt := strings.Index(out, "[y/N]")
	if promptAt < 0 {
		t.Fatalf("no prompt in output:\n%s", out)
	}
	before := out[:promptAt]
	if !strings.Contains(before, "name: jind-ai") {
		t.Error("the skill's text was not shown before the prompt")
	}
	if !strings.Contains(before, env.claudeSkill()) {
		t.Error("the destination path was not shown before the prompt")
	}
}

func TestInitDryRunWritesNothing(t *testing.T) {
	env := newInitEnv(t, "claude", "codex")
	withTerminal(t, true)

	out, err := runInitCmd(t, "y\n", "--dry-run")
	if err != nil {
		t.Fatalf("init --dry-run: %v", err)
	}
	if !strings.Contains(out, "nothing was written") {
		t.Errorf("dry-run did not say so:\n%s", out)
	}
	// Both destinations are still shown — the point of --dry-run is to see
	// what would happen.
	for _, p := range []string{env.claudeSkill(), env.agentsSkill()} {
		if !strings.Contains(out, p) {
			t.Errorf("dry-run did not show destination %s", p)
		}
	}
	if files := env.files(t); len(files) != 0 {
		t.Errorf("--dry-run wrote files: %v", files)
	}
	if dirs := env.dirs(t); len(dirs) != 0 {
		t.Errorf("--dry-run created directories: %v", dirs)
	}
}

func TestInitNonInteractiveSkips(t *testing.T) {
	env := newInitEnv(t, "claude")
	withTerminal(t, false)

	out, err := runInitCmd(t, "")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "Not a terminal") {
		t.Errorf("no explanation for the non-interactive skip:\n%s", out)
	}
	if files := env.files(t); len(files) != 0 {
		t.Errorf("wrote files without being able to ask: %v", files)
	}
}

// TestInitForceAloneWritesNothingNonInteractively is the guarantee that a
// scripted `jin init --force`, which exists to refresh config.yaml, cannot
// quietly plant a file in the user's home as a side effect.
func TestInitForceAloneWritesNothingNonInteractively(t *testing.T) {
	env := newInitEnv(t, "claude")
	withTerminal(t, false)

	if _, err := runInitCmd(t, "", "--force"); err != nil {
		t.Fatalf("init --force: %v", err)
	}
	if files := env.files(t); len(files) != 0 {
		t.Errorf("--force alone wrote into HOME: %v", files)
	}
}

func TestInitNoSkill(t *testing.T) {
	env := newInitEnv(t, "claude")
	withTerminal(t, true)

	out, err := runInitCmd(t, "y\n", "--no-skill")
	if err != nil {
		t.Fatalf("init --no-skill: %v", err)
	}
	if strings.Contains(out, "[y/N]") {
		t.Errorf("--no-skill still asked:\n%s", out)
	}
	if files := env.files(t); len(files) != 0 {
		t.Errorf("--no-skill wrote files: %v", files)
	}
}

func TestInitSkillAndNoSkillConflict(t *testing.T) {
	newInitEnv(t, "claude")
	if _, err := runInitCmd(t, "", "--skill", "--no-skill"); err == nil {
		t.Error("--skill --no-skill was accepted")
	}
}

// TestInitPartiallyInstalled covers the machine where one agent was set up
// earlier: the present file is left alone and the missing one is still
// written. Aborting the whole step would leave the second agent permanently
// without the skill.
func TestInitPartiallyInstalled(t *testing.T) {
	env := newInitEnv(t, "claude", "codex")
	if err := os.MkdirAll(filepath.Dir(env.claudeSkill()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const sentinel = "# pre-existing\n"
	if err := os.WriteFile(env.claudeSkill(), []byte(sentinel), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := runInitCmd(t, "", "--skill")
	if err != nil {
		t.Fatalf("init --skill: %v", err)
	}
	if !strings.Contains(out, "already present") {
		t.Errorf("the pre-existing file was not reported:\n%s", out)
	}

	body, err := os.ReadFile(env.claudeSkill())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != sentinel {
		t.Error("an existing skill was overwritten without --force")
	}
	if _, err := os.Stat(env.agentsSkill()); err != nil {
		t.Errorf("the missing destination was not written: %v", err)
	}
}

func TestInitForceReplacesExistingSkill(t *testing.T) {
	env := newInitEnv(t, "claude")
	if err := os.MkdirAll(filepath.Dir(env.claudeSkill()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(env.claudeSkill(), []byte("# old\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := runInitCmd(t, "", "--skill", "--force"); err != nil {
		t.Fatalf("init --skill --force: %v", err)
	}
	body, err := os.ReadFile(env.claudeSkill())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) == "# old\n" {
		t.Error("--force did not replace the existing skill")
	}
}

// TestInitSkillDir bypasses detection entirely — the flag exists for the
// project-local case, where second-guessing the choice against PATH would make
// it useless.
func TestInitSkillDir(t *testing.T) {
	env := newInitEnv(t) // deliberately no agents on PATH
	dir := filepath.Join(t.TempDir(), "project-skills", "jind-ai")

	if _, err := runInitCmd(t, "", "--skill", "--skill-dir", dir); err != nil {
		t.Fatalf("init --skill-dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Errorf("skill not written to --skill-dir: %v", err)
	}
	if files := env.files(t); len(files) != 0 {
		t.Errorf("--skill-dir still wrote into HOME: %v", files)
	}
}

// TestInitSkillStepRunsWithExistingConfig pins the behaviour change: an
// existing config.yaml used to end the command. Someone who has run jin for
// months has a config and no skill, and they are exactly who the offer is for.
func TestInitSkillStepRunsWithExistingConfig(t *testing.T) {
	env := newInitEnv(t, "claude")

	if _, err := runInitCmd(t, "", "--skill"); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := os.Remove(env.claudeSkill()); err != nil {
		t.Fatalf("remove skill: %v", err)
	}

	out, err := runInitCmd(t, "", "--skill")
	if err != nil {
		t.Fatalf("second init: %v", err)
	}
	if !strings.Contains(out, "Config already exists") {
		t.Errorf("expected the config to be reported as existing:\n%s", out)
	}
	if _, err := os.Stat(env.claudeSkill()); err != nil {
		t.Errorf("skill step did not run for an existing config: %v", err)
	}
}

// configFile is where `jin init` writes config.yaml under this env's isolation.
func (e initEnv) configFile() string {
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "jind-ai", "config.yaml")
}

// TestInitSkillDoesNotFollowSymlink is the regression test for the review's
// must finding: os.WriteFile follows symlinks, so a link planted at the target
// would redirect the write while the consent screen — and the "Created: ..."
// line — still named the path the user agreed to.
//
// Two shapes, because they fail differently. A dangling link is invisible to
// os.Stat, so planning believes the target is free and the write creates the
// victim outright; a link to an existing file survives planning only under
// --force, and there the write truncates the victim.
func TestInitSkillDoesNotFollowSymlink(t *testing.T) {
	t.Run("dangling link is not followed", func(t *testing.T) {
		env := newInitEnv(t, "claude")
		outside := filepath.Join(t.TempDir(), "victim.md")

		if err := os.MkdirAll(filepath.Dir(env.claudeSkill()), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(outside, env.claudeSkill()); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		if _, err := runInitCmd(t, "", "--skill"); err != nil {
			t.Fatalf("init --skill: %v", err)
		}
		if _, err := os.Stat(outside); err == nil {
			t.Error("the write followed a dangling symlink and created a file outside the agreed path")
		}
	})

	t.Run("link to an existing file is not followed under --force", func(t *testing.T) {
		env := newInitEnv(t, "claude")
		outside := filepath.Join(t.TempDir(), "precious")
		const original = "export SECRET=1\n"
		if err := os.WriteFile(outside, []byte(original), 0o644); err != nil {
			t.Fatalf("seed victim: %v", err)
		}

		if err := os.MkdirAll(filepath.Dir(env.claudeSkill()), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(outside, env.claudeSkill()); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		if _, err := runInitCmd(t, "", "--skill", "--force"); err != nil {
			t.Fatalf("init --skill --force: %v", err)
		}
		body, err := os.ReadFile(outside)
		if err != nil {
			t.Fatalf("read victim: %v", err)
		}
		if string(body) != original {
			t.Errorf("--force followed the symlink and clobbered %s", outside)
		}
		// The skill still has to land, at the path that was displayed.
		if fi, err := os.Lstat(env.claudeSkill()); err != nil {
			t.Errorf("skill not written to the agreed path: %v", err)
		} else if fi.Mode()&os.ModeSymlink != 0 {
			t.Error("the agreed path is still a symlink; --force replaced the target instead of the link")
		}
	})
}

// TestInitDryRunLeavesFilesystemUntouched covers the half of --dry-run's
// contract the review found unverified: the skill assertions lived in
// TestInitDryRunWritesNothing, but nothing checked config.yaml, and the
// existing walk only covered HOME while config.yaml lives under
// XDG_CONFIG_HOME.
func TestInitDryRunLeavesFilesystemUntouched(t *testing.T) {
	env := newInitEnv(t, "claude", "codex")
	withTerminal(t, true)

	if _, err := runInitCmd(t, "y\n", "--dry-run"); err != nil {
		t.Fatalf("init --dry-run: %v", err)
	}
	if _, err := os.Stat(env.configFile()); err == nil {
		t.Error("--dry-run wrote config.yaml")
	}
	// "would create" has to mean the filesystem is untouched, parent
	// directories included.
	if _, err := os.Stat(filepath.Dir(env.configFile())); err == nil {
		t.Error("--dry-run created the config directory")
	}
	if files := env.skillFiles(t); len(files) != 0 {
		t.Errorf("--dry-run wrote skills: %v", files)
	}
}

// TestInitAsksOnceForMultipleTargets pins T-B4's "show every path and ask a
// single time". The review's mutation — wrapping the prompt in a per-target
// loop — survived the suite because every interactive test ran with just one
// agent installed.
func TestInitAsksOnceForMultipleTargets(t *testing.T) {
	env := newInitEnv(t, "claude", "codex")
	withTerminal(t, true)

	// One "y" only: a second prompt would read EOF and be refused, so the
	// file count catches the regression even if the prompt count does not.
	out, err := runInitCmd(t, "y\n")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if n := strings.Count(out, "[y/N]"); n != 1 {
		t.Errorf("asked %d times, want exactly 1:\n%s", n, out)
	}
	for _, p := range []string{env.claudeSkill(), env.agentsSkill()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("one consent did not cover %s: %v", p, err)
		}
	}
}

// TestInitConfigWrittenNormally guards the pre-existing config.yaml behaviour
// through the real command. The original init_test.go reimplements the logic
// inline instead of invoking initCmd, so it cannot catch a regression here.
func TestInitConfigWrittenNormally(t *testing.T) {
	env := newInitEnv(t, "claude")

	if _, err := runInitCmd(t, "", "--no-skill"); err != nil {
		t.Fatalf("init: %v", err)
	}
	body, err := os.ReadFile(env.configFile())
	if err != nil {
		t.Fatalf("config.yaml not written: %v", err)
	}
	if string(body) != configTemplate {
		t.Error("config.yaml does not match the template")
	}

	// Second run without --force leaves it alone.
	if err := os.WriteFile(env.configFile(), []byte("# edited\n"), 0o644); err != nil {
		t.Fatalf("edit config: %v", err)
	}
	out, err := runInitCmd(t, "", "--no-skill")
	if err != nil {
		t.Fatalf("second init: %v", err)
	}
	if !strings.Contains(out, "Config already exists") {
		t.Errorf("existing config not reported:\n%s", out)
	}
	if body, _ := os.ReadFile(env.configFile()); string(body) != "# edited\n" {
		t.Error("an existing config.yaml was overwritten without --force")
	}
}

// TestInitRefusesSymlinkedParent covers what O_EXCL and rename cannot see.
// Both judge only the final element, so a link one level up redirects the
// whole write while the consent screen still names the original path.
func TestInitRefusesSymlinkedParent(t *testing.T) {
	t.Run("skills directory is a link", func(t *testing.T) {
		env := newInitEnv(t, "claude")
		outside := t.TempDir()

		if err := os.MkdirAll(filepath.Join(env.home, ".claude"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(env.home, ".claude", "skills")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		out, err := runInitCmd(t, "", "--skill")
		if err != nil {
			t.Fatalf("init --skill: %v", err)
		}
		if _, err := os.Stat(filepath.Join(outside, "jind-ai", "SKILL.md")); err == nil {
			t.Error("the write followed a symlinked parent directory")
		}
		if !strings.Contains(out, "Refusing to write") {
			t.Errorf("no refusal reported:\n%s", out)
		}
		if strings.Contains(out, "Created: "+filepath.Join(outside, "jind-ai")) ||
			strings.Contains(out, "Created: "+env.claudeSkill()) {
			t.Errorf("reported a skill write that did not happen:\n%s", out)
		}
	})

	t.Run("skill directory is a link, with --force", func(t *testing.T) {
		env := newInitEnv(t, "claude")
		outside := t.TempDir()
		victim := filepath.Join(outside, agentdocs.SkillFileName)
		const original = "IMPORTANT VICTIM CONTENT\n"
		if err := os.WriteFile(victim, []byte(original), 0o644); err != nil {
			t.Fatalf("seed victim: %v", err)
		}

		if err := os.MkdirAll(filepath.Join(env.home, ".claude", "skills"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(env.home, ".claude", "skills", "jind-ai")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		if _, err := runInitCmd(t, "", "--skill", "--force"); err != nil {
			t.Fatalf("init --skill --force: %v", err)
		}
		body, err := os.ReadFile(victim)
		if err != nil {
			t.Fatalf("read victim: %v", err)
		}
		if string(body) != original {
			t.Error("--force followed a symlinked parent and clobbered the target")
		}
	})
}

// TestWriteSkillFile exercises the write contract directly. The review found
// that turning errSkillExists into a nil return survived the whole suite,
// which would make jin print "Created: <path>" for a file it never wrote —
// a quieter version of the very symptom the symlink fix exists to prevent.
func TestWriteSkillFile(t *testing.T) {
	t.Run("existing file without force is reported, not overwritten", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "SKILL.md")
		const original = "# mine\n"
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}

		if err := writeSkillFile(path, false); !errors.Is(err, errSkillExists) {
			t.Errorf("err = %v, want errSkillExists", err)
		}
		if body, _ := os.ReadFile(path); string(body) != original {
			t.Error("the file was modified despite the error")
		}
	})

	t.Run("symlink without force is reported, not followed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "SKILL.md")
		outside := filepath.Join(t.TempDir(), "victim")
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		if err := writeSkillFile(path, false); !errors.Is(err, errSkillExists) {
			t.Errorf("err = %v, want errSkillExists", err)
		}
		if _, err := os.Stat(outside); err == nil {
			t.Error("the link target was created")
		}
	})

	t.Run("force replaces the content", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "SKILL.md")
		if err := os.WriteFile(path, []byte("# old\n"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}

		if err := writeSkillFile(path, true); err != nil {
			t.Fatalf("writeSkillFile with force: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(body) != agentdocs.Skill() {
			t.Error("force did not write the skill")
		}
	})

	t.Run("fresh path is created", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "SKILL.md")
		if err := writeSkillFile(path, false); err != nil {
			t.Fatalf("writeSkillFile: %v", err)
		}
		if body, _ := os.ReadFile(path); string(body) != agentdocs.Skill() {
			t.Error("content mismatch")
		}
	})
}

// TestInitDanglingSymlinkReportsHonestly pins the user-visible half of the
// same contract: a skipped write must not be announced as "Created".
func TestInitDanglingSymlinkReportsHonestly(t *testing.T) {
	env := newInitEnv(t, "claude")
	outside := filepath.Join(t.TempDir(), "victim.md")

	if err := os.MkdirAll(filepath.Dir(env.claudeSkill()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, env.claudeSkill()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	out, err := runInitCmd(t, "", "--skill")
	if err != nil {
		t.Fatalf("init --skill: %v", err)
	}
	if strings.Contains(out, "Created: "+env.claudeSkill()) {
		t.Errorf("announced a skill write that was skipped:\n%s", out)
	}
	if !strings.Contains(out, "leaving it alone") {
		t.Errorf("did not report why nothing was written:\n%s", out)
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("no recovery hint offered:\n%s", out)
	}
}

// TestInitNonInteractiveDoesNotPrintProposal pins the noise fix: a run that
// cannot ask has no use for the full skill text, and `jin init` inside a setup
// script would otherwise bury its own output under 30-odd lines.
func TestInitNonInteractiveDoesNotPrintProposal(t *testing.T) {
	newInitEnv(t, "claude")
	withTerminal(t, false)

	out, err := runInitCmd(t, "")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if strings.Contains(out, "=== "+agentdocs.SkillFileName+" ===") {
		t.Errorf("printed the full skill text with no way to ask:\n%s", out)
	}
	if !strings.Contains(out, "Not a terminal") {
		t.Errorf("no reason given:\n%s", out)
	}
}

// TestInitNonInteractiveDryRunPrintsProposal is the counterpart: --dry-run
// exists to show what would happen, so suppressing the text there would defeat
// the flag. This is the main way CI and agents inspect the skill before
// agreeing to it.
func TestInitNonInteractiveDryRunPrintsProposal(t *testing.T) {
	newInitEnv(t, "claude")
	withTerminal(t, false)

	out, err := runInitCmd(t, "", "--dry-run")
	if err != nil {
		t.Fatalf("init --dry-run: %v", err)
	}
	if !strings.Contains(out, "=== "+agentdocs.SkillFileName+" ===") {
		t.Errorf("--dry-run did not show the skill text:\n%s", out)
	}
	if !strings.Contains(out, "nothing was written") {
		t.Errorf("--dry-run did not say it wrote nothing:\n%s", out)
	}
}
