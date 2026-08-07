package opencode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/agentdocs"
)

func TestWritePlugin_Layout(t *testing.T) {
	stateDir := t.TempDir()

	configDir, err := WritePlugin(stateDir, "/usr/local/bin/jin")
	if err != nil {
		t.Fatalf("WritePlugin: %v", err)
	}

	if want := filepath.Join(stateDir, "opencode"); configDir != want {
		t.Errorf("configDir = %q, want %q", configDir, want)
	}
	// The path must satisfy opencode's {plugin,plugins}/*.{ts,js} glob,
	// relative to the directory handed over as OPENCODE_CONFIG_DIR.
	fi, err := os.Stat(filepath.Join(configDir, "plugin", "jin.ts"))
	if err != nil {
		t.Fatalf("plugin not at <configDir>/plugin/jin.ts: %v", err)
	}
	// Spelled out rather than compared against pluginFileMode, which would
	// restate the constant and pass however it changed. os.CreateTemp makes its
	// file 0600, so this only holds because the write chmods.
	if got, want := fi.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Errorf("plugin mode = %v, want %v", got, want)
	}
}

func TestWritePlugin_SubstitutesExecPath(t *testing.T) {
	stateDir := t.TempDir()
	execPath := "/opt/tools/jin"

	configDir, err := WritePlugin(stateDir, execPath)
	if err != nil {
		t.Fatalf("WritePlugin: %v", err)
	}

	body := readPlugin(t, configDir)
	if strings.Contains(body, execPathPlaceholder) {
		t.Error("placeholder still present; exec path was not substituted")
	}
	if !strings.Contains(body, `"`+execPath+`"`) {
		t.Errorf("exec path %q not found as a string literal", execPath)
	}
}

// A path containing a quote or backslash would otherwise terminate the
// TypeScript literal and produce a module opencode cannot import.
func TestWritePlugin_EscapesExecPath(t *testing.T) {
	stateDir := t.TempDir()
	execPath := `/home/some user/we"ird\path/jin`

	configDir, err := WritePlugin(stateDir, execPath)
	if err != nil {
		t.Fatalf("WritePlugin: %v", err)
	}

	body := readPlugin(t, configDir)
	if !strings.Contains(body, `"/home/some user/we\"ird\\path/jin"`) {
		t.Error("exec path was not escaped for a JavaScript string literal")
	}
}

// Rewriting on every call is what lets a reinstall that moves the binary be
// picked up on the next session start.
func TestWritePlugin_RewritesOnExecPathChange(t *testing.T) {
	stateDir := t.TempDir()

	if _, err := WritePlugin(stateDir, "/old/bin/jin"); err != nil {
		t.Fatalf("first WritePlugin: %v", err)
	}
	configDir, err := WritePlugin(stateDir, "/new/bin/jin")
	if err != nil {
		t.Fatalf("second WritePlugin: %v", err)
	}

	body := readPlugin(t, configDir)
	if strings.Contains(body, "/old/bin/jin") {
		t.Error("stale exec path survived the rewrite")
	}
	if !strings.Contains(body, "/new/bin/jin") {
		t.Error("new exec path missing after rewrite")
	}
}

func TestWritePlugin_RejectsEmptyInputs(t *testing.T) {
	if _, err := WritePlugin("", "/usr/local/bin/jin"); err == nil {
		t.Error("empty state dir returned nil error")
	}
	if _, err := WritePlugin(t.TempDir(), ""); err == nil {
		t.Error("empty exec path returned nil error")
	}
}

// Setup swallows write failures so the session still starts; the adapter
// then reports no config dir and SpawnCommand degrades to a bare command.
func TestAgent_SetupFailure_FailsOpen(t *testing.T) {
	a := New()

	if err := a.Setup(agent.SetupContext{StateDir: "", ExecPath: ""}); err != nil {
		t.Errorf("Setup returned %v, want nil (failures must not block spawn)", err)
	}

	plan := a.SpawnCommand(agent.SpawnOptions{})
	if plan.Command != "opencode" {
		t.Errorf("Command = %q, want %q", plan.Command, "opencode")
	}
	if len(plan.ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v, want empty after a failed Setup", plan.ExtraEnv)
	}
}

func TestAgent_Setup_WiresConfigDir(t *testing.T) {
	stateDir := t.TempDir()
	a := New()

	if err := a.Setup(agent.SetupContext{StateDir: stateDir, ExecPath: "/usr/local/bin/jin"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	plan := a.SpawnCommand(agent.SpawnOptions{})
	want := filepath.Join(stateDir, "opencode")
	if got := plan.ExtraEnv["OPENCODE_CONFIG_DIR"]; got != want {
		t.Errorf("OPENCODE_CONFIG_DIR = %q, want %q", got, want)
	}
}

func readPlugin(t *testing.T, configDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "plugin", "jin.ts"))
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	return string(data)
}

// bunOrSkip returns the bun executable, skipping the test when the machine
// has none. bun is the only runtime that parses the plugin's TypeScript;
// `node --check` cannot, so there is deliberately no fallback. CI installs
// bun (.github/workflows/ci.yml) precisely so these checks do not silently
// vanish — without them a broken plugin ships as "status never updates"
// with every Go test still green.
func bunOrSkip(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not on PATH; cannot check the embedded plugin")
	}
	return bin
}

// The plugin must parse after substitution. Run against an exec path full
// of characters that would break a string literal, since "still valid
// JavaScript" is the property quoteForJS actually has to deliver — asserting
// on the escaped bytes alone would only check a proxy for it.
func TestPluginSource_Parses(t *testing.T) {
	bun := bunOrSkip(t)

	for _, execPath := range []string{
		"/usr/local/bin/jin",
		`/home/some user/we"ird\path/jin`,
		"/日本語/jin",
		"/bell\x07/jin",
	} {
		t.Run(execPath, func(t *testing.T) {
			configDir, err := WritePlugin(t.TempDir(), execPath)
			if err != nil {
				t.Fatalf("WritePlugin: %v", err)
			}

			path := filepath.Join(configDir, "plugin", "jin.ts")
			if out, err := exec.Command(bun, "build", "--no-bundle", path).CombinedOutput(); err != nil {
				t.Errorf("plugin failed to parse: %v\n%s", err, out)
			}
		})
	}
}

// Routing — which bus event becomes which canonical hook — is the part of
// this adapter that no Go test can reach, and the part whose bugs are
// silent (a subagent event leaking through re-keys the session id and
// breaks resume). plugin/jin.test.ts exercises it against a stub `jin`.
func TestPluginRouting_BunTest(t *testing.T) {
	bun := bunOrSkip(t)

	cmd := exec.Command(bun, "test", "jin.test.ts")
	cmd.Dir = "plugin"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("plugin routing tests failed: %v\n%s", err, out)
	}
}

// The Agent interface documents that Setup and SpawnCommand may run from
// several per-session goroutines at once. Run under -race.
func TestAgent_ConcurrentSetupAndSpawn(t *testing.T) {
	stateDir := t.TempDir()
	a := New()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.Setup(agent.SetupContext{StateDir: stateDir, ExecPath: "/usr/local/bin/jin"})
			_ = a.SpawnCommand(agent.SpawnOptions{})
		}()
	}
	wg.Wait()

	want := filepath.Join(stateDir, "opencode")
	if got := a.SpawnCommand(agent.SpawnOptions{}).ExtraEnv["OPENCODE_CONFIG_DIR"]; got != want {
		t.Errorf("OPENCODE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// A failing Setup on one session must not disable status reporting for the
// sessions that already succeeded — the adapter is shared process-wide.
func TestAgent_SetupFailure_KeepsPreviousConfigDir(t *testing.T) {
	stateDir := t.TempDir()
	a := New()

	if err := a.Setup(agent.SetupContext{StateDir: stateDir, ExecPath: "/usr/local/bin/jin"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	// Second session start fails (empty state dir).
	if err := a.Setup(agent.SetupContext{}); err != nil {
		t.Fatalf("Setup(empty) returned %v, want nil", err)
	}

	want := filepath.Join(stateDir, "opencode")
	if got := a.SpawnCommand(agent.SpawnOptions{}).ExtraEnv["OPENCODE_CONFIG_DIR"]; got != want {
		t.Errorf("OPENCODE_CONFIG_DIR = %q, want the last good %q", got, want)
	}
}

// WritePlugin is reached from concurrent Setup calls; the temp-file plus
// rename dance must leave exactly one intact file behind.
func TestWritePlugin_Concurrent(t *testing.T) {
	stateDir := t.TempDir()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := WritePlugin(stateDir, "/usr/local/bin/jin"); err != nil {
				t.Errorf("WritePlugin: %v", err)
			}
		}()
	}
	wg.Wait()

	// Exactly one file also proves no temp file survived any of the
	// racing writes — atomicfile.Write creates them in this directory.
	entries, err := os.ReadDir(filepath.Join(stateDir, "opencode", "plugin"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "jin.ts" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("plugin dir = %v, want exactly [jin.ts]", names)
	}
}

func TestWritePlugin_UnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	if _, err := WritePlugin(stateDir, "/usr/local/bin/jin"); err == nil {
		t.Error("WritePlugin on an unwritable state dir returned nil error")
	}
}

// TestPluginTmpPattern_StaysOutsideOpencodeGlob pins the one property the temp
// name has to have. opencode globs {plugin,plugins}/*.{ts,js} in this very
// directory on every start, so a temp file named like a module would be
// imported half-written — which is the whole reason WritePlugin passes a
// pattern rather than letting the helper choose. Nothing else would catch the
// constant drifting into a name the glob accepts.
func TestPluginTmpPattern_StaysOutsideOpencodeGlob(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, pluginTmpPattern)
	if err != nil {
		t.Fatalf("CreateTemp(%q): %v", pluginTmpPattern, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The real arbiter is opencode's own loader, so this stands in for it with
	// Go's Glob. The approximation errs strict: Go's Match does not skip
	// dotfiles the way a shell would, so a leading "." never hides a name here
	// that opencode might still pick up.
	for _, glob := range []string{"*.ts", "*.js"} {
		matches, err := filepath.Glob(filepath.Join(dir, glob))
		if err != nil {
			t.Fatalf("Glob(%q): %v", glob, err)
		}
		if len(matches) != 0 {
			t.Errorf("pluginTmpPattern %q produced %s, which opencode's %q glob would import",
				pluginTmpPattern, filepath.Base(matches[0]), glob)
		}
	}
}

func TestWriteAgentContext_Layout(t *testing.T) {
	stateDir := t.TempDir()
	configDir, err := WritePlugin(stateDir, "/usr/local/bin/jin")
	if err != nil {
		t.Fatalf("WritePlugin: %v", err)
	}
	if err := WriteAgentContext(configDir); err != nil {
		t.Fatalf("WriteAgentContext: %v", err)
	}

	// Both files sit directly in OPENCODE_CONFIG_DIR: opencode reads
	// opencode.json from there because the directory is on its config search
	// path, and the markdown is named from inside that config.
	contextPath := filepath.Join(configDir, contextFileName)
	body, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("context file not written: %v", err)
	}
	if string(body) != agentdocs.Context() {
		t.Error("context file does not match agentdocs.Context()")
	}

	data, err := os.ReadFile(filepath.Join(configDir, configFileName))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg openCodeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config is not valid JSON (%v): %s", err, data)
	}
	if len(cfg.Instructions) != 1 {
		t.Fatalf("instructions = %v, want exactly one entry", cfg.Instructions)
	}
	if cfg.Instructions[0] != contextPath {
		t.Errorf("instructions[0] = %q, want %q", cfg.Instructions[0], contextPath)
	}
	// opencode resolves instruction paths against a directory this package
	// does not choose, so a relative entry could silently point nowhere.
	if !filepath.IsAbs(cfg.Instructions[0]) {
		t.Errorf("instructions entry is not absolute: %q", cfg.Instructions[0])
	}
	if _, err := os.Stat(cfg.Instructions[0]); err != nil {
		t.Errorf("instructions entry does not exist: %v", err)
	}
}

// TestWriteAgentContext_ConfigCarriesNothingElse keeps the contributed config
// minimal. opencode merges every config on its search path, and `instructions`
// is specifically unioned rather than replaced — any other field jind-ai wrote
// here could override a setting the user made deliberately.
func TestWriteAgentContext_ConfigCarriesNothingElse(t *testing.T) {
	configDir := t.TempDir()
	if err := WriteAgentContext(configDir); err != nil {
		t.Fatalf("WriteAgentContext: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, configFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if len(raw) != 1 {
		t.Errorf("config has %d keys (%v), want only \"instructions\"", len(raw), raw)
	}
	if _, ok := raw["instructions"]; !ok {
		t.Errorf("config has no instructions key: %v", raw)
	}
}

func TestWriteAgentContext_RewritesOnEveryCall(t *testing.T) {
	configDir := t.TempDir()
	if err := WriteAgentContext(configDir); err != nil {
		t.Fatalf("WriteAgentContext: %v", err)
	}

	// A user (or a stray process) truncating the file must not leave the
	// session permanently without context.
	contextPath := filepath.Join(configDir, contextFileName)
	if err := os.WriteFile(contextPath, nil, 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := WriteAgentContext(configDir); err != nil {
		t.Fatalf("second WriteAgentContext: %v", err)
	}
	body, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != agentdocs.Context() {
		t.Error("context file was not restored")
	}
}

func TestWriteAgentContext_RejectsEmptyDir(t *testing.T) {
	if err := WriteAgentContext(""); err == nil {
		t.Error("WriteAgentContext(\"\") returned no error")
	}
}

func TestAgent_Setup_WritesAgentContext(t *testing.T) {
	stateDir := t.TempDir()
	a := New()
	if err := a.Setup(agent.SetupContext{StateDir: stateDir, ExecPath: "/usr/local/bin/jin"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	configDir := filepath.Join(stateDir, "opencode")
	for _, name := range []string{contextFileName, configFileName} {
		if _, err := os.Stat(filepath.Join(configDir, name)); err != nil {
			t.Errorf("Setup did not write %s: %v", name, err)
		}
	}
}

// TestAgentContextTmpPatterns_StayOutsideOpencodeReads is the counterpart to
// TestPluginTmpPattern_StaysOutsideOpencodeGlob. A temp file left behind by a
// crash must not be mistaken for one of opencode's own files: the config names
// are exact, so the risk is a pattern that could produce one of them.
func TestAgentContextTmpPatterns_StayOutsideOpencodeReads(t *testing.T) {
	for _, pattern := range []string{contextTmpPattern, configTmpPattern} {
		if !strings.HasPrefix(pattern, ".") {
			t.Errorf("%q does not start with a dot", pattern)
		}
		if !strings.HasSuffix(pattern, ".tmp") {
			t.Errorf("%q does not end in .tmp", pattern)
		}
		for _, read := range []string{"opencode.json", "opencode.jsonc"} {
			if strings.HasPrefix(pattern, strings.TrimSuffix(read, filepath.Ext(read))) {
				t.Errorf("%q could collide with %q", pattern, read)
			}
		}
	}
	if contextTmpPattern == configTmpPattern {
		t.Error("the two temp patterns are identical; concurrent writes could race on the same name")
	}
}

// TestWriteAgentContext_WriteOrder pins the ordering the doc comment states:
// the markdown lands before the config that names it, so opencode reading the
// directory mid-write never sees an instructions entry pointing at nothing.
//
// A comment cannot enforce that — a refactor could swap the two writes and the
// suite would stay green. Occupying the markdown's path with a directory makes
// its write fail (atomicfile's rename cannot replace a directory), so the
// config must be absent afterwards. Reverse the order and opencode.json
// survives, which fails here.
func TestWriteAgentContext_WriteOrder(t *testing.T) {
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, contextFileName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := WriteAgentContext(configDir); err == nil {
		t.Fatal("expected an error when the context file cannot be written")
	}
	if _, err := os.Stat(filepath.Join(configDir, configFileName)); err == nil {
		t.Error("opencode.json was written even though the file its instructions name could not be")
	}
}
