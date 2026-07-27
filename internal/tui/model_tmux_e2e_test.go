//go:build e2e

package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/testutil"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// The display pane's residue after a delete is invisible to the plain unit
// tests: the pane label survives respawn-pane (see docs/gotchas.md), and only
// a live tmux server can say so. These tests run the same two-pane layout
// `jin ui` builds, on a throwaway socket.

// outerTmuxFixture builds the outer tmux layout (`ui` window: TUI pane on the
// left, display pane on the right) on a private socket and returns a client
// plus the display pane's ID.
func outerTmuxFixture(t *testing.T) (*tmux.Client, string) {
	t.Helper()

	tc, err := tmux.NewClientWithSocket(testutil.TmuxSocket(t))
	if err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	// The session name is fixed: recordDisplayedSession / clearDisplayedSession
	// address the outer server's environment by tmux.SessionName.
	if err := tc.NewSessionWithCmd(tmux.SessionName, 120, 40, tmux.UIWindowName, tmux.PlaceholderCmd); err != nil {
		t.Skipf("cannot start a tmux server: %v", err)
	}
	target := tmux.SessionName + ":" + tmux.UIWindowName
	if _, err := tc.SplitPane(target, tmux.SplitOptions{Direction: "right", Size: "75%", Cmd: tmux.PlaceholderCmd}); err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	// The freshly split pane is the active one, so the window target resolves
	// to it — same assumption runTUI makes when it records JIN_DISPLAY_PANE.
	displayPaneID, err := tc.GetPaneID(target)
	if err != nil || displayPaneID == "" {
		t.Fatalf("GetPaneID: %q %v", displayPaneID, err)
	}
	return tc, displayPaneID
}

// paneLabel reads back the text pane-border-format renders above the pane.
func paneLabel(t *testing.T, tc *tmux.Client, paneID string) string {
	t.Helper()
	got, err := tc.GetPaneOption(paneID, tmux.PaneLabelOption)
	if err != nil {
		t.Fatalf("GetPaneOption: %v", err)
	}
	return got
}

// TestE2E_DeleteLastSessionClearsPaneLabel is the regression this whole change
// exists for: deleting the last session used to leave its name on the display
// pane's border, because nothing ever reset the pane label.
func TestE2E_DeleteLastSessionClearsPaneLabel(t *testing.T) {
	tc, displayPaneID := outerTmuxFixture(t)

	m := Model{
		sessions:      []session.Info{{ID: "s1", Description: "doomed-session"}},
		cursor:        0,
		deletingIDs:   map[string]bool{"s1": true},
		height:        100,
		tmuxClient:    tc,
		displayPaneID: displayPaneID,
	}
	// Put the pane in the state a displayed session leaves behind.
	m.recordDisplayedSession(&m.sessions[0])
	if got := paneLabel(t, tc, displayPaneID); got != "doomed-session" {
		t.Fatalf("fixture did not label the pane: %q", got)
	}

	// The delete finished: the record is gone from the daemon's list.
	next, _ := m.updateListMode(sessionsMsg(nil))
	nm := next.(Model)

	if got := paneLabel(t, tc, displayPaneID); got != "" {
		t.Errorf("pane label = %q, want empty — the deleted session's name is still on the pane border", got)
	}
	if got := tc.GetEnvironment(tmux.SessionName, "JIN_CURRENT_SESSION"); got != "" {
		t.Errorf("JIN_CURRENT_SESSION = %q, want empty — a relaunched TUI would restore a deleted session", got)
	}
	if nm.lastDisplayedDesc != "" {
		t.Errorf("lastDisplayedDesc = %q, want empty", nm.lastDisplayedDesc)
	}
	if nm.currentSessionID != placeholderSessionID {
		t.Errorf("currentSessionID = %q, want %q", nm.currentSessionID, placeholderSessionID)
	}
}

// TestE2E_DeleteInFlightClearsPaneLabel covers the window the daemon spends
// finalizing: it accepts the delete, then removes the worktree and kills the
// tmux session on a background goroutine. The pane must be off the target
// before any of that runs, not one poll after it finishes.
func TestE2E_DeleteInFlightClearsPaneLabel(t *testing.T) {
	tc, displayPaneID := outerTmuxFixture(t)

	_, client := startFakeDaemon(t)
	m := Model{
		client:        client,
		sessions:      []session.Info{{ID: "s1", Description: "doomed-session"}},
		cursor:        0,
		deletingIDs:   map[string]bool{},
		height:        100,
		tmuxClient:    tc,
		displayPaneID: displayPaneID,
	}
	m.recordDisplayedSession(&m.sessions[0])

	next, cmd := m.deleteSession("s1", false, false)
	if cmd == nil {
		t.Fatal("deleteSession issued no Cmd")
	}
	if got := paneLabel(t, tc, displayPaneID); got != "" {
		t.Errorf("pane label = %q, want empty — the pane still names the session being deleted", got)
	}
	if got := next.(Model).currentSessionID; got != placeholderSessionID {
		t.Errorf("currentSessionID = %q, want %q", got, placeholderSessionID)
	}
}

// waitForPaneText polls the pane until want shows up, so a respawned command
// gets the few milliseconds it needs to draw. Returns the last capture either
// way, for the failure message.
func waitForPaneText(t *testing.T, tc *tmux.Client, paneID, want string) (string, bool) {
	t.Helper()
	var last string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := tc.CapturePane(paneID, false); err == nil {
			last = got
			if strings.Contains(got, want) {
				return last, true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last, false
}

// paneStartCommand reads back the command the pane was last spawned with, which
// says which switchToSession branch ran without waiting for output. GetPaneOption
// wraps its argument in #{…}, so a built-in format variable reads back the same
// way a custom @option does. tmux wraps a command containing spaces in double
// quotes; strip them so callers can compare against what they passed in.
func paneStartCommand(t *testing.T, tc *tmux.Client, paneID string) string {
	t.Helper()
	got, err := tc.GetPaneOption(paneID, "pane_start_command")
	if err != nil {
		t.Fatalf("GetPaneOption(pane_start_command): %v", err)
	}
	return strings.TrimSuffix(strings.TrimPrefix(got, `"`), `"`)
}

// TestE2E_KillDoesNotReattachOnStaleList is the pane-level half of
// TestSessionsMsg_KillArmSurvivesStaleList: the session poll can deliver a
// snapshot taken before the daemon saw the Kill, and spending the reswitch on
// it respawns the pane onto `tmux attach` for a session that is about to
// disappear. Only a real tmux can say which command the pane ended up running.
func TestE2E_KillDoesNotReattachOnStaleList(t *testing.T) {
	tc, displayPaneID := outerTmuxFixture(t)

	// An inner session name no live jin server can resolve. The pre-fix path
	// really does run `tmux -L jin attach -t <name>` in the pane, and attach
	// against an absent session neither starts a server nor disturbs one.
	const innerSession = "sess-e2e-kill-reswitch-does-not-exist"

	running := []session.Info{{
		ID:             "s1",
		Description:    "doomed-session",
		Status:         session.StatusRunning,
		TmuxWindowName: innerSession,
	}}
	m := Model{
		sessions:           running,
		cursor:             0,
		deletingIDs:        map[string]bool{},
		height:             100,
		tmuxClient:         tc,
		displayPaneID:      displayPaneID,
		displayLocalAttach: true,
		pendingKillID:      "s1",
	}
	m.recordDisplayedSession(&m.sessions[0])
	if got := paneStartCommand(t, tc, displayPaneID); got != tmux.PlaceholderCmd {
		t.Fatalf("fixture: display pane runs %q, want the placeholder (%q)", got, tmux.PlaceholderCmd)
	}

	// The poll that started before the Kill reached the daemon. It must leave
	// the pane exactly as it found it — respawning here is both the bug and, on
	// a session that is merely slow to die, a visible flash.
	next, _ := m.updateListMode(sessionsMsg(running))
	if got := paneStartCommand(t, tc, displayPaneID); got != tmux.PlaceholderCmd {
		t.Fatalf("display pane runs %q after a snapshot that predates the kill, want it untouched (%q)",
			got, tmux.PlaceholderCmd)
	}

	// The Kill's own List(): the record is still listed, now stopped.
	next, _ = next.(Model).updateListMode(sessionsMsg([]session.Info{
		{ID: "s1", Description: "doomed-session", Status: session.StatusStopped},
	}))

	if got := paneStartCommand(t, tc, displayPaneID); !strings.HasPrefix(got, "printf") {
		t.Fatalf("display pane runs %q, want the stopped-session placeholder", got)
	}
	got, ok := waitForPaneText(t, tc, displayPaneID, "Press Enter to restart")
	if !ok {
		t.Errorf("display pane was respawned with the placeholder but never drew it.\npane content:\n%s", got)
	}
	if nm := next.(Model); nm.pendingKillID != "" {
		t.Errorf("pendingKillID = %q, want empty", nm.pendingKillID)
	}
}

// TestE2E_PlaceholderThenNewSessionRelabels guards the recovery direction: the
// cleared label must not be sticky, or a session created after the list went
// empty shows up on a pane whose border stays blank.
func TestE2E_PlaceholderThenNewSessionRelabels(t *testing.T) {
	tc, displayPaneID := outerTmuxFixture(t)

	m := Model{
		cursor:        0,
		deletingIDs:   map[string]bool{},
		height:        100,
		tmuxClient:    tc,
		displayPaneID: displayPaneID,
	}
	m.respawnPlaceholder()
	if got := paneLabel(t, tc, displayPaneID); got != "" {
		t.Fatalf("fixture: pane label = %q, want empty", got)
	}

	next, _ := m.updateListMode(sessionsMsg([]session.Info{
		{ID: "s2", Description: "fresh-session", Status: session.StatusStopped},
	}))
	if got := paneLabel(t, tc, displayPaneID); got != "fresh-session" {
		t.Errorf("pane label = %q, want %q", got, "fresh-session")
	}
	if got := next.(Model).currentSessionID; got != "s2" {
		t.Errorf("currentSessionID = %q, want %q", got, "s2")
	}
}
