package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/internal/git"
)

func TestDeriveWorktreeName(t *testing.T) {
	cases := []struct {
		name      string
		sessionID string
		override  string
		want      string
	}{
		{"override wins", "3f9a2b4c-1111-2222-3333-444444444444", "custom-wt", "custom-wt"},
		{"no override, canonical UUID", "3f9a2b4c-1111-2222-3333-444444444444", "", "jin-3f9a2b4c"},
		{"session id shorter than 8 chars", "abc", "", "jin-abc"},
		{"exactly 8 char id", "12345678", "", "jin-12345678"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveWorktreeName(tc.sessionID, tc.override); got != tc.want {
				t.Errorf("deriveWorktreeName(%q, %q) = %q, want %q",
					tc.sessionID, tc.override, got, tc.want)
			}
		})
	}
}

func TestDeriveBranchName(t *testing.T) {
	cases := []struct {
		name         string
		worktreeName string
		prefix       string
		override     string
		want         string
	}{
		{"default prefix strips jin-", "jin-3f9a2b4c", "jin/", "", "jin/3f9a2b4c"},
		{"custom prefix strips jin-", "jin-3f9a2b4c", "topic/", "", "topic/3f9a2b4c"},
		{"empty prefix strips jin-", "jin-3f9a2b4c", "", "", "3f9a2b4c"},
		{"non jin- worktree name preserved", "custom-wt", "jin/", "", "jin/custom-wt"},
		{"override wins", "jin-3f9a2b4c", "jin/", "feat/xyz", "feat/xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveBranchName(tc.worktreeName, tc.prefix, tc.override); got != tc.want {
				t.Errorf("deriveBranchName(%q, %q, %q) = %q, want %q",
					tc.worktreeName, tc.prefix, tc.override, got, tc.want)
			}
		})
	}
}

// TestWorktreeTemplate pins where an unset worktree.base_dir lands. The state
// dir is the Manager's, not the process-wide one, so a Manager built over a
// different state dir keeps its worktrees inside it — see worktreeTemplate for
// what reading the global cost.
func TestWorktreeTemplate(t *testing.T) {
	cases := []struct {
		name       string
		cfgBaseDir string
		stateDir   string
		want       string
	}{
		{
			name:       "unset base_dir resolves under the state dir it is given",
			cfgBaseDir: "",
			stateDir:   "/state/jind-ai",
			want:       "/state/jind-ai/worktrees/{name}",
		},
		{
			name:       "a configured base_dir wins over the state dir",
			cfgBaseDir: "/tmp/wt/{name}",
			stateDir:   "/state/jind-ai",
			want:       "/tmp/wt/{name}",
		},
		{
			// Not an absolute path, so expandBaseDir rejects it. The
			// alternative is a worktree in the process's working directory.
			name:       "no state dir yields something expandBaseDir refuses",
			cfgBaseDir: "",
			stateDir:   "",
			want:       "worktrees/{name}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := worktreeTemplate(tc.cfgBaseDir, tc.stateDir)
			if got != tc.want {
				t.Errorf("worktreeTemplate(%q, %q) = %q, want %q",
					tc.cfgBaseDir, tc.stateDir, got, tc.want)
			}
			if tc.stateDir == "" {
				if _, err := expandBaseDir(got, "jin-abc", "repo"); err == nil {
					t.Error("expandBaseDir accepted the relative template; it must reject it")
				}
			}
		})
	}
}

func TestExpandBaseDir(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")

	cases := []struct {
		name         string
		template     string
		worktreeName string
		repoBasename string
		wantPath     string
		wantErr      bool
	}{
		{
			// The default lives in worktreeTemplate, so an empty template
			// arriving here means a caller skipped it.
			name:         "empty template errors instead of defaulting",
			template:     "",
			worktreeName: "jin-abc",
			repoBasename: "myrepo",
			wantErr:      true,
		},
		{
			name:         "explicit {name} substitution",
			template:     "/tmp/wt/{name}",
			worktreeName: "jin-xyz",
			repoBasename: "myrepo",
			wantPath:     "/tmp/wt/jin-xyz",
		},
		{
			name:         "explicit {repo}/{name} substitution",
			template:     "/tmp/{repo}/{name}",
			worktreeName: "wt1",
			repoBasename: "myrepo",
			wantPath:     "/tmp/myrepo/wt1",
		},
		{
			name:         "env var expansion",
			template:     "${HOME}/.wt/{name}",
			worktreeName: "wt1",
			repoBasename: "r",
			wantPath:     "/home/testuser/.wt/wt1",
		},
		{
			name:         "unknown template variable errors",
			template:     "/tmp/{unknown}/{name}",
			worktreeName: "wt1",
			repoBasename: "r",
			wantErr:      true,
		},
		{
			name:         "relative path errors",
			template:     "relative/{name}",
			worktreeName: "wt1",
			repoBasename: "r",
			wantErr:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandBaseDir(tc.template, tc.worktreeName, tc.repoBasename)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expandBaseDir(%q) expected error, got %q", tc.template, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandBaseDir(%q) unexpected error: %v", tc.template, err)
			}
			if got != tc.wantPath {
				t.Errorf("expandBaseDir(%q) = %q, want %q", tc.template, got, tc.wantPath)
			}
		})
	}
}

func TestFindAvailableWorktreeName_NoCollision(t *testing.T) {
	got, err := findAvailableWorktreeName("jin-abc", func(string) bool { return false })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "jin-abc" {
		t.Errorf("got %q, want %q", got, "jin-abc")
	}
}

func TestFindAvailableWorktreeName_FirstCollisionSuffixed(t *testing.T) {
	taken := map[string]bool{"jin-abc": true}
	got, err := findAvailableWorktreeName("jin-abc", func(c string) bool { return taken[c] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "jin-abc-2" {
		t.Errorf("got %q, want %q", got, "jin-abc-2")
	}
}

func TestFindAvailableWorktreeName_ExhaustsAllAttempts(t *testing.T) {
	_, err := findAvailableWorktreeName("jin-abc", func(string) bool { return true })
	if err == nil {
		t.Fatal("expected error when every candidate collides")
	}
	if !strings.Contains(err.Error(), "jin-abc") {
		t.Errorf("error message %q should mention base name", err.Error())
	}
}

func TestFindAvailableWorktreeName_ThirdSuffix(t *testing.T) {
	taken := map[string]bool{
		"jin-abc":   true,
		"jin-abc-2": true,
	}
	got, err := findAvailableWorktreeName("jin-abc", func(c string) bool { return taken[c] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "jin-abc-3" {
		t.Errorf("got %q, want %q", got, "jin-abc-3")
	}
}

// recordingWorktreeRunner captures the path handed to `git worktree add` and
// then fails, so provisionWorktree returns before the post-create machinery
// runs. The path is the whole point of the test; nothing after the add call
// bears on it.
type recordingWorktreeRunner struct {
	addPath string
}

func (r *recordingWorktreeRunner) Run(dir string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case joined == "symbolic-ref refs/remotes/origin/HEAD":
		return []byte("refs/remotes/origin/main\n"), nil
	case len(args) >= 2 && args[0] == "worktree" && args[1] == "prune":
		return nil, nil
	case len(args) >= 1 && args[0] == "rev-parse":
		return nil, errors.New("exit status 1") // branch does not exist
	case len(args) >= 2 && args[0] == "worktree" && args[1] == "add":
		// `git worktree add -b <branch> <path> <baseRef>`
		r.addPath = args[4]
		return nil, errors.New("exit status 128")
	}
	return nil, fmt.Errorf("unexpected git call: %s", joined)
}

// TestProvisionWorktree_PlacesWorktreesUnderTheStateDirTheManagerWasBuiltOver
// pins the wiring, not just the helper: NewManager's stateDir argument has to
// reach the path `git worktree add` is given.
//
// It bites on a real defect rather than a hypothetical one. Resolving that path
// from the process-wide state dir instead meant a Manager built over a temp dir
// still wrote into the developer's own — measured at two directories per full
// test-suite run, which accumulated into four figures before anyone noticed.
// Nothing failed when it happened, so only a test that reads the path back can
// hold it.
func TestProvisionWorktree_PlacesWorktreesUnderTheStateDirTheManagerWasBuiltOver(t *testing.T) {
	configDir := t.TempDir()
	configMgr, err := config.NewManager(configDir)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	// Distinct from every other temp dir, so the assertion below can only pass
	// if this specific argument is what the path was built from.
	stateDir := t.TempDir()
	mgr, err := NewManager(t.TempDir(), stateDir, configMgr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	runner := &recordingWorktreeRunner{}
	mgr.SetGitClient(git.NewClientWithRunner(runner))

	_, err = mgr.provisionWorktree("3f9a2b4c-1111-2222-3333-444444444444", CreateOptions{
		WorkDir:  repo,
		Worktree: true,
	})
	if err == nil {
		t.Fatal("provisionWorktree succeeded; the scripted `worktree add` was supposed to fail it")
	}

	want := filepath.Join(stateDir, "worktrees", "jin-3f9a2b4c")
	if runner.addPath != want {
		t.Errorf("`git worktree add` path = %q, want %q", runner.addPath, want)
	}
}
