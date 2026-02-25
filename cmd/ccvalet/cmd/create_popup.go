package cmd

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/tui"
)

var createPopupCmd = &cobra.Command{
	Use:    "create-popup",
	Short:  "Internal: session creation form for tmux popup",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Ensure SSH_AUTH_SOCK is available (tmux popup may not inherit it)
		if os.Getenv("SSH_AUTH_SOCK") == "" {
			if tc, err := tmux.NewMgrClient(); err == nil {
				if sock := tc.GetEnvironment(tmux.SessionName, "SSH_AUTH_SOCK"); sock != "" {
					os.Setenv("SSH_AUTH_SOCK", sock)
				}
			}
		}

		client := daemon.NewClient(getSocketPath())

		// セッション一覧を取得（"in use" 表示に必要）
		sessions, _ := client.List()

		model := tui.NewCreateFormModel(client, sessions)
		p := tea.NewProgram(model, tea.WithAltScreen())
		_, err := p.Run()
		return err
	},
}

func init() {
	rootCmd.AddCommand(createPopupCmd)
}
