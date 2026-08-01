package session

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/takaaki-s/jind-ai/internal/atomicfile"
)

// Store handles session persistence
type Store struct {
	dataDir string
}

// tmpSuffixPattern is appended to a session id to form the os.CreateTemp
// pattern Save hands to atomicfile.Write. The trailing ".tmp" keeps LoadAll
// from picking the file up mid-write, since LoadAll only considers a ".json"
// extension, and cleanupTempFiles globs this same suffix to reclaim strays.
// The pattern is therefore a contract between three places, not a detail of
// Save — which is why atomicfile.Write takes it rather than choosing a name.
const tmpSuffixPattern = ".json.*.tmp"

// sessionFileMode is the permission new session files are created with.
// Session records live under XDG state and are per-user data, so they are not
// world-readable.
const sessionFileMode os.FileMode = 0600

// atomicWrite is the write Save publishes through. It is a variable so tests
// can capture the temp pattern Save builds: that pattern is a contract with
// LoadAll and cleanupTempFiles, and now that it crosses a package boundary as
// an argument, nothing else would catch it drifting.
//
// Extracting the pattern into a helper and testing that instead would not do
// the same job — it would pin what the helper returns, not what Save passes,
// leaving Save free to hand over something else entirely.
var atomicWrite = atomicfile.Write

// NewStore creates a new store
func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	s := &Store{dataDir: dataDir}
	s.cleanupTempFiles()
	return s, nil
}

// cleanupTempFiles removes temp files stranded by a Save that was interrupted
// between CreateTemp and Rename (daemon killed, power loss). They are inert —
// LoadAll ignores them — but nothing else would ever reclaim them.
//
// Only safe to call at construction: it would delete the in-flight temp file of
// a Save running concurrently in another process. The daemon socket keeps a
// single daemon per state dir, so that does not arise in practice.
func (s *Store) cleanupTempFiles() {
	matches, err := filepath.Glob(filepath.Join(s.dataDir, "*"+tmpSuffixPattern))
	if err != nil {
		return
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			debugLog("[STORE] stale temp file %s: %v", m, err)
		}
	}
}

// Save persists a session.
//
// The write is atomic (see atomicfile.Write). Several goroutines reach Save
// without holding a shared lock, so a plain os.WriteFile could interleave two
// truncate/write pairs and leave a half-written record — which LoadAll then
// skips, making the session disappear. The rename buys atomicity, not
// durability: a machine crash can still lose the most recent save.
//
// Save takes session by value: it marshals every field, so a caller reading a
// live *Session outside a lock would race with concurrent mutators. Taking
// the parameter by value forces that copy to happen at the call site. A
// caller that unlocks before calling Save must take the copy first — see
// Manager.snapshotAndUnlock and its callers for the pattern. A caller that
// holds its lock for Save's whole duration (e.g. startSessionTmux, which runs
// under StartBackground's lock) has no such window and may just dereference.
func (s *Store) Save(session Session) error {
	path := filepath.Join(s.dataDir, session.ID+".json")
	data, err := json.MarshalIndent(&session, "", "  ")
	if err != nil {
		return err
	}

	// Preserve the mode of an existing record so a user who tightened (or
	// loosened) it does not have it reset on every save.
	mode := sessionFileMode
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}

	return atomicWrite(path, data, mode, session.ID+tmpSuffixPattern)
}

// Load loads a session by ID. Legacy schema (top-level "name") is migrated
// in-place to the current schema; the migrated JSON is written back to disk
// so we only pay the cost once per session file.
func (s *Store) Load(id string) (*Session, error) {
	path := filepath.Join(s.dataDir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	migrated, changed, err := migrateSessionJSON(data)
	if err != nil {
		return nil, err
	}

	var session Session
	if err := json.Unmarshal(migrated, &session); err != nil {
		return nil, err
	}

	if changed {
		if err := s.Save(session); err != nil {
			return nil, err
		}
	}
	return &session, nil
}

// LoadAll loads all sessions.
//
// Files that fail Load (unparseable JSON, migration write-back failure, missing
// permissions, ...) are skipped instead of aborting so a single corrupt file
// doesn't strand every session. The individual failure is emitted via
// debugLog so it still surfaces under JIN_DEBUG=1.
func (s *Store) LoadAll() ([]*Session, error) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []*Session
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5] // Remove .json
		session, err := s.Load(id)
		if err != nil {
			debugLog("[LOAD] skip %s: %v", id, err)
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// Delete removes a session file
func (s *Store) Delete(id string) error {
	path := filepath.Join(s.dataDir, id+".json")
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
