package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/debug"
	"github.com/takaaki-s/jind-ai/internal/plugin"
)

// TestNewServer_AssemblesTheIdentityItsChildrenAreGiven pins the three answers
// no test in internal/session or internal/plugin can reach, because only this
// package knows what the right ones are.
//
// The hop is the one most likely to be got wrong and least likely to be
// noticed. NewServer takes four paths in a row, so a transposition compiles;
// and nothing fails when it happens — an agent handed the wrong socket runs,
// its hooks exit 0, and only the status stops moving.
//
// What each field is compared against is chosen so that a wrong answer cannot
// coincide with the right one: the socket against the Server's own record, the
// binary against the copy's location rather than any path this process could
// resolve, the flag against the real one rather than a literal.
func TestNewServer_AssemblesTheIdentityItsChildrenAreGiven(t *testing.T) {
	s, dir := newTestServerIn(t)

	if got := s.manager.Identity().SocketPath; got != s.socketPath {
		t.Errorf("children are told to call back to %q, but this daemon listens on %q", got, s.socketPath)
	}
	// The stable copy under stateDir, not os.Executable(): a child's
	// environment outlives the daemon's own executable, and
	// session.EstablishHookBinary has what goes wrong when it does not.
	wantBin := filepath.Join(dir, "state", "bin", "jin")
	if got := s.manager.Identity().BinPath; got != wantBin {
		t.Errorf("children are told to re-enter %q, want the stable copy at %q", got, wantBin)
	}
	// Compared against the live flag rather than false, so that a hardcoded
	// answer fails under `JIN_DEBUG=1 go test` instead of agreeing by accident.
	// That is the only condition it can bite in, and it is the condition that
	// matters: the symptom of getting this wrong is a log someone turned on and
	// that was never written.
	if got, want := s.manager.Identity().Debug, debug.Enabled(); got != want {
		t.Errorf("children are told JIN_DEBUG=%v, but this process has debugging %v", got, want)
	}
}

// identityProbePlugin dumps the three identity variables it was handed. Its
// entrypoint is a bash fragment, which is what the plugin runtime execs.
const identityProbePlugin = `schema_version: 1
name: identprobe
version: 0.1.0
description: dumps the identity the daemon gave it
jin: ">=0.0.0"
install:
  source:
    build: ["true"]
    entrypoint: bash -c 'echo "sock=$JIN_SOCKET bin=$JIN_BIN" >> out.txt'
on: []
`

// TestNewServer_RunsPluginsWithThatSameIdentity closes the hop the test above
// cannot see: that the value NewServer assembled is the one a plugin actually
// receives, and that it is the same one the agents get.
//
// It goes through a real run rather than reading the dispatcher back, because
// what a plugin is told is the thing that was wrong — a plugin used to be
// handed the daemon's live executable while an agent got the stable copy, and
// after a single rebuild those name different binaries (3/3). Comparing against
// the manager's identity rather than a literal is the point: the two spawn
// sites must agree, whatever the value turns out to be.
func TestNewServer_RunsPluginsWithThatSameIdentity(t *testing.T) {
	// paths.Plugins() reads XDG_DATA_HOME, and NewServer resolves it. Set it
	// first, or this installs into the real plugin directory.
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)

	s, dir := newTestServerIn(t)

	pluginDir := filepath.Join(dataDir, "jind-ai", "plugins", "identprobe")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "jind-ai-plugin.yaml"),
		[]byte(identityProbePlugin), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := plugin.LoadLock(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Set("identprobe", plugin.LockEntry{Source: "test", InstalledAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := s.pluginDisp.RunAction("identprobe", "", plugin.Event{Name: "action"}, 0, plugin.ActionContext{}); err != nil {
		t.Fatalf("RunAction: %v", err)
	}

	out := filepath.Join(pluginDir, "out.txt")
	deadline := time.Now().Add(5 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil {
			data = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if data == nil {
		t.Fatal("plugin did not run")
	}

	id := s.manager.Identity()
	want := "sock=" + id.SocketPath + " bin=" + id.BinPath
	if got := strings.TrimSpace(string(data)); got != want {
		t.Errorf("plugin saw %q, but the agents are told %q", got, want)
	}
}
