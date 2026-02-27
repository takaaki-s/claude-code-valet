package worktree

import (
	"fmt"
	"os/exec"
	"strings"
)

// HasUncommittedChanges は未コミットの変更があるか確認する（untracked filesは除外）
func HasUncommittedChanges(workDir string) (bool, error) {
	cmd := exec.Command("git", "-C", workDir, "status", "--porcelain", "-uno")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check git status: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// GetCurrentBranch は現在のブランチ名を取得する
func GetCurrentBranch(workDir string) (string, error) {
	cmd := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// CheckoutBranch は指定されたブランチにチェックアウトする
func CheckoutBranch(workDir, branch string) error {
	cmd := exec.Command("git", "-C", workDir, "checkout", branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to checkout branch %s: %s", branch, string(output))
	}
	return nil
}

// CreateAndCheckoutBranch は新しいブランチを作成してチェックアウトする
func CreateAndCheckoutBranch(workDir, branch, baseBranch string) error {
	var cmd *exec.Cmd
	if baseBranch != "" {
		base := baseBranch
		if !strings.HasPrefix(baseBranch, "origin/") && !strings.HasPrefix(baseBranch, "refs/") {
			checkCmd := exec.Command("git", "-C", workDir, "rev-parse", "--verify", "origin/"+baseBranch)
			if checkCmd.Run() == nil {
				base = "origin/" + baseBranch
			}
		}
		cmd = exec.Command("git", "-C", workDir, "checkout", "-b", branch, base)
	} else {
		cmd = exec.Command("git", "-C", workDir, "checkout", "-b", branch)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create and checkout branch %s: %s", branch, string(output))
	}
	return nil
}

// FetchRemoteBranch はリモートブランチをfetchする
func FetchRemoteBranch(workDir, branch string) error {
	cmd := exec.Command("git", "-C", workDir, "fetch", "origin", branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to fetch branch %s: %s", branch, string(output))
	}
	return nil
}

// BranchExists はブランチが存在するか確認する（ローカル + リモート）
func BranchExists(repoPath, branch string) bool {
	cmd := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err := cmd.Run(); err == nil {
		return true
	}
	cmd = exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return cmd.Run() == nil
}

// ListBranches はローカル+リモートブランチの一覧を返す
func ListBranches(repoPath string) []string {
	var branches []string

	cmd := exec.Command("git", "-C", repoPath, "branch", "--format=%(refname:short)")
	if output, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" {
				branches = append(branches, line)
			}
		}
	}

	cmd = exec.Command("git", "-C", repoPath, "branch", "-r", "--format=%(refname:short)")
	if output, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" && strings.HasPrefix(line, "origin/") {
				branch := strings.TrimPrefix(line, "origin/")
				if branch != "HEAD" {
					branches = append(branches, branch)
				}
			}
		}
	}

	seen := make(map[string]bool)
	var unique []string
	for _, b := range branches {
		if !seen[b] {
			seen[b] = true
			unique = append(unique, b)
		}
	}

	return unique
}
