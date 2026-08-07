//go:build e2e

package session

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/internal/testutil"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// What a kill leaves behind is only partly visible to the mock: the mock is
// told what a pane does, while the point of the change is what tmux itself
// does with a pane whose process was signalled rather than killed. These tests
// run the real Manager against a real tmux server on a throwaway socket.

// e2eAgent spawns a process that just sits there, so the pane stays up until
// something stops it. The shared fakeAgent spawns "claude", which these tests
// must not run.
type e2eAgent struct{}

func (e2eAgent) Kind() string                        { return "e2e" }
func (e2eAgent) Setup(SetupContext) error            { return nil }
func (e2eAgent) SpawnCommand(SpawnOptions) SpawnPlan { return SpawnPlan{Command: "sleep 900"} }
func (e2eAgent) Description() DescriptionEnhancer    { return nil }
func (e2eAgent) StatusSource() StatusSource          { return fakeStatusSource{} }
func (e2eAgent) ClearInputKeys() []string            { return nil }
func (e2eAgent) PastePlaceholder(string) string      { return "" }

// DismissOverlayKeys returns nil: these tests drive a pane running `sleep`,
// which has no input area and so no overlay to close.
func (e2eAgent) DismissOverlayKeys(string) []string { return nil }

type e2eResolver struct{}

func (e2eResolver) Resolve(string) (Agent, error) { return e2eAgent{}, nil }

// killFixture builds a Manager wired to a private tmux server, plus the state
// directory it persists to (the daemon-restart test reopens it).
func killFixture(t *testing.T) (*Manager, *tmux.Client, string, string) {
	t.Helper()

	socket := testutil.TmuxSocket(t)
	tc, err := tmux.NewClientWithSocket(socket)
	if err != nil {
		t.Skipf("tmux not available: %v", err)
	}

	stateDir, configDir := t.TempDir(), t.TempDir()
	configMgr, err := config.NewManager(configDir)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	mgr, err := NewManager(stateDir, configDir, configMgr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetTmuxClient(tc)
	mgr.SetAgentResolver(e2eResolver{})
	return mgr, tc, stateDir, configDir
}

// startedSession creates and starts a session, and returns it once its pane is
// actually up.
func startedSession(t *testing.T, mgr *Manager, tc *tmux.Client) *Session {
	t.Helper()

	sess, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: t.TempDir(), Description: "e2e-kill"})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	if err := mgr.StartBackground(sess.ID); err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	got, _ := mgr.Get(sess.ID)
	if got.TmuxWindowName == "" || got.TmuxPaneID == "" {
		t.Fatalf("session started without tmux refs: window=%q pane=%q", got.TmuxWindowName, got.TmuxPaneID)
	}
	waitFor(t, "agent pane to come up", func() bool { return !tc.IsPaneDead(got.TmuxPaneID) })
	return sess
}

// splitAuxPane adds the kind of pane a plugin or the user would open next to
// the agent, and returns its ID.
func splitAuxPane(t *testing.T, tc *tmux.Client, target string) string {
	t.Helper()
	id, err := tc.SplitPane(target, tmux.SplitOptions{Direction: "down", Size: "30%", NoFocus: true, Cmd: "sleep 900"})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	return strings.TrimSpace(id)
}

func windowLayout(t *testing.T, socket, target string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, "#{window_layout}").Output()
	if err != nil {
		t.Fatalf("read window_layout: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// paneShape strips the layout's leading checksum and pane ids, leaving the
// geometry. Reviving a pane changes neither, but comparing the raw string
// would tie the assertion to tmux's id bookkeeping.
func paneShape(layout string) string {
	if _, rest, found := strings.Cut(layout, ","); found {
		return rest
	}
	return layout
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestE2E_KillKeepsWindowAndOtherPanes is V-001 and V-002 together: the panes
// the session was carrying have to survive the kill, and the restart has to
// put the agent back in its own pane rather than rebuild the window.
func TestE2E_KillKeepsWindowAndOtherPanes(t *testing.T) {
	mgr, tc, _, _ := killFixture(t)
	socket := tc.GetSocketName()

	sess := startedSession(t, mgr, tc)
	before, _ := mgr.Get(sess.ID)
	agentPane, window := before.TmuxPaneID, before.TmuxWindowName

	auxPane := splitAuxPane(t, tc, agentPane)
	layoutBefore := windowLayout(t, socket, window)

	if err := mgr.Kill(sess.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	// V-001
	if !tc.HasSession(window) {
		t.Fatal("inner tmux session died with the kill; the session's other panes went with it")
	}
	if !tc.IsPaneDead(agentPane) {
		t.Error("agent pane is still running after the kill")
	}
	if tc.IsPaneDead(auxPane) {
		t.Error("the pane next to the agent was taken down by the kill")
	}
	panes, err := tc.ListPaneIDs(window)
	if err != nil {
		t.Fatalf("ListPaneIDs: %v", err)
	}
	if len(panes) != 2 {
		t.Errorf("panes = %v, want the agent's and the one beside it", panes)
	}
	if got := paneShape(windowLayout(t, socket, window)); got != paneShape(layoutBefore) {
		t.Errorf("layout = %q, want it unchanged from %q", got, paneShape(layoutBefore))
	}
	killed, _ := mgr.Get(sess.ID)
	if killed.Status != StatusStopped {
		t.Errorf("Status = %q, want %q", killed.Status, StatusStopped)
	}
	if killed.TmuxWindowName != window || killed.TmuxPaneID != agentPane {
		t.Errorf("tmux refs = %q/%q, want them kept as %q/%q", killed.TmuxWindowName, killed.TmuxPaneID, window, agentPane)
	}

	// V-002
	if err := mgr.StartBackground(sess.ID); err != nil {
		t.Fatalf("restart: %v", err)
	}
	revived, _ := mgr.Get(sess.ID)
	if revived.TmuxPaneID != agentPane {
		t.Errorf("agent pane = %q after restart, want the original %q", revived.TmuxPaneID, agentPane)
	}
	waitFor(t, "agent pane to come back", func() bool { return !tc.IsPaneDead(agentPane) })
	if tc.IsPaneDead(auxPane) {
		t.Error("the pane next to the agent did not survive the restart")
	}
	if got := paneShape(windowLayout(t, socket, window)); got != paneShape(layoutBefore) {
		t.Errorf("layout = %q after restart, want it unchanged from %q", got, paneShape(layoutBefore))
	}
}

// TestE2E_KillRightAfterStartStaysStopped is V-009: a session killed inside
// the quick-resume window must not be handed back by the monitor's retry. The
// wait covers the monitor's first tick.
func TestE2E_KillRightAfterStartStaysStopped(t *testing.T) {
	mgr, tc, _, _ := killFixture(t)

	sess := startedSession(t, mgr, tc)
	started, _ := mgr.Get(sess.ID)

	if err := mgr.Kill(sess.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// The monitor polls every 10s; give it a tick plus slack to misbehave in.
	time.Sleep(12 * time.Second)

	got, _ := mgr.Get(sess.ID)
	if got.Status != StatusStopped {
		t.Errorf("Status = %q, want it still %q", got.Status, StatusStopped)
	}
	if !tc.IsPaneDead(started.TmuxPaneID) {
		t.Error("the agent came back on its own after a kill inside the quick-resume window")
	}
}

// TestE2E_KillThenDeleteReclaimsWindow is V-012: kill hands the tmux resources
// to delete rather than releasing them, so delete has to actually get them.
func TestE2E_KillThenDeleteReclaimsWindow(t *testing.T) {
	mgr, tc, _, _ := killFixture(t)

	sess := startedSession(t, mgr, tc)
	before, _ := mgr.Get(sess.ID)
	window := before.TmuxWindowName
	splitAuxPane(t, tc, before.TmuxPaneID)

	if err := mgr.Kill(sess.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := mgr.Delete(sess.ID, false, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	waitFor(t, "the inner session to be reclaimed", func() bool { return !tc.HasSession(window) })
}

// TestE2E_KilledSessionSurvivesDaemonRestart is V-014: a new Manager over the
// same state directory is what a daemon restart looks like from here. The
// killed session has to come back stopped but still revivable in place.
func TestE2E_KilledSessionSurvivesDaemonRestart(t *testing.T) {
	mgr, tc, stateDir, configDir := killFixture(t)

	sess := startedSession(t, mgr, tc)
	before, _ := mgr.Get(sess.ID)
	agentPane, window := before.TmuxPaneID, before.TmuxWindowName
	auxPane := splitAuxPane(t, tc, agentPane)

	if err := mgr.Kill(sess.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	configMgr, err := config.NewManager(configDir)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	restarted, err := NewManager(stateDir, configDir, configMgr)
	if err != nil {
		t.Fatalf("NewManager after restart: %v", err)
	}
	restarted.SetTmuxClient(tc)
	restarted.SetAgentResolver(e2eResolver{})
	restarted.RecoverTmuxSessions()

	got, ok := restarted.Get(sess.ID)
	if !ok {
		t.Fatal("session did not survive the restart")
	}
	if got.Status != StatusStopped {
		t.Errorf("Status = %q, want %q", got.Status, StatusStopped)
	}
	if got.TmuxWindowName != window || got.TmuxPaneID != agentPane {
		t.Fatalf("tmux refs = %q/%q, want them recovered as %q/%q", got.TmuxWindowName, got.TmuxPaneID, window, agentPane)
	}

	if err := restarted.StartBackground(sess.ID); err != nil {
		t.Fatalf("restart after daemon restart: %v", err)
	}
	revived, _ := restarted.Get(sess.ID)
	if revived.TmuxPaneID != agentPane {
		t.Errorf("agent pane = %q, want the original %q", revived.TmuxPaneID, agentPane)
	}
	if tc.IsPaneDead(auxPane) {
		t.Error("the pane next to the agent did not survive the daemon restart cycle")
	}
}
