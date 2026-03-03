package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/takaaki-s/claude-code-valet/internal/config"
)

// DirPickerModel is a directory browser component for selecting a working directory.
type DirPickerModel struct {
	currentDir string   // 現在表示中のディレクトリ
	entries    []string // 現在のディレクトリ内のサブディレクトリ名
	filtered   []string // フィルタ後のエントリ
	cursor     int      // カーソル位置
	offset     int      // スクロールオフセット

	filterInput textinput.Model // フィルタ入力
	showHidden  bool            // 隠しディレクトリを表示するか

	selected bool   // ディレクトリが選択されたか
	result   string // 選択されたディレクトリパス

	width  int
	height int

	// Remote host support
	hostConfig *config.HostConfig // nil = local mode
	remoteHome string             // リモートのホームディレクトリ
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

// SetRemoteHost switches the directory picker to browse a remote host's filesystem via SSH.
func (m *DirPickerModel) SetRemoteHost(hc *config.HostConfig) {
	if hc == nil || hc.Type != "ssh" {
		m.hostConfig = nil
		return
	}
	m.hostConfig = hc

	// Get remote home directory
	home, err := getRemoteHome(hc)
	if err != nil || home == "" {
		home = "/home"
	}
	m.remoteHome = home
	m.currentDir = home
	m.cursor = 0
	m.offset = 0
	m.loadEntries()
}

// ClearRemoteHost switches back to local directory browsing.
func (m *DirPickerModel) ClearRemoteHost() {
	m.hostConfig = nil
	m.remoteHome = ""
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/"
	}
	m.currentDir = home
	m.cursor = 0
	m.offset = 0
	m.loadEntries()
}

// IsRemote returns true if the directory picker is browsing a remote host.
func (m *DirPickerModel) IsRemote() bool {
	return m.hostConfig != nil
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
			if m.IsRemote() {
				// リモートの場合: homeプレフィックスを ~ に変換して返す
				if m.remoteHome != "" && strings.HasPrefix(m.currentDir, m.remoteHome) {
					m.result = "~" + m.currentDir[len(m.remoteHome):]
					if m.result == "~" {
						m.result = "~"
					}
				} else {
					m.result = m.currentDir
				}
			} else {
				m.result = m.currentDir
			}
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
			if m.IsRemote() {
				// リモート: ~ をリモートhomeに展開
				if strings.HasPrefix(path, "~/") && m.remoteHome != "" {
					path = filepath.Join(m.remoteHome, path[2:])
				} else if path == "~" && m.remoteHome != "" {
					path = m.remoteHome
				}
				// リモートでディレクトリ存在チェック
				if remoteDirExists(m.hostConfig, path) {
					m.currentDir = path
					m.filterInput.SetValue("")
					m.cursor = 0
					m.offset = 0
					m.loadEntries()
					return m, cmd
				}
			} else {
				// ローカル
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
	if m.IsRemote() {
		// リモート: homeプレフィックスを ~ に変換
		if m.remoteHome != "" && strings.HasPrefix(displayPath, m.remoteHome) {
			displayPath = "~" + displayPath[len(m.remoteHome):]
		}
	} else {
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(displayPath, home) {
			displayPath = "~" + displayPath[len(home):]
		}
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
	if m.IsRemote() {
		m.loadRemoteEntries()
	} else {
		m.loadLocalEntries()
	}
}

func (m *DirPickerModel) loadLocalEntries() {
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

func (m *DirPickerModel) loadRemoteEntries() {
	m.entries = nil
	m.filtered = nil

	if m.hostConfig == nil {
		return
	}

	entries, err := listRemoteDirectories(m.hostConfig, m.currentDir, m.showHidden)
	if err != nil {
		return
	}

	m.entries = entries
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

// --- SSH remote helpers ---

// getRemoteHome gets the remote user's home directory via SSH.
func getRemoteHome(hc *config.HostConfig) (string, error) {
	args := []string{"-o", "ControlMaster=no", "-o", "ClearAllForwardings=yes"}
	args = append(args, hc.SSHOpts...)
	args = append(args, hc.Host, "echo $HOME")

	cmd := exec.Command("ssh", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// listRemoteDirectories lists subdirectories of the given path on a remote host via SSH.
func listRemoteDirectories(hc *config.HostConfig, remotePath string, showHidden bool) ([]string, error) {
	// Use ls to list directories (compatible with most systems)
	remoteCmd := "ls -1 -p " + remotePath + " 2>/dev/null | grep '/$' | sed 's|/$||'"
	if !showHidden {
		remoteCmd = "ls -1 -p " + remotePath + " 2>/dev/null | grep '/$' | grep -v '^\\..*/$' | sed 's|/$||'"
	} else {
		remoteCmd = "ls -1 -a -p " + remotePath + " 2>/dev/null | grep '/$' | grep -v '^\\.\\.\\?/$' | sed 's|/$||'"
	}

	args := []string{"-o", "ControlMaster=no", "-o", "ClearAllForwardings=yes"}
	args = append(args, hc.SSHOpts...)
	args = append(args, hc.Host, remoteCmd)

	cmd := exec.Command("ssh", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}

// remoteDirExists checks if a directory exists on a remote host via SSH.
func remoteDirExists(hc *config.HostConfig, remotePath string) bool {
	if hc == nil {
		return false
	}
	args := []string{"-o", "ControlMaster=no", "-o", "ClearAllForwardings=yes"}
	args = append(args, hc.SSHOpts...)
	args = append(args, hc.Host, "test -d "+remotePath)

	cmd := exec.Command("ssh", args...)
	return cmd.Run() == nil
}
