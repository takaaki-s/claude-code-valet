package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/host"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
)

var attachCmd = &cobra.Command{
	Use:               "attach <session-name>",
	Short:             "Attach to a session",
	Long:              `Attach to a Claude Code session. Stopped sessions are automatically resumed. You can specify either session name or ID.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]
		client := daemon.NewClient(getSocketPath())

		sessions, err := client.List()
		if err != nil {
			return err
		}

		var sess *session.Info
		for i := range sessions {
			if sessions[i].Name == nameOrID || sessions[i].ID == nameOrID {
				sess = &sessions[i]
				break
			}
		}
		if sess == nil {
			return fmt.Errorf("session not found: %s", nameOrID)
		}

		if sess.Status == session.StatusCreating {
			return fmt.Errorf("cannot attach to session being created")
		}

		// Start stopped sessions (resume)
		if sess.Status == session.StatusStopped {
			if err := client.Start(sess.ID, sess.HostID); err != nil {
				return fmt.Errorf("failed to start session: %w", err)
			}
			fmt.Printf("Resuming session: %s\n", sess.Name)
		}

		// Determine tmux window name
		windowName := sess.TmuxWindowName
		if windowName == "" {
			windowName = tmux.InnerSessionName(sess.ID)
		}

		// Remote session: SSH or Docker attach
		if sess.HostID != "" && sess.HostID != "local" {
			configMgr, _ := config.NewManager(getConfigDir())
			if configMgr == nil {
				return fmt.Errorf("config not available")
			}
			hostConfig := configMgr.GetHost(sess.HostID)
			if hostConfig == nil {
				return fmt.Errorf("host not found: %s", sess.HostID)
			}

			host.EnsureSSHMaster(*hostConfig)
			attachExec := host.AttachCommand(*hostConfig, windowName)
			attachExec.Stdin = os.Stdin
			attachExec.Stdout = os.Stdout
			attachExec.Stderr = os.Stderr
			return attachExec.Run()
		}

		// Local session: attach to inner tmux
		tc, err := tmux.NewClient()
		if err != nil {
			return fmt.Errorf("tmux not available: %w", err)
		}

		attachExec := tc.AttachCmd(windowName)
		attachExec.Stdin = os.Stdin
		attachExec.Stdout = os.Stdout
		attachExec.Stderr = os.Stderr
		return attachExec.Run()
	},
}

func init() {
	sessionCmd.AddCommand(attachCmd)
}
