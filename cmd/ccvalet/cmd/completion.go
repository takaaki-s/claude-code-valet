package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

// completeSessionNames returns session names for shell completion
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	client := daemon.NewClient(getSocketPath())
	sessions, err := client.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, toComplete) {
			names = append(names, s.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeRepoNames returns repository names for shell completion
func completeRepoNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	configMgr, err := config.NewManager(getConfigDir())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	repos := configMgr.GetRepositories()
	var names []string
	for _, r := range repos {
		if strings.HasPrefix(r.Name, toComplete) {
			names = append(names, r.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeRepoNameArg returns repository names for the first argument only
func completeRepoNameArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeRepoNames(cmd, args, toComplete)
}

// completePromptNames returns prompt template names for shell completion
func completePromptNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	promptDir := filepath.Join(getConfigDir(), "prompts")
	entries, err := os.ReadDir(promptDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Remove extension
		if ext := filepath.Ext(name); ext != "" {
			name = strings.TrimSuffix(name, ext)
		}
		if strings.HasPrefix(name, toComplete) {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeBranchNames returns branch names for shell completion
func completeBranchNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	repoName, _ := cmd.Flags().GetString("repo")
	if repoName == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	configMgr, err := config.NewManager(getConfigDir())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	repo := configMgr.GetRepository(repoName)
	if repo == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	wtMgr := worktree.NewManager(configMgr)
	branches := wtMgr.ListBranches(repo.Path)

	var names []string
	for _, b := range branches {
		if strings.HasPrefix(b, toComplete) {
			names = append(names, b)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeWorktreePaths returns worktree paths for shell completion
func completeWorktreePaths(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	repoName, _ := cmd.Flags().GetString("repo")
	if repoName == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	configMgr, err := config.NewManager(getConfigDir())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	repo := configMgr.GetRepository(repoName)
	if repo == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	wtMgr := worktree.NewManager(configMgr)
	worktrees, err := wtMgr.List(repoName)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var paths []string
	// Add repository main path
	if strings.HasPrefix(repo.Path, toComplete) {
		paths = append(paths, repo.Path)
	}
	// Add worktree paths
	for _, wt := range worktrees {
		if strings.HasPrefix(wt.Path, toComplete) {
			paths = append(paths, wt.Path)
		}
	}
	return paths, cobra.ShellCompDirectiveNoFileComp
}

// completeWorktreeCreateArgs returns completions for worktree create command arguments
// Args: <path> <repo-name> <branch>
func completeWorktreeCreateArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		// path - use default directory completion
		return nil, cobra.ShellCompDirectiveDefault
	case 1:
		// repo-name
		return completeRepoNames(cmd, nil, toComplete)
	case 2:
		// branch - need to get repo from args[1]
		repoName := args[1]
		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		repo := configMgr.GetRepository(repoName)
		if repo == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		wtMgr := worktree.NewManager(configMgr)
		branches := wtMgr.ListBranches(repo.Path)
		var names []string
		for _, b := range branches {
			if strings.HasPrefix(b, toComplete) {
				names = append(names, b)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeWorktreeListArgs returns completions for worktree list command arguments
// Args: [repo-name]
func completeWorktreeListArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeRepoNames(cmd, nil, toComplete)
}

// completeWorktreeDeleteArgs returns completions for worktree delete command arguments
// Args: <repo-name> <worktree-name>
func completeWorktreeDeleteArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		// repo-name
		return completeRepoNames(cmd, nil, toComplete)
	case 1:
		// worktree-name - list worktree names (directory names)
		repoName := args[0]
		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		wtMgr := worktree.NewManager(configMgr)
		worktrees, err := wtMgr.List(repoName)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, wt := range worktrees {
			name := filepath.Base(wt.Path)
			if !wt.IsMain && strings.HasPrefix(name, toComplete) {
				names = append(names, name)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeWorktreeBaseBranch returns completions for --base flag in worktree create
func completeWorktreeBaseBranch(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) < 2 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	repoName := args[1]
	configMgr, err := config.NewManager(getConfigDir())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	repo := configMgr.GetRepository(repoName)
	if repo == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	wtMgr := worktree.NewManager(configMgr)
	branches := wtMgr.ListBranches(repo.Path)
	var names []string
	for _, b := range branches {
		if strings.HasPrefix(b, toComplete) {
			names = append(names, b)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
