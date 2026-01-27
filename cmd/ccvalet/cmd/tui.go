package cmd

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/tui"
)

var tuiCmd = &cobra.Command{
	Use:     "ui",
	Aliases: []string{"tui"},
	Short:   "Open the interactive TUI",
	Long:    `Open the interactive terminal user interface for managing Claude Code sessions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := daemon.NewClient(getSocketPath())
		if !client.IsRunning() {
			return fmt.Errorf("daemon is not running. Start with: ccvalet daemon start")
		}

		configMgr, err := config.NewManager(getConfigDir())
		if err != nil {
			return fmt.Errorf("failed to initialize config: %w", err)
		}

		detachKey := configMgr.GetDetachKey()
		detachKeyHint := configMgr.GetDetachKeyHint()
		detachKeyCSIu := configMgr.GetDetachKeyCSIu()

		for {
			model := tui.NewModel(client)

			p := tea.NewProgram(model, tea.WithAltScreen())
			finalModel, err := p.Run()
			if err != nil {
				return err
			}

			// Check if we need to attach to a session
			m := finalModel.(tui.Model)
			select {
			case sessionID := <-m.AttachSignal():
				// Attach to the selected session
				if err := client.Attach(sessionID, detachKey, detachKeyHint, detachKeyCSIu); err != nil {
					// If attach fails, continue to TUI
					continue
				}
				// Reset terminal state after detach
				resetTerminal()
				// After detach, loop back to TUI
				continue
			default:
				// User quit without selecting
				return nil
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

// resetTerminal resets the terminal state using stty
func resetTerminal() {
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	cmd.Run()
}
