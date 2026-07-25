package cmd

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/tmux"
	"github.com/takaaki-s/jind-ai/internal/tui"
)

var confirmPopupCmd = &cobra.Command{
	Use:    "confirm-popup",
	Short:  "Internal: kill/delete confirmation for tmux popup",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// The confirmation lives in its own popup (rather than in the parent
		// TUI's pane) because a popup owns keyboard focus while open — the
		// parent pane does not when the action palette started the action.
		// Which prompt to show comes in through the outer tmux env, written
		// by the parent just before it opens this popup.
		tc, err := tmux.NewMgrClient()
		if err != nil {
			// No tmux, so no confirm request to answer. Exiting quietly keeps
			// non-tmux invocations non-fatal (V-014).
			return nil
		}
		// One tmux call for the whole request: this runs on the popup's cold
		// start, with the user watching an empty popup until the dialog paints.
		// A tmux failure yields an empty map, which the guard below rejects for
		// the same reason it rejects a stale mode.
		//
		// JIN_CONFIRM_TARGET_ID is intentionally not read here: the popup only
		// needs to render and answer. The parent wrote the ID and the parent
		// consumes it when dispatching the result.
		env := tc.ListEnvironment(tmux.SessionName)
		model, ok := tui.NewConfirmPopupModel(env[tui.EnvConfirmMode], env[tui.EnvConfirmTargetDesc])
		if !ok {
			// Empty or stale env: never render a prompt we can't attribute to
			// a real request, since answering it would destroy a session.
			return nil
		}
		p := tea.NewProgram(model, tea.WithAltScreen())
		finalModel, err := p.Run()
		if err != nil {
			return err
		}

		if m, ok := finalModel.(tui.ConfirmPopupModel); ok {
			pushConfirmResult(m.Selected(), tc)
		}
		return nil
	},
}

// pushConfirmResult writes the user's answer to the outer-tmux env so the
// parent TUI can consume it on the next envTick. An empty answer (Ctrl+C
// dismissal) writes nothing — see pushPopupResult.
func pushConfirmResult(selected string, tc envWriter) {
	pushPopupResult(tc, tui.EnvConfirmResult, selected)
}

func init() {
	rootCmd.AddCommand(confirmPopupCmd)
}
