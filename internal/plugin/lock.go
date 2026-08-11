package plugin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const lockFilename = "plugins.lock.yaml"

// LockEntry records how one installed plugin was obtained: its source, the
// requested ref, the commit approved at install time, whether it is a symlink
// (linked) rather than a clone, whether the user pinned it, and when it was
// installed. Linked plugins leave Commit empty because their contents can
// change out from under the lock. Pinned=true means `plugin update` must not
// silently move the plugin off the ref the user chose (pass `-v` / `@ref` at
// install to opt in); Pinned=false follows "the plugin's latest release".
type LockEntry struct {
	Source      string    `yaml:"source"`
	Ref         string    `yaml:"ref,omitempty"`
	Commit      string    `yaml:"commit,omitempty"`
	Linked      bool      `yaml:"linked,omitempty"`
	Pinned      bool      `yaml:"pinned,omitempty"`
	InstalledAt time.Time `yaml:"installed_at"`
}

// lockFile is the on-disk YAML shape: a single top-level `plugins:` map keyed
// by plugin name. Keeping the wrapper separate from the in-memory map lets the
// file carry future top-level keys without breaking the entry decoder.
type lockFile struct {
	Plugins map[string]LockEntry `yaml:"plugins"`
}

// Lock is the in-memory view of the persisted plugins.lock.yaml. All mutations
// flush to disk under an exclusive flock so that concurrent daemon + CLI
// processes never corrupt the file.
type Lock struct {
	path    string
	mu      sync.Mutex
	entries map[string]LockEntry
}

// LoadLock reads plugins.lock.yaml from stateDir. A missing file is not an
// error — it returns an empty lock so first-run callers can use the returned
// pointer immediately.
func LoadLock(stateDir string) (*Lock, error) {
	path := filepath.Join(stateDir, lockFilename)
	l := &Lock{path: path, entries: map[string]LockEntry{}}

	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return l, nil
		}
		return nil, fmt.Errorf("read lock: %w", err)
	}
	if len(b) == 0 {
		return l, nil
	}
	var lf lockFile
	if err := yaml.Unmarshal(b, &lf); err != nil {
		return nil, fmt.Errorf("decode lock %s: %w", path, err)
	}
	if lf.Plugins != nil {
		l.entries = lf.Plugins
	}
	return l, nil
}

// Get returns the entry for name and whether it exists.
//
// The on-disk file is re-read under a shared flock on every call so that
// entries written by another process — `jin plugin install` from the CLI while
// the daemon runs — are visible without a restart. Re-read failures fall back
// to the in-memory map, so a transient I/O error cannot make previously-visible
// entries disappear.
func (l *Lock) Get(name string) (LockEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Reload errors are intentionally swallowed: if the file becomes
	// temporarily unreadable we prefer serving the last-known map over
	// treating every entry as gone. Callers get a stale-but-safe answer.
	_ = l.reloadLocked()
	entry, ok := l.entries[name]
	return entry, ok
}

// All returns a deep copy of the current entries so callers can iterate
// without holding the Lock mutex. Like Get, it re-reads from disk first so the
// daemon observes CLI-side installs.
func (l *Lock) All() map[string]LockEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.reloadLocked()
	out := make(map[string]LockEntry, len(l.entries))
	for k, v := range l.entries {
		out[k] = v
	}
	return out
}

// reloadLocked refreshes l.entries from disk under a shared flock. Called by
// Get and All before every lookup. Missing file is treated as an empty lock
// (mirrors LoadLock). Caller must hold l.mu.
func (l *Lock) reloadLocked() error {
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.entries = map[string]LockEntry{}
			return nil
		}
		return fmt.Errorf("open lock: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return fmt.Errorf("flock lock (shared): %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	b, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	fresh := map[string]LockEntry{}
	if len(b) > 0 {
		var lf lockFile
		if err := yaml.Unmarshal(b, &lf); err != nil {
			return fmt.Errorf("decode lock: %w", err)
		}
		if lf.Plugins != nil {
			fresh = lf.Plugins
		}
	}
	l.entries = fresh
	return nil
}

// Set inserts or overwrites the entry for name and persists the file.
func (l *Lock) Set(name string, e LockEntry) error {
	return l.mutate(func(entries map[string]LockEntry) {
		entries[name] = e
	})
}

// Remove deletes the entry for name if present. Missing entries are a no-op
// (not an error) — callers just want the post-condition "no entry".
func (l *Lock) Remove(name string) error {
	return l.mutate(func(entries map[string]LockEntry) {
		delete(entries, name)
	})
}

// mutate serializes read-modify-write against both intra-process goroutines
// (l.mu) and other processes (flock on the YAML file). The apply callback runs
// on a *disk-fresh* map so writes from other processes since Load are
// preserved.
func (l *Lock) mutate(apply func(map[string]LockEntry)) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("mkdir lock dir: %w", err)
	}

	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open lock: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	fresh := map[string]LockEntry{}
	b, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	if len(b) > 0 {
		var lf lockFile
		if err := yaml.Unmarshal(b, &lf); err != nil {
			return fmt.Errorf("decode lock: %w", err)
		}
		if lf.Plugins != nil {
			fresh = lf.Plugins
		}
	}

	apply(fresh)

	out, err := yaml.Marshal(lockFile{Plugins: fresh})
	if err != nil {
		return fmt.Errorf("encode lock: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek lock: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate lock: %w", err)
	}
	if _, err := f.Write(out); err != nil {
		return fmt.Errorf("write lock: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync lock: %w", err)
	}

	l.entries = fresh
	return nil
}
