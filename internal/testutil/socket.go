package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// SocketPath returns a path for a Unix domain socket in a fresh temp directory,
// removed at the end of the test.
//
// Not t.TempDir(): that derives the directory name from the test name, and a
// Unix socket path is capped at ~108 bytes (sun_path). A long test or subtest
// name pushes the socket over the limit and listen fails with the unhelpful
// "bind: invalid argument". Go 1.26 started truncating the pattern, so the
// same test can pass locally on a newer toolchain and fail on the version in
// go.mod — this keeps the path short on every version instead.
func SocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jin")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}
