package debug

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envRead is the expression this package exists to replace. Writing it plainly
// is safe: the walk skips this package, which is where the sanctioned read
// lives.
const envRead = `os.Getenv("JIN_DEBUG")`

// moduleRoot walks up from the working directory to the directory holding
// go.mod, so the check covers the whole module however the test is invoked.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// TestNoPackageReadsTheDebugEnvDirectly keeps "is debugging on" a single
// decision.
//
// It is a test rather than a linter because the linter cannot express it:
// forbidigo matches the function expression only, so the finest rule available
// is `^os\.Getenv$`, which would forbid every environment read in the project.
// A pattern carrying the argument was tried and matched nothing — and a rule
// that matches nothing reads exactly like a tree with no violations.
//
// The rule matters because the two readings genuinely differ. This package
// decides once at startup; a direct read decides per call, so a process that
// changed the variable mid-run would have parts of itself disagreeing about
// whether they were recording. The previous convention actively told new
// packages to make their own copy, and two of them did.
func TestNoPackageReadsTheDebugEnvDirectly(t *testing.T) {
	root := moduleRoot(t)
	// This package is where the sanctioned read lives.
	allowed := filepath.Join(root, "internal", "debug")

	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasPrefix(path, allowed+string(os.PathSeparator)) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), envRead) {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, f := range found {
		t.Errorf("%s reads %s directly; call debug.Enabled() so the answer is decided once", f, envRead)
	}
}
