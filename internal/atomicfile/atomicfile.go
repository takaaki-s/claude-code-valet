// Package atomicfile writes files atomically: the data goes to a temp file in
// the target's own directory and is then renamed over the target, so a reader
// sees either the previous file or the complete new one — never a half-written
// mix of the two.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path atomically. It creates a temp file in path's
// directory from tmpPattern (an os.CreateTemp pattern), writes data into it,
// applies mode, and renames the result over path.
//
// tmpPattern is a required argument rather than something derived from path
// because callers scan the directory they write into, which makes the temp name
// part of their contract rather than an implementation detail. Each call site
// states the rule its own scan imposes; a helper that chose the name would
// break those protections silently.
//
// This buys atomicity, not durability: the data is not fsynced, so a machine
// crash can still lose the most recent write. Guaranteeing otherwise needs both
// the file and its parent directory synced, which costs roughly an order of
// magnitude more per write than the whole write does today — and no caller
// needs it.
func Write(path string, data []byte, mode os.FileMode, tmpPattern string) error {
	if tmpPattern == "" {
		// The empty pattern is the one misuse catchable from here: os.CreateTemp
		// accepts it and produces a bare random name with no suffix at all,
		// which no caller's sweep could match. Whether a non-empty name is safe
		// depends on the scan the caller runs, so that judgement stays there.
		return fmt.Errorf("atomicfile: empty tmpPattern for %s", path)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), tmpPattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// CreateTemp always makes the file 0600, so the mode has to be set
	// explicitly rather than left to the umask the way os.WriteFile would.
	// Going through the descriptor rather than the path keeps the file being
	// chmodded the same one that was just written.
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
