package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src-bin")
	want := []byte("\x7fELF fake binary bytes")
	if err := os.WriteFile(src, want, 0o755); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "bin", "jin") // nested dir must be created
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content mismatch: got %q, want %q", got, want)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("dst is not owner-executable: mode %v", info.Mode())
	}

	// No temp files (.jin-hook-*) must survive a successful copy — the atomic
	// rename should consume the one it created.
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if name := e.Name(); name != "jin" {
			t.Errorf("leftover file in dst dir: %q", name)
		}
	}
}

func TestCopyExecutableOverwrites(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "bin", "jin")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "new-bin")
	if err := os.WriteFile(src, []byte("fresh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh" {
		t.Errorf("dst not overwritten: got %q", got)
	}
}

func TestCopyExecutableMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := copyExecutable(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "bin", "jin"))
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}

// TestEstablishHookBinary_FallsBackToTheLivePathWhenTheCopyFails pins the
// promise the rest of the wiring rests on. Every caller takes the returned path
// instead of deriving <stateDir>/bin/jin for itself precisely because this is
// best-effort: derive it and you name a file that is not there whenever the
// copy did not happen. Returning "" would be the same failure by another route
// — jinenv omits an empty BinPath, so children would silently fall back to
// whatever `jin` is on PATH.
func TestEstablishHookBinary_FallsBackToTheLivePathWhenTheCopyFails(t *testing.T) {
	stateDir := t.TempDir()
	// A file where the bin directory needs to go, so MkdirAll cannot succeed.
	if err := os.WriteFile(filepath.Join(stateDir, "bin"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := EstablishHookBinary(stateDir)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got != self {
		t.Errorf("EstablishHookBinary = %q, want the live executable %q", got, self)
	}
}

func TestEstablishHookBinary(t *testing.T) {
	stateDir := t.TempDir()

	got := EstablishHookBinary(stateDir)

	// The returned path must be the stable location under stateDir, and that
	// file must exist and be executable — this is the path baked into hook
	// wiring, so a broken one would silently freeze sessions.
	want := filepath.Join(stateDir, "bin", "jin")
	if got != want {
		t.Fatalf("EstablishHookBinary = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat established binary: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("established binary not owner-executable: mode %v", info.Mode())
	}

	// The copy must be byte-identical to the running executable, since a hook
	// exec'd from it must speak the same protocol the daemon serves.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selfBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	copyBytes, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(selfBytes, copyBytes) {
		t.Errorf("established copy is not byte-identical to the source executable")
	}
}
