package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// EstablishHookBinary copies the running daemon's executable to a stable,
// jin-owned path under stateDir and returns that path — the BinPath every child
// this daemon spawns is given. It is best-effort: on a failed copy it returns
// the live os.Executable() instead, which is no worse than the pre-copy
// behaviour, and "" if even that cannot be resolved.
//
// Why a copy exists at all. The path handed to a child that calls back into
// jind-ai — an agent's hook wiring, a plugin's $JIN_BIN — is read once when that
// child starts and never revisited. Baking os.Executable() directly means the
// path points at wherever the daemon happened to launch from, and neither of the
// two ways that path stops describing the running daemon is exotic:
//
//   - Rebuilding. `go build -o` over a running binary unlinks it and creates a
//     new file at the same path, so it succeeds against a live daemon and
//     afterwards /proc/<pid>/exe reads "… (deleted)" while the path holds the
//     new build. One rebuild during development is the whole setup; measured
//     3/3. Where the IPC shape had changed the child's call then failed with the
//     protocol-version message — loud, and correct. Where it had not, the call
//     succeeded against a binary nobody chose.
//   - Deleting the launch directory. A developer who runs jin out of a git
//     worktree and then removes that worktree leaves the path gone outright:
//     callbacks exit 127, measured 3/3. `"${JIN_BIN:-jin}"`, the form documented
//     for plugin authors, does not rescue it — `:-` substitutes only when a
//     variable is unset or empty, and a dead path is neither.
//
// Copying to a path under stateDir (the parent of worktrees/, never itself a
// worktree) removes that coupling.
//
// Why at startup, from the daemon's own executable. Running this once, here,
// makes the copy byte-identical to the running daemon for the daemon's whole
// lifetime: a `jin hook` child exec'd from it speaks exactly the IPC protocol
// the daemon serves, so no version skew is possible by construction. Refreshing
// the copy on every session spawn would reintroduce skew — it could pick up a
// binary rebuilt in place while the old daemon still runs, pairing a newer hook
// with an older daemon. `jin daemon restart` is the one moment the copy is
// meant to move, and it re-establishes here from the new binary.
//
// This only helps sessions started after it runs; a session already running
// holds whatever path it read at startup, so recovering a frozen one still
// needs a restart.
// It is a free function, and returns the path rather than storing it, so that
// the caller cannot build anything that names a jin binary before this has run:
// the value is the argument, not an ordering to remember.
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

// copyExecutable copies src to dst, publishing it atomically: it writes to a
// temp sibling and renames into place, so a concurrent `jin hook` never execs a
// half-written file. A rename over a binary a hook is already executing is safe
// on POSIX — the running process keeps the old inode, and new hooks pick up the
// new one — so no locking is needed against in-flight hooks.
//
// It keeps its own temp-and-rename rather than going through atomicfile.Write:
// that helper takes the payload as a []byte, while streaming through io.Copy
// keeps a whole executable out of memory.
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
	// Clean up the temp file on any error path. Deferred LIFO, so Close runs
	// before Remove; both are harmless no-ops after the explicit Close +
	// Rename below (a second Close just returns an ignored already-closed
	// error, and Remove finds the file already renamed away).
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
