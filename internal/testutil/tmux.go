package testutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TmuxSocket returns a random, per-test tmux socket name and registers cleanup
// that kills the server and removes its socket file. It does not start a
// server or touch any environment variable — callers decide how the name is
// reached (a client built with it, or $JIN_TMUX_SOCKET for code that resolves
// the socket itself).
//
// The explicit unlink is not redundant: tmux 3.x leaves the socket file behind
// on kill-server, so without it stale sockets pile up under
// $TMUX_TMPDIR/tmux-$UID/ across runs.
func TmuxSocket(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	name := "jin-test-" + hex.EncodeToString(b[:])
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", name, "kill-server").Run()
		tmpdir := os.Getenv("TMUX_TMPDIR")
		if tmpdir == "" {
			tmpdir = "/tmp"
		}
		_ = os.Remove(filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()), name))
	})
	return name
}
