package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
)

var killCmd = &cobra.Command{
	Use:               "kill <session-name>",
	Short:             "Kill a running session",
	Long:              `Kill a running Claude Code session without deleting it. You can specify either session name or ID.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]
		client := daemon.NewClient(getSocketPath())

		sessionID, sessionName, err := resolveSession(client, nameOrID)
		if err != nil {
			return err
		}

		if err := client.Kill(sessionID, ""); err != nil {
			return err
		}
		fmt.Printf("Killed session: %s\n", sessionName)
		return nil
	},
}

var deleteCmd = &cobra.Command{
	Use:               "delete <session-name>",
	Aliases:           []string{"rm"},
	Short:             "Delete a session",
	Long:              `Delete a Claude Code session. This will kill the session if running. You can specify either session name or ID.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]
		client := daemon.NewClient(getSocketPath())

		sessionID, sessionName, err := resolveSession(client, nameOrID)
		if err != nil {
			return err
		}

		if err := client.Delete(sessionID, ""); err != nil {
			return err
		}
		fmt.Printf("Deleted session: %s\n", sessionName)
		return nil
	},
}

// resolveSession resolves a session name or ID to the actual session ID and name
func resolveSession(client *daemon.Client, nameOrID string) (id, name string, err error) {
	sessions, err := client.List()
	if err != nil {
		return "", "", err
	}

	for _, s := range sessions {
		if s.Name == nameOrID || s.ID == nameOrID {
			return s.ID, s.Name, nil
		}
	}

	return "", "", fmt.Errorf("session not found: %s", nameOrID)
}

func init() {
	sessionCmd.AddCommand(killCmd)
	sessionCmd.AddCommand(deleteCmd)
}
