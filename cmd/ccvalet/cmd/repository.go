package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/repository"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
	"golang.org/x/term"
)

var repositoryCmd = &cobra.Command{
	Use:     "repository",
	Aliases: []string{"repo"},
	Short:   "Manage repositories",
	Long:    `Add, list, and remove repositories for worktree management.`,
}

var (
	repoName       string
	repoBaseBranch string
	repoSetup      []string
)

var repoAddCmd = &cobra.Command{
	Use:   "add <path>",
	Short: "Add a repository",
	Long:  `Register a git repository for worktree management. Repository name is automatically detected from git remote URL.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		registry := repository.NewRegistry(configMgr)

		name, err := registry.Add(path, repoName, repoBaseBranch, repoSetup)
		if err != nil {
			return fmt.Errorf("failed to add repository: %w", err)
		}

		fmt.Printf("Repository '%s' added successfully\n", name)
		return nil
	},
}

var repoListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List repositories",
	Long:    `List all registered repositories.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		registry := repository.NewRegistry(configMgr)
		repos := registry.List()

		if len(repos) == 0 {
			fmt.Println("No repositories registered. Add one with: ccvalet repository add <path>")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPATH\tSETUP")
		for _, r := range repos {
			setup := "-"
			if len(r.Setup) > 0 {
				setup = fmt.Sprintf("%d commands", len(r.Setup))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, r.Path, setup)
		}
		w.Flush()
		return nil
	},
}

var repoRemoveCmd = &cobra.Command{
	Use:               "remove <name>",
	Aliases:           []string{"rm"},
	Short:             "Remove a repository",
	Long:              `Remove a registered repository.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRepoNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		registry := repository.NewRegistry(configMgr)

		if err := registry.Remove(name); err != nil {
			return fmt.Errorf("failed to remove repository: %w", err)
		}

		fmt.Printf("Repository '%s' removed\n", name)
		return nil
	},
}

var repoShowCmd = &cobra.Command{
	Use:               "show <name>",
	Short:             "Show repository details",
	Long:              `Show detailed information about a registered repository.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRepoNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		registry := repository.NewRegistry(configMgr)
		repo := registry.Get(name)
		if repo == nil {
			return fmt.Errorf("repository '%s' not found", name)
		}

		fmt.Printf("Name: %s\n", repo.Name)
		fmt.Printf("Path: %s\n", repo.Path)
		if repo.BaseBranch != "" {
			fmt.Printf("Base Branch: %s\n", repo.BaseBranch)
		}

		if len(repo.Setup) > 0 {
			fmt.Println("Setup Commands:")
			for i, cmd := range repo.Setup {
				fmt.Printf("  %d. %s\n", i+1, cmd)
			}
		} else {
			fmt.Println("Setup: (none)")
		}

		// リモートURLを取得
		remoteURL, err := registry.GetRemoteURL(name)
		if err == nil && remoteURL != "" {
			fmt.Printf("Remote: %s\n", remoteURL)
		}

		return nil
	},
}

var repoUpdateCmd = &cobra.Command{
	Use:               "update <name>",
	Short:             "Update repository settings",
	Long:              `Update setup commands or script for a registered repository.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRepoNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		registry := repository.NewRegistry(configMgr)

		if err := registry.Update(name, repoBaseBranch, repoSetup); err != nil {
			return fmt.Errorf("failed to update repository: %w", err)
		}

		fmt.Printf("Repository '%s' updated\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(repositoryCmd)

	// add command
	repositoryCmd.AddCommand(repoAddCmd)
	repoAddCmd.Flags().StringVarP(&repoName, "name", "n", "", "Repository name (default: directory name)")
	repoAddCmd.Flags().StringVarP(&repoBaseBranch, "base-branch", "b", "", "Default base branch for new worktrees (e.g., main, develop)")
	repoAddCmd.Flags().StringArrayVarP(&repoSetup, "setup", "s", nil, "Setup command (can be specified multiple times)")

	// list command
	repositoryCmd.AddCommand(repoListCmd)

	// remove command
	repositoryCmd.AddCommand(repoRemoveCmd)

	// show command
	repositoryCmd.AddCommand(repoShowCmd)

	// update command
	repositoryCmd.AddCommand(repoUpdateCmd)
	repoUpdateCmd.Flags().StringVarP(&repoBaseBranch, "base-branch", "b", "", "Default base branch for new worktrees (e.g., main, develop)")
	repoUpdateCmd.Flags().StringArrayVarP(&repoSetup, "setup", "s", nil, "Setup command (can be specified multiple times)")

	// script commands
	repositoryCmd.AddCommand(repoEditScriptCmd)
	repositoryCmd.AddCommand(repoShowScriptCmd)
	repositoryCmd.AddCommand(repoRemoveScriptCmd)
}

// getConfigDir returns the configuration directory path
func getConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccvalet")
}

var repoEditScriptCmd = &cobra.Command{
	Use:               "edit-script <name>",
	Short:             "Edit setup script for a repository",
	Long:              `Open the setup script in your editor. If the script doesn't exist, a template will be created.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRepoNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		// リポジトリが登録されているか確認
		registry := repository.NewRegistry(configMgr)
		if registry.Get(name) == nil {
			return fmt.Errorf("repository '%s' not found", name)
		}

		wtMgr := worktree.NewManager(configMgr)
		scriptPath := wtMgr.GetSetupScriptPath(name)

		// ディレクトリを作成
		scriptDir := filepath.Dir(scriptPath)
		if err := os.MkdirAll(scriptDir, 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		// ファイルが存在しない場合はテンプレートを作成
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			if err := writeSetupScriptTemplate(scriptPath); err != nil {
				return fmt.Errorf("failed to create template: %w", err)
			}
			fmt.Printf("Created template: %s\n", scriptPath)
		}

		// エディタを起動
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}

		// ターミナル状態を保存（エディタ終了後に復元するため）
		oldState, err := term.GetState(int(os.Stdin.Fd()))
		if err != nil {
			oldState = nil
		}

		editorCmd := exec.Command(editor, scriptPath)
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		runErr := editorCmd.Run()

		// ターミナル状態を復元
		if oldState != nil {
			term.Restore(int(os.Stdin.Fd()), oldState)
		}

		if runErr != nil {
			return fmt.Errorf("editor failed: %w", runErr)
		}

		// 実行権限を付与
		if err := os.Chmod(scriptPath, 0755); err != nil {
			return fmt.Errorf("failed to set execute permission: %w", err)
		}

		fmt.Printf("Setup script saved: %s\n", scriptPath)
		return nil
	},
}

var repoShowScriptCmd = &cobra.Command{
	Use:               "show-script <name>",
	Short:             "Show setup script content",
	Long:              `Display the content of the setup script for a repository.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRepoNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		wtMgr := worktree.NewManager(configMgr)
		scriptPath := wtMgr.GetSetupScriptPath(name)

		content, err := os.ReadFile(scriptPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("setup script not found for '%s'\nRun 'ccvalet repo edit-script %s' to create one", name, name)
			}
			return fmt.Errorf("failed to read script: %w", err)
		}

		fmt.Printf("# %s\n", scriptPath)
		fmt.Println(string(content))
		return nil
	},
}

var repoRemoveScriptCmd = &cobra.Command{
	Use:               "remove-script <name>",
	Aliases:           []string{"rm-script"},
	Short:             "Remove setup script",
	Long:              `Remove the setup script for a repository.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRepoNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		wtMgr := worktree.NewManager(configMgr)
		scriptPath := wtMgr.GetSetupScriptPath(name)

		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return fmt.Errorf("setup script not found for '%s'", name)
		}

		if err := os.Remove(scriptPath); err != nil {
			return fmt.Errorf("failed to remove script: %w", err)
		}

		fmt.Printf("Setup script removed: %s\n", scriptPath)
		return nil
	},
}

// writeSetupScriptTemplate はセットアップスクリプトのテンプレートを作成する
func writeSetupScriptTemplate(path string) error {
	template := `#!/bin/bash
# ccvalet worktree setup script
#
# This script runs automatically when a new worktree is created.
# Working directory is the newly created worktree.
#
# Available environment variables:
#   CCVALET_SOURCE_REPO  - Source repository path (original repo)
#   CCVALET_WORKTREE     - Created worktree path (also current directory)
#   CCVALET_BRANCH       - Branch name
#   CCVALET_REPO_NAME    - Repository name

set -e

# Example: Copy local settings from source repo
# cp "$CCVALET_SOURCE_REPO/.claude/settings.local.json" .claude/ 2>/dev/null || true

# Example: Install dependencies
# pnpm install
# npm install

echo "Setup completed for $CCVALET_REPO_NAME (branch: $CCVALET_BRANCH)"
`
	return os.WriteFile(path, []byte(template), 0755)
}
