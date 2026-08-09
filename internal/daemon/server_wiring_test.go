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
func TestNewServer_TellsTheManagerWhichSocketItsAgentsCallBackTo(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	s, err := NewServer(sockPath,
		filepath.Join(dir, "sessions"), filepath.Join(dir, "config"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if got := s.manager.AgentIdentity().SocketPath; got != sockPath {
		t.Errorf("agents are told to call back to %q, but this daemon listens on %q", got, sockPath)
	}
	// The binary an agent re-enters must be the copy EstablishHookBinary made
	// under stateDir, not whatever path the daemon happened to launch from:
	// that path can stop existing while a session is still running.
	wantBin := filepath.Join(dir, "state", "bin", "jin")
	if got := s.manager.AgentIdentity().BinPath; got != wantBin {
		t.Errorf("agents are told to re-enter %q, want the stable copy at %q", got, wantBin)
	}
}
