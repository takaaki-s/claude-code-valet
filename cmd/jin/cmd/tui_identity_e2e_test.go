//go:build e2e

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/jinenv"
	"github.com/takaaki-s/jind-ai/internal/paths"
	"github.com/takaaki-s/jind-ai/internal/plugin"
	"github.com/takaaki-s/jind-ai/internal/testutil"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// These cover the two wirings that decide which daemon this whole UI talks to,
// and that no plain test can reach: one needs a live tmux client to build the
// Model, the other is an orchestrator that ends by taking over the terminal.
// Left unreachable, either could be reverted with every unit test still green
// while the running UI went back to resolving a daemon from whatever forked the
// tmux server — measured as a different daemon in 3 of 3 trials.

// uiTestClient gives the test the same client production uses for the outer
// tmux, on a throwaway socket. NewMgrClient rather than NewClientWithSocket
// deliberately: it is what `jin ui` builds, and it passes -f /dev/null, so the
// server under test is not the developer's ~/.tmux.conf. Without that the local
// run is the weaker one — a user conf can set pane-base-index, hooks,
// default-command, even spawn processes, none of which CI has.
//
// The XDG dirs are isolated so nothing here writes to the runner's home;
// XDG_RUNTIME_DIR is among them because paths.Socket() derives from it, and a
// test added later must not reach the real one.
func uiTestClient(t *testing.T) *tmux.Client {
	t.Helper()
	root := t.TempDir()
	for _, kv := range [][2]string{
		{"XDG_CONFIG_HOME", "cfg"}, {"XDG_STATE_HOME", "state"},
		{"XDG_DATA_HOME", "data"}, {"XDG_RUNTIME_DIR", "run"},
	} {
		t.Setenv(kv[0], filepath.Join(root, kv[1]))
	}
	t.Setenv("JIN_TMUX_MGR_SOCKET", testutil.TmuxSocket(t))

	tc, err := tmux.NewMgrClient()
	if err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	return tc
}

// uiTestTmux is uiTestClient plus the outer session `jin ui` reattaches to.
func uiTestTmux(t *testing.T) *tmux.Client {
	t.Helper()
	tc := uiTestClient(t)
	if err := tc.NewSessionWithCmd(tmux.SessionName, 80, 24, tmux.UIWindowName, tmux.PlaceholderCmd); err != nil {
		t.Skipf("cannot start a tmux server: %v", err)
	}
	return tc
}

// stubAttach stops an orchestrator where it would take over the terminal, and
// reports whether it got that far. Replacing attachToSessionFn is what makes
// createAndAttachTmux and reattachTmux enterable from a test at all: the real
// one blocks until the user detaches.
func stubAttach(t *testing.T) *bool {
	t.Helper()
	attached := false
	prev := attachToSessionFn
	attachToSessionFn = func(*tmux.Client) error { attached = true; return nil }
	t.Cleanup(func() { attachToSessionFn = prev })
	return &attached
}

// assertPaneCarriesSocket reads the environment the respawned TUI pane dumped
// and checks it names the socket this process resolved, rather than the one the
// tmux server was holding. The trailing newline anchors the end of the value:
// `env` prints one assignment per line.
func assertPaneCarriesSocket(t *testing.T, envDump, socket string) {
	t.Helper()
	paneEnv, err := waitForFile(t, envDump)
	if err != nil {
		t.Fatalf("read pane env: %v", err)
	}
	if !strings.Contains(string(paneEnv), "JIN_SOCKET="+socket+"\n") {
		t.Errorf("the TUI pane did not carry this process's socket %s; its env was:\n%s", socket, paneEnv)
	}
}

func TestTUIModelForPane_CarriesThisProcessIdentity(t *testing.T) {
	tc := uiTestTmux(t)
	withUIEnv(t, "/tmp/e2e-ui.sock", "/tmp/e2e-bin", true)

	got := tuiModelForPane(nil, tc, nil, "", "").Identity()
	if got.SocketPath != "/tmp/e2e-ui.sock" {
		t.Errorf("SocketPath = %q, want the socket this process resolved", got.SocketPath)
	}
	if got.BinPath != "/tmp/e2e-bin" {
		t.Errorf("BinPath = %q, want %q", got.BinPath, "/tmp/e2e-bin")
	}
	if !got.Debug {
		t.Error("Debug = false, want this process's flag")
	}
}

// TestReattachTmux_PublishesTheIdentity enters the orchestrator itself, stopping
// where it would attach. Reattach is the path that matters most — it is the one
// that runs against a tmux server older than this invocation, so it is the one
// most likely to be holding another jin's values.
func TestReattachTmux_PublishesTheIdentity(t *testing.T) {
	const socket = "/tmp/e2e-reattach.sock"
	tc := uiTestTmux(t)
	withUIEnv(t, socket, "/tmp/e2e-bin", false)
	installProbePlugin(t)

	// The layout a previous `jin ui` left behind: reattach binds against the
	// display pane it finds recorded, and a fixture without one would exercise
	// the path with a pane id of "".
	target := tmux.SessionName + ":" + tmux.UIWindowName
	if _, err := tc.SplitPane(target, tmux.SplitOptions{Direction: "right", Size: "75%", Cmd: tmux.PlaceholderCmd}); err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	displayPaneID, err := tc.GetPaneID(target)
	if err != nil || displayPaneID == "" {
		t.Fatalf("GetPaneID: %q %v", displayPaneID, err)
	}
	_ = tc.SetEnvironment(tmux.SessionName, "JIN_DISPLAY_PANE", displayPaneID)

	// The values a server forked by something else would be holding.
	_ = tc.SetEnvironment(tmux.SessionName, "JIN_SOCKET", "/tmp/stale.sock")
	_ = tc.SetEnvironment(tmux.SessionName, jinenv.EnvDepth, "1")

	attached := stubAttach(t)

	// The TUI pane's command is a probe that dumps its environment. What that
	// distinguishes is narrower than it looks: -e and the session entry carry
	// byte-identical values, both from uiChildEnv, so the dump only proves the
	// pane was given the identity from *somewhere*. It is the respawn running
	// before the session write that makes -e the only available source here —
	// TestRespawnTUIPane_AlwaysCarriesTheIdentity is what pins -e itself.
	//
	// It sleeps afterwards because the real TUI is long-lived: a command that
	// exits takes the pane, and with it the only window and the session the
	// writes below address.
	envDump := filepath.Join(t.TempDir(), "pane-env")
	if err := reattachTmux(tc, "env > "+envDump+"; sleep 30", "codex"); err != nil {
		t.Fatalf("reattachTmux: %v", err)
	}
	if !*attached {
		t.Error("reattachTmux returned without attaching")
	}
	assertSetupArgumentsLanded(t, tc)

	if got := tc.GetEnvironment(tmux.SessionName, "JIN_SOCKET"); got != socket {
		t.Errorf("session JIN_SOCKET = %q, want this process's socket", got)
	}
	if got := tc.GetEnvironment(tmux.SessionName, jinenv.EnvDepth); got != "" {
		t.Errorf("session %s = %q, want it cleared", jinenv.EnvDepth, got)
	}

	assertPaneCarriesSocket(t, envDump, socket)
}

// waitForFile reads path once the respawned pane has written it. respawn-pane
// returns as soon as tmux has forked the command, so the file appears a moment
// later; polling is the whole of the synchronisation. Three seconds, the same
// budget the repo's other tmux-backed suites allow a shell to start.
func waitForFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	var last error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 {
			return b, nil
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		// The file exists but stayed empty. Returning nil here would surface as
		// "the pane did not carry the socket", which is a different fault.
		return nil, fmt.Errorf("%s stayed empty for 3s", path)
	}
	return nil, last
}

// TestReattachTmux_RespawnsATrackedDeadPaneWithTheIdentity is the branch a real
// reattach almost always takes: the TUI pane is tracked and its process has
// exited, so reattach brings it back. It is a separate test because the pane has
// to be tagged (remain-on-exit) and killed first, which is the state `jin ui`
// leaves behind when the user quits the TUI but not the tmux server.
func TestReattachTmux_RespawnsATrackedDeadPaneWithTheIdentity(t *testing.T) {
	const socket = "/tmp/e2e-tracked.sock"
	tc := uiTestTmux(t)
	withUIEnv(t, socket, "/tmp/e2e-bin", false)

	paneID, err := tc.GetPaneID(tmux.SessionName + ":" + tmux.UIWindowName)
	if err != nil || paneID == "" {
		t.Fatalf("GetPaneID: %q %v", paneID, err)
	}
	if err := tc.TagManagedPane(paneID); err != nil {
		t.Fatalf("TagManagedPane: %v", err)
	}
	_ = tc.SetEnvironment(tmux.SessionName, "JIN_TUI_PANE", paneID)
	// remain-on-exit keeps the pane in place once its command ends, which is
	// what makes it dead rather than gone — the state reattach looks for.
	if err := tc.RespawnPane(paneID, "true", nil); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	waitUntil(t, "the tracked TUI pane is dead", func() bool { return tc.IsPaneDead(paneID) })

	stubAttach(t)

	envDump := filepath.Join(t.TempDir(), "pane-env")
	if err := reattachTmux(tc, "env > "+envDump+"; sleep 30", ""); err != nil {
		t.Fatalf("reattachTmux: %v", err)
	}

	assertPaneCarriesSocket(t, envDump, socket)
}

// waitUntil polls cond, which describes a tmux-side state change that the
// command causing it reports nothing about. what names it, so a CI failure says
// which wait ran out.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting until %s", what)
}

// TestCreateAndAttachTmux_PublishesTheIdentity covers the path a cold tmux
// server takes — the first `jin ui` of a session, and the one a user meets
// first. It went two review iterations without coverage because the seam that
// made reattach reachable was only ever pointed at reattach.
func TestCreateAndAttachTmux_PublishesTheIdentity(t *testing.T) {
	const socket = "/tmp/e2e-create.sock"
	// No session yet: createAndAttachTmux builds its own.
	tc := uiTestClient(t)
	withUIEnv(t, socket, "/tmp/e2e-bin", false)
	t.Setenv(jinenv.EnvDepth, "1")
	installProbePlugin(t)

	attached := stubAttach(t)

	envDump := filepath.Join(t.TempDir(), "pane-env")
	if err := createAndAttachTmux(tc, "env > "+envDump+"; sleep 30", "codex"); err != nil {
		t.Fatalf("createAndAttachTmux: %v", err)
	}

	assertSetupArgumentsLanded(t, tc)
	if !*attached {
		t.Error("createAndAttachTmux returned without attaching")
	}

	if got := tc.GetEnvironment(tmux.SessionName, "JIN_SOCKET"); got != socket {
		t.Errorf("session JIN_SOCKET = %q, want this process's socket", got)
	}
	if got := tc.GetEnvironment(tmux.SessionName, jinenv.EnvDepth); got != "" {
		t.Errorf("session %s = %q, want it cleared", jinenv.EnvDepth, got)
	}

	assertPaneCarriesSocket(t, envDump, socket)
}

// listKeys reads the outer server's root key table. The tmux client type has no
// accessor for it, and the bindings are the only place the strings the
// orchestrator distributed can be told apart after the fact.
func listKeys(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", os.Getenv("JIN_TMUX_MGR_SOCKET"), "-f", "/dev/null", "list-keys", "-T", "root").Output()
	if err != nil {
		t.Fatalf("tmux list-keys: %v", err)
	}
	return string(out)
}

// keyBindingLine returns the first root binding whose command contains want.
func keyBindingLine(keys, want string) string {
	for _, line := range strings.Split(keys, "\n") {
		if strings.Contains(line, want) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// assertSetupArgumentsLanded reads back what an orchestrator distributed to
// applyOuterSessionSetup.
//
// The three strings it passes are interchangeable to the compiler, and a swap
// is silent in production: the create form would preselect an adapter named
// after the jin binary, and a key would resize a pane named after it. Both
// orchestrators pass them, so both are checked here. The create path grew these
// assertions first and reattach went without them for a round, which is why
// they live in a helper now rather than in whichever test was written last.
//
// Some transpositions are caught by the compiler instead, when the swap leaves
// a variable unused. That is not a guarantee to lean on: it lasts only while
// the variable has exactly one use.
func assertSetupArgumentsLanded(t *testing.T, tc *tmux.Client) {
	t.Helper()
	if got := tc.GetEnvironment(tmux.SessionName, "JIN_UI_AGENT"); got != "codex" {
		t.Errorf("JIN_UI_AGENT = %q, want the --agent flag", got)
	}
	displayPane := tc.GetEnvironment(tmux.SessionName, "JIN_DISPLAY_PANE")
	if displayPane == "" {
		t.Fatal("JIN_DISPLAY_PANE is empty: on the create path the layout did not finish; on reattach the fixture did not record one")
	}
	selfBin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// tmux quotes the arguments it echoes back, so each binding is located by
	// its command and then read for the string the orchestrator gave it. `-t`
	// distinguishes the toggle from tmux's own mouse menu, which zooms the pane
	// under the cursor and so names none.
	keys := listKeys(t)
	if line := keyBindingLine(keys, "resize-pane -Z -t"); !strings.Contains(line, displayPane) {
		t.Errorf("the toggle-pane binding is %q, want it to name the display pane %s", line, displayPane)
	}
	if line := keyBindingLine(keys, "action-popup"); !strings.Contains(line, selfBin) {
		t.Errorf("the action-palette binding is %q, want it to name this binary %s", line, selfBin)
	}
	// The plugin bindings are the one set that comes from the on-disk registry
	// rather than from the config alone, so this is the only assertion here that
	// fails if an orchestrator stops passing a plugin set. A binding never
	// issued is a key that does nothing, and nothing reports it.
	if line := keyBindingLine(keys, "plugin run probe"); line == "" {
		t.Errorf("no binding runs the installed probe plugin; bindings were:\n%s", keys)
	}
}

// installProbePlugin puts one enabled plugin where the registry looks and binds
// an action of it, so applyPluginActionBindings has something to issue. Both
// halves are needed: the config names the key, the registry decides whether the
// plugin is runnable, and a binding appears only when they agree.
func installProbePlugin(t *testing.T) {
	t.Helper()
	dir := filepath.Join(paths.Plugins(), "probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: 1
name: probe
version: 0.1.0
description: probe
jin: ">=0.0.0"
install:
  source:
    build: ["true"]
    entrypoint: "true"
on: []
`
	if err := os.WriteFile(filepath.Join(dir, "jind-ai-plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := plugin.LoadLock(getStateDir())
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if err := lock.Set("probe", plugin.LockEntry{Source: "test", InstalledAt: time.Now()}); err != nil {
		t.Fatalf("lock.Set: %v", err)
	}
	cfgDir := getConfigDir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "keybindings:\n  plugins:\n    probe:\n      actions:\n        default: { keys: [\"M-y\"] }\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}
