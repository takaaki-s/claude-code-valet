package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
)

var attachCmd = &cobra.Command{
	Use:               "attach <session-name>",
	Short:             "Attach to a session",
	Long:              `Attach to a running Claude Code session. You can specify either session name or ID.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]
		client := daemon.NewClient(getSocketPath())

		sessionID, _, err := resolveSession(client, nameOrID)
		if err != nil {
			return err
		}

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		detachKey := configMgr.GetDetachKey()
		detachKeyHint := configMgr.GetDetachKeyHint()
		detachKeyCSIu := configMgr.GetDetachKeyCSIu()

		return client.Attach(sessionID, detachKey, detachKeyHint, detachKeyCSIu)
	},
}

func init() {
	sessionCmd.AddCommand(attachCmd)
}
