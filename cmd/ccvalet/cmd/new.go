package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new Claude Code session",
	Long: `Create a new Claude Code session and start it in background.

Usage (v3 design):
  ccvalet session new --repo <repo> --workdir <path> --branch <branch>
  ccvalet session new --repo <repo> --new-worktree --branch <branch> [--new-branch] [--base <base>]

Examples:
  # Use repository main directory
  ccvalet session new --repo myrepo --workdir ~/repos/myrepo --branch main

  # Use existing worktree
  ccvalet session new --repo myrepo --workdir ~/.ccvalet/worktrees/myrepo/feature-x --branch feature-x

  # Create new worktree with existing branch
  ccvalet session new --repo myrepo --new-worktree --branch develop

  # Create new worktree with new branch
  ccvalet session new --repo myrepo --new-worktree --branch feature-y --new-branch --base main

For interactive session creation, use 'ccvalet ui' (TUI).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 必須フラグの取得
		repoName, _ := cmd.Flags().GetString("repo")
		workDir, _ := cmd.Flags().GetString("workdir")
		branch, _ := cmd.Flags().GetString("branch")

		// オプションフラグの取得
		name, _ := cmd.Flags().GetString("name")
		hostID, _ := cmd.Flags().GetString("host")
		newWorktree, _ := cmd.Flags().GetBool("new-worktree")
		newBranch, _ := cmd.Flags().GetBool("new-branch")
		baseBranch, _ := cmd.Flags().GetString("base")
		worktreeName, _ := cmd.Flags().GetString("worktree")
		noStart, _ := cmd.Flags().GetBool("no-start")
		promptName, _ := cmd.Flags().GetString("prompt")
		promptArgs, _ := cmd.Flags().GetString("args")

		isRemote := hostID != "" && hostID != "local"

		// バリデーション: リポジトリは必須
		if repoName == "" {
			return fmt.Errorf("--repo is required")
		}

		// バリデーション: ブランチは必須
		if branch == "" {
			return fmt.Errorf("--branch is required")
		}

		// バリデーション: --workdir か --new-worktree のどちらかが必要
		if workDir == "" && !newWorktree {
			return fmt.Errorf("either --workdir or --new-worktree is required")
		}

		// バリデーション: --workdir と --new-worktree は排他
		if workDir != "" && newWorktree {
			return fmt.Errorf("--workdir and --new-worktree cannot be used together")
		}

		var actualWorktreeName string

		if isRemote {
			// リモートホストの場合: リポジトリ検証とworktree作成はslave側に委譲
			if newWorktree && workDir == "" {
				// worktree作成はdaemon(slave)側で行うので、workDirは空のままでOK
				fmt.Printf("Creating session on host '%s' (worktree will be created on remote)...\n", hostID)
			}
		} else {
			// ローカルの場合: 従来通りの処理
			configMgr, err := config.NewManager(getConfigDir())
			if err != nil {
				return fmt.Errorf("failed to initialize config: %w", err)
			}

			// バリデーション: リポジトリが登録されているか確認
			if configMgr.GetRepository(repoName) == nil {
				return fmt.Errorf("repository '%s' not found. Register it first with: ccvalet repo add <path>", repoName)
			}

			// 新規worktreeモードの場合
			if newWorktree {
				wtMgr := worktree.NewManager(configMgr)

				fmt.Printf("Creating worktree for %s/%s...\n", repoName, branch)
				wt, wtName, err := wtMgr.CreateWithOptions(worktree.CreateOptions{
					RepoName:     repoName,
					Branch:       branch,
					NewBranch:    newBranch,
					BaseBranch:   baseBranch,
					WorktreeName: worktreeName,
				})
				if err != nil {
					return fmt.Errorf("failed to create worktree: %w", err)
				}
				fmt.Printf("Created worktree at: %s\n", wt.Path)
				workDir = wt.Path
				actualWorktreeName = wtName
			}
		}

		client := daemon.NewClient(getSocketPath())
		s, err := client.NewWithOptions(daemon.NewOptions{
			Name:          name,
			WorkDir:       workDir,
			Start:         !noStart,
			PromptName:    promptName,
			PromptArgs:    promptArgs,
			Repository:    repoName,
			Branch:        branch,
			BaseBranch:    baseBranch,
			NewBranch:     newBranch,
			IsNewWorktree: newWorktree,
			WorktreeName:  actualWorktreeName,
			HostID:        hostID,
		})
		if err != nil {
			return err
		}

		fmt.Printf("Created session: %s (%s)\n", s.Name, s.ID)
		fmt.Printf("Working directory: %s\n", s.WorkDir)
		fmt.Printf("Repository: %s (branch: %s)\n", s.Repository, s.Branch)
		if s.WorktreeName != "" {
			fmt.Printf("Worktree: %s\n", s.WorktreeName)
		}
		if promptName != "" {
			fmt.Printf("Prompt: %s\n", promptName)
		}
		fmt.Printf("Status: %s\n", s.Status)
		fmt.Printf("\nTo attach: ccvalet session attach %s\n", s.ID)
		return nil
	},
}

func init() {
	sessionCmd.AddCommand(newCmd)

	// 必須フラグ
	newCmd.Flags().StringP("repo", "r", "", "Repository name (required)")
	newCmd.Flags().StringP("branch", "b", "", "Branch name (required)")

	// ワークディレクトリ指定（既存ディレクトリ使用時）
	newCmd.Flags().StringP("workdir", "d", "", "Working directory (repository main or existing worktree)")

	// 新規worktree作成モード
	newCmd.Flags().Bool("new-worktree", false, "Create a new worktree")
	newCmd.Flags().Bool("new-branch", false, "Create a new branch (error if exists)")
	newCmd.Flags().String("base", "", "Base branch for new branch (e.g., main, develop)")
	newCmd.Flags().StringP("worktree", "w", "", "Worktree name (default: branch name)")

	// ホスト指定
	newCmd.Flags().StringP("host", "H", "", "Target host (default: local)")

	// オプション
	newCmd.Flags().StringP("name", "n", "", "Session name (default: repo/branch)")
	newCmd.Flags().Bool("no-start", false, "Don't start the session immediately")

	// プロンプト
	newCmd.Flags().StringP("prompt", "p", "", "Prompt template name for auto-injection")
	newCmd.Flags().StringP("args", "a", "", "Arguments for prompt template (${args} variable)")

	// フラグ補完
	newCmd.RegisterFlagCompletionFunc("repo", completeRepoNames)
	newCmd.RegisterFlagCompletionFunc("branch", completeBranchNames)
	newCmd.RegisterFlagCompletionFunc("base", completeBranchNames)
	newCmd.RegisterFlagCompletionFunc("workdir", completeWorktreePaths)
	newCmd.RegisterFlagCompletionFunc("prompt", completePromptNames)
}

func getDataDir() string {
	home, _ := os.UserHomeDir()
	return home + "/.ccvalet/sessions"
}
