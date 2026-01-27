package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
)

var workdirCmd = &cobra.Command{
	Use:   "workdir <session-name>",
	Short: "Print the working directory of a session",
	Long: `Print the working directory path of a session.

Useful for shell integration:
  cd $(ccvalet session workdir <session-name>)

Or define a shell function in your .bashrc/.zshrc:
  cc-cd() { cd "$(ccvalet session workdir "$1")"; }`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]
		client := daemon.NewClient(getSocketPath())

		sessions, err := client.List()
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}

		for _, s := range sessions {
			if s.Name == nameOrID || s.ID == nameOrID {
				if s.WorkDir == "" {
					return fmt.Errorf("session %s has no working directory", nameOrID)
				}
				fmt.Println(s.WorkDir)
				return nil
			}
		}

		return fmt.Errorf("session not found: %s", nameOrID)
	},
}

func init() {
	sessionCmd.AddCommand(workdirCmd)
}
