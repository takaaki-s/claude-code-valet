package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// maxRepoRootWalk caps the walk-up loop in findRepoRoot to avoid pathological
// deeply nested paths pinning CPU inside the create hot path.
const maxRepoRootWalk = 100

// GenerateBaselineDescription assembles a repo-derived label for a session.
//
// Format:
//   - main repo:     "<repo>[:<branch>][:<subpath>]"
//   - worktree:      "<worktree-name>[:<branch>][:<subpath>]"
//   - non-git dir:   "<dir-basename>"
//   - empty input:   "session"
//
// The result is always non-empty. The worktree case omits the main repo name on
// purpose: the session list shows the repo in its own field, so repeating it
// would spend the session's only identifying column on a string every row
// already shares.
//
// It never shells out to git — CreateWithOptions calls this on the hot path —
// and it detects a worktree by inspecting workDir on disk, so a caller may pass
// isWorktree=false without silently disabling that branch.
func GenerateBaselineDescription(workDir, currentBranch string, isWorktree bool, tmuxHint string) string {
	_ = isWorktree
	_ = tmuxHint

	return baselineDescription(workDir, currentBranch, false)
}

// legacyBaselineDescription reproduces the pre-master-detail baseline format,
// which prefixed a worktree's label with the main repo name. It exists ONLY so
// baselineDescriptions can recognise descriptions written by an older jind-ai;
// never write its result to a session.
func legacyBaselineDescription(workDir string) string {
	return baselineDescription(workDir, "", true)
}

// baselineDescriptions returns every string that counts as "this session's
// description is still the untouched Layer A baseline" for workDir. Index 0 is
// the format written today; any further entry is a historical format that
// sessions on disk may still carry.
//
// The historical entries are needed because Session.DescriptionLayer is
// json:"-": a daemon restart resets it to Baseline while the persisted
// Description keeps an older format. Comparing against today's format alone
// would make every pre-existing worktree session look drifted, and Guard 1 in
// TryUpgradeDescription would then refuse to promote it, permanently.
func baselineDescriptions(workDir string) []string {
	current := GenerateBaselineDescription(workDir, "", false, "")
	legacy := legacyBaselineDescription(workDir)
	if legacy == current {
		return []string{current}
	}
	return []string{current, legacy}
}

// baselineDescription is the shared body behind the current and legacy
// generators. includeMainRepo selects the only axis on which the two formats
// differ.
func baselineDescription(workDir, currentBranch string, includeMainRepo bool) string {
	if workDir == "" {
		return "session"
	}

	cleanWorkDir := filepath.Clean(workDir)

	localRoot, ok := findRepoRoot(cleanWorkDir)
	if !ok {
		return filepath.Base(cleanWorkDir)
	}
	localRoot = filepath.Clean(localRoot)

	parts := make([]string, 0, 4)

	// A worktree's ".git" is a regular file pointing at the main repo. The
	// legacy format resolved it and prepended the original repo dir name to
	// the worktree directory basename (e.g. "jind-ai:jin-da43e8da").
	if includeMainRepo {
		if mainRoot, isWt := resolveMainRepoIfWorktree(localRoot); isWt {
			parts = append(parts, filepath.Base(mainRoot))
		}
	}
	parts = append(parts, filepath.Base(localRoot))

	if currentBranch != "" {
		parts = append(parts, currentBranch)
	}

	if rel, err := filepath.Rel(localRoot, cleanWorkDir); err == nil && rel != "." && rel != "" {
		parts = append(parts, rel)
	}

	return strings.Join(parts, ":")
}

// ResolveRepoName returns the human-facing repository name for workDir: the
// basename of the repo root, or — when workDir sits inside a git worktree —
// the basename of the *main* repo the worktree was created from. A worktree
// directory is named after the session that created it ("jin-b63188fe"), which
// tells a reader nothing about which project they are looking at.
//
// Returns "" when workDir is empty or outside any git repo. It stats the
// filesystem, so callers must evaluate it BEFORE taking Manager.mu.
func ResolveRepoName(workDir string) string {
	if workDir == "" {
		return ""
	}
	localRoot, ok := findRepoRoot(filepath.Clean(workDir))
	if !ok {
		return ""
	}
	localRoot = filepath.Clean(localRoot)
	if mainRoot, isWorktree := resolveMainRepoIfWorktree(localRoot); isWorktree {
		return filepath.Base(mainRoot)
	}
	return filepath.Base(localRoot)
}

// resolveMainRepoIfWorktree reads the ".git" entry at localRoot. A regular file
// holding "gitdir: /path/to/main/.git/worktrees/<name>" means a worktree and
// yields the main repo root; anything else (absent, a directory, malformed)
// makes localRoot the repo root.
func resolveMainRepoIfWorktree(localRoot string) (mainRoot string, isWorktree bool) {
	gitPath := filepath.Join(localRoot, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil || !fi.Mode().IsRegular() {
		return localRoot, false
	}
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return localRoot, false
	}
	raw := strings.TrimSpace(string(content))
	if !strings.HasPrefix(raw, "gitdir: ") {
		return localRoot, false
	}
	gitdir := strings.TrimPrefix(raw, "gitdir: ")
	// Require the canonical worktree marker layout so a garbled or truncated
	// gitdir (e.g. "/nowhere") does not resolve to filesystem root.
	if !strings.Contains(gitdir, "/.git/worktrees/") {
		return localRoot, false
	}
	// gitdir layout: <main-repo>/.git/worktrees/<name> — three Dir() calls to
	// reach the main repo root.
	mainRepo := filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
	if mainRepo == "" || mainRepo == "." || mainRepo == "/" {
		return localRoot, false
	}
	return mainRepo, true
}

// findRepoRoot walks up from dir looking for a .git entry (a directory in the
// main repo, a regular file in a worktree). Bounded by maxRepoRootWalk against
// symlink loops and unexpectedly deep paths.
func findRepoRoot(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	cur := dir
	for i := 0; i < maxRepoRootWalk; i++ {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
	return "", false
}

// DescriptionLayer classifies the source that last wrote a session's
// Description. Larger values are better sources: TryUpgradeDescription only
// accepts a candidate whose layer is strictly greater, so promotion is
// monotonic within a daemon lifetime. The layer is runtime-only (see
// Session.DescriptionLayer), so a daemon restart resets it to zero.
type DescriptionLayer int

const (
	// DescriptionLayerBaseline is Layer A: the repo:branch label produced by
	// GenerateBaselineDescription. Always present, never informative on its own.
	DescriptionLayerBaseline DescriptionLayer = 0
	// DescriptionLayerAgentNameDerived is Layer C-name (weak): the agent wrote
	// a session name but flagged it as externally supplied (Claude Code 2.x
	// nameSource="derived", i.e. round-tripped from the tmux window name jind-ai
	// handed the process). A genuinely conversation-derived name may overwrite it.
	DescriptionLayerAgentNameDerived DescriptionLayer = 1
	// DescriptionLayerAgentName is Layer C-name (strong): an agent-supplied name
	// whose source is NOT the "derived" round-trip. Available as early as the
	// SessionStart hook, or on a later hook once the agent re-classifies the name.
	DescriptionLayerAgentName DescriptionLayer = 2
	// DescriptionLayerTranscript is Layer C-transcript: the first meaningful user
	// prompt mined from the agent transcript. Unused by the Claude Code adapter,
	// which stops at C-name because CC produces its own topic-derived name;
	// reserved for adapters with no native session-name field.
	DescriptionLayerTranscript DescriptionLayer = 3
)

// descriptionDriftedFrom reports whether the session's Description has moved
// off every accepted Layer A baseline while DescriptionLayer is still zero.
//
// That combination means the drift did not come from this daemon process —
// most often a restart lost the runtime layer while the persisted Description
// still carries a Layer C value. Guard 1 in TryUpgradeDescription refuses to
// overwrite it, since nothing can tell whether a candidate is better.
func (s *Session) descriptionDriftedFrom(baselines []string) bool {
	if s.DescriptionLayer != DescriptionLayerBaseline {
		return false
	}
	return !slices.Contains(baselines, s.Description)
}

// DescriptionEnhancer produces an agent-specific "Layer C" description upgrade
// from live session state (e.g., the first user prompt in a transcript, or a
// session name Claude Code writes to disk at start-up).
// Implementations must be side-effect free and safe to call concurrently.
type DescriptionEnhancer interface {
	// TryGenerate returns a candidate description together with the layer it
	// belongs to. Returns ("", 0, false) when no useful signal is available
	// yet (e.g., the transcript has no meaningful first user turn and the
	// agent has not yet named the session). Must not mutate sess.
	TryGenerate(sess *Session) (string, DescriptionLayer, bool)
}
