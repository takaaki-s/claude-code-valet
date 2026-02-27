package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all sessions",
	Long:    `List all Claude Code sessions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := daemon.NewClient(getSocketPath())
		sessions, err := client.List()
		if err != nil {
			return err
		}

		if len(sessions) == 0 {
			fmt.Println("No sessions found. Create one with: ccvalet session new")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATUS\tWORKDIR\tBRANCH\tLAST_ACTIVE")
		for _, s := range sessions {
			statusStr := string(s.Status)
			if s.Status == "error" && s.ErrorMessage != "" {
				statusStr = fmt.Sprintf("error: %s", truncateStr(s.ErrorMessage, 30))
			}

			// Use CurrentWorkDir if available, fall back to WorkDir
			displayDir := s.CurrentWorkDir
			if displayDir == "" {
				displayDir = s.WorkDir
			}
			// Shorten home directory
			if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(displayDir, home) {
				displayDir = "~" + displayDir[len(home):]
			}

			branch := s.CurrentBranch
			if branch == "" {
				branch = "-"
			}

			lastActive := "-"
			if !s.LastActiveAt.IsZero() {
				lastActive = s.LastActiveAt.Format("2006-01-02 15:04")
			} else {
				lastActive = s.CreatedAt.Format("2006-01-02 15:04")
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				s.Name,
				statusStr,
				truncatePath(displayDir, 40),
				branch,
				lastActive,
			)
		}
		w.Flush()
		return nil
	},
}

func init() {
	sessionCmd.AddCommand(listCmd)
}

func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen+3:]
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
