package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// go-runewidth reads the East-Asian ambiguous-width table from the process
// locale when it initialises, so "○", "■" and "▶" measure 2 cells under
// LANG=ja_JP.UTF-8 and 1 under C.UTF-8. Nothing that actually draws follows
// the locale — lipgloss and the terminals we target treat them as 1 — so a
// layout measured with the locale-sensitive table comes out misaligned for
// exactly the users whose locale is East Asian.
//
// model.go measures with x/ansi (the ruler lipgloss itself uses) and does not
// depend on this. The sibling popup models still measure with runewidth, so
// pinning the table here keeps the whole package on one answer.
func init() {
	runewidth.DefaultCondition.EastAsianWidth = false
}

var (
	// Colors - Tokyo Night inspired palette
	primaryColor   = lipgloss.Color("#7aa2f7") // Blue
	secondaryColor = lipgloss.Color("#565f89") // Gray
	successColor   = lipgloss.Color("#9ece6a") // Green
	warningColor   = lipgloss.Color("#ff9e64") // Orange
	errorColor     = lipgloss.Color("#f7768e") // Red
	dimColor       = lipgloss.Color("#414868") // Dark gray
	purpleColor    = lipgloss.Color("#bb9af7") // Purple (thinking)
	cyanColor      = lipgloss.Color("#7dcfff") // Cyan (running)

	// Help style (outside pane)
	helpStyle = lipgloss.NewStyle().
			Foreground(secondaryColor)

	// Selected item style: the "cursor" — a bold, blue vertical bar '▎' in the
	// row's first column, and a bold, blue name beside it.
	selectedItemStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(primaryColor)

	// Viewed background: the session currently shown in the display pane
	// gets a subtle background across the full width of its row.
	// AdaptiveColor auto-picks a subdued shade for light and dark terminal
	// themes (lipgloss queries the terminal background via OSC 11 on start).
	// Chosen to be perceptibly present without stealing attention from the
	// selection cursor.
	viewedRowBg = lipgloss.AdaptiveColor{
		Light: "#dfe1e6",
		Dark:  "#292e42",
	}

	// Session name style
	sessionNameStyle = lipgloss.NewStyle().
				Bold(true)

	// Status styles - Tokyo Night inspired
	thinkingStyle = lipgloss.NewStyle().
			Foreground(purpleColor).
			Bold(true)

	permissionStyle = lipgloss.NewStyle().
			Foreground(warningColor).
			Bold(true)

	runningStyle = lipgloss.NewStyle().
			Foreground(cyanColor).
			Bold(true)

	creatingStyle = lipgloss.NewStyle().
			Foreground(primaryColor)

	idleStyle = lipgloss.NewStyle().
			Foreground(successColor)

	stoppedStyle = lipgloss.NewStyle().
			Foreground(dimColor)

	deletingStyle = lipgloss.NewStyle().
			Foreground(secondaryColor)

	// Confirm dialog tokens (the kill/delete confirm popup).
	// Descriptions reuse helpStyle (secondaryColor).
	//
	// There is deliberately no frame style here: the confirm dialog draws no
	// border of its own. tmux already draws one around every display-popup
	// (measurably — a 12-row popup yields a 10-row inner pty), so a second
	// frame would nest inside the first, and none of the sibling popup models
	// draw one either. The "this destroys something" signal rides on the
	// warning-tinted title instead.
	confirmTitleStyle = lipgloss.NewStyle().Foreground(warningColor).Bold(true)
	confirmKeyStyle   = lipgloss.NewStyle().Foreground(primaryColor).Bold(true)
)

// createPaneStyle wraps content with 1-column horizontal padding and a fixed
// height so the help line stays at the bottom. No border is drawn — tmux
// already draws the pane divider, so an extra border would be redundant.
// The focused flag is currently unused; focus is conveyed through the title
// color in the header instead.
func createPaneStyle(width, height int, _ bool) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		MaxHeight(height).
		Padding(0, 1)
}
