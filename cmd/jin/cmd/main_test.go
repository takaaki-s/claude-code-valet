package cmd

import (
	"os"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/testutil"
)

// isolationSocket is the path TestMain pointed JIN_SOCKET at, kept so the
// assertions below compare against a value rather than against the absence of a
// file — see testutil.IsolateFromRealDaemon for why absence proves nothing.
var isolationSocket string

// TestMain cuts this package's tests off from a running jind-ai daemon. Several
// commands here — `jin hook` above all — resolve their target from the ambient
// environment, so without this a suite run from inside a managed session
// delivers its fixture payloads to that session.
func TestMain(m *testing.M) {
	isolationSocket = testutil.IsolateFromRealDaemon()
	os.Exit(m.Run())
}

// TestIsolationIsInForce checks that TestMain actually applied the isolation,
// which testutil's own tests cannot: they exercise the function, not this
// package's wiring to it. It also catches a test here that clobbers the
// environment for the ones that run after it — os.Setenv where t.Setenv was
// meant.
func TestIsolationIsInForce(t *testing.T) {
	if got, ok := os.LookupEnv("JIN_SESSION_ID"); ok {
		t.Errorf("JIN_SESSION_ID is set to %q; a hook fixture would be delivered to that session", got)
	}
	if got := getSocketPath(); got != isolationSocket {
		t.Errorf("getSocketPath() = %q, want the isolation path %q", got, isolationSocket)
	}
}
