//go:build e2e

package tui

import (
	"testing"

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
