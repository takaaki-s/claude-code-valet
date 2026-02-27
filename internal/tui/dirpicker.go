package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DirPickerModel is a directory browser component for selecting a working directory.
type DirPickerModel struct {
	currentDir string   // 現在表示中のディレクトリ
	entries    []string // 現在のディレクトリ内のサブディレクトリ名
	filtered  []string // フィルタ後のエントリ
	cursor    int      // カーソル位置
	offset    int      // スクロールオフセット

	filterInput textinput.Model // フィルタ入力
	showHidden  bool            // 隠しディレクトリを表示するか

	selected bool   // ディレクトリが選択されたか
	result   string // 選択されたディレクトリパス

	width  int
	height int
}

// NewDirPickerModel creates a new directory picker starting at the given path.
// If startDir is empty, defaults to the user's home directory.
func NewDirPickerModel(startDir string) DirPickerModel {
	if startDir == "" {
		startDir, _ = os.UserHomeDir()
	}
	// Expand ~
	if strings.HasPrefix(startDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			startDir = filepath.Join(home, startDir[2:])
		}
	}
	// Ensure absolute path
	if !filepath.IsAbs(startDir) {
		if abs, err := filepath.Abs(startDir); err == nil {
			startDir = abs
		}
	}

	fi := textinput.New()
	fi.Placeholder = "type to filter..."
	fi.CharLimit = 256
	fi.Width = 40
	fi.Focus()

	m := DirPickerModel{
		currentDir:  startDir,
		filterInput: fi,
	}
	m.loadEntries()
	return m
}

// Selected returns true if a directory was selected.
func (m *DirPickerModel) Selected() bool {
	return m.selected
}

// Result returns the selected directory path.
func (m *DirPickerModel) Result() string {
	return m.result
}

// Init implements tea.Model.
func (m DirPickerModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m DirPickerModel) Update(msg tea.Msg) (DirPickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			// Enter a directory
			if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
				selected := m.filtered[m.cursor]
				m.currentDir = filepath.Join(m.currentDir, selected)
				m.filterInput.SetValue("")
				m.cursor = 0
				m.offset = 0
				m.loadEntries()
			}
			return m, nil

		case "tab", "ctrl+d":
			// Select current directory
			m.selected = true
			m.result = m.currentDir
			return m, nil

		case "backspace":
			// If filter is empty, go to parent
			if m.filterInput.Value() == "" {
				parent := filepath.Dir(m.currentDir)
				if parent != m.currentDir {
					m.currentDir = parent
					m.cursor = 0
					m.offset = 0
					m.loadEntries()
				}
				return m, nil
			}

		case "up":
			if m.cursor > 0 {
				m.cursor--
				m.adjustScroll()
			}
			return m, nil

		case "down":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
				m.adjustScroll()
			}
			return m, nil

		case "ctrl+h":
			// Toggle hidden directories
			m.showHidden = !m.showHidden
			m.loadEntries()
			return m, nil
		}
	}

	// Update filter input
	oldQuery := m.filterInput.Value()
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)

	// Check for direct path navigation
	val := m.filterInput.Value()
	if val != oldQuery {
		if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "~") {
			// Direct path navigation
			path := val
			if strings.HasPrefix(path, "~/") {
				if home, err := os.UserHomeDir(); err == nil {
					path = filepath.Join(home, path[2:])
				}
			}
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				m.currentDir = path
				m.filterInput.SetValue("")
				m.cursor = 0
				m.offset = 0
				m.loadEntries()
				return m, cmd
			}
		}
		// Normal filtering
		m.applyFilter()
	}

	return m, cmd
}

// View renders the directory picker.
func (m DirPickerModel) View() string {
	var b strings.Builder

	// Breadcrumb: current path
	pathStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7aa2f7"))
	displayPath := m.currentDir
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(displayPath, home) {
		displayPath = "~" + displayPath[len(home):]
	}
	b.WriteString(pathStyle.Render("  📂 " + displayPath))
	b.WriteString("\n")

	// Filter input
	b.WriteString("  " + m.filterInput.View())
	b.WriteString("\n")

	// Separator
	sepWidth := m.width - 4
	if sepWidth < 10 {
		sepWidth = 40
	}
	b.WriteString("  " + strings.Repeat("─", sepWidth))
	b.WriteString("\n")

	// Calculate visible lines
	visibleLines := m.height - 8
	if visibleLines < 3 {
		visibleLines = 10
	}

	dirStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7dcfff"))
	selectedStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("255")).
		Background(lipgloss.Color("#7aa2f7"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#414868"))

	if len(m.filtered) == 0 {
		b.WriteString("  " + dimStyle.Render("(empty)"))
		b.WriteString("\n")
	} else {
		end := m.offset + visibleLines
		if end > len(m.filtered) {
			end = len(m.filtered)
		}
		for i := m.offset; i < end; i++ {
			entry := m.filtered[i]
			displayName := entry + "/"

			if i == m.cursor {
				// Pad selected line to full width
				padded := "▸ " + displayName
				availWidth := m.width - 4
				if availWidth > 0 && len(padded) < availWidth {
					padded += strings.Repeat(" ", availWidth-len(padded))
				}
				b.WriteString("  " + selectedStyle.Render(padded))
			} else {
				b.WriteString("    " + dirStyle.Render(displayName))
			}
			b.WriteString("\n")
		}
	}

	// Footer hints
	b.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	hiddenHint := "Ctrl+H:show hidden"
	if m.showHidden {
		hiddenHint = "Ctrl+H:hide hidden"
	}
	b.WriteString("  " + hintStyle.Render("Enter:open  Tab:select  Backspace:parent  "+hiddenHint))

	return b.String()
}

// --- Internal ---

func (m *DirPickerModel) loadEntries() {
	m.entries = nil
	m.filtered = nil

	dirEntries, err := os.ReadDir(m.currentDir)
	if err != nil {
		return
	}

	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !m.showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		m.entries = append(m.entries, name)
	}

	sort.Strings(m.entries)
	m.applyFilter()
}

func (m *DirPickerModel) applyFilter() {
	query := strings.ToLower(m.filterInput.Value())
	if query == "" || strings.HasPrefix(query, "/") || strings.HasPrefix(query, "~") {
		m.filtered = m.entries
	} else {
		m.filtered = nil
		for _, e := range m.entries {
			if strings.Contains(strings.ToLower(e), query) {
				m.filtered = append(m.filtered, e)
			}
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
	m.offset = 0
}

func (m *DirPickerModel) adjustScroll() {
	visibleLines := m.height - 8
	if visibleLines < 3 {
		visibleLines = 10
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visibleLines {
		m.offset = m.cursor - visibleLines + 1
	}
}
