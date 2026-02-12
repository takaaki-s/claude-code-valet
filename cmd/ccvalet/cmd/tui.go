package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/tui"
	"golang.org/x/term"
)

const envCcvaletTmux = "CCVALET_TMUX"

var tuiCmd = &cobra.Command{
	Use:     "ui",
	Aliases: []string{"tui"},
	Short:   "Open the interactive TUI",
	Long:    `Open the interactive terminal user interface for managing Claude Code sessions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// If running inside ccvalet tmux session, run the TUI directly
		if os.Getenv(envCcvaletTmux) == "1" {
			return runTUIInner()
		}
		// Otherwise, set up tmux and attach
		return runTUIWithTmux()
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

// tuiInnerCommand returns the shell command for the inner TUI process.
func tuiInnerCommand() (string, error) {
	selfBin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}
	return fmt.Sprintf("%s=1 '%s' ui", envCcvaletTmux, selfBin), nil
}

// runTUIWithTmux creates or reattaches to a tmux session with 2-pane layout.
func runTUIWithTmux() error {
	client := daemon.NewClient(getSocketPath())
	if !client.IsRunning() {
		return fmt.Errorf("daemon is not running. Start with: ccvalet daemon start")
	}

	tc, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux is required: %w", err)
	}

	tuiInnerCmd, err := tuiInnerCommand()
	if err != nil {
		return err
	}

	// Reattach to existing session if it exists
	if tc.HasSession(tmux.SessionName) {
		return reattachTmux(tc, tuiInnerCmd)
	}

	// Create new tmux session
	return createAndAttachTmux(tc, tuiInnerCmd)
}

// createAndAttachTmux creates a new tmux session with TUI fullscreen and attaches.
func createAndAttachTmux(tc *tmux.Client, tuiInnerCmd string) error {
	// Get terminal size
	cols, rows := 120, 40
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			cols, rows = w, h
		}
	}

	// Create tmux session with the TUI command running directly in the named "ui" window
	if err := tc.NewSessionWithCmd(tmux.SessionName, cols, rows, tmux.UIWindowName, tuiInnerCmd); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Normalize indices to 0-based (override user's .tmux.conf settings)
	tc.SetOption("base-index", "0", true)
	tc.SetOption("pane-base-index", "0", true)

	// Configure the session
	tc.SetOption("remain-on-exit", "on", true) // Keep all panes on exit (managed panes need this)
	tc.SetupAutoCleanDeadPanes()               // Auto-kill untagged dead panes (user-added)
	tc.TagManagedPane(tmux.UITarget(0))        // TUI pane survives exit
	tc.SetOption("status", "off", true)        // Hide tmux status bar
	tc.SetOption("mouse", "on", true)

	// Bind Ctrl+] to switch to TUI pane (initial: select-pane -L, rebound in runTUIInner with pane ID)
	tc.BindKey("C-]", "select-pane", "-L")

	// No split — TUI starts fullscreen. It will join-pane into session windows on Enter.

	return attachToSession(tc)
}

// reattachTmux reattaches to an existing tmux session, respawning the TUI pane.
func reattachTmux(tc *tmux.Client, tuiInnerCmd string) error {
	// Ensure pane-died hook is active (handles upgrade from older version)
	tc.SetupAutoCleanDeadPanes()

	tuiPaneID := tc.GetEnvironment(tmux.SessionName, "CCVALET_TUI_PANE")

	if tuiPaneID != "" {
		if tc.IsPaneDead(tuiPaneID) {
			// TUI pane exists but dead → respawn it
			tc.RespawnPane(tuiPaneID, tuiInnerCmd)
		}
		// TUI is alive (shouldn't happen normally, but handle gracefully)
		// Select TUI pane (switches to its window automatically)
		tc.SelectPane(tuiPaneID)
	} else {
		// No tracked TUI pane → respawn in UI window pane 0
		tc.RespawnPane(tmux.UITarget(0), tuiInnerCmd)
		tc.SelectWindow(tmux.SessionName + ":" + tmux.UIWindowName)
	}

	return attachToSession(tc)
}

// attachToSession attaches to the tmux session and blocks until detach.
func attachToSession(tc *tmux.Client) error {
	attachCmd := tc.AttachCmd(tmux.SessionName)
	attachCmd.Stdin = os.Stdin
	attachCmd.Stdout = os.Stdout
	attachCmd.Stderr = os.Stderr
	return attachCmd.Run()
}

// runTUIInner runs the Bubble Tea TUI inside the tmux pane.
func runTUIInner() error {
	client := daemon.NewClient(getSocketPath())
	if !client.IsRunning() {
		return fmt.Errorf("daemon is not running. Start with: ccvalet daemon start")
	}

	tc, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux not available in inner mode: %w", err)
	}

	// Get TUI pane ID and store it for cross-window tracking
	tuiPaneID, _ := tc.GetPaneID(tmux.UITarget(0))
	if tuiPaneID != "" {
		tc.SetEnvironment(tmux.SessionName, "CCVALET_TUI_PANE", tuiPaneID)
		// Tag TUI pane so pane-died hook preserves it (survives join-pane moves)
		tc.TagManagedPane(tuiPaneID)
		// Rebind Ctrl+] to focus TUI pane (works from any window)
		tc.BindKey("C-]", "run-shell",
			fmt.Sprintf("tmux -L %s select-pane -t %s", tmux.SocketName, tuiPaneID))
	}

	model := tui.NewModelWithTmux(client, tc, tuiPaneID)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return err
	}

	// Detach the client instead of killing the session.
	// The tmux session stays alive with CC processes running in background.
	tc.DetachClient(tmux.SessionName)
	return nil
}
