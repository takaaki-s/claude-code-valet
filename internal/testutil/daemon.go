package testutil

import (
	"os"
	"path/filepath"
)

// isolationSocketDir names a directory nothing creates, so a socket path built
// under it cannot resolve to a listening daemon. Named after the reason, so a
// stray connection attempt is self-explanatory in an error string.
const isolationSocketDir = "jin-tests-must-not-reach-a-daemon"

// IsolateFromRealDaemon cuts the calling test binary off from a running jind-ai
// daemon, and returns the socket path it installed so a caller can assert by
// value. Call it from TestMain.
//
// jin's commands resolve their target from the ambient environment rather than
// from arguments: JIN_SESSION_ID names the session to act on, and JIN_SOCKET
// names the daemon to reach. A `go test` process inherits both from whoever
// started it, so a suite run from inside a jind-ai-managed session delivers its
// fixture payloads to that very session — `jin hook` most sharply, since it
// forwards a payload read from stdin, and a SessionEnd fixture drives the
// running session to "stopped" and rewrites its recorded agent session id.
//
// The failure is invisible from inside the suite: every assertion still passes,
// because the damage lands on a process no test looks at. That is why this
// belongs in TestMain rather than in the tests that happen to need it today —
// a test added later would reintroduce it with nothing to catch the mistake.
//
// Asserting the ambient environment is NOT a substitute. On a clean machine
// JIN_SESSION_ID is already unset and no socket exists, so such a check passes
// whether this ran or not: green on CI, red only where the damage had already
// been noticed. Compare against the returned path instead — and note that
// clearing JIN_SOCKET rather than redirecting it is not equivalent either,
// because the resolver then falls back to the real default.
func IsolateFromRealDaemon() (socketPath string) {
	os.Unsetenv("JIN_SESSION_ID")
	socketPath = filepath.Join(os.TempDir(), isolationSocketDir, "daemon.sock")
	os.Setenv("JIN_SOCKET", socketPath)
	return socketPath
}
