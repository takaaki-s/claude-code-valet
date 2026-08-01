package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// EstablishHookBinary copies the running daemon's executable to a stable,
// jin-owned path and upgrades m.hookExecPath (defaulted to the live
// os.Executable() in NewManager) to point at that copy. It is best-effort: on
// any failure the field keeps its default, which is no worse than the pre-copy
// behaviour.
//
// Why a copy exists at all. The path handed to each agent's hook wiring —
// Claude Code's --settings file, Codex's `-c hooks.X=[...]` injection — is read
// once when the agent starts and never revisited. Baking os.Executable()
// directly means that path points at wherever the daemon happened to launch
// from. A developer who runs jin out of a git worktree, then deletes that
// worktree, leaves every already-running session's hook pointing at a path that
// now 404s: the hook fails with "not found", no status update reaches the
// daemon, and the session looks frozen in the TUI. Copying to a path under
// stateDir (the parent of worktrees/, never itself a worktree) removes that
// coupling.
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
func (m *Manager) EstablishHookBinary() {
	src, err := os.Executable()
	if err != nil {
		debugLog("[HOOKBIN] os.Executable failed, hooks use the live path: %v", err)
		return
	}
	dst := m.hookBinaryPath()
	if err := copyExecutable(src, dst); err != nil {
		debugLog("[HOOKBIN] copy %s -> %s failed, hooks use the live path: %v", src, dst, err)
		return
	}
	m.hookExecPath = dst
	debugLog("[HOOKBIN] hook binary established at %s", dst)
}

// hookBinaryPath is the stable location EstablishHookBinary copies to. It sits
// directly under stateDir, a sibling of worktrees/ and hooks-settings.json, so
// it is never inside a worktree the daemon might later remove.
func (m *Manager) hookBinaryPath() string {
	return filepath.Join(m.stateDir, "bin", "jin")
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
