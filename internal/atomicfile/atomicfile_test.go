package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testTmpPattern = "target-*.tmp"

// assertNoStrays fails unless dir holds nothing but the file at path. The tests
// that need to prove no temp file survived use it, on the success path and the
// failure paths alike.
func assertNoStrays(t *testing.T, dir, path string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	for _, e := range entries {
		if name := filepath.Join(dir, e.Name()); name != path {
			t.Errorf("stray left behind: %s", e.Name())
		}
	}
}

// TestWrite_CreatesFileWithMode covers the mode being applied explicitly:
// os.CreateTemp always makes the file 0600, so a caller asking for anything
// else only gets it because Write chmods. Only the 0644 case can show that —
// 0600 matches CreateTemp's default and would pass with no chmod at all — but
// it is worth keeping as the other half of "the mode argument is honoured".
func TestWrite_CreatesFileWithMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "target.json")
			want := []byte("hello")

			if err := Write(path, want, mode, testTmpPattern); err != nil {
				t.Fatalf("Write: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("content = %q, want %q", got, want)
			}

			fi, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if fi.Mode().Perm() != mode {
				t.Errorf("mode = %v, want %v", fi.Mode().Perm(), mode)
			}
		})
	}
}

// TestWrite_OverwritesExisting checks the rename replaces the old file whole:
// a shorter second write must not leave a tail of the first behind.
func TestWrite_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")

	if err := Write(path, []byte("first-and-longer"), 0o644, testTmpPattern); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := Write(path, []byte("second"), 0o644, testTmpPattern); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}
	assertNoStrays(t, dir, path)
}

// TestWrite_LeavesNoTempFile pins half of the contract callers depend on: the
// temp file is gone once Write returns. That the name came from tmpPattern is
// pinned by TestWrite_MissingDir, whose error carries it.
func TestWrite_LeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")

	if err := Write(path, []byte("x"), 0o644, ".jin-probe-*.tmp"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	assertNoStrays(t, dir, path)
}

// TestWrite_RejectsEmptyPattern covers the guard: os.CreateTemp would accept an
// empty pattern and produce a bare random name, which no caller's stray sweep
// would ever match.
func TestWrite_RejectsEmptyPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")

	err := Write(path, []byte("x"), 0o644, "")
	if err == nil {
		t.Fatal("Write with an empty tmpPattern succeeded, want error")
	}
	assertNoStrays(t, dir, path)
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("target was written despite the guard")
	}
}

// TestWrite_RemovesTempWhenRenameFails covers the deferred cleanup, which is
// the only thing standing between a failed write and a stray nobody reclaims.
// The failure has to happen *after* the temp file exists, so an unwritable
// directory is no good — CreateTemp fails there before anything is created.
// Renaming onto an existing directory fails at exactly the right moment.
func TestWrite_RemovesTempWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := Write(path, []byte("x"), 0o644, testTmpPattern); err == nil {
		t.Fatal("Write renaming onto a directory succeeded, want error")
	}

	assertNoStrays(t, dir, path)
}

// TestWrite_MissingDir covers a target whose directory does not exist: an
// error, not a panic.
func TestWrite_MissingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope", "target.json")

	err := Write(path, []byte("x"), 0o644, testTmpPattern)
	if err == nil {
		t.Fatal("Write into a missing dir succeeded, want error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v, want it to name the missing directory", err)
	}
	// CreateTemp's error carries the name it tried, so this is direct evidence
	// that tmpPattern shaped the temp file rather than being ignored.
	if !strings.Contains(err.Error(), "target-") {
		t.Errorf("error = %v, want the temp name built from tmpPattern", err)
	}
}

// TestWrite_EmptyData covers the boundary: nil and empty both produce an empty
// file rather than no file at all.
func TestWrite_EmptyData(t *testing.T) {
	for name, data := range map[string][]byte{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "target.json")

			if err := Write(path, data, 0o644, testTmpPattern); err != nil {
				t.Fatalf("Write: %v", err)
			}

			fi, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if fi.Size() != 0 {
				t.Errorf("size = %d, want 0", fi.Size())
			}
		})
	}
}
