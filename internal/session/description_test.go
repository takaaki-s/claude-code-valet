package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestGenerateBaselineDescription exercises the pure-function layer A
// generator across all documented branches: empty input, non-git dirs, git
// roots with and without branch/subpath, and a worktree layout where .git is
// a regular file.
func TestGenerateBaselineDescription(t *testing.T) {
	// Non-git baseline: a plain temp dir with no .git anywhere upstream.
	nonGitDir := t.TempDir()

	// Isolate the git fixtures from the parent test tree (t.TempDir may sit
	// inside a real git checkout on the test host). Placing them under a
	// nested TempDir does not help because walk-up would still find the host
	// repo, so we build fixtures that override the closest .git along the way.
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	repoSubdir := filepath.Join(repoRoot, "internal", "session")
	if err := os.MkdirAll(repoSubdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	// Worktree fixture with an unresolvable gitdir: the generator should
	// gracefully fall back to the worktree directory basename rather than
	// producing a broken "<empty>:<worktree>" label.
	orphanWorktreeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(orphanWorktreeDir, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatalf("write orphan .git file: %v", err)
	}

	// Realistic worktree layout: a fake main repo with a worktree directory
	// whose .git file points back into <main-repo>/.git/worktrees/<name>. The
	// generator should prepend the *main repo* basename to the *worktree*
	// basename so multiple worktrees of the same repo remain distinguishable.
	mainRepoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".git", "worktrees", "jin-abcd1234"), 0o755); err != nil {
		t.Fatalf("mkdir main repo .git/worktrees: %v", err)
	}
	worktreeDir := filepath.Join(t.TempDir(), "jin-abcd1234")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree dir: %v", err)
	}
	gitFileContent := "gitdir: " + filepath.Join(mainRepoDir, ".git", "worktrees", "jin-abcd1234") + "\n"
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte(gitFileContent), 0o644); err != nil {
		t.Fatalf("write worktree .git file: %v", err)
	}

	tests := []struct {
		name          string
		workDir       string
		currentBranch string
		isWorktree    bool
		tmuxHint      string
		want          string
	}{
		{
			name: "empty workDir falls back to session",
			want: "session",
		},
		{
			name:    "non-git directory uses basename",
			workDir: nonGitDir,
			want:    filepath.Base(nonGitDir),
		},
		{
			name:    "git repo root without branch",
			workDir: repoRoot,
			want:    filepath.Base(repoRoot),
		},
		{
			name:          "git repo root with branch",
			workDir:       repoRoot,
			currentBranch: "main",
			want:          filepath.Base(repoRoot) + ":main",
		},
		{
			name:          "git repo subdir with branch and subpath",
			workDir:       repoSubdir,
			currentBranch: "feat/x",
			want:          filepath.Base(repoRoot) + ":feat/x:internal/session",
		},
		{
			name:    "git repo subdir without branch preserves subpath",
			workDir: repoSubdir,
			want:    filepath.Base(repoRoot) + ":internal/session",
		},
		{
			name:       "worktree with unresolvable gitdir falls back to worktree basename",
			workDir:    orphanWorktreeDir,
			isWorktree: true,
			want:       filepath.Base(orphanWorktreeDir),
		},
		{
			// The main repo name is deliberately absent: the session list
			// carries it as its own field, so repeating it here would spend
			// the label on a string every row in that repo already shares.
			name:       "worktree uses the worktree basename alone",
			workDir:    worktreeDir,
			isWorktree: true,
			want:       "jin-abcd1234",
		},
		{
			name:          "worktree with branch appends branch after worktree name",
			workDir:       worktreeDir,
			currentBranch: "wip/refactor",
			isWorktree:    true,
			want:          "jin-abcd1234:wip/refactor",
		},
		{
			name:     "tmuxHint is ignored in this phase",
			workDir:  nonGitDir,
			tmuxHint: "should-not-appear",
			want:     filepath.Base(nonGitDir),
		},
		{
			name:    "trailing slash is cleaned before basename",
			workDir: nonGitDir + "/",
			want:    filepath.Base(nonGitDir),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GenerateBaselineDescription(tc.workDir, tc.currentBranch, tc.isWorktree, tc.tmuxHint)
			if got != tc.want {
				t.Errorf("GenerateBaselineDescription(%q, %q, %v, %q) = %q, want %q",
					tc.workDir, tc.currentBranch, tc.isWorktree, tc.tmuxHint, got, tc.want)
			}
			if got == "" {
				t.Errorf("invariant violated: returned empty string")
			}
		})
	}
}

// repoFixtures builds a main repo plus a worktree pointing back into it, the
// two layouts every repo-name assertion below needs.
func repoFixtures(t *testing.T) (mainRepoDir, worktreeDir string) {
	t.Helper()

	mainRepoDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".git", "worktrees", "jin-abcd1234"), 0o755); err != nil {
		t.Fatalf("mkdir main repo .git/worktrees: %v", err)
	}
	worktreeDir = filepath.Join(t.TempDir(), "jin-abcd1234")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree dir: %v", err)
	}
	gitFile := "gitdir: " + filepath.Join(mainRepoDir, ".git", "worktrees", "jin-abcd1234") + "\n"
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte(gitFile), 0o644); err != nil {
		t.Fatalf("write worktree .git file: %v", err)
	}
	return mainRepoDir, worktreeDir
}

// TestResolveRepoName pins the one behaviour the detail pane depends on: a
// worktree reports the repo it came from, not its own "jin-xxxxxxxx" dir name.
func TestResolveRepoName(t *testing.T) {
	mainRepoDir, worktreeDir := repoFixtures(t)

	nonGitDir := t.TempDir()

	orphanWorktreeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(orphanWorktreeDir, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatalf("write orphan .git file: %v", err)
	}

	subdir := filepath.Join(mainRepoDir, "internal", "session")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	tests := []struct {
		name    string
		workDir string
		want    string
	}{
		{name: "empty input", workDir: "", want: ""},
		{name: "non-git dir has no repo", workDir: nonGitDir, want: ""},
		{name: "main repo root", workDir: mainRepoDir, want: filepath.Base(mainRepoDir)},
		{name: "subdir walks up to the repo root", workDir: subdir, want: filepath.Base(mainRepoDir)},
		{name: "worktree resolves to the main repo", workDir: worktreeDir, want: filepath.Base(mainRepoDir)},
		{name: "unresolvable gitdir falls back to the local root", workDir: orphanWorktreeDir, want: filepath.Base(orphanWorktreeDir)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveRepoName(tc.workDir); got != tc.want {
				t.Errorf("ResolveRepoName(%q) = %q, want %q", tc.workDir, got, tc.want)
			}
		})
	}
}

// TestBaselineDescriptions_AcceptsLegacyFormat is the regression guarding the
// silent failure that shortening the worktree baseline would otherwise cause:
// a session persisted by an older jind-ai carries "<main-repo>:<worktree>",
// DescriptionLayer is json:"-" so a daemon restart zeroes it, and comparing
// only against the new format would make Guard 1 read that as "someone already
// upgraded this" — blocking Layer C for the rest of the session's life.
func TestBaselineDescriptions_AcceptsLegacyFormat(t *testing.T) {
	mainRepoDir, worktreeDir := repoFixtures(t)

	legacy := filepath.Base(mainRepoDir) + ":jin-abcd1234"
	current := "jin-abcd1234"

	got := baselineDescriptions(worktreeDir)
	if len(got) != 2 {
		t.Fatalf("baselineDescriptions(worktree) = %q, want both the current and legacy formats", got)
	}
	if got[0] != current {
		t.Errorf("baselineDescriptions()[0] = %q, want the current format %q (index 0 is what we write)", got[0], current)
	}
	if !slices.Contains(got, legacy) {
		t.Errorf("baselineDescriptions() = %q, want it to contain the legacy format %q", got, legacy)
	}

	// A session left on the legacy label must still look untouched.
	sess := &Session{Description: legacy, DescriptionLayer: DescriptionLayerBaseline}
	if sess.descriptionDriftedFrom(got) {
		t.Error("a legacy-format baseline was treated as drifted; Layer C would be blocked forever")
	}

	// The current format, and only genuine drift, keep their old meaning.
	sess.Description = current
	if sess.descriptionDriftedFrom(got) {
		t.Error("the current-format baseline was treated as drifted")
	}
	sess.Description = "refactor the plugin registry"
	if !sess.descriptionDriftedFrom(got) {
		t.Error("a Layer C description was not treated as drifted")
	}

	// Non-worktree dirs have nothing to be compatible with: one entry only.
	if got := baselineDescriptions(mainRepoDir); len(got) != 1 {
		t.Errorf("baselineDescriptions(main repo) = %q, want a single entry", got)
	}
}

// TestDescriptionDriftedFrom_IgnoresNonBaselineLayers verifies the guard only
// speaks about sessions still sitting at Layer A; once a layer is recorded in
// this process, the upgrade path relies on Guard 2 instead.
func TestDescriptionDriftedFrom_IgnoresNonBaselineLayers(t *testing.T) {
	sess := &Session{Description: "anything at all", DescriptionLayer: DescriptionLayerAgentName}
	if sess.descriptionDriftedFrom([]string{"some-baseline"}) {
		t.Error("descriptionDriftedFrom must return false once DescriptionLayer is non-zero")
	}
}

// TestFindRepoRoot verifies the walk-up terminates correctly at both a
// discovered .git and at the filesystem root.
func TestFindRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	nested := filepath.Join(repoRoot, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got, ok := findRepoRoot(nested)
	if !ok {
		t.Fatalf("findRepoRoot(%q) = _, false; want true", nested)
	}
	if filepath.Clean(got) != filepath.Clean(repoRoot) {
		t.Errorf("findRepoRoot(%q) = %q, want %q", nested, got, repoRoot)
	}

	if _, ok := findRepoRoot(""); ok {
		t.Error("findRepoRoot(\"\") should return ok=false")
	}
}
