package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxWorktreeNameAttempts caps the number of suffixes (-2, -3, ...) tried
// during collision resolution. Ten is well above the point where a UUID-based
// prefix could realistically collide — beyond it, something is structurally
// wrong and the caller should hear about it instead of silently spinning.
const maxWorktreeNameAttempts = 10

// WorktreePlacement is the resolved worktree name, branch, and filesystem
// path chosen for a new session.
type WorktreePlacement struct {
	Name   string
	Branch string
	Path   string
}

// deriveWorktreeName picks the worktree name. An explicit override wins;
// otherwise the name is "jin-<first 8 hex of sessionID>".
func deriveWorktreeName(sessionID, override string) string {
	if override != "" {
		return override
	}
	if len(sessionID) < 8 {
		return "jin-" + sessionID
	}
	return "jin-" + sessionID[:8]
}

// deriveBranchName picks the branch name. An explicit override wins;
// otherwise the branch is prefix + worktreeName with the auto "jin-" prefix
// stripped — so a "jin-abcd1234" worktree pairs with a "jin/abcd1234" branch
// instead of the doubled "jin/jin-abcd1234".
func deriveBranchName(worktreeName, prefix, override string) string {
	if override != "" {
		return override
	}
	return prefix + strings.TrimPrefix(worktreeName, "jin-")
}

// worktreeTemplate returns the placement template for a new worktree: the
// user's worktree.base_dir when set, otherwise worktrees/{name} under the state
// dir the Manager was built over.
//
// The state dir is threaded in rather than read from paths.State(), because a
// Manager honours the one it was given for every other artifact it writes —
// hooks-settings.json, and bin/jin, which hookBinaryPath already describes as a
// sibling of worktrees/. Resolving this one globally broke that relationship: a
// Manager built over a different state dir still placed its worktrees in the
// process's real one. The measured cost was two directories per test-suite run
// under the developer's own state dir, but any second Manager pays it.
//
// An empty stateDir yields a relative template, which expandBaseDir rejects.
// That is the intended failure — the alternative is a worktree in whatever the
// working directory happens to be.
func worktreeTemplate(cfgBaseDir, stateDir string) string {
	if cfgBaseDir != "" {
		return cfgBaseDir
	}
	return filepath.Join(stateDir, "worktrees", "{name}")
}

// expandBaseDir expands {name}, {repo}, and ${ENV} in a base_dir template.
// Returns an absolute path, or an error if the template contains an unknown
// {xxx} variable or does not resolve to an absolute path. The default for an
// unset base_dir is worktreeTemplate's, not this function's.
func expandBaseDir(template, worktreeName, repoBasename string) (string, error) {
	expanded := os.ExpandEnv(template)
	replaced := strings.ReplaceAll(expanded, "{name}", worktreeName)
	replaced = strings.ReplaceAll(replaced, "{repo}", repoBasename)

	if idx := strings.Index(replaced, "{"); idx >= 0 {
		if end := strings.Index(replaced[idx:], "}"); end > 0 {
			return "", fmt.Errorf("unknown template variable %q in worktree.base_dir",
				replaced[idx:idx+end+1])
		}
	}

	if !filepath.IsAbs(replaced) {
		return "", fmt.Errorf("worktree.base_dir must resolve to an absolute path, got %q", replaced)
	}
	return replaced, nil
}

// findAvailableWorktreeName tries baseName, baseName-2, baseName-3, ...
// up to maxWorktreeNameAttempts times. collides is called once per candidate
// and should return true if either the worktree directory or the branch would
// clash with an existing artifact.
func findAvailableWorktreeName(baseName string, collides func(candidate string) bool) (string, error) {
	for i := 1; i <= maxWorktreeNameAttempts; i++ {
		candidate := baseName
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d", baseName, i)
		}
		if !collides(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf(
		"could not find an available worktree name after %d attempts (base: %q)",
		maxWorktreeNameAttempts, baseName,
	)
}
