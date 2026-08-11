package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// EstablishHookBinary copies the running daemon's executable to a stable,
// jin-owned path under stateDir and returns that path — the BinPath every child
// this daemon spawns is given. It is best-effort: a failed copy returns the live
// os.Executable() instead, and "" if even that cannot be resolved.
//
// The path a child is given is read once when that child starts and never
// revisited, so naming os.Executable() couples it to wherever the daemon
// launched from. Two ordinary developer actions break that:
//
//   - `go build -o` over a running binary unlinks it and creates a new file at
//     the same path, so it succeeds against a live daemon; /proc/<pid>/exe then
//     reads "… (deleted)" while the path holds the new build. Where the IPC
//     shape had changed the child failed loudly, which is correct. Where it had
//     not, the call succeeded against a binary nobody chose.
//   - Removing the git worktree jin was run from leaves the path gone outright
//     and callbacks exit 127. `"${JIN_BIN:-jin}"` does not rescue it: `:-`
//     substitutes only on unset or empty, and a dead path is neither.
//
// Running once at startup, from the daemon's own executable, makes the copy
// byte-identical to the daemon for its whole lifetime, so a `jin hook` exec'd
// from it cannot be skewed against the daemon it calls. Refreshing per spawn
// would reintroduce exactly that. `jin daemon restart` is the one moment the
// copy is meant to move.
//
// Only sessions started after it runs benefit; a running session holds the path
// it read at startup, so recovering a frozen one still needs a restart.
func EstablishHookBinary(stateDir string) string {
	src, err := os.Executable()
	if err != nil {
		debugLog("[HOOKBIN] os.Executable failed, children get no JIN_BIN: %v", err)
		return ""
	}
	dst := hookBinaryPath(stateDir)
	if err := copyExecutable(src, dst); err != nil {
		debugLog("[HOOKBIN] copy %s -> %s failed, children get the live path: %v", src, dst, err)
		return src
	}
	debugLog("[HOOKBIN] hook binary established at %s", dst)
	return dst
}

// hookBinaryPath is the stable location EstablishHookBinary copies to. It sits
// directly under stateDir, a sibling of worktrees/ and hooks-settings.json, so
// it is never inside a worktree the daemon might later remove.
func hookBinaryPath(stateDir string) string {
	return filepath.Join(stateDir, "bin", "jin")
}

// copyExecutable copies src to dst atomically: it writes a temp sibling and
// renames into place, so a concurrent `jin hook` never execs a half-written
// file. A rename over a binary a hook is already executing is safe on POSIX —
// the running process keeps the old inode — so no locking is needed.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source executable: %w", err)
	}
	defer in.Close()

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create hook binary dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".jin-hook-*")
	if err != nil {
		return fmt.Errorf("create temp hook binary: %w", err)
	}
	tmpName := tmp.Name()
	// Cleans up on any error path. A harmless no-op once the explicit Close +
	// Rename below have run.
	defer os.Remove(tmpName)
	defer tmp.Close()

	if _, err := io.Copy(tmp, in); err != nil {
		return fmt.Errorf("copy hook binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("chmod hook binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp hook binary: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("publish hook binary: %w", err)
	}
	return nil
}
