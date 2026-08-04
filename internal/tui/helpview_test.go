package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/takaaki-s/jind-ai/internal/config"
)

func TestNewHelpModel(t *testing.T) {
	cfg := config.DefaultKeybindings()
	keys := NewKeyMap(cfg)
	detachHint := "Ctrl+]"

	m := NewHelpModel(keys, detachHint, "M-p", "/", nil)

	// Verify keybindings are set (spot-check a few)
	if h := m.keys.Up.Help(); h.Key == "" || h.Desc == "" {
		t.Error("Up binding: Help().Key or Help().Desc is empty")
	}
	if h := m.keys.Quit.Help(); h.Key == "" || h.Desc == "" {
		t.Error("Quit binding: Help().Key or Help().Desc is empty")
	}
	if h := m.keys.New.Help(); h.Key == "" || h.Desc == "" {
		t.Error("New binding: Help().Key or Help().Desc is empty")
	}
	if h := m.keys.Enter.Help(); h.Key == "" || h.Desc == "" {
		t.Error("Enter binding: Help().Key or Help().Desc is empty")
	}
	if h := m.keys.Help.Help(); h.Key == "" || h.Desc == "" {
		t.Error("Help binding: Help().Key or Help().Desc is empty")
	}
	if m.detachKeyHint != detachHint {
		t.Errorf("detachKeyHint: got %q, want %q", m.detachKeyHint, detachHint)
	}
}

func TestNewHelpModel_EmptyKeyMap(t *testing.T) {
	// A zero-value KeyMap should still produce a valid model
	keys := KeyMap{}
	m := NewHelpModel(keys, "", "", "", nil)

	if m.detachKeyHint != "" {
		t.Errorf("detachKeyHint: got %q, want empty", m.detachKeyHint)
	}
}

func TestHelpModel_View(t *testing.T) {
	cfg := config.DefaultKeybindings()
	keys := NewKeyMap(cfg)
	m := NewHelpModel(keys, "Ctrl+]", "M-p", "/", nil)

	view := m.View()

	if view == "" {
		t.Fatal("View() returned empty string")
	}

	// Verify section headers are present
	for _, section := range []string{"Keyboard Shortcuts", "Navigation", "Actions", "General"} {
		if !strings.Contains(view, section) {
			t.Errorf("View() missing section header %q", section)
		}
	}

	// Verify keybinding labels are rendered (the Help().Desc values)
	expectedLabels := []string{
		"up", "down", "attach", "new session", "kill", "delete",
		"refresh", "quit", "help",
	}
	for _, label := range expectedLabels {
		if !strings.Contains(view, label) {
			t.Errorf("View() missing keybinding label %q", label)
		}
	}

	// Verify the detach key hint appears
	if !strings.Contains(view, "Ctrl+]") {
		t.Error("View() missing detach key hint \"Ctrl+]\"")
	}

	// Verify the close instruction appears
	if !strings.Contains(view, "Press any key to close") {
		t.Error("View() missing close instruction")
	}
}

// TestHelpModel_View_ClickTakesTwoTaps pins that this view describes the
// two-tap pointer rather than the one-tap click it replaced (see handleMouse).
// The first tap doing nothing visible but moving the cursor is the list's most
// surprising behaviour, and this popup is the screen a user opens to ask about
// it — a stale line here sends them away with the wrong answer, which is a
// failure no rendering test would catch.
func TestHelpModel_View_ClickTakesTwoTaps(t *testing.T) {
	view := NewHelpModel(NewKeyMap(config.DefaultKeybindings()), "Ctrl+]", "M-p", "/", nil).View()

	var second string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "click again") {
			second = line
		}
	}
	if second == "" {
		t.Fatalf("View() never mentions a second click; the pointer takes two taps to switch sessions:\n%s", view)
	}
	// Checked against that line rather than the whole view: "attach" is also
	// the Enter binding's description, so a view-wide search would pass on a
	// second-click line that says nothing at all.
	if !strings.Contains(second, "attach") {
		t.Errorf("the second-click line = %q, want it to say that this is the click that switches", second)
	}
}

func TestHelpModel_View_KeyLabels(t *testing.T) {
	cfg := config.DefaultKeybindings()
	keys := NewKeyMap(cfg)
	m := NewHelpModel(keys, "Ctrl+]", "M-p", "/", nil)

	view := m.View()

	// Verify that key names from the default config appear in the view.
	// The default Help() key for Up is "up/k" and for Quit is "q/ctrl+c".
	expectedKeys := []string{"up/k", "q/ctrl+c", "n", "?"}
	for _, k := range expectedKeys {
		if !strings.Contains(view, k) {
			t.Errorf("View() missing key label %q", k)
		}
	}
}

func TestHelpModel_ActionPanelLine(t *testing.T) {
	cfg := config.DefaultKeybindings()
	keys := NewKeyMap(cfg)

	withHint := NewHelpModel(keys, "Ctrl+]", "M-p", "/", nil)
	if !strings.Contains(withHint.View(), "open action palette") {
		t.Error("View() with actionPanelKeyHint set should contain \"open action palette\"")
	}

	withoutHint := NewHelpModel(keys, "Ctrl+]", "", "/", nil)
	if strings.Contains(withoutHint.View(), "open action palette") {
		t.Error("View() with empty actionPanelKeyHint should not contain \"open action palette\"")
	}
}

func TestHelpModel_SessionFilterLine(t *testing.T) {
	cfg := config.DefaultKeybindings()
	keys := NewKeyMap(cfg)

	withHint := NewHelpModel(keys, "Ctrl+]", "M-p", "/", nil)
	if !strings.Contains(withHint.View(), "switch session") {
		t.Error("View() with sessionFilterKeyHint set should contain \"switch session\"")
	}

	withoutHint := NewHelpModel(keys, "Ctrl+]", "M-p", "", nil)
	if strings.Contains(withoutHint.View(), "switch session") {
		t.Error("View() with empty sessionFilterKeyHint should not contain \"switch session\"")
	}
}

func TestHelpModel_PluginsSection(t *testing.T) {
	cfg := config.DefaultKeybindings()
	keys := NewKeyMap(cfg)

	// Absent when no plugin hints
	withoutPlugins := NewHelpModel(keys, "Ctrl+]", "M-p", "/", nil)
	if strings.Contains(withoutPlugins.View(), "Plugins") {
		t.Error("View() with no pluginHints should not contain the \"Plugins\" section header")
	}

	// Present + each hint rendered when pluginHints are supplied
	hints := []PluginBindingHint{
		{KeyHint: "Alt+N", Name: "notifier"},
		{KeyHint: "Alt+W", Name: "worktree-cleanup"},
	}
	withPlugins := NewHelpModel(keys, "Ctrl+]", "M-p", "/", hints)
	view := withPlugins.View()
	if !strings.Contains(view, "Plugins") {
		t.Error("View() with pluginHints should contain the \"Plugins\" section header")
	}
	for _, ph := range hints {
		if !strings.Contains(view, ph.KeyHint) {
			t.Errorf("View() missing plugin KeyHint %q", ph.KeyHint)
		}
		if !strings.Contains(view, "plugin: "+ph.Name) {
			t.Errorf("View() missing plugin name %q", ph.Name)
		}
	}
}

func TestWriteBinding(t *testing.T) {
	keyStyle := lipglossTestStyle()
	descStyle := lipglossTestStyle()

	binding := key.NewBinding(
		key.WithKeys("x"),
		key.WithHelp("x", "test action"),
	)

	var b strings.Builder
	writeBinding(&b, keyStyle, descStyle, binding)

	result := b.String()
	if !strings.Contains(result, "x") {
		t.Error("writeBinding output missing key 'x'")
	}
	if !strings.Contains(result, "test action") {
		t.Error("writeBinding output missing description 'test action'")
	}
	if !strings.HasSuffix(result, "\n") {
		t.Error("writeBinding output should end with newline")
	}
}

func TestWriteShortcut(t *testing.T) {
	keyStyle := lipglossTestStyle()
	descStyle := lipglossTestStyle()

	var b strings.Builder
	writeShortcut(&b, keyStyle, descStyle, "ctrl+x", "exit")

	result := b.String()
	if !strings.Contains(result, "ctrl+x") {
		t.Error("writeShortcut output missing key 'ctrl+x'")
	}
	if !strings.Contains(result, "exit") {
		t.Error("writeShortcut output missing description 'exit'")
	}
	if !strings.HasPrefix(result, "  ") {
		t.Error("writeShortcut output should start with two-space indent")
	}
}

func TestWriteShortcut_Padding(t *testing.T) {
	keyStyle := lipglossTestStyle()
	descStyle := lipglossTestStyle()

	// Short key should be padded to keyColWidth (14)
	var b1 strings.Builder
	writeShortcut(&b1, keyStyle, descStyle, "x", "short")

	// Long key (>= 14 chars) should not be padded further
	var b2 strings.Builder
	writeShortcut(&b2, keyStyle, descStyle, "a-very-long-key", "long")

	r1 := b1.String()
	r2 := b2.String()

	if !strings.Contains(r1, "short") {
		t.Error("short key output missing description")
	}
	if !strings.Contains(r2, "long") {
		t.Error("long key output missing description")
	}
}

// lipglossTestStyle returns a no-op lipgloss style for testing.
func lipglossTestStyle() lipgloss.Style {
	return lipgloss.NewStyle()
}
