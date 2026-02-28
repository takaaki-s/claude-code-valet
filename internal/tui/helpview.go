package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HelpModel is a standalone Bubble Tea model for displaying keyboard shortcuts.
// Designed to run inside a tmux popup and exits on any key press.
type HelpModel struct {
	keys          KeyMap
	detachKeyHint string
	width         int
	height        int
}

// NewHelpModel creates a new HelpModel with the given KeyMap and detach key hint.
func NewHelpModel(keys KeyMap, detachKeyHint string) HelpModel {
	return HelpModel{keys: keys, detachKeyHint: detachKeyHint}
}

func (m HelpModel) Init() tea.Cmd {
	return nil
}

func (m HelpModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m HelpModel) View() string {
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(primaryColor)
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	descStyle := lipgloss.NewStyle().Foreground(secondaryColor)

	k := m.keys
	var b strings.Builder

	b.WriteString(sectionStyle.Render("Keyboard Shortcuts"))
	b.WriteString("\n\n")

	b.WriteString(sectionStyle.Render("Navigation"))
	b.WriteString("\n")
	writeBinding(&b, keyStyle, descStyle, k.Up)
	writeBinding(&b, keyStyle, descStyle, k.Down)
	writeBinding(&b, keyStyle, descStyle, k.PrevPage)
	writeBinding(&b, keyStyle, descStyle, k.NextPage)
	writeBinding(&b, keyStyle, descStyle, k.Search)
	b.WriteString("\n")

	b.WriteString(sectionStyle.Render("Actions"))
	b.WriteString("\n")
	writeBinding(&b, keyStyle, descStyle, k.Enter)
	writeBinding(&b, keyStyle, descStyle, k.New)
	writeBinding(&b, keyStyle, descStyle, k.Kill)
	writeBinding(&b, keyStyle, descStyle, k.Delete)
	writeBinding(&b, keyStyle, descStyle, k.Refresh)
	writeBinding(&b, keyStyle, descStyle, k.Vscode)
	b.WriteString("\n")

	b.WriteString(sectionStyle.Render("General"))
	b.WriteString("\n")
	writeBinding(&b, keyStyle, descStyle, k.Notifications)
	writeBinding(&b, keyStyle, descStyle, k.Quit)
	writeShortcut(&b, keyStyle, descStyle, m.detachKeyHint, "return to TUI pane")
	writeBinding(&b, keyStyle, descStyle, k.Help)
	b.WriteString("\n")

	b.WriteString(helpStyle.Render("Press any key to close"))

	return b.String()
}

// writeBinding writes a shortcut line from a key.Binding's Help() metadata.
func writeBinding(b *strings.Builder, keyStyle, descStyle lipgloss.Style, binding key.Binding) {
	h := binding.Help()
	writeShortcut(b, keyStyle, descStyle, h.Key, h.Desc)
}

// writeShortcut writes a single shortcut line with aligned columns.
func writeShortcut(b *strings.Builder, keyStyle, descStyle lipgloss.Style, k, desc string) {
	const keyColWidth = 14
	padded := k
	if len(k) < keyColWidth {
		padded = k + strings.Repeat(" ", keyColWidth-len(k))
	}
	b.WriteString("  ")
	b.WriteString(keyStyle.Render(padded))
	b.WriteString(descStyle.Render(desc))
	b.WriteString("\n")
}
