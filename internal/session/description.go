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
// The result is always non-empty.
//
// The worktree case deliberately omits the main repo name even though
// ResolveRepoName can recover it: the session list shows the repo as its own
// field, and a baseline that repeats it spends the session's only identifying
// column on a string every row already shares. What is left — the worktree
// name — is the part that differs per session. Main-repo sessions keep the
// repo name because they have no other token to be named after.
//
// This function only depends on filepath + os.Lstat / os.ReadFile; it never
// invokes the git subprocess. That matters because CreateWithOptions calls
// this on the hot path, and shelling out to git would add tens of milliseconds
// per create.
//
// isWorktree and tmuxHint are accepted for signature stability across the
// three call sites documented in the F001/F004 review notes; the actual
// worktree detection is done by inspecting workDir on disk here so the three
// sites can pass isWorktree=false without silently disabling the branch.
func GenerateBaselineDescription(workDir, currentBranch string, isWorktree bool, tmuxHint string) string {
	_ = isWorktree
	_ = tmuxHint

	return baselineDescription(workDir, currentBranch, false)
}

// legacyBaselineDescription reproduces the pre-master-detail baseline format,
// which prefixed a worktree's label with the main repo name
// ("<main-repo>:<worktree-name>"). It exists ONLY so baselineDescriptions can
// still recognise descriptions written by an older jind-ai; never write its
// result to a session.
func legacyBaselineDescription(workDir string) string {
	return baselineDescription(workDir, "", true)
}

// baselineDescriptions returns every string that counts as "this session's
// description is still the untouched Layer A baseline" for workDir. Index 0 is
// the format written today; any further entry is a historical format that
// sessions created by an older jind-ai still carry on disk.
//
// The set exists because Session.DescriptionLayer is json:"-": a daemon
// restart resets it to DescriptionLayerBaseline while the persisted
// Description keeps whatever an earlier process wrote. Comparing against the
// current format alone would make every pre-existing worktree session look
// drifted, and TryUpgradeDescription's Guard 1 would then refuse to ever
// promote it to Layer C — the F001/F004 failure mode, except silent and
// permanent.
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
// Returns "" when workDir is empty or outside any git repo; callers treat that
// as "no repository to show".
//
// Like the rest of this file it only touches filepath + os.Lstat /
// os.ReadFile, never the git subprocess. It does stat the filesystem, so
// callers must evaluate it BEFORE taking Manager.mu.
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

// resolveMainRepoIfWorktree inspects the ".git" entry at localRoot. When it is
// a regular file containing "gitdir: /path/to/main/.git/worktrees/<name>",
// returns the main repo root plus isWorktree=true. Otherwise (".git" absent,
// a directory, or a malformed pointer) returns (localRoot, false) so the
// caller can treat localRoot as the repo root.
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

// findRepoRoot walks up from dir looking for a directory that contains a .git
// entry (either a directory in the main repo, or a regular file in a
// worktree). Bounded by maxRepoRootWalk as a safety net against symlink loops
// or unexpectedly deep paths.
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
// Description. Larger values represent higher-quality, more informative
// sources. Manager.TryUpgradeDescription only accepts a candidate whose layer
// is strictly greater than the session's current layer, so promotion is
// monotonic within a daemon lifetime.
//
// The zero value (DescriptionLayerBaseline) is what freshly-created sessions
// carry and what daemon restart resets in-memory sessions to, since the layer
// is runtime-only (see Session.DescriptionLayer).
type DescriptionLayer int

const (
	// DescriptionLayerBaseline is Layer A: the repo:branch label produced by
	// GenerateBaselineDescription. Always present, never informative on its own.
	DescriptionLayerBaseline DescriptionLayer = 0
	// DescriptionLayerAgentNameDerived is Layer C-name (weak): the agent
	// wrote a session name but flagged it as externally supplied (Claude Code
	// 2.x nameSource="derived", i.e. round-tripped from the tmux window name
	// jind-ai itself handed the process). Slightly better than nothing —
	// it at least matches CC's own /resume picker — but a genuinely
	// conversation-derived name should still be allowed to overwrite it.
	DescriptionLayerAgentNameDerived DescriptionLayer = 1
	// DescriptionLayerAgentName is Layer C-name (strong): an agent-supplied
	// session name whose source is NOT the "derived" hint round-trip
	// (e.g. Claude Code has renamed the session from the conversation topic).
	// Available as early as the SessionStart hook when the agent already had
	// a strong name; otherwise arrives on a later hook once the agent
	// re-classifies the name field.
	DescriptionLayerAgentName DescriptionLayer = 2
	// DescriptionLayerTranscript is Layer C-transcript: the first meaningful
	// user prompt mined from the agent transcript, only available after the
	// first user turn has been flushed to disk. Not used by the Claude Code
	// adapter (see internal/agent/claude/description.go — the CC enhancer
	// stops at Layer C-name because CC produces its own topic-derived name).
	// Reserved for future adapters that lack a native session-name field.
	DescriptionLayerTranscript DescriptionLayer = 3
)

// descriptionDriftedFrom reports whether the session's Description has moved
// off every accepted Layer A baseline while DescriptionLayer is still zero.
//
// That combination means the drift did not come from this daemon process:
// most commonly a restart lost the runtime layer while the persisted
// Description still carries a Layer C value written earlier. It is the signal
// TryUpgradeDescription's Guard 1 refuses to overwrite, since there is no way
// to tell whether an incoming candidate is better than what is already there.
//
// baselines is a set rather than a single string so a description written in
// an older baseline format is not mistaken for such a Layer C value; see
// baselineDescriptions.
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
