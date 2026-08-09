package daemon

import (
	"path/filepath"
	"testing"
)

// TestNewServer_TellsTheManagerWhichSocketItsAgentsCallBackTo pins the one hop
// no test inside internal/session can reach: whether the socket this daemon
// listens on is the socket its agents are told to reach.
//
// It is the hop most likely to be got wrong and least likely to be noticed.
// NewManager takes three paths in a row, so a transposition compiles; and
// nothing fails when it happens — an agent handed the wrong socket runs, its
// hooks exit 0, and only the status stops moving. The dispatcher next to it is
// built from the same value, so the two are wrong or right together.
//
// The comparison is against the Server's own record rather than a path spelled
// out here: what must hold is that the two agree, whatever the value is.
func TestNewServer_TellsTheManagerWhichSocketItsAgentsCallBackTo(t *testing.T) {
	s, dir := newTestServerIn(t)

	if got := s.manager.AgentIdentity().SocketPath; got != s.socketPath {
		t.Errorf("agents are told to call back to %q, but this daemon listens on %q", got, s.socketPath)
	}
	// The binary an agent re-enters must be the copy EstablishHookBinary makes
	// under stateDir — which NewServer does *after* building the Manager, so
	// this also pins that the identity reads the upgraded path rather than
	// capturing whatever it was at construction.
	wantBin := filepath.Join(dir, "state", "bin", "jin")
	if got := s.manager.AgentIdentity().BinPath; got != wantBin {
		t.Errorf("agents are told to re-enter %q, want the stable copy at %q", got, wantBin)
	}
}
