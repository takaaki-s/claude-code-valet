package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeConfig seeds ~/.claude.json inside a fake home with the given literal
// JSON. Tests state the on-disk bytes rather than building a struct so that a
// key the adapter must preserve cannot silently vanish from the fixture too.
func writeConfig(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to seed config: %v", err)
	}
	return path
}

// readConfig decodes ~/.claude.json into an untyped map so assertions can look
// at keys the adapter has no type for.
func readConfig(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}
	return cfg
}

// projectsOf pulls the projects map out of a decoded config.
func projectsOf(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	raw, ok := cfg["projects"]
	if !ok {
		t.Fatalf("config has no projects key, got keys: %v", keysOf(cfg))
	}
	projects, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("projects is %T, want object", raw)
	}
	return projects
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// assertTrusted checks that dir has hasTrustDialogAccepted=true.
func assertTrusted(t *testing.T, projects map[string]any, dir string) {
	t.Helper()
	raw, ok := projects[dir]
	if !ok {
		t.Fatalf("no project entry for %s, got keys: %v", dir, keysOf(projects))
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("entry for %s is %T, want object", dir, raw)
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Errorf("hasTrustDialogAccepted = %v, want true", entry["hasTrustDialogAccepted"])
	}
}

func TestEnsureTrustState(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	workDir := t.TempDir()

	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	absWorkDir, _ := filepath.Abs(workDir)
	assertTrusted(t, projectsOf(t, readConfig(t, fakeHome)), absWorkDir)

	// The old location must stay untouched: writing there is what this change
	// stopped doing, and a stray write would silently resurrect the dead
	// 1800-entry map users already have.
	if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "settings.local.json")); err == nil {
		t.Error("settings.local.json was written; trust state must go to ~/.claude.json only")
	}

	// Claude Code's own write ends at the closing brace. json.Encoder adds a
	// newline that json.MarshalIndent does not, so without the trim the two
	// writers would flip the last byte back and forth for the file's lifetime.
	data, err := os.ReadFile(filepath.Join(fakeHome, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '}' {
		t.Errorf("config ends with %q, want the closing brace and nothing after it", string(data[max(0, len(data)-2):]))
	}
}

// TestEnsureTrustState_PreservesTopLevelKeys is the test this whole change
// exists for. ~/.claude.json holds the OAuth session and the MCP configuration;
// losing a key here logs the user out rather than merely re-prompting them.
func TestEnsureTrustState_PreservesTopLevelKeys(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	writeConfig(t, fakeHome, `{
  "oauthAccount": {"accountUuid": "abc-123", "emailAddress": "user@example.com"},
  "userID": "deadbeef",
  "numStartups": 464,
  "mcpServers": {"some-server": {"command": "run-me"}},
  "cachedGrowthBookFeatures": {"nested": {"deep": [1, 2, 3]}},
  "autoUpdates": false,
  "projects": {"/existing": {"hasTrustDialogAccepted": true}}
}`)

	workDir := t.TempDir()
	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	cfg := readConfig(t, fakeHome)

	want := map[string]string{
		"oauthAccount":             `{"accountUuid":"abc-123","emailAddress":"user@example.com"}`,
		"userID":                   `"deadbeef"`,
		"numStartups":              `464`,
		"mcpServers":               `{"some-server":{"command":"run-me"}}`,
		"cachedGrowthBookFeatures": `{"nested":{"deep":[1,2,3]}}`,
		"autoUpdates":              `false`,
	}
	for key, wantJSON := range want {
		got, ok := cfg[key]
		if !ok {
			t.Errorf("top-level key %q was dropped; surviving keys: %v", key, keysOf(cfg))
			continue
		}
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("failed to re-marshal %q: %v", key, err)
		}
		if string(gotJSON) != wantJSON {
			t.Errorf("top-level key %q = %s, want %s", key, gotJSON, wantJSON)
		}
	}

	// The pre-existing project entry survives alongside the new one.
	projects := projectsOf(t, cfg)
	assertTrusted(t, projects, "/existing")
	absWorkDir, _ := filepath.Abs(workDir)
	assertTrusted(t, projects, absWorkDir)
}

// TestEnsureTrustState_PreservesProjectKeys covers the second level of the
// merge: Claude Code keeps ~30 keys per project and jind-ai sets exactly one.
func TestEnsureTrustState_PreservesProjectKeys(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	workDir := t.TempDir()
	absWorkDir, _ := filepath.Abs(workDir)

	writeConfig(t, fakeHome, `{"projects": {`+jsonString(t, absWorkDir)+`: {
      "allowedTools": ["Bash", "Read"],
      "lastCost": 1.23,
      "mcpServers": {},
      "lastSessionId": "session-uuid",
      "hasTrustDialogAccepted": false
    }}}`)

	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	projects := projectsOf(t, readConfig(t, fakeHome))
	entry, ok := projects[absWorkDir].(map[string]any)
	if !ok {
		t.Fatalf("entry for %s missing or wrong type", absWorkDir)
	}

	if entry["hasTrustDialogAccepted"] != true {
		t.Errorf("hasTrustDialogAccepted = %v, want true (false must be flipped)", entry["hasTrustDialogAccepted"])
	}
	if got, err := json.Marshal(entry["allowedTools"]); err != nil || string(got) != `["Bash","Read"]` {
		t.Errorf("allowedTools = %s, want [\"Bash\",\"Read\"]", got)
	}
	if entry["lastCost"] != 1.23 {
		t.Errorf("lastCost = %v, want 1.23", entry["lastCost"])
	}
	if entry["lastSessionId"] != "session-uuid" {
		t.Errorf("lastSessionId = %v, want session-uuid", entry["lastSessionId"])
	}
}

func TestEnsureTrustState_Idempotent(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	workDir := t.TempDir()

	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(fakeHome, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(fakeHome, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	if string(first) != string(second) {
		t.Error("second call rewrote the file; an already-trusted workDir must be a no-op")
	}

	projects := projectsOf(t, readConfig(t, fakeHome))
	if len(projects) != 1 {
		t.Errorf("expected 1 project entry, got %d", len(projects))
	}
}

// TestEnsureTrustState_AncestorTrustIsNoOp is the mechanism that keeps
// ~/.claude.json from growing an entry per throwaway worktree: Claude Code
// walks up to the root looking for trust, so a trusted parent means there is
// nothing to write.
func TestEnsureTrustState_AncestorTrustIsNoOp(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	base := t.TempDir()
	child := filepath.Join(base, "worktrees", "jin-abc123")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatalf("failed to create child dir: %v", err)
	}

	writeConfig(t, fakeHome, `{"projects": {`+jsonString(t, base)+`: {"hasTrustDialogAccepted": true}}}`)
	before, err := os.ReadFile(filepath.Join(fakeHome, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	if err := EnsureTrustState(child); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(fakeHome, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("file changed under a trusted ancestor:\nbefore: %s\nafter:  %s", before, after)
	}

	projects := projectsOf(t, readConfig(t, fakeHome))
	if len(projects) != 1 {
		t.Errorf("expected the ancestor entry only, got %d entries: %v", len(projects), keysOf(projects))
	}
}

// TestEnsureTrustState_AncestorFalseStillWrites guards the inverse: an ancestor
// present but explicitly not trusted must not be mistaken for coverage.
func TestEnsureTrustState_AncestorFalseStillWrites(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	base := t.TempDir()
	child := filepath.Join(base, "sub")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatalf("failed to create child dir: %v", err)
	}

	writeConfig(t, fakeHome, `{"projects": {`+jsonString(t, base)+`: {"hasTrustDialogAccepted": false}}}`)

	if err := EnsureTrustState(child); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	projects := projectsOf(t, readConfig(t, fakeHome))
	assertTrusted(t, projects, child)
}

func TestEnsureTrustState_MultipleProjects(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	workDir1 := t.TempDir()
	workDir2 := t.TempDir()

	if err := EnsureTrustState(workDir1); err != nil {
		t.Fatalf("first project failed: %v", err)
	}
	if err := EnsureTrustState(workDir2); err != nil {
		t.Fatalf("second project failed: %v", err)
	}

	projects := projectsOf(t, readConfig(t, fakeHome))
	if len(projects) != 2 {
		t.Errorf("expected 2 project entries, got %d", len(projects))
	}

	abs1, _ := filepath.Abs(workDir1)
	abs2, _ := filepath.Abs(workDir2)
	assertTrusted(t, projects, abs1)
	assertTrusted(t, projects, abs2)
}

// TestEnsureTrustState_MalformedConfigIsNotRewritten is the safety property:
// an unparseable config is reported, never replaced. The previous
// implementation reset its view to an empty struct here, which against this
// file would have discarded the OAuth session.
func TestEnsureTrustState_MalformedConfigIsNotRewritten(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"truncated", `{"projects": {`},
		{"empty file", ``},
		{"not an object", `["a", "b"]`},
		{"projects is a string", `{"projects": "nope"}`},
		{"project entry is a string", `{"projects": {"/x": "nope"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeHome := t.TempDir()
			t.Setenv("HOME", fakeHome)
			path := writeConfig(t, fakeHome, tt.content)

			err := EnsureTrustState(t.TempDir())
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), "refusing to rewrite") {
				t.Errorf("error = %v, want it to say why the file was left alone", err)
			}

			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("failed to read config: %v", readErr)
			}
			if string(after) != tt.content {
				t.Errorf("file was modified:\nbefore: %q\nafter:  %q", tt.content, after)
			}
		})
	}
}

func TestEnsureTrustState_UnreadableConfig(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: mode bits do not deny access")
	}

	const seeded = `{"projects": {}}`

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	path := writeConfig(t, fakeHome, seeded)
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatalf("failed to chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })

	if err := EnsureTrustState(t.TempDir()); err == nil {
		t.Fatal("expected an error for an unreadable config, got nil")
	}

	// Same property the malformed cases assert: a config we could not read must
	// come out the other side untouched, not replaced with one built from
	// nothing.
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatalf("failed to restore mode: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(after) != seeded {
		t.Errorf("file was modified:\nbefore: %q\nafter:  %q", seeded, after)
	}
}

// TestEnsureTrustState_CreatesMissingConfig covers a machine that has jind-ai
// but has never started Claude Code.
func TestEnsureTrustState_CreatesMissingConfig(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	workDir := t.TempDir()
	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	path := filepath.Join(fakeHome, ".claude.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("mode = %o, want 600 (the file holds credentials)", got)
	}

	absWorkDir, _ := filepath.Abs(workDir)
	assertTrusted(t, projectsOf(t, readConfig(t, fakeHome)), absWorkDir)
}

// TestEnsureTrustState_BareObject covers "{}" and an explicit null projects
// block — both are well-formed and must be filled in rather than rejected.
func TestEnsureTrustState_BareObject(t *testing.T) {
	for _, content := range []string{`{}`, `{"projects": null}`} {
		t.Run(content, func(t *testing.T) {
			fakeHome := t.TempDir()
			t.Setenv("HOME", fakeHome)
			writeConfig(t, fakeHome, content)

			workDir := t.TempDir()
			if err := EnsureTrustState(workDir); err != nil {
				t.Fatalf("EnsureTrustState failed: %v", err)
			}

			absWorkDir, _ := filepath.Abs(workDir)
			assertTrusted(t, projectsOf(t, readConfig(t, fakeHome)), absWorkDir)
		})
	}
}

// TestEnsureTrustState_RootDir pins the walk's termination: filepath.Dir("/")
// returns "/", so the loop needs the parent == dir guard to stop. Without it
// this test hangs until `go test -timeout` kills the run.
func TestEnsureTrustState_RootDir(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	if err := EnsureTrustState("/"); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	assertTrusted(t, projectsOf(t, readConfig(t, fakeHome)), "/")
}

// TestEnsureTrustState_PathNormalization checks that spellings of the same
// directory collapse to one entry. Symlinks are deliberately not resolved —
// see the note in docs/gotchas.md.
func TestEnsureTrustState_PathNormalization(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	workDir := t.TempDir()
	absWorkDir, _ := filepath.Abs(workDir)

	for _, spelling := range []string{workDir, workDir + "/", workDir + "/./", workDir + "/sub/.."} {
		if err := EnsureTrustState(spelling); err != nil {
			t.Fatalf("EnsureTrustState(%q) failed: %v", spelling, err)
		}
	}

	projects := projectsOf(t, readConfig(t, fakeHome))
	if len(projects) != 1 {
		t.Errorf("expected 1 entry for 4 spellings of the same path, got %d: %v", len(projects), keysOf(projects))
	}
	assertTrusted(t, projects, absWorkDir)
}

// TestEnsureTrustState_NoTempFileLeft checks the atomic write cleans up after
// itself; a crash-orphaned sibling in the home directory would be confusing at
// best.
//
// It asserts on the whole directory listing rather than on names starting with
// configTmpPattern's prefix. Searching for the prefix would make the check pass
// by construction the moment the write started using a different name, which is
// exactly the change it is supposed to catch.
func TestEnsureTrustState_NoTempFileLeft(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	if err := EnsureTrustState(t.TempDir()); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	entries, err := os.ReadDir(fakeHome)
	if err != nil {
		t.Fatalf("failed to read home: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != ".claude.json" {
		t.Errorf("home contains %v, want exactly [.claude.json]", names)
	}
}

// TestConfigTmpPattern pins the contract configTmpPattern's comment states.
//
// The temp file is unobservable from outside — atomicfile removes it on every
// path out of Write, success or failure — so no end-to-end test can constrain
// the name. Asserting on the constant directly is the only way its rules get
// enforced at all, and the rules are the whole reason the pattern is passed in
// rather than derived from the target path.
//
// Note what this therefore does not cover: passing some other string at the
// call site instead of the constant. That is unobservable for the same reason.
func TestConfigTmpPattern(t *testing.T) {
	// Claude Code carries a list of sensitive dotfile names containing the
	// literal ".claude.json"; a crash-orphaned sibling must not look like it.
	if strings.HasPrefix(configTmpPattern, ".claude.json") {
		t.Errorf("configTmpPattern = %q, must not start with .claude.json", configTmpPattern)
	}
	// "Recognisably ours" — the file can land in the user's dotfiles directory
	// when ~/.claude.json is a symlink, so it has to be attributable on sight.
	if !strings.HasPrefix(configTmpPattern, ".jin-") {
		t.Errorf("configTmpPattern = %q, want a .jin- prefix so a stray file is attributable", configTmpPattern)
	}
	// os.CreateTemp only randomises where the pattern has a "*"; without one
	// every concurrent write would fight over a single fixed name.
	if !strings.Contains(configTmpPattern, "*") {
		t.Errorf("configTmpPattern = %q, want a * for os.CreateTemp to randomise", configTmpPattern)
	}
}

// TestEnsureTrustState_RelativeWorkDir pins that a relative workDir is resolved
// against the working directory before it is used as a key.
//
// Claude Code keys the projects map by absolute path, so a relative spelling
// written verbatim would never match what the CLI looks up, and every session
// would get the trust dialog this function exists to suppress — while the file
// grew an entry per spelling. The absolute-path tests cannot see this: their
// inputs are already absolute, so filepath.Abs and filepath.Clean are
// indistinguishable there.
func TestEnsureTrustState_RelativeWorkDir(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "sub"), 0o755); err != nil {
		t.Fatalf("failed to create workdir: %v", err)
	}
	t.Chdir(parent)

	// Read the working directory back rather than reusing parent: Chdir can
	// land on a different spelling of the same place, and it is the spelling
	// filepath.Abs sees that ends up in the file.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	want := filepath.Join(cwd, "sub")

	for _, spelling := range []string{"sub", "./sub", "sub/"} {
		if err := EnsureTrustState(spelling); err != nil {
			t.Fatalf("EnsureTrustState(%q) failed: %v", spelling, err)
		}
	}

	projects := projectsOf(t, readConfig(t, fakeHome))
	if len(projects) != 1 {
		t.Errorf("expected 1 entry for 3 relative spellings, got %d: %v", len(projects), keysOf(projects))
	}
	assertTrusted(t, projects, want)
}

// TestEnsureTrustState_Concurrent is a regression test for a lost update.
//
// Before the write was serialised, two sessions starting at once both read the
// file, both added their own entry to their own copy, and the second rename
// threw the first one's entry away — 85 times in 100 attempts. The daemon
// serves each client on its own goroutine and Setup runs outside the Manager's
// lock, so this is the ordinary case of starting two sessions, not a contrived
// one. Run under -race it also covers the map itself being shared.
func TestEnsureTrustState_Concurrent(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	writeConfig(t, fakeHome, `{"oauthAccount":{"emailAddress":"user@example.com"},"projects":{}}`)

	const sessions = 8
	dirs := make([]string, sessions)
	for i := range dirs {
		dirs[i] = filepath.Join(fakeHome, "worktrees", fmt.Sprintf("wt-%d", i))
		if err := os.MkdirAll(dirs[i], 0o755); err != nil {
			t.Fatalf("failed to create workdir: %v", err)
		}
	}

	// Released all at once so the goroutines contend rather than queueing up
	// behind their own start-up cost.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, sessions)
	for i, dir := range dirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			<-start
			errs[i] = EnsureTrustState(dir)
		}(i, dir)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureTrustState(%s) failed: %v", dirs[i], err)
		}
	}

	cfg := readConfig(t, fakeHome)
	projects := projectsOf(t, cfg)
	for _, dir := range dirs {
		assertTrusted(t, projects, dir)
	}
	if _, ok := cfg["oauthAccount"]; !ok {
		t.Error("oauthAccount lost during concurrent writes")
	}
}

// TestEnsureTrustState_ReloadsWhenConfigChangesUnderIt covers the window the
// mutex cannot: Claude Code writing the file from its own process between
// jind-ai's read and its rename. Renaming a snapshot built before that write
// would roll it back — and what gets rolled back is whatever Claude Code just
// saved, up to and including the OAuth session.
//
// The fake stat writes the file for real on the second call, which is the
// re-check at the end of the first attempt, then reports the true stamp. So the
// change is genuine and the retry has to notice it, reload, and merge into the
// new contents. Asserting the other writer's keys survived is what makes this a
// test of re-merging rather than of retry-count bookkeeping.
func TestEnsureTrustState_ReloadsWhenConfigChangesUnderIt(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	writeConfig(t, fakeHome, `{"projects":{}}`)

	realStat := statConfig
	t.Cleanup(func() { statConfig = realStat })

	calls := 0
	statConfig = func(path string) (configStamp, error) {
		calls++
		if calls == 2 {
			writeConfig(t, fakeHome, `{"lastCost":42,"projects":{"/other":{"hasTrustDialogAccepted":true}}}`)
		}
		return realStat(path)
	}

	workDir := t.TempDir()
	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	cfg := readConfig(t, fakeHome)
	projects := projectsOf(t, cfg)

	absWorkDir, _ := filepath.Abs(workDir)
	assertTrusted(t, projects, absWorkDir)

	// Both of these come only from the write that landed mid-merge.
	if _, ok := cfg["lastCost"]; !ok {
		t.Error("lastCost is gone; the concurrent write was rolled back")
	}
	assertTrusted(t, projects, "/other")
}

// TestEnsureTrustState_SymlinkedConfig pins that a ~/.claude.json symlinked
// into a dotfiles repository is followed, not replaced.
//
// atomicfile.Write ends in os.Rename, which swaps the link itself rather than
// the file behind it. Left unresolved, the write would leave a plain file in
// $HOME and a stale orphan in the repository — the user's dotfiles sync broken
// with no error anywhere. The previous implementation used os.WriteFile, which
// followed the link, so this is a property the move to an atomic write had to
// preserve rather than a new feature.
func TestEnsureTrustState_SymlinkedConfig(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	dotfiles := t.TempDir()
	target := filepath.Join(dotfiles, "claude.json")
	const seed = `{"oauthAccount":{"emailAddress":"user@example.com"},"projects":{}}`
	if err := os.WriteFile(target, []byte(seed), 0600); err != nil {
		t.Fatalf("failed to seed dotfiles config: %v", err)
	}
	link := filepath.Join(fakeHome, ".claude.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("failed to symlink config: %v", err)
	}

	workDir := t.TempDir()
	if err := EnsureTrustState(workDir); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("failed to lstat config: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("~/.claude.json is no longer a symlink; the rename replaced the link")
	}

	// Read through the target, not the link, so a link that survived while the
	// write went somewhere else still fails.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("failed to read dotfiles config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal dotfiles config: %v", err)
	}
	absWorkDir, _ := filepath.Abs(workDir)
	assertTrusted(t, projectsOf(t, cfg), absWorkDir)
	if _, ok := cfg["oauthAccount"]; !ok {
		t.Error("oauthAccount lost when writing through the symlink")
	}

	// The temp file lands beside the resolved target, so the dotfiles directory
	// is the one that has to come out clean.
	entries, err := os.ReadDir(dotfiles)
	if err != nil {
		t.Fatalf("failed to read dotfiles dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "claude.json" {
		t.Errorf("dotfiles dir contains %d entries, want exactly [claude.json]", len(entries))
	}
}

// TestEnsureTrustState_BrokenSymlinkedConfig covers a link whose target does
// not exist yet — dotfiles repository not cloned, volume not mounted, sync tool
// mid-operation. filepath.EvalSymlinks refuses a path it cannot stat, so
// without the os.Readlink fallback the write would land on the link path and
// replace the user's symlink with a regular file while never creating the
// target: the same damage the resolution exists to prevent, and harder to spot
// because the target was already missing.
func TestEnsureTrustState_BrokenSymlinkedConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(dir string) string
	}{
		{"absolute target", func(dir string) string { return filepath.Join(dir, "gone.json") }},
		{"relative target", func(string) string { return "dotfiles/gone.json" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeHome := t.TempDir()
			t.Setenv("HOME", fakeHome)

			// The relative case resolves against the link's own directory, so
			// give it somewhere inside the fake home to land.
			if err := os.MkdirAll(filepath.Join(fakeHome, "dotfiles"), 0o755); err != nil {
				t.Fatalf("failed to create dotfiles dir: %v", err)
			}
			target := tc.target(filepath.Join(fakeHome, "dotfiles"))
			link := filepath.Join(fakeHome, ".claude.json")
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf("failed to symlink config: %v", err)
			}

			workDir := t.TempDir()
			if err := EnsureTrustState(workDir); err != nil {
				t.Fatalf("EnsureTrustState failed: %v", err)
			}

			info, err := os.Lstat(link)
			if err != nil {
				t.Fatalf("failed to lstat config: %v", err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Error("~/.claude.json is no longer a symlink; the write replaced the link instead of creating its target")
			}

			// Reading through the link only succeeds if the target now exists.
			absWorkDir, _ := filepath.Abs(workDir)
			assertTrusted(t, projectsOf(t, readConfig(t, fakeHome)), absWorkDir)
		})
	}
}

// TestEnsureTrustState_DoesNotEscapeHTML pins that values jind-ai does not
// understand come back byte for byte.
//
// encoding/json escapes <, > and & by default, inside json.RawMessage as well,
// so an MCP server URL with a query string would be rewritten on every session
// start. The result parses to the same value, but the file is Claude Code's —
// JavaScript's JSON.stringify does not escape those — and "jind-ai sets one
// flag and leaves everything else alone" has to be literally true to be worth
// relying on.
//
// The URL is seeded in both places it can occur, because the two are encoded by
// separate calls. A user-scope server sits at the top level; a local-scope one
// sits inside a project entry, which is the shape Claude Code's own per-project
// template uses — and that one is only reached through the nested encode.
func TestEnsureTrustState_DoesNotEscapeHTML(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	const url = "https://example.com/mcp?a=1&b=2&tag=<x>"
	writeConfig(t, fakeHome, `{"mcpServers":{"docs":{"url":"`+url+`"}},`+
		`"projects":{"/other":{"mcpServers":{"local":{"url":"`+url+`"}}}}}`)

	if err := EnsureTrustState(t.TempDir()); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(fakeHome, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if got := strings.Count(string(data), url); got != 2 {
		t.Errorf("found the literal URL %d times, want 2 (top level and inside the project entry); file:\n%s", got, data)
	}
}

// TestEnsureTrustState_Indentation pins the two-space indent the encoder is
// configured for. Without it nothing constrains the setting: the file parses
// the same whether it is written on one line or indented four spaces, and the
// only other formatting assertion looks at the final byte.
func TestEnsureTrustState_Indentation(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	if err := EnsureTrustState(t.TempDir()); err != nil {
		t.Fatalf("EnsureTrustState failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(fakeHome, ".claude.json"))
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if !strings.Contains(string(data), "\n  \"projects\": {\n") {
		t.Errorf("projects is not indented by two spaces; file:\n%s", data)
	}
}

// TestEnsureTrustState_InvalidUTF8IsNotRewritten covers a file whose bytes are
// not valid UTF-8. Values survive a write because they are held as raw
// messages, but object keys do not: the decoder replaces the offending bytes
// with U+FFFD, so a project path containing one would come back renamed and
// that project's settings would be stranded under a key nothing looks up.
func TestEnsureTrustState_InvalidUTF8IsNotRewritten(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// \xff is not valid UTF-8, and is a byte a Linux path may legally contain.
	seed := "{\"projects\":{\"/tmp/bad\xffdir\":{\"allowedTools\":[]}}}"
	path := writeConfig(t, fakeHome, seed)

	err := EnsureTrustState(t.TempDir())
	if err == nil {
		t.Fatal("EnsureTrustState succeeded, want a refusal to rewrite")
	}
	if !strings.Contains(err.Error(), "refusing to rewrite") {
		t.Errorf("error = %v, want it to say it is refusing to rewrite", err)
	}

	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("failed to read config: %v", readErr)
	}
	if string(after) != seed {
		t.Errorf("the file was modified:\n got %q\nwant %q", after, seed)
	}
}

func TestIsTrusted(t *testing.T) {
	projects := map[string]rawProject{
		"/a":       {trustKey: json.RawMessage("true")},
		"/b":       {trustKey: json.RawMessage("false")},
		"/c":       {trustKey: json.RawMessage("null")},
		"/d":       {trustKey: json.RawMessage(`"true"`)}, // string, not bool
		"/e":       {"someOtherKey": json.RawMessage("1")},
		"/f/g/h/i": {trustKey: json.RawMessage("true")},
	}

	tests := []struct {
		dir  string
		want bool
	}{
		{"/a", true},
		{"/a/deeply/nested/child", true}, // inherited from /a
		{"/b", false},
		{"/b/child", false},
		{"/c", false},
		{"/d", false}, // only literal true counts
		{"/e", false},
		{"/f", false},   // trust does not propagate downward-to-upward
		{"/f/g", false}, // ancestors of a trusted dir stay untrusted
		{"/f/g/h/i", true},
		{"/unknown", false},
		{"/", false},
	}

	for _, tt := range tests {
		if got := isTrusted(projects, tt.dir); got != tt.want {
			t.Errorf("isTrusted(%q) = %v, want %v", tt.dir, got, tt.want)
		}
	}
}
