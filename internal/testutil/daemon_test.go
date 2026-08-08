package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsolateFromRealDaemon feeds the function the hostile environment it exists
// for — the one a suite run from inside a jind-ai-managed session inherits — and
// checks both halves take effect. Written this way rather than as a check on the
// ambient environment because the ambient one is already clean on CI, where a
// check like that would pass no matter what the function did.
func TestIsolateFromRealDaemon(t *testing.T) {
	t.Setenv("JIN_SESSION_ID", "a-live-session-that-must-not-be-touched")
	reachable := filepath.Join(t.TempDir(), "daemon.sock")
	if err := os.WriteFile(reachable, nil, 0o600); err != nil {
		t.Fatalf("seed a reachable socket path: %v", err)
	}
	t.Setenv("JIN_SOCKET", reachable)

	sock := IsolateFromRealDaemon()

	if got, ok := os.LookupEnv("JIN_SESSION_ID"); ok {
		t.Errorf("JIN_SESSION_ID is still %q; a hook fixture would be delivered to that session", got)
	}
	if got := os.Getenv("JIN_SOCKET"); got != sock {
		t.Errorf("JIN_SOCKET = %q, want the returned path %q", got, sock)
	}
	if sock == reachable {
		t.Fatalf("JIN_SOCKET still points at %q; the suite can reach a daemon", sock)
	}
	// The returned path must not merely differ from the seeded one — clearing
	// JIN_SOCKET would do that too, and send the resolver to the real default.
	if _, err := os.Stat(filepath.Dir(sock)); err == nil {
		t.Errorf("the isolation directory %q exists; a daemon could bind there mid-run", filepath.Dir(sock))
	}
}
