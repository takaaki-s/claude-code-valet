package cmd

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/tui"
)

var createPopupCmd = &cobra.Command{
	Use:    "create-popup",
	Short:  "Internal: session creation form for tmux popup",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
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
