package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/daemon"
	"github.com/takaaki-s/jind-ai/internal/tmux"
	"github.com/takaaki-s/jind-ai/internal/tui"
)

var sessionFilterPopupCmd = &cobra.Command{
	Use:    "session-filter-popup",
	Short:  "Internal: switch-session picker for tmux popup",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if tc, err := tmux.NewMgrClient(); err == nil {
			ensureSSHAuthSockFromTmux(tc)
		}

		client := daemon.NewClient(getSocketPath())
		sessions, err := client.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to list sessions: %v\n", err)
			return err
		}

		model := tui.NewSessionFilterModel(sessions)
		p := tea.NewProgram(model, tea.WithAltScreen())
		finalModel, err := p.Run()
		if err != nil {
			return err
		}

		if m, ok := finalModel.(tui.SessionFilterModel); ok {
			if tc, err := tmux.NewMgrClient(); err == nil {
				pushFocusSession(m.Selected(), tc)
			}
		}
		return nil
	},
}

// pushFocusSession writes the picked session ID to the outer-tmux env so the
// parent TUI can consume it on the next envTick. An empty selection (Esc /
// Ctrl+C dismissal) writes nothing — see pushPopupResult.
func pushFocusSession(selected string, tc envWriter) {
	pushPopupResult(tc, "JIN_FOCUS_SESSION", selected)
}

func init() {
	rootCmd.AddCommand(sessionFilterPopupCmd)
}
