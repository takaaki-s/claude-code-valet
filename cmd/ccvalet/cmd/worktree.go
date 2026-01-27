package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

var worktreeCmd = &cobra.Command{
	Use:     "worktree",
	Aliases: []string{"wt"},
	Short:   "Manage git worktrees",
	Long:    `Create, list, and delete git worktrees for repositories.`,
}

var (
	wtNewBranch  bool
	wtBaseBranch string
	wtForce      bool
)

var wtCreateCmd = &cobra.Command{
	Use:               "create <path> <repo-name> <branch>",
	Short:             "Create a new worktree",
	Long:              `Create a new git worktree at the specified path for the given repository and branch.`,
	Args:              cobra.ExactArgs(3),
	ValidArgsFunction: completeWorktreeCreateArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		repoName := args[1]
		branch := args[2]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		wtMgr := worktree.NewManager(configMgr)

		wt, err := wtMgr.Create(path, repoName, branch, wtNewBranch, wtBaseBranch)
		if err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}

		fmt.Printf("Worktree created: %s\n", wt.Path)
		return nil
	},
}

var wtListCmd = &cobra.Command{
	Use:               "list [repo-name]",
	Aliases:           []string{"ls"},
	Short:             "List worktrees",
	Long:              `List all worktrees from git. Optionally filter by repository name.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeWorktreeListArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		wtMgr := worktree.NewManager(configMgr)

		var worktrees []worktree.Worktree
		if len(args) > 0 {
			worktrees, err = wtMgr.List(args[0])
		} else {
			worktrees, err = wtMgr.ListAll()
		}
		if err != nil {
			return fmt.Errorf("failed to list worktrees: %w", err)
		}

		if len(worktrees) == 0 {
			fmt.Println("No worktrees found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "REPO\tBRANCH\tPATH\tTYPE")
		for _, wt := range worktrees {
			branch := wt.Branch
			if wt.IsDetached {
				branch = "(detached)"
			}
			wtType := "worktree"
			if wt.IsMain {
				wtType = "main"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				wt.RepoName,
				branch,
				truncatePath(wt.Path, 50),
				wtType,
			)
		}
		w.Flush()
		return nil
	},
}

var wtDeleteCmd = &cobra.Command{
	Use:               "delete <repo-name> <worktree-name>",
	Aliases:           []string{"rm", "remove"},
	Short:             "Delete a worktree",
	Long:              `Delete a git worktree for the specified repository and worktree name.`,
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeWorktreeDeleteArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		repoName := args[0]
		worktreeName := args[1]

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		wtMgr := worktree.NewManager(configMgr)

		// worktree名からworktreeを検索
		wt, err := wtMgr.GetByName(repoName, worktreeName)
		if err != nil {
			return fmt.Errorf("worktree not found: %w", err)
		}

		if wt.IsMain {
			return fmt.Errorf("cannot delete main worktree")
		}

		if err := wtMgr.Delete(repoName, wt.Path, wtForce); err != nil {
			return fmt.Errorf("failed to delete worktree: %w", err)
		}

		fmt.Printf("Worktree deleted: %s\n", wt.Path)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(worktreeCmd)

	// create command
	worktreeCmd.AddCommand(wtCreateCmd)
	wtCreateCmd.Flags().BoolVarP(&wtNewBranch, "new-branch", "b", false, "Create a new branch")
	wtCreateCmd.Flags().StringVar(&wtBaseBranch, "base", "", "Base branch for new branch (e.g., main, develop)")
	wtCreateCmd.RegisterFlagCompletionFunc("base", completeWorktreeBaseBranch)

	// list command
	worktreeCmd.AddCommand(wtListCmd)

	// delete command
	worktreeCmd.AddCommand(wtDeleteCmd)
	wtDeleteCmd.Flags().BoolVarP(&wtForce, "force", "f", false, "Force delete even with uncommitted changes")
}
