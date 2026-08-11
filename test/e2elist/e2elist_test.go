// Package e2elist holds one check: that the Makefile's e2e package list names
// every package that has an e2e-tagged test.
//
// It exists because that list was wrong. A new e2e test was added to a package
// the list did not name, and it ran nowhere — `go test ./...` does not build
// e2e files and the e2e CI job runs the list, so the file was green locally
// under `go test -tags e2e ./...` and executed zero times in CI. The Makefile
// comment calls itself the sole owner of the list, and docs/gotchas.md tells
// readers the list only ever has to be edited there; neither statement was
// enforced by anything.
//
// This test carries no build tag on purpose: it has to run in the unit job,
// which is the one that runs when the list is wrong.
//
// Neither half of the question is answered here. What the target runs comes
// from `make -n test-e2e`, and which files carry the tag comes from go/build.
// Both were hand-rolled first, and both were wrong in the same direction: the
// constraint reader missed two spellings the toolchain builds, and the Makefile
// reader was fooled by an `@echo` of the command and by variables it did not
// expand. A guard that approximates its own inputs fails by letting things
// through, which is the failure it was written to prevent.
//
// What it still approximates is the `go test` command line — which tokens are
// packages and which are flag values. valueFlags below is the whole of that
// judgement, and its doc is where the rule for membership lives.
//
// The check is one-directional on purpose. A package listed without e2e tests
// is harmless — its ordinary tests simply run twice — while one omitted is a
// test suite nobody runs.
package e2elist

import (
	"errors"
	"go/build"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// e2eTag is the tag the CI job passes.
const e2eTag = "e2e"

// isE2EFile reports whether the toolchain builds dir/name with -tags e2e and
// not without it. Both halves matter: an untagged file is built either way and
// is not what the e2e list is for.
//
// go/build implements the same constraint rules the go command applies (cmd/go
// carries its own maintained copy rather than calling this package, so "same
// rules" is the claim, not "same code"). It resolves against this host's
// GOOS/GOARCH, which is what the check wants: the question is which tests the
// job on this platform would run.
func isE2EFile(dir, name string) bool {
	tagged := build.Default
	tagged.BuildTags = []string{e2eTag}
	withTag, err := tagged.MatchFile(dir, name)
	if err != nil || !withTag {
		return false
	}
	// build.Default carries no tags today; assigning nil states that rather
	// than relying on it.
	plain := build.Default
	plain.BuildTags = nil
	withoutTag, err := plain.MatchFile(dir, name)
	return err == nil && !withoutTag
}

// skipDir reports whether a directory is outside the source the go tool would
// build: version control and this repo's gitignored working directories, the
// vendored trees, and the names the tool itself ignores.
func skipDir(name string) bool {
	switch name {
	case "vendor", "node_modules", "bin", "testdata":
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// e2ePackagesIn returns every package directory under root holding an e2e-only
// test file, as `./path` — the form goTestPackages normalizes arguments to.
func e2ePackagesIn(root string) (map[string]bool, error) {
	out := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is skipped rather than fatal:
			// this runs on developer machines as well as in CI, and a local
			// scratch directory is not evidence about the package list. A file
			// that cannot be read must not take its siblings with it, which is
			// why the two cases are separated.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		if !isE2EFile(filepath.Dir(path), d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		if rel == "." {
			out["./"] = true
			return nil
		}
		out["./"+filepath.ToSlash(rel)] = true
		return nil
	})
	return out, err
}

// valueFlags are the `go test` flags that take their value as a separate
// argument. Membership is decided by that alone, not by whether the value looks
// like a path: `-o ./internal/tui` and `-run ./...` both consume the token after
// them, and a token consumed here is one that cannot be mistaken for a package.
// Counting one would report coverage the list does not have, which is the
// direction that lets an omission through.
//
// Boolean flags are deliberately absent: listing one would make the package
// after it disappear from the count. `-buildvcs` was here and is not, because
// `go test -buildvcs ./pkg` runs ./pkg — measured, not assumed.
//
// The set covers the flags a `go test` recipe plausibly carries, not every one
// the toolchain accepts — the profile-rate flags (`-memprofilerate`,
// `-blockprofilerate`, `-mutexprofilefraction`) are absent because a Makefile
// running the e2e suite has no reason to name them, and a missing one only
// matters if its value looks like a package.
//
// Membership comes from `go help build`, `go help testflag` and `go help test`,
// which say which flags take a value. All three are needed: `-exec` appears
// only in the last, and dropping it would count `-exec ./wrapper` as a package
// — the shape that reads as coverage the list does not have. (`-o` is in `go
// help build`'s usage line, checked after this comment said otherwise.)
// Running the flag and watching what happens does not answer it: `go test
// -zzznotaflag ./pkg -list .` consumes ./pkg exactly as `go test -o ./pkg
// -list .` does, because an unrecognised flag is passed to the test binary
// along with the argument after it — measured, and the reason an earlier
// version of this comment prescribed a probe that could not tell the two apart.
var valueFlags = map[string]bool{
	"-tags": true, "-run": true, "-skip": true, "-bench": true, "-benchtime": true,
	"-timeout": true, "-count": true, "-parallel": true, "-cpu": true,
	"-o": true, "-exec": true, "-overlay": true, "-coverpkg": true,
	"-covermode": true, "-coverprofile": true, "-outputdir": true,
	"-cpuprofile": true, "-memprofile": true, "-blockprofile": true,
	"-mutexprofile": true, "-trace": true, "-gcflags": true, "-ldflags": true,
	"-asmflags": true, "-gccgoflags": true, "-p": true, "-mod": true,
	"-buildmode": true, "-pkgdir": true, "-toolexec": true, "-installsuffix": true,
	"-fuzz": true, "-fuzztime": true, "-fuzzminimizetime": true, "-shuffle": true,
	"-C": true, "-modfile": true, "-pgo": true, "-vet": true,
	"-list": true, "-compiler": true,
}

// unquote strips the shell quoting `make -n` prints verbatim. The recipe is a
// shell command line, and `-tags "e2e"` is an ordinary way to write one.
func unquote(tok string) string { return strings.Trim(tok, `"'`) }

// endsCommand reports whether a token ends the `go test` invocation: a shell
// comment starts one, and everything after -args belongs to the test binary.
// Named because both scans below have to stop at the same words — a -tags past
// either is not the build's, and a package past either is a note rather than a
// run.
func endsCommand(tok string) bool {
	return tok == "-args" || strings.HasPrefix(tok, "#")
}

// goTestPackages returns the package patterns the given commands pass to
// `go test` with the e2e tag, normalized so that `./a` and `./a/` are one key.
//
// The commands are expected to be what make resolved, one per line. Selecting
// on the command word rather than on "a line mentioning go test" is what keeps
// an `echo` of the command out. Flag values and redirection targets are dropped
// for the same reason as each other: they are paths, not packages.
func goTestPackages(commands []string) map[string]bool {
	out := map[string]bool{}
	for _, cmd := range commands {
		fields := strings.Fields(cmd)
		if len(fields) < 2 {
			continue
		}
		// make prints the recipe with its @ / - / + prefixes stripped, but a
		// hand-written line may still carry them.
		fields[0] = strings.TrimLeft(fields[0], "@-+")
		// A shell command may be preceded by environment assignments, which are
		// a normal way to write a recipe and say nothing about the packages.
		for len(fields) > 2 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
			fields = fields[1:]
		}
		if fields[0] != "go" || fields[1] != "test" {
			continue
		}
		if !hasE2ETag(fields) {
			continue
		}
		skipNext := false
		for _, raw := range fields[2:] {
			if skipNext {
				skipNext = false
				continue
			}
			// `... ./a # ./b` runs only ./a, and reading the note after it as
			// coverage would hide exactly the omission this check is for.
			if endsCommand(raw) {
				break
			}
			tok := unquote(raw)
			if valueFlags[tok] || tok == ">" || tok == ">>" || tok == "2>" || tok == "2>>" {
				skipNext = true
				continue
			}
			if strings.HasPrefix(tok, "./") {
				out[strings.TrimSuffix(tok, "/")] = true
			}
		}
	}
	if out["."] {
		out["./"] = true
		delete(out, ".")
	}
	return out
}

// covers reports whether the listed patterns run pkg, honouring go's `...`
// wildcard. `./...` is the case worth naming — it is the most obvious permanent
// answer to the problem this check exists for, and reporting every package as
// unrun against it would make the guard block its own best fix — but the same
// reasoning applies to `./internal/...`, so the suffix is handled rather than
// the one literal.
func covers(listed map[string]bool, pkg string) bool {
	if listed[pkg] {
		return true
	}
	for pattern := range listed {
		prefix, ok := strings.CutSuffix(pattern, "/...")
		if !ok {
			continue
		}
		if prefix == "." || pkg == prefix || strings.HasPrefix(pkg, prefix+"/") {
			return true
		}
	}
	return false
}

// hasE2ETag reports whether the command's -tags value includes e2e. The value
// is a list, so a substring test would also accept `-tags e2etools`.
func hasE2ETag(fields []string) bool {
	found := false
	for i, raw := range fields {
		// A -tags the build never sees cannot supply the tag.
		if endsCommand(raw) {
			break
		}
		f := unquote(raw)
		var value string
		switch {
		case f == "-tags" && i+1 < len(fields):
			value = unquote(fields[i+1])
		case strings.HasPrefix(f, "-tags="):
			value = unquote(strings.TrimPrefix(f, "-tags="))
		default:
			continue
		}
		// go keeps the last -tags and drops the earlier ones, so this does too;
		// returning on the first match would disagree with the build. The
		// separator is a comma. go also honours a deprecated space-separated
		// form, which cannot survive splitting the command line on whitespace —
		// that reads as no e2e tag, and the check then reports the recipe as
		// naming no packages rather than passing quietly.
		found = false
		for _, tag := range strings.Split(value, ",") {
			if strings.TrimSpace(tag) == e2eTag {
				found = true
			}
		}
	}
	return found
}

// makeDryRun returns the commands `make -n <target>` says it would run.
//
// make resolves variable references, conditionals, makefile comments and the
// `@` / `-` recipe prefixes, which is most of what the earlier hand-written
// reader got wrong. It does not join a recipe's backslash continuations — they
// are passed through to the shell — so that is done here, and it is the reason
// this returns a slice rather than the raw output.
//
// Not a complete dry run: a recipe line prefixed with `+` is executed even
// under -n. This target has no such line, and a guard that ran the e2e suite to
// check the e2e list would be its own kind of mistake, so it is worth knowing.
//
// The child gets a stripped environment rather than a counter-flag because -w
// is the only such state a flag can cancel, and even that is not portable:
// --no-print-directory loses to a makefile's own `MAKEFLAGS += -w` on the CI
// runner's make and wins on 4.4.1. A makefile that raises -w itself is out of
// scope; nothing this reads does that.
func makeDryRun(t *testing.T, dir, target string) []string {
	t.Helper()
	requireMake(t)
	cmd := exec.Command("make", "-n", target)
	cmd.Dir = dir
	cmd.Env = withoutMakeState(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("make -n %s: %v: %s", target, err, ee.Stderr)
		}
		t.Fatalf("make -n %s: %v", target, err)
	}
	joined := strings.ReplaceAll(string(out), "\\\n", " ")
	return strings.Split(strings.TrimSpace(joined), "\n")
}

// requireMake skips when make is not installed. Nothing here tests that path,
// so it is checked by hand: docs/gotchas.md promises this package skips there.
func requireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make not available: %v", err)
	}
}

// makeStateEnv is the state one make passes to the next. Each name has a case
// in TestMakeDryRunJoinsContinuations that fails if it is dropped; the list is
// the doors somebody has walked through, not a proof that no other exists.
var makeStateEnv = map[string]bool{
	"MAKEFLAGS":    true,
	"GNUMAKEFLAGS": true,
	"MAKELEVEL":    true,
}

// withoutMakeState returns env without the variables above, so that a child
// make starts as if no make had started this process.
func withoutMakeState(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		if key, _, ok := strings.Cut(kv, "="); ok && makeStateEnv[key] {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// repoRoot walks up from this package to the directory holding the Makefile.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no Makefile found above the test's working directory")
		}
		dir = parent
	}
}

func TestMakefileNamesEveryE2EPackage(t *testing.T) {
	root := repoRoot(t)
	commands := makeDryRun(t, root, "test-e2e")
	listed := goTestPackages(commands)
	if len(listed) == 0 {
		t.Fatalf("make -n test-e2e names no packages for `go test -tags %s`; the e2e CI job runs that target:\n  %s",
			e2eTag, strings.Join(commands, "\n  "))
	}

	found, err := e2ePackagesIn(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("no e2e-tagged test files found; this check would pass vacuously")
	}
	for pkg := range found {
		if !covers(listed, pkg) {
			t.Errorf("%s has e2e-tagged tests, and no `go test -tags %s` command below names it, "+
				"so CI never runs them — unless this check failed to read the recipe:\n  %s",
				pkg, e2eTag, strings.Join(commands, "\n  "))
		}
	}
}

// TestIsE2EFile covers the spellings the toolchain accepts, including the two a
// text match missed. Each case is a file on disk because that is what go/build
// reads.
func TestIsE2EFile(t *testing.T) {
	// Assembled only so the table below reads as data rather than as this
	// file's own header; go/build stops at the package clause, so a directive
	// down here could not become a constraint either way.
	d := "//go:" + "build"
	legacy := "// +" + "build"
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{"plain", d + " e2e\n\npackage x\n", true},
		{"tab separated", d + "\te2e\n\npackage x\n", true},
		{"legacy directive", legacy + " e2e\n\npackage x\n", true},
		{"tag second", d + " linux && e2e\n\npackage x\n", true},
		{"no spaces", d + " linux&&e2e\n\npackage x\n", true},
		{"parenthesised", d + " (linux || darwin) && e2e\n\npackage x\n", true},
		{"negated", d + " !e2e\n\npackage x\n", false},
		{"another tag", d + " integration\n\npackage x\n", false},
		{"substring of another tag", d + " e2etools\n\npackage x\n", false},
		{"no constraint", "package x\n", false},
		{"mentioned after the package clause", "package x\n\nvar s = \"" + d + " e2e\"\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			name := "probe_test.go"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(tt.src), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := isE2EFile(dir, name); got != tt.want {
				t.Errorf("isE2EFile = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGoTestPackages(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
		want     map[string]bool
	}{
		{"one command", []string{"go test -tags e2e ./a/ ./b/c/"}, map[string]bool{"./a": true, "./b/c": true}},
		{"no trailing slash", []string{"go test -tags e2e ./a ./b/c"}, map[string]bool{"./a": true, "./b/c": true}},
		{"equals form", []string{"go test -tags=e2e ./a"}, map[string]bool{"./a": true}},
		{"comma-separated tags", []string{"go test -tags=integration,e2e ./a"}, map[string]bool{"./a": true}},
		{"two commands", []string{"go test -tags e2e ./a", "go test -tags e2e ./b"}, map[string]bool{"./a": true, "./b": true}},
		{"module root", []string{"go test -tags e2e ./"}, map[string]bool{"./": true}},
		// An echo of the command is the natural thing to write beside a list one
		// is asked to keep in sync, and it names packages nothing runs.
		{"echo is not a run", []string{"echo keep in sync -- go test -tags e2e ./a"}, map[string]bool{}},
		{"@echo is not a run", []string{"@echo go test -tags e2e ./a"}, map[string]bool{}},
		{"untagged run", []string{"go test ./a"}, map[string]bool{}},
		{"a tag that merely starts with e2e", []string{"go test -tags e2etools ./a"}, map[string]bool{}},
		{"redirection target is not a package", []string{"go test -tags e2e ./a > ./out.log"}, map[string]bool{"./a": true}},
		{"prefixed recipe", []string{"@go test -tags e2e ./a"}, map[string]bool{"./a": true}},
		{"env assignment prefix", []string{"GOFLAGS= CGO_ENABLED=0 go test -tags e2e ./a"}, map[string]bool{"./a": true}},
		{"another tool", []string{"golangci-lint run -tags e2e ./a"}, map[string]bool{}},
		// Each half of "the command is `go test`" needs an input that only it
		// rejects, or one of them is never the reason anything is dropped.
		{"go but not test", []string{"go vet -tags e2e ./a"}, map[string]bool{}},
		{"test but not go", []string{"docker test -tags e2e ./a"}, map[string]bool{}},
		{"flag value that looks like a package", []string{"go test -tags e2e -o ./b ./a"}, map[string]bool{"./a": true}},
		{"coverpkg is not a run target", []string{"go test -tags e2e -coverpkg ./b ./a"}, map[string]bool{"./a": true}},
		{"args after the separator", []string{"go test -tags e2e ./a -args ./b"}, map[string]bool{"./a": true}},
		{"a path that is not a package prefix", []string{"go test -tags e2e .x/a"}, map[string]bool{}},
		// The whole module covers everything, including packages added later.
		{"whole module", []string{"go test -tags e2e ./..."}, map[string]bool{"./...": true}},
		// The shortcut must come from a package argument, not from any `./...`
		// on the line: `-run ./...` names a test pattern.
		{"a run pattern is not the module", []string{"go test -tags e2e -run ./... ./a"}, map[string]bool{"./a": true}},
		// A -tags after -args belongs to the test binary, not the build.
		{"tags after the separator", []string{"go test ./a -args -tags e2e"}, map[string]bool{}},
		// A shell comment ends the command; a package named after one is a note.
		{"a trailing shell comment is not a run", []string{"go test -tags e2e ./a # ./b"}, map[string]bool{"./a": true}},
		{"a comment cannot supply the tag", []string{"go test ./a # -tags e2e"}, map[string]bool{}},
		// make -n prints the recipe verbatim, quoting included.
		{"quoted tag", []string{`go test -tags "e2e" ./a`}, map[string]bool{"./a": true}},
		{"quoted equals form", []string{`go test -tags="e2e" ./a`}, map[string]bool{"./a": true}},
		// The quoting reaches package arguments too, and a quoted one that went
		// uncounted would read as an omission the list does not have.
		{"quoted package", []string{`go test -tags e2e "./a"`}, map[string]bool{"./a": true}},
		{"single-quoted package", []string{"go test -tags e2e './a'"}, map[string]bool{"./a": true}},
		// A quoted value containing a space is not handled: splitting the line on
		// whitespace has already broken it in two. That form is deprecated, and
		// the failure is loud — the recipe reads as naming no packages — so a
		// shell lexer would be more approximation than it buys.
		//
		// go keeps the last -tags; so must this, or the two disagree about what
		// the build does.
		{"a later -tags replaces the earlier one", []string{"go test -tags e2e -tags other ./a"}, map[string]bool{}},
		{"a later -tags can supply it", []string{"go test -tags other -tags e2e ./a"}, map[string]bool{"./a": true}},
		// Flags that take their value as a separate argument, and one that does
		// not — the distinction valueFlags is built on.
		// go rejects -C anywhere but first; the check reads it wherever it
		// appears, which errs toward not counting a package that is not one.
		{"-C is not a package", []string{"go test -C ./b -tags e2e ./a"}, map[string]bool{"./a": true}},
		{"-pgo is not a package", []string{"go test -tags e2e -pgo ./b ./a"}, map[string]bool{"./a": true}},
		{"-list is not a package", []string{"go test -tags e2e -list ./b ./a"}, map[string]bool{"./a": true}},
		{"-compiler is not a package", []string{"go test -tags e2e -compiler ./b ./a"}, map[string]bool{"./a": true}},
		{"a boolean flag does not eat its package", []string{"go test -tags e2e -buildvcs ./a"}, map[string]bool{"./a": true}},
		{"an equals-form flag value is not a package", []string{"go test -tags e2e -o=./b ./a"}, map[string]bool{"./a": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := goTestPackages(tt.commands); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("goTestPackages(%q) = %v, want %v", tt.commands, got, tt.want)
			}
		})
	}
}

// TestCoversTreatsTheWholeModuleAsEverything is the assertion behind the ./...
// shortcut. Without it the shortcut and its absence are indistinguishable —
// `allPackages` is the literal `./...`, so the map a test compares against is
// the same either way, and the branch that keeps this guard from blocking the
// most obvious permanent fix would be free to disappear.
func TestCoversTreatsTheWholeModuleAsEverything(t *testing.T) {
	whole := goTestPackages([]string{"go test -tags e2e ./..."})
	if !covers(whole, "./never/listed/anywhere") {
		t.Error("a recipe running ./... does not cover an arbitrary package")
	}
	// The same reasoning as ./... applies one level down.
	subtree := goTestPackages([]string{"go test -tags e2e ./internal/..."})
	if !covers(subtree, "./internal/tui") {
		t.Error("a recipe running ./internal/... does not cover ./internal/tui")
	}
	if covers(subtree, "./cmd/jin/cmd") {
		t.Error("a recipe running ./internal/... covers a package outside it")
	}
	if !covers(subtree, "./internal") {
		t.Error("a recipe running ./internal/... does not cover ./internal itself")
	}

	if covers(subtree, "./internalx") {
		t.Error("a recipe running ./internal/... covers ./internalx; the separator is not being required")
	}

	named := goTestPackages([]string{"go test -tags e2e ./a"})
	if covers(named, "./never/listed/anywhere") {
		t.Error("a recipe naming ./a covers a package it does not name")
	}
	if !covers(named, "./a") {
		t.Error("a recipe naming ./a does not cover ./a")
	}
}

func TestE2EPackagesInFindsTaggedPackagesOnly(t *testing.T) {
	d := "//go:" + "build"
	tagged := d + " e2e\n\npackage a\n"
	files := map[string]string{
		"root_test.go":           tagged,
		"tagged/a_test.go":       tagged,
		"plain/b_test.go":        "package b\n",
		"notatest/c.go":          tagged,
		".hidden/d_test.go":      tagged,
		"_ignored/e_test.go":     tagged,
		"vendor/f/f_test.go":     tagged,
		"testdata/g/g_test.go":   tagged,
		"bin/i_test.go":          tagged,
		"node_modules/j_test.go": tagged,
		"nested/deep/h_test.go":  tagged,
	}
	want := map[string]bool{"./": true, "./tagged": true, "./nested/deep": true}

	// Once under a plain root and once under a dot-named one: the walk skips dot
	// directories, and skipping the root itself would empty the result rather
	// than fail loudly.
	for _, rootName := range []string{"plain-root", ".dot-root"} {
		t.Run(rootName, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), rootName)
			for rel, src := range files {
				path := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := e2ePackagesIn(root)
			if err != nil {
				t.Fatalf("e2ePackagesIn: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("found %v, want %v", got, want)
			}
		})
	}
}

// TestE2EPackagesInSurvivesAnUnreadableDirectory pins the walk's error policy:
// one directory the runner cannot enter must not end the scan, because the
// packages after it are the ones this check is for.
func TestE2EPackagesInSurvivesAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	tagged := "//go:" + "build e2e\n\npackage a\n"
	for _, rel := range []string{"aaa-locked/x_test.go", "zzz-visible/y_test.go"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(tagged), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(root, "aaa-locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got, err := e2ePackagesIn(root)
	if err != nil {
		t.Fatalf("e2ePackagesIn: %v", err)
	}
	if !got["./zzz-visible"] {
		t.Errorf("found %v, want the readable package after the unreadable one", got)
	}
}

// TestMakeDryRunJoinsContinuations pins the two things makeDryRun adds on top
// of make. One is joining: `make -n` prints a recipe's backslash continuation
// as two lines, and a command split that way would otherwise be read as two,
// neither of which is a `go test` invocation naming packages. The other is that
// none of the state below reaches the child.
//
// The cases set that state themselves rather than leaving it to the invocation,
// so the plain `go test ./...` that CI runs catches an insulation going missing.
// The run through make that exposed this happens on a developer's machine and
// nowhere in CI.
func TestMakeDryRunJoinsContinuations(t *testing.T) {
	tests := []struct {
		name string
		// env is what the process running the suite carries in.
		env map[string]string
		// preamble is state arriving through the makefile instead, which is
		// the door an environment cannot close.
		preamble string
	}{
		{name: "--trace, which no flag cancels", env: map[string]string{"MAKEFLAGS": "--trace"}},
		{name: "-d through GNUMAKEFLAGS", env: map[string]string{"GNUMAKEFLAGS": "-d"}},
		{
			name:     "the makefile branches on $(MAKELEVEL)",
			env:      map[string]string{"MAKELEVEL": "1"},
			preamble: "ifneq ($(MAKELEVEL),0)\n$(info nested build)\nendif\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireMake(t)

			dir := t.TempDir()
			makefile := tt.preamble + "test-e2e:\n\tgo test -tags e2e ./a/ \\\n\t\t./b/\n"
			if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(makefile), 0o644); err != nil {
				t.Fatal(err)
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			got := makeDryRun(t, dir, "test-e2e")
			if len(got) != 1 {
				t.Fatalf("makeDryRun returned %d commands, want 1: %q", len(got), got)
			}
			want := map[string]bool{"./a": true, "./b": true}
			if pkgs := goTestPackages(got); !reflect.DeepEqual(pkgs, want) {
				t.Errorf("packages = %v, want %v", pkgs, want)
			}
		})
	}
}
