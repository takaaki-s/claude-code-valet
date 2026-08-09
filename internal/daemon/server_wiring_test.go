package daemon

import (
	"path/filepath"
	"testing"
)

// TestNewServer_TellsItsChildrenWhichJinStartedThem pins the one hop no test
// inside internal/session or internal/plugin can reach: whether the daemon that
// listens on a socket is the daemon its children are told to reach, and whether
// both kinds of child are told the same thing.
//
// It is the hop most likely to be got wrong and least likely to be noticed.
// NewManager takes three paths in a row, so a transposition compiles; and
// nothing fails when it happens — an agent handed the wrong socket runs, its
// hooks exit 0, and only the status stops moving.
//
// The comparison is against the Server's own record rather than a path spelled
// out here: what must hold is that they agree, whatever the value is.
func TestNewServer_TellsItsChildrenWhichJinStartedThem(t *testing.T) {
	s, dir := newTestServerIn(t)

	if got := s.manager.Identity().SocketPath; got != s.socketPath {
		t.Errorf("children are told to call back to %q, but this daemon listens on %q", got, s.socketPath)
	}
	// The binary a child re-enters must be the copy EstablishHookBinary makes
	// under stateDir — which NewServer does *after* building the Manager, so
	// this also pins that the identity reads the upgraded path rather than
	// capturing whatever it was at construction.
	wantBin := filepath.Join(dir, "state", "bin", "jin")
	if got := s.manager.Identity().BinPath; got != wantBin {
		t.Errorf("children are told to re-enter %q, want the stable copy at %q", got, wantBin)
	}
	// Agents and plugins are separate spawn sites that once answered "which jin
	// am I" separately, and the answers diverged: the agent was given the stable
	// copy while a plugin was given the daemon's live executable, which stops
	// being the same file after one rebuild. One value now, checked here because
	// this is the only place both sides are visible at once.
	if got := s.pluginDisp.Identity(); got != s.manager.Identity() {
		t.Errorf("plugins are told %+v, agents %+v — the two spawn sites disagree", got, s.manager.Identity())
	}
}
