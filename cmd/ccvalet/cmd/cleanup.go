package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

var (
	cleanupWorktree bool
	cleanupDryRun   bool
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean up sessions and worktrees",
	Long: `Clean up stopped sessions and their associated worktrees.

Examples:
  ccvalet cleanup stopped              # Delete all stopped sessions
  ccvalet cleanup stopped --worktree   # Delete stopped sessions and their worktrees
  ccvalet cleanup stopped --dry-run    # Show what would be deleted`,
}

var cleanupStoppedCmd = &cobra.Command{
	Use:   "stopped",
	Short: "Delete all stopped sessions",
	Long: `Delete all stopped sessions and optionally their associated worktrees.

This command finds all sessions with status "stopped" and deletes them.
If --worktree flag is specified, also deletes the associated git worktrees.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := daemon.NewClient(getSocketPath())

		// Get all sessions
		sessions, err := client.List()
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}

		// Filter stopped sessions
		var stoppedSessions []session.Info
		for _, s := range sessions {
			if s.Status == session.StatusStopped {
				stoppedSessions = append(stoppedSessions, s)
			}
		}

		if len(stoppedSessions) == 0 {
			fmt.Println("No stopped sessions found.")
			return nil
		}

		fmt.Printf("Found %d stopped session(s):\n", len(stoppedSessions))
		for _, s := range stoppedSessions {
			fmt.Printf("  - %s (%s)\n", s.Name, s.ID[:8])
			if s.Repository != "" && s.Branch != "" {
				fmt.Printf("    Repository: %s, Branch: %s\n", s.Repository, s.Branch)
			}
			if s.WorktreeName != "" {
				fmt.Printf("    Worktree: %s\n", s.WorktreeName)
			}
		}

		if cleanupDryRun {
			fmt.Println("\nDry run mode - no changes made.")
			return nil
		}

		fmt.Println()

		// Initialize worktree manager if needed
		var wtMgr *worktree.Manager
		if cleanupWorktree {
			configMgr, err := config.NewManager(getConfigDir())
			if err != nil {
				return fmt.Errorf("failed to initialize config: %w", err)
			}
			wtMgr = worktree.NewManager(configMgr)
		}

		// Delete sessions (and optionally worktrees)
		deletedSessions := 0
		deletedWorktrees := 0
		for _, s := range stoppedSessions {
			// Delete worktree first if requested
			if cleanupWorktree && s.Repository != "" && s.Branch != "" && wtMgr != nil {
				wt, err := wtMgr.GetByBranch(s.Repository, s.Branch)
				if err == nil && !wt.IsMain {
					if err := wtMgr.Delete(s.Repository, wt.Path, false); err != nil {
						fmt.Printf("Warning: failed to delete worktree for %s: %v\n", s.Name, err)
					} else {
						fmt.Printf("Deleted worktree: %s\n", wt.Path)
						deletedWorktrees++
					}
				}
			}

			// Delete session
			if err := client.Delete(s.ID); err != nil {
				fmt.Printf("Warning: failed to delete session %s: %v\n", s.Name, err)
			} else {
				fmt.Printf("Deleted session: %s (%s)\n", s.Name, s.ID[:8])
				deletedSessions++
			}
		}

		fmt.Printf("\nCleanup complete: %d session(s) deleted", deletedSessions)
		if cleanupWorktree {
			fmt.Printf(", %d worktree(s) deleted", deletedWorktrees)
		}
		fmt.Println()

		return nil
	},
}

func init() {
	rootCmd.AddCommand(cleanupCmd)
	cleanupCmd.AddCommand(cleanupStoppedCmd)

	cleanupStoppedCmd.Flags().BoolVarP(&cleanupWorktree, "worktree", "w", false, "Also delete associated worktrees")
	cleanupStoppedCmd.Flags().BoolVar(&cleanupDryRun, "dry-run", false, "Show what would be deleted without actually deleting")
}
