package cmd

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/agent/agenttest"
	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/internal/paths"
	"github.com/takaaki-s/jind-ai/internal/plugin"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// fakeBinder records BindKey invocations so tests can assert what tmux commands
// applyTogglePaneBinding would have issued, without spawning tmux.
type fakeBinder struct {
	calls [][]string // each entry: [key, cmdArg0, cmdArg1, ...]
	err   error
}

func (f *fakeBinder) BindKey(key string, cmdArgs ...string) error {
	entry := append([]string{key}, cmdArgs...)
	f.calls = append(f.calls, entry)
	return f.err
}

// mgrWithYAML builds a config.Manager from an inline YAML fragment. Using the
// real loader keeps these tests honest about viper's decode behaviour instead
// of hand-constructing Manager internals from another package.
func mgrWithYAML(t *testing.T, yamlContent string) *config.Manager {
	t.Helper()
	dir := t.TempDir()
	if yamlContent != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	m, err := config.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestApplyTogglePaneBinding_NilConfigMgrIsNoOp(t *testing.T) {
	fb := &fakeBinder{}
	applyTogglePaneBinding(fb, nil, "%42")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyTogglePaneBinding_EmptyPaneIDIsNoOp(t *testing.T) {
	fb := &fakeBinder{}
	// Default config (no config.yaml → default M-\ binding would apply if paneID were set).
	applyTogglePaneBinding(fb, mgrWithYAML(t, ""), "")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyTogglePaneBinding_DefaultBindsBackslash(t *testing.T) {
	fb := &fakeBinder{}
	applyTogglePaneBinding(fb, mgrWithYAML(t, ""), "%42")
	want := [][]string{
		{"M-\\", "resize-pane", "-Z", "-t", "%42"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyTogglePaneBinding_ExplicitEmptyDisables(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  toggle_pane: []\n"
	applyTogglePaneBinding(fb, mgrWithYAML(t, yaml), "%42")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls when user set toggle_pane=[], got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyTogglePaneBinding_MultipleKeys(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  toggle_pane: [\"M-\\\\\", \"M-b\"]\n"
	applyTogglePaneBinding(fb, mgrWithYAML(t, yaml), "%7")
	want := [][]string{
		{"M-\\", "resize-pane", "-Z", "-t", "%7"},
		{"M-b", "resize-pane", "-Z", "-t", "%7"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyTogglePaneBinding_EmptyKeyIsSkipped(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  toggle_pane: [\"\", \"M-b\"]\n"
	applyTogglePaneBinding(fb, mgrWithYAML(t, yaml), "%9")
	want := [][]string{
		{"M-b", "resize-pane", "-Z", "-t", "%9"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyActionPanelBinding_DefaultKey(t *testing.T) {
	fb := &fakeBinder{}
	applyActionPanelBinding(fb, mgrWithYAML(t, ""), "/usr/local/bin/jin")
	want := [][]string{
		{"M-p", "display-popup", "-w", "70%", "-h", "70%", "-T", " Action Palette ", "-E", "'/usr/local/bin/jin' action-popup"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyActionPanelBinding_NoConfig(t *testing.T) {
	fb := &fakeBinder{}
	applyActionPanelBinding(fb, nil, "/usr/local/bin/jin")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyActionPanelBinding_EmptySelfBin(t *testing.T) {
	fb := &fakeBinder{}
	applyActionPanelBinding(fb, mgrWithYAML(t, ""), "")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyActionPanelBinding_ExplicitEmpty(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  action_panel: []\n"
	applyActionPanelBinding(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls when user set action_panel=[], got %d: %v", len(fb.calls), fb.calls)
	}
}

// --- applyPluginActionBindings ---
// Uses installedFn injection to bypass the on-disk registry read so tests
// can exercise bind-key issuance without a fixture plugin tree.

// wantNotifierBind returns the expected fakeBinder.calls row for a bind
// issued by applyPluginActionBindings against the `notifier` plugin's given
// action at the given key, with selfBin `/usr/local/bin/jin`. Centralizing
// the run-shell format keeps every plugin-binding test in lockstep when the
// shell string changes (e.g., adding output redirects).
func wantNotifierBind(key, actionID string) []string {
	return []string{key, "run-shell", "-b", "'/usr/local/bin/jin' plugin run notifier " + actionID + " >/dev/null 2>&1"}
}

// notifierYAML builds a config.yaml fragment binding one action of the
// notifier plugin in the current (actions-nested) shape — e.g.
// notifierYAML("default", `["M-n"]`). Multi-action configs are inlined at
// their single call site instead.
func notifierYAML(actionID, keys string) string {
	return fmt.Sprintf("keybindings:\n  plugins:\n    notifier:\n      actions:\n        %s: { keys: %s }\n", actionID, keys)
}

// pluginSet is a shorthand for building fake installedPluginSetFn results.
func pluginSet(names ...string) installedPluginSetFn {
	return func() map[string]struct{} {
		out := make(map[string]struct{}, len(names))
		for _, n := range names {
			out[n] = struct{}{}
		}
		return out
	}
}

// withMutedLog silences package log output for the test's lifetime and
// returns a buffer that captures any messages the code under test emits.
// Tests that don't need to assert on the log contents just discard the
// buffer. Not safe with t.Parallel(): stomps process-global log.Writer, so
// parallel callers would leak each other's output into the returned buffer.
func withMutedLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func TestApplyPluginActionBindings_NilConfigMgrIsNoOp(t *testing.T) {
	fb := &fakeBinder{}
	applyPluginActionBindings(fb, nil, "/usr/local/bin/jin", pluginSet("notifier"))
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyPluginActionBindings_EmptySelfBinIsNoOp(t *testing.T) {
	fb := &fakeBinder{}
	yaml := notifierYAML("default", `["M-n"]`)
	applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "", pluginSet("notifier"))
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyPluginActionBindings_EmptyBindingsIsNoOp(t *testing.T) {
	fb := &fakeBinder{}
	// No keybindings.plugins set — default is no per-plugin bindings.
	applyPluginActionBindings(fb, mgrWithYAML(t, ""), "/usr/local/bin/jin", pluginSet("notifier"))
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyPluginActionBindings_IssuesRunShell(t *testing.T) {
	fb := &fakeBinder{}
	yaml := notifierYAML("send-dm", `["M-d"]`)
	applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet("notifier"))
	want := [][]string{wantNotifierBind("M-d", "send-dm")}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyPluginActionBindings_SkipsUninstalled(t *testing.T) {
	fb := &fakeBinder{}
	yaml := notifierYAML("default", `["M-n"]`)
	// installed set is empty → notifier is not present → skip.
	applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet())
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls for uninstalled plugin, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyPluginActionBindings_EmptyKeyIsSkipped(t *testing.T) {
	fb := &fakeBinder{}
	yaml := notifierYAML("default", `["", "M-n"]`)
	applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet("notifier"))
	want := [][]string{wantNotifierBind("M-n", "default")}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyPluginActionBindings_MultipleKeysPerAction(t *testing.T) {
	fb := &fakeBinder{}
	yaml := notifierYAML("default", `["M-n", "M-!"]`)
	applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet("notifier"))
	want := [][]string{
		wantNotifierBind("M-n", "default"),
		wantNotifierBind("M-!", "default"),
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyPluginActionBindings_MultipleActionsWithMultipleKeys(t *testing.T) {
	fb := &fakeBinder{}
	yaml := `keybindings:
  plugins:
    notifier:
      actions:
        default: { keys: ["M-n", "M-!"] }
        send-dm: { keys: ["M-d", "M-D"] }
`
	applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet("notifier"))
	// Actions are iterated in sorted order so the bind sequence is stable.
	want := [][]string{
		wantNotifierBind("M-n", "default"),
		wantNotifierBind("M-!", "default"),
		wantNotifierBind("M-d", "send-dm"),
		wantNotifierBind("M-D", "send-dm"),
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyPluginActionBindings_DeprecatedV1ShapeIsIgnored(t *testing.T) {
	// The warning itself is config-layer behavior covered by
	// TestPluginKeybindings_DeprecatedV1Shape_WarnsAndDrops; here the muted
	// log just keeps test output quiet. What matters at this layer: no
	// bind-key is issued and — critically — startup does not crash.
	withMutedLog(t)
	fb := &fakeBinder{}
	// Pre-0.8 shape: keys directly under the plugin name.
	yaml := "keybindings:\n  plugins:\n    notifier: { keys: [\"M-n\"] }\n"
	applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet("notifier"))
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls for deprecated v1 shape, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplyPluginActionBindings_LogsCollisionWithCoreKey(t *testing.T) {
	// Each row covers a distinct branch of reservedOuterTmuxKeys so a copy/
	// paste regression on any of the three collectors is caught. Spec F7:
	// "warn only, bind-key 発行は行う" — tmux last-write-wins decides which
	// binding actually fires.
	cases := []struct {
		name    string
		yamlKey string // raw string as it appears in the yaml literal (needs \\ escaping for M-\)
		wantTag string
	}{
		{"ActionPanel", "M-p", "core:action-panel"},
		{"TogglePane", `M-\\`, "core:toggle-pane"},
		{"SessionFilter", "M-f", "core:session-filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := withMutedLog(t)
			fb := &fakeBinder{}
			yaml := notifierYAML("default", fmt.Sprintf(`["%s"]`, tc.yamlKey))
			applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet("notifier"))

			if !strings.Contains(buf.String(), "collides with "+tc.wantTag) {
				t.Errorf("expected collision log against %s, got: %s", tc.wantTag, buf.String())
			}
			if len(fb.calls) != 1 {
				t.Errorf("expected 1 BindKey despite collision, got %d: %v", len(fb.calls), fb.calls)
			}
		})
	}
}

func TestApplyPluginActionBindings_NormalizesPlusNotation(t *testing.T) {
	// The "+" style is the more natural notation to reach for and appears in
	// other keybindings.* fields (quit / detach) as bubbletea-style tokens.
	// Silently accepting it on the tmux-side plugin bindings keeps the two
	// styles from being confusing without exposing raw yaml errors.
	cases := []struct {
		yamlKey string
		wantKey string
	}{
		{"ctrl+f", "C-f"},
		{"Ctrl+F", "C-F"},
		{"alt+n", "M-n"},
		{"shift+tab", "S-tab"},
		{"ctrl+alt+p", "C-M-p"},
	}
	for _, tc := range cases {
		t.Run(tc.yamlKey, func(t *testing.T) {
			fb := &fakeBinder{}
			yaml := notifierYAML("default", fmt.Sprintf(`["%s"]`, tc.yamlKey))
			applyPluginActionBindings(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin", pluginSet("notifier"))
			want := [][]string{wantNotifierBind(tc.wantKey, "default")}
			if !reflect.DeepEqual(fb.calls, want) {
				t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
			}
		})
	}
}

// withRegistry snapshots the current process-global agent registry (which
// root.go's blank import of internal/agent/register has already populated
// with the real adapters at init time), wipes it, and installs the given
// kinds as stubs. Cleanup restores the pre-test snapshot so later tests
// running in the same binary still see the real adapters — otherwise the
// registry would be permanently empty and any subsequent test that touches
// agent.Lookup would silently fail.
func withRegistry(t *testing.T, kinds ...string) {
	t.Helper()
	orig := agenttest.Snapshot()
	agenttest.Reset()
	for _, k := range kinds {
		agent.Register(&agenttest.StubAgent{KindStr: k})
	}
	t.Cleanup(func() { agenttest.Restore(orig) })
}

func TestValidateTuiAgentFlag_EmptyAllowed(t *testing.T) {
	withRegistry(t, "claude", "codex")
	if err := validateTuiAgentFlag(""); err != nil {
		t.Errorf("empty flag returned error: %v", err)
	}
}

func TestValidateTuiAgentFlag_KnownKind(t *testing.T) {
	withRegistry(t, "claude", "codex")
	if err := validateTuiAgentFlag("codex"); err != nil {
		t.Errorf("known kind returned error: %v", err)
	}
}

func TestValidateTuiAgentFlag_UnknownKind(t *testing.T) {
	withRegistry(t, "claude", "codex")
	err := validateTuiAgentFlag("nonsense")
	if err == nil {
		t.Fatal("unknown kind should error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown agent kind") {
		t.Errorf("error = %q, want to contain 'unknown agent kind'", msg)
	}
	if !strings.Contains(msg, "available") {
		t.Errorf("error = %q, want to contain 'available'", msg)
	}
}

// fakeAgentEnvSetter records SetEnvironment / UnsetEnvironment calls so tests
// can assert what setTransientAgentEnv would have issued, without spawning tmux.
type fakeAgentEnvSetter struct {
	sets   [][3]string // [session, name, value]
	unsets [][2]string // [session, name]
}

func (f *fakeAgentEnvSetter) SetEnvironment(session, name, value string) error {
	f.sets = append(f.sets, [3]string{session, name, value})
	return nil
}

func (f *fakeAgentEnvSetter) UnsetEnvironment(session, name string) error {
	f.unsets = append(f.unsets, [2]string{session, name})
	return nil
}

func TestSetTransientAgentEnv_SetsWhenNonEmpty(t *testing.T) {
	fe := &fakeAgentEnvSetter{}
	setTransientAgentEnv(fe, "codex")

	wantSet := [][3]string{{tmux.SessionName, "JIN_UI_AGENT", "codex"}}
	if !reflect.DeepEqual(fe.sets, wantSet) {
		t.Errorf("sets = %v, want %v", fe.sets, wantSet)
	}
	if len(fe.unsets) != 0 {
		t.Errorf("unsets = %v, want none", fe.unsets)
	}
}

func TestSetTransientAgentEnv_UnsetsWhenEmpty(t *testing.T) {
	fe := &fakeAgentEnvSetter{}
	setTransientAgentEnv(fe, "")

	wantUnset := [][2]string{{tmux.SessionName, "JIN_UI_AGENT"}}
	if !reflect.DeepEqual(fe.unsets, wantUnset) {
		t.Errorf("unsets = %v, want %v", fe.unsets, wantUnset)
	}
	if len(fe.sets) != 0 {
		t.Errorf("sets = %v, want none", fe.sets)
	}
}

func TestApplyActionPanelBinding_UsesConfigSize(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "popups:\n  action: { width: 90, height: 90 }\n"
	applyActionPanelBinding(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin")
	want := [][]string{
		{"M-p", "display-popup", "-w", "90%", "-h", "90%", "-T", " Action Palette ", "-E", "'/usr/local/bin/jin' action-popup"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplyActionPanelBinding_MultipleKeys(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  action_panel: [\"M-p\", \"M-x\"]\n"
	applyActionPanelBinding(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin")
	want := [][]string{
		{"M-p", "display-popup", "-w", "70%", "-h", "70%", "-T", " Action Palette ", "-E", "'/usr/local/bin/jin' action-popup"},
		{"M-x", "display-popup", "-w", "70%", "-h", "70%", "-T", " Action Palette ", "-E", "'/usr/local/bin/jin' action-popup"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplySessionFilterBinding_BindsAllKeys(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  search: [\"/\"]\n"
	applySessionFilterBinding(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin")
	want := [][]string{
		{"/", "display-popup", "-w", "70%", "-h", "70%", "-T", " Switch Session ", "-E", "'/usr/local/bin/jin' session-filter-popup"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplySessionFilterBinding_UsesConfigSize(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  search: [\"/\"]\npopups:\n  session_filter: { width: 90, height: 90 }\n"
	applySessionFilterBinding(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin")
	want := [][]string{
		{"/", "display-popup", "-w", "90%", "-h", "90%", "-T", " Switch Session ", "-E", "'/usr/local/bin/jin' session-filter-popup"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

func TestApplySessionFilterBinding_NilConfigMgr_NoOp(t *testing.T) {
	fb := &fakeBinder{}
	applySessionFilterBinding(fb, nil, "/usr/local/bin/jin")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplySessionFilterBinding_EmptySelfBin_NoOp(t *testing.T) {
	fb := &fakeBinder{}
	applySessionFilterBinding(fb, mgrWithYAML(t, ""), "")
	if len(fb.calls) != 0 {
		t.Errorf("expected 0 BindKey calls, got %d: %v", len(fb.calls), fb.calls)
	}
}

func TestApplySessionFilterBinding_EmptyKeySkip(t *testing.T) {
	fb := &fakeBinder{}
	yaml := "keybindings:\n  search: [\"\", \"ctrl+p\"]\n"
	applySessionFilterBinding(fb, mgrWithYAML(t, yaml), "/usr/local/bin/jin")
	want := [][]string{
		{"C-p", "display-popup", "-w", "70%", "-h", "70%", "-T", " Switch Session ", "-E", "'/usr/local/bin/jin' session-filter-popup"},
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Errorf("BindKey calls mismatch\n got: %v\nwant: %v", fb.calls, want)
	}
}

// --- identity: which jin the TUI and its popups reach ---

// fakeTUIRespawner records what respawnTUIPane handed tmux. The environment is
// the whole point of the call, and respawn-pane reports nothing about it, so a
// recorder is the only way to see it.
type fakeTUIRespawner struct {
	calls []respawnCall
}

type respawnCall struct {
	target, cmd string
	env         []string
}

func (f *fakeTUIRespawner) RespawnPane(target, shellCmd string, env []string) error {
	f.calls = append(f.calls, respawnCall{target: target, cmd: shellCmd, env: env})
	return nil
}

// withUIEnv points this process's identity inputs at fixed values for one test.
func withUIEnv(t *testing.T, socket, bin string, debug bool) {
	t.Helper()
	t.Setenv("JIN_SOCKET", socket)
	t.Setenv("JIN_BIN", bin)
	prev := debugEnabled
	debugEnabled = func() bool { return debug }
	t.Cleanup(func() { debugEnabled = prev })
}

func TestUIIdentity_TakesTheSocketThisProcessResolved(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "/tmp/jin-bin", true)

	id := uiIdentity()
	if id.SocketPath != "/tmp/ui.sock" {
		t.Errorf("SocketPath = %q, want the resolved socket %q", id.SocketPath, "/tmp/ui.sock")
	}
	if id.BinPath != "/tmp/jin-bin" {
		t.Errorf("BinPath = %q, want %q", id.BinPath, "/tmp/jin-bin")
	}
	if !id.Debug {
		t.Error("Debug = false, want true")
	}
}

// TestUIIdentity_FallsBackToTheDefaultSocket covers the main production path:
// a `jin ui` from a plain shell has no JIN_SOCKET, and resolving the default
// here rather than leaving the child to do it is what stops the child resolving
// it from the tmux server's XDG instead.
func TestUIIdentity_FallsBackToTheDefaultSocket(t *testing.T) {
	withUIEnv(t, "", "/tmp/jin-bin", false)

	got := uiIdentity().SocketPath
	if got == "" {
		t.Fatal("SocketPath is empty; the child would resolve one from its own environment")
	}
	if want := paths.Socket(); got != want {
		t.Errorf("SocketPath = %q, want %q", got, want)
	}
}

// TestUIIdentity_MissingBinIsEmptyNotInherited pins the deliberate half: a
// `jin ui` from a plain shell has no JIN_BIN, and the pane must be told that
// rather than left to take whatever the tmux server holds. TmuxEnviron emits
// the key regardless, so an empty value is what overrides a stale one.
func TestUIIdentity_MissingBinIsEmptyNotInherited(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "", false)

	if got := uiIdentity().BinPath; got != "" {
		t.Errorf("BinPath = %q, want empty", got)
	}
	if !slices.Contains(uiChildEnv(), "JIN_BIN=") {
		t.Errorf("uiChildEnv() = %q, want it to contain %q", uiChildEnv(), "JIN_BIN=")
	}
}

// TestUIChildEnv_NamesNoSession keeps the TUI out of any session's identity: a
// stale JIN_SESSION_ID in the outer tmux server is a plausible UUID, and a pane
// that inherited one would report another session's work as its own.
func TestUIChildEnv_NamesNoSession(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "/tmp/jin-bin", false)
	// The precondition is the whole test: with JIN_SESSION_ID unset this
	// assertion cannot fail, and `jin ui` launched from an agent's pane is
	// exactly where it is set.
	t.Setenv("JIN_SESSION_ID", "11111111-1111-1111-1111-111111111111")

	if !slices.Contains(uiChildEnv(), "JIN_SESSION_ID=") {
		t.Errorf("uiChildEnv() = %q, want it to contain %q", uiChildEnv(), "JIN_SESSION_ID=")
	}
}

// TestUIChildEnv_NamesNoPluginChain is the other inherited value that must not
// travel: a depth in the environment makes the daemon refuse every plugin run
// started from a pane of this server.
func TestUIChildEnv_NamesNoPluginChain(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "/tmp/jin-bin", false)
	t.Setenv(plugin.EnvDepth, "1")

	if !slices.Contains(uiChildEnv(), plugin.EnvDepth+"=") {
		t.Errorf("uiChildEnv() = %q, want it to contain %q", uiChildEnv(), plugin.EnvDepth+"=")
	}
}

// TestRespawnTUIPane_AlwaysCarriesTheIdentity is the assertion the three call
// sites rely on. They pass no environment of their own, so this is the only
// place the answer can be got wrong — which is why the environment is not a
// parameter.
func TestRespawnTUIPane_AlwaysCarriesTheIdentity(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "/tmp/jin-bin", true)

	fr := &fakeTUIRespawner{}
	if err := respawnTUIPane(fr, "%3", "JIN_TMUX=1 'jin' ui"); err != nil {
		t.Fatalf("respawnTUIPane: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("RespawnPane called %d times, want 1", len(fr.calls))
	}
	got := fr.calls[0]
	if got.target != "%3" || got.cmd != "JIN_TMUX=1 'jin' ui" {
		t.Errorf("respawn = (%q, %q), want (%q, %q)", got.target, got.cmd, "%3", "JIN_TMUX=1 'jin' ui")
	}
	// The depth clear is in this list too. uiChildEnv is one list precisely so
	// the pane and the session cannot carry different keys, and reattach
	// respawns before it writes the session — a pane missing the clear there
	// inherits the tmux server's depth, and anything it launches (the editor
	// `v` opens, for one) inherits it in turn.
	for _, kv := range []string{
		"JIN_SOCKET=/tmp/ui.sock", "JIN_BIN=/tmp/jin-bin", "JIN_DEBUG=1",
		"JIN_SESSION_ID=", plugin.EnvDepth + "=",
	} {
		if !slices.Contains(got.env, kv) {
			t.Errorf("respawn env = %q, want it to contain %q", got.env, kv)
		}
	}
}

// TestApplyOuterSessionIdentity_WritesEveryKey covers the floor under the
// popups and plugin runs tmux starts from a key binding, which carry no
// environment of their own. Empty values are written rather than skipped: a
// session entry set to the empty string masks the server's global one, while
// leaving the key out lets the global value through.
func TestApplyOuterSessionIdentity_WritesEveryKey(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "", true)
	fe := &fakeAgentEnvSetter{}
	applyOuterSessionIdentity(fe)

	want := [][3]string{
		{tmux.SessionName, "JIN_SOCKET", "/tmp/ui.sock"},
		{tmux.SessionName, "JIN_BIN", ""},
		{tmux.SessionName, "JIN_DEBUG", "1"},
		{tmux.SessionName, "JIN_SESSION_ID", ""},
		{tmux.SessionName, plugin.EnvDepth, ""},
	}
	if !reflect.DeepEqual(fe.sets, want) {
		t.Errorf("sets = %v, want %v", fe.sets, want)
	}
	if len(fe.unsets) != 0 {
		t.Errorf("unsets = %v, want none (unsetting only stops overriding the server's value)", fe.unsets)
	}
}

// TestApplyOuterSessionIdentity_SplitsAtTheFirstEquals pins the parse against a
// value that contains one. Splitting at the last would write a variable named
// "JIN_SOCKET=/tmp/a" — a name nothing reads, so the socket would simply be
// missing, which is the silent shape this whole change is about.
func TestApplyOuterSessionIdentity_SplitsAtTheFirstEquals(t *testing.T) {
	withUIEnv(t, "/tmp/a=b.sock", "", false)
	fe := &fakeAgentEnvSetter{}
	applyOuterSessionIdentity(fe)

	if !slices.Contains(fe.sets, [3]string{tmux.SessionName, "JIN_SOCKET", "/tmp/a=b.sock"}) {
		t.Errorf("sets = %v, want JIN_SOCKET set to the whole value", fe.sets)
	}
}

// TestApplyOuterSessionIdentity_ClearsAnInheritedPluginDepth is the measured
// failure this closes: a depth left in the tmux server's environment made the
// daemon refuse every `jin plugin run` started from a pane of that server, and
// the plugin key bindings discard their output, so nothing said so.
func TestApplyOuterSessionIdentity_ClearsAnInheritedPluginDepth(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "/tmp/jin-bin", false)
	t.Setenv(plugin.EnvDepth, "1")
	fe := &fakeAgentEnvSetter{}
	applyOuterSessionIdentity(fe)

	if !slices.Contains(fe.sets, [3]string{tmux.SessionName, plugin.EnvDepth, ""}) {
		t.Errorf("sets = %v, want it to clear %s", fe.sets, plugin.EnvDepth)
	}
}

// fakeUISessionOps records both halves of the outer-session setup: the
// environment writes and the key bindings.
type fakeUISessionOps struct {
	fakeAgentEnvSetter
	binds [][]string
}

func (f *fakeUISessionOps) BindKey(key string, cmdArgs ...string) error {
	f.binds = append(f.binds, append([]string{key}, cmdArgs...))
	return nil
}

// TestApplyOuterSessionSetup_DoesEverythingBothOrchestratorsNeed pins the whole
// contents of the collapsed list, not just the identity write that prompted the
// collapse. The two orchestrators call this instead of repeating its parts, so
// this is the only place any of them can go missing from both at once — and a
// missing one is silent: a key binding that was never issued is a key that does
// nothing, and an identity that was never published is a popup on another
// daemon.
func TestApplyOuterSessionSetup_DoesEverythingBothOrchestratorsNeed(t *testing.T) {
	withUIEnv(t, "/tmp/ui.sock", "/tmp/jin-bin", false)
	f := &fakeUISessionOps{}

	// A config that binds every core key and one plugin action, so each of the
	// four binders has something to issue and its absence is visible.
	yaml := "keybindings:\n" +
		"  toggle_pane: [\"M-\\\\\"]\n" +
		"  action_panel: [\"M-p\"]\n" +
		"  search: [\"M-f\"]\n" +
		"  plugins:\n    notifier:\n      actions:\n        default: { keys: [\"M-n\"] }\n"
	const selfBin, displayPane = "/usr/local/bin/jin", "%7"
	applyOuterSessionSetup(f, outerSessionSetup{
		ConfigMgr:        mgrWithYAML(t, yaml),
		AgentFlag:        "codex",
		SelfBin:          selfBin,
		DisplayPaneID:    displayPane,
		InstalledPlugins: pluginSet("notifier"),
	})

	for _, want := range [][3]string{
		{tmux.SessionName, "JIN_SOCKET", "/tmp/ui.sock"},
		{tmux.SessionName, plugin.EnvDepth, ""},
		// setTransientAgentEnv: the create form reads this to preselect --agent.
		{tmux.SessionName, "JIN_UI_AGENT", "codex"},
	} {
		if !slices.Contains(f.sets, want) {
			t.Errorf("sets = %v, want it to contain %v", f.sets, want)
		}
	}

	// One key per binder, so dropping any single applier fails here rather than
	// leaving the others to carry the assertion — and each is checked against
	// what its command must contain, not merely that something was bound.
	// agentFlag, selfBin and displayPaneID are three strings in a row, so a
	// caller can transpose two of them and still compile; the result is a
	// binding that resizes a pane named `/usr/local/bin/jin` or opens a popup
	// by running `'%7' action-popup`, which is the silent shape this whole
	// change is about.
	bound := map[string]string{}
	for _, call := range f.binds {
		bound[call[0]] = strings.Join(call[1:], " ")
	}
	for _, want := range []struct{ key, what, contains string }{
		{"M-\\", "toggle pane", displayPane},
		{"M-p", "action palette", selfBin},
		{"M-f", "session filter", selfBin},
		{"M-n", "the notifier plugin action", selfBin},
	} {
		got, ok := bound[want.key]
		if !ok {
			t.Errorf("no binding for %s (%s); bound keys were %v", want.key, want.what, bound)
			continue
		}
		if !strings.Contains(got, want.contains) {
			t.Errorf("binding for %s (%s) runs %q, want it to name %q",
				want.key, want.what, got, want.contains)
		}
	}
}
