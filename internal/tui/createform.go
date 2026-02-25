package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/prompt"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

// CreateFormModel is a standalone Bubble Tea model for the session creation form.
// It is designed to run inside a tmux popup and communicates the result
// back to the parent TUI via the CCVALET_CREATED_SESSION environment variable.
type CreateFormModel struct {
	client   *daemon.Client
	sessions []session.Info
	width    int
	height   int
	err      error

	// Processing state
	processingMsg string
	fetchingMsg   string // fetch中の表示メッセージ

	// Config managers
	configMgr *config.Manager
	stateMgr  *config.StateManager

	// Form field: session name
	nameInput  textinput.Model
	focusIndex int

	// Host selection
	selectedHostID    string
	hosts             []daemon.HostInfo
	hostInput         textinput.Model
	filteredHosts     []daemon.HostInfo
	hostSelectedIndex int
	hostDropdownOpen  bool

	// Repository selection
	repoInput         textinput.Model
	repositories      []config.RepositoryConfig
	filteredRepos     []config.RepositoryConfig
	repoSelectedIndex int
	repoDropdownOpen  bool

	// Worktree selection
	worktreeInput         textinput.Model
	worktrees             []worktree.Worktree
	filteredWorktrees     []worktree.Worktree
	worktreeSelectedIndex int
	worktreeDropdownOpen  bool
	isNewWorktree         bool

	// Branch selection
	branchInput         textinput.Model
	branches            []string
	filteredBranches    []string
	branchSelectedIndex int
	branchDropdownOpen  bool
	newBranchMode       bool

	// Base branch selection (new branch mode)
	baseBranchInput         textinput.Model
	allBranches             []string
	filteredBaseBranches    []string
	baseBranchSelectedIndex int
	baseBranchDropdownOpen  bool

	// Prompt selection
	promptInput         textinput.Model
	argsInput           textinput.Model
	prompts             []string
	filteredPrompts     []string
	promptSelectedIndex int
	promptDropdownOpen  bool
}

// createFormCompleteMsg is sent when async session creation finishes.
type createFormCompleteMsg struct {
	sessionID string
	err       error
}

// fetchRepoCompleteMsg is sent when git fetch completes.
type fetchRepoCompleteMsg struct {
	err error
}

// NewCreateFormModel creates a new CreateFormModel with all inputs initialized.
func NewCreateFormModel(client *daemon.Client, sessions []session.Info) CreateFormModel {
	// Host input
	hostInput := textinput.New()
	hostInput.Placeholder = "target host"
	hostInput.CharLimit = 50

	// Name input
	nameInput := textinput.New()
	nameInput.Placeholder = "session name (optional)"
	nameInput.CharLimit = 50

	// Repository input
	repoInput := textinput.New()
	repoInput.Placeholder = "repository name"
	repoInput.CharLimit = 50

	// Worktree input
	worktreeInput := textinput.New()
	worktreeInput.Placeholder = "select worktree"
	worktreeInput.CharLimit = 100

	// Branch input
	branchInput := textinput.New()
	branchInput.Placeholder = "branch name"
	branchInput.CharLimit = 100

	// Base branch input
	baseBranchInput := textinput.New()
	baseBranchInput.Placeholder = "base branch (e.g., main)"
	baseBranchInput.CharLimit = 100

	// Prompt input
	promptInput := textinput.New()
	promptInput.Placeholder = "prompt name (optional)"
	promptInput.CharLimit = 50

	// Args input
	argsInput := textinput.New()
	argsInput.Placeholder = "prompt arguments"
	argsInput.CharLimit = 500

	// Initialize config manager and state manager
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".ccvalet")
	configMgr, _ := config.NewManager(configDir)
	stateMgr, _ := config.NewStateManager(configDir)

	// Load host list via daemon
	var hosts []daemon.HostInfo
	if hostInfos, err := client.ListHosts(); err == nil {
		hosts = hostInfos
	}
	// Hide host field when only local host is available
	if len(hosts) <= 1 {
		hosts = nil
	}

	// Load repository list via daemon
	var repos []config.RepositoryConfig
	if repoInfos, err := client.ListRepos("local"); err == nil {
		for _, r := range repoInfos {
			repos = append(repos, config.RepositoryConfig{
				Name:       r.Name,
				Path:       r.Path,
				BaseBranch: r.BaseBranch,
			})
		}
	} else if configMgr != nil {
		// Fallback: read directly from config
		repos = configMgr.GetRepositories()
	}

	// Restore last used repository as default
	if stateMgr != nil {
		if lastUsedRepo := stateMgr.GetLastUsedRepository(); lastUsedRepo != "" {
			repoInput.SetValue(lastUsedRepo)
		}
	}

	// Load available prompts
	promptMgr := prompt.NewManager(configDir)
	var promptNames []string
	if templates, err := promptMgr.List(); err == nil {
		for _, t := range templates {
			promptNames = append(promptNames, t.Name)
		}
	}

	m := CreateFormModel{
		client:         client,
		sessions:       sessions,
		selectedHostID: "local",
		hosts:          hosts,
		hostInput:      hostInput,
		filteredHosts:  hosts,
		nameInput:      nameInput,
		repoInput:      repoInput,
		worktreeInput:  worktreeInput,
		branchInput:    branchInput,
		baseBranchInput: baseBranchInput,
		promptInput:    promptInput,
		argsInput:      argsInput,
		configMgr:      configMgr,
		stateMgr:       stateMgr,
		repositories:   repos,
		filteredRepos:  repos,
		prompts:        promptNames,
		filteredPrompts: promptNames,
		newBranchMode:  true, // Default to new branch mode
	}

	// Set initial focus
	if m.hasHostField() {
		m.hostInput.Focus()
	} else {
		m.nameInput.Focus()
	}

	return m
}

// Init implements tea.Model.
func (m CreateFormModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m CreateFormModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle window size
	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = wsm.Width
		m.height = wsm.Height
	}

	// While processing, ignore key input; only handle completion message
	if m.processingMsg != "" {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			return m, nil
		case createFormCompleteMsg:
			return m.handleCreateComplete(msg)
		default:
			_ = msg
		}
		return m, nil
	}

	// While fetching, ignore key input; only handle fetch completion
	if m.fetchingMsg != "" {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			return m, nil
		case fetchRepoCompleteMsg:
			m.fetchingMsg = ""
			if msg.err != nil {
				m.err = fmt.Errorf("fetch failed: %v", msg.err)
			} else {
				m.err = nil
				// Reload branches after successful fetch
				m.branches = nil
				m.allBranches = nil
				m.loadBranches()
				m.loadAllBranches()
				m.filterBranches()
				m.filterBaseBranches()
			}
			return m, nil
		default:
			_ = msg
		}
		return m, nil
	}

	// Handle create completion message
	if msg, ok := msg.(createFormCompleteMsg); ok {
		return m.handleCreateComplete(msg)
	}

	// Handle fetch completion message
	if msg, ok := msg.(fetchRepoCompleteMsg); ok {
		m.fetchingMsg = ""
		if msg.err != nil {
			m.err = fmt.Errorf("fetch failed: %v", msg.err)
		} else {
			m.err = nil
			m.branches = nil
			m.allBranches = nil
			m.loadBranches()
			m.loadAllBranches()
			m.filterBranches()
			m.filterBaseBranches()
		}
		return m, nil
	}

	var cmd tea.Cmd

	fieldCount := m.getFieldCount()
	offset := m.getHostFieldOffset()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, tea.Quit

		case "ctrl+b":
			m.newBranchMode = !m.newBranchMode
			m.branchInput.Reset()
			m.baseBranchInput.Reset()
			m.baseBranchDropdownOpen = false
			if m.newBranchMode {
				m.branchDropdownOpen = false
			} else {
				m.filterBranches()
			}
			return m, nil

		case "ctrl+w":
			m.isNewWorktree = !m.isNewWorktree
			m.worktreeInput.Reset()
			m.branchInput.Reset()
			m.branchDropdownOpen = false
			if m.isNewWorktree {
				m.worktreeDropdownOpen = false
				m.newBranchMode = true
				m.baseBranchInput.Reset()
			} else {
				m.filterWorktrees()
			}
			return m, nil

		case "ctrl+r":
			// Fetch latest branches from remote
			repoName := m.repoInput.Value()
			if repoName == "" {
				m.err = fmt.Errorf("select a repository first")
				return m, nil
			}
			m.fetchingMsg = "Fetching..."
			m.err = nil
			client := m.client
			hostID := m.selectedHostID
			return m, func() tea.Msg {
				err := client.FetchRepo(hostID, repoName)
				return fetchRepoCompleteMsg{err: err}
			}

		case "tab":
			// Confirm dropdown selection for required fields
			if m.hasHostField() && m.focusIndex == 0 && m.hostDropdownOpen {
				m.selectCurrentHost()
			}
			if m.focusIndex == offset+1 && m.repoDropdownOpen {
				m.selectCurrentRepo()
			}
			if m.focusIndex == offset+2 && m.worktreeDropdownOpen {
				m.selectCurrentWorktree()
			}
			if m.focusIndex == offset+3 && m.branchDropdownOpen {
				m.selectCurrentBranch()
			}
			if m.focusIndex == m.getBaseBranchFieldIndex() && m.baseBranchDropdownOpen {
				m.selectCurrentBaseBranch()
			}
			// Move focus to next input
			m.focusIndex = (m.focusIndex + 1) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "down":
			// Navigate dropdown if open
			if m.hasHostField() && m.focusIndex == 0 && m.hostDropdownOpen && len(m.filteredHosts) > 0 {
				m.hostSelectedIndex = (m.hostSelectedIndex + 1) % len(m.filteredHosts)
				return m, nil
			}
			if m.focusIndex == offset+1 && m.repoDropdownOpen && len(m.filteredRepos) > 0 {
				m.repoSelectedIndex = (m.repoSelectedIndex + 1) % len(m.filteredRepos)
				return m, nil
			}
			if m.focusIndex == offset+2 && m.worktreeDropdownOpen && len(m.filteredWorktrees) > 0 {
				m.worktreeSelectedIndex = (m.worktreeSelectedIndex + 1) % len(m.filteredWorktrees)
				return m, nil
			}
			if m.focusIndex == offset+3 && m.branchDropdownOpen && len(m.filteredBranches) > 0 {
				m.branchSelectedIndex = (m.branchSelectedIndex + 1) % len(m.filteredBranches)
				return m, nil
			}
			if m.focusIndex == m.getBaseBranchFieldIndex() && m.baseBranchDropdownOpen && len(m.filteredBaseBranches) > 0 {
				m.baseBranchSelectedIndex = (m.baseBranchSelectedIndex + 1) % len(m.filteredBaseBranches)
				return m, nil
			}
			if m.focusIndex == m.getPromptFieldIndex() && m.promptDropdownOpen && len(m.filteredPrompts) > 0 {
				m.promptSelectedIndex = (m.promptSelectedIndex + 1) % len(m.filteredPrompts)
				return m, nil
			}
			// Default: move to next field
			m.focusIndex = (m.focusIndex + 1) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "shift+tab":
			m.focusIndex = (m.focusIndex - 1 + fieldCount) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "up":
			// Navigate dropdown if open
			if m.hasHostField() && m.focusIndex == 0 && m.hostDropdownOpen && len(m.filteredHosts) > 0 {
				m.hostSelectedIndex = (m.hostSelectedIndex - 1 + len(m.filteredHosts)) % len(m.filteredHosts)
				return m, nil
			}
			if m.focusIndex == offset+1 && m.repoDropdownOpen && len(m.filteredRepos) > 0 {
				m.repoSelectedIndex = (m.repoSelectedIndex - 1 + len(m.filteredRepos)) % len(m.filteredRepos)
				return m, nil
			}
			if m.focusIndex == offset+2 && m.worktreeDropdownOpen && len(m.filteredWorktrees) > 0 {
				m.worktreeSelectedIndex = (m.worktreeSelectedIndex - 1 + len(m.filteredWorktrees)) % len(m.filteredWorktrees)
				return m, nil
			}
			if m.focusIndex == offset+3 && m.branchDropdownOpen && len(m.filteredBranches) > 0 {
				m.branchSelectedIndex = (m.branchSelectedIndex - 1 + len(m.filteredBranches)) % len(m.filteredBranches)
				return m, nil
			}
			if m.focusIndex == m.getBaseBranchFieldIndex() && m.baseBranchDropdownOpen && len(m.filteredBaseBranches) > 0 {
				m.baseBranchSelectedIndex = (m.baseBranchSelectedIndex - 1 + len(m.filteredBaseBranches)) % len(m.filteredBaseBranches)
				return m, nil
			}
			if m.focusIndex == m.getPromptFieldIndex() && m.promptDropdownOpen && len(m.filteredPrompts) > 0 {
				m.promptSelectedIndex = (m.promptSelectedIndex - 1 + len(m.filteredPrompts)) % len(m.filteredPrompts)
				return m, nil
			}
			// Default: move to previous field
			m.focusIndex = (m.focusIndex - 1 + fieldCount) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "enter":
			// Confirm dropdown selection if open
			if m.hasHostField() && m.focusIndex == 0 && m.hostDropdownOpen {
				m.selectCurrentHost()
				m.hostDropdownOpen = false
				return m, nil
			}
			if m.focusIndex == offset+1 && m.repoDropdownOpen {
				m.selectCurrentRepo()
				m.repoDropdownOpen = false
				return m, nil
			}
			if m.focusIndex == offset+2 && m.worktreeDropdownOpen {
				m.selectCurrentWorktree()
				m.worktreeDropdownOpen = false
				return m, nil
			}
			if m.focusIndex == offset+3 && m.branchDropdownOpen {
				m.selectCurrentBranch()
				m.branchDropdownOpen = false
				return m, nil
			}
			if m.focusIndex == m.getBaseBranchFieldIndex() && m.baseBranchDropdownOpen {
				m.selectCurrentBaseBranch()
				m.baseBranchDropdownOpen = false
				return m, nil
			}
			if m.focusIndex == m.getPromptFieldIndex() && m.promptDropdownOpen {
				m.selectCurrentPrompt()
				m.promptDropdownOpen = false
				return m, nil
			}
			return m.handleSubmit()
		}
	}

	// Update the focused input
	cmd = m.updateFocusedInput(msg)

	return m, cmd
}

// View implements tea.Model.
func (m CreateFormModel) View() string {
	// Fetching indicator
	if m.fetchingMsg != "" {
		return "\n  " + m.fetchingMsg
	}

	// Processing indicator
	if m.processingMsg != "" {
		return "\n  " + m.processingMsg
	}

	var b strings.Builder

	// Title
	b.WriteString(titleStyle.Render("New Session"))
	b.WriteString("\n\n")

	// Error message
	if m.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(fmt.Sprintf("Error: %v", m.err)))
		b.WriteString("\n\n")
	}

	// Form fields
	labelStyle := lipgloss.NewStyle().Width(15).Foreground(secondaryColor)
	focusedLabelStyle := lipgloss.NewStyle().Width(15).Foreground(primaryColor).Bold(true)
	disabledLabelStyle := lipgloss.NewStyle().Width(15).Foreground(lipgloss.Color("8"))
	offset := m.getHostFieldOffset()

	// Host field (only when multiple hosts)
	if m.hasHostField() {
		if m.focusIndex == 0 {
			b.WriteString(focusedLabelStyle.Render("Host:"))
		} else {
			b.WriteString(labelStyle.Render("Host:"))
		}
		b.WriteString(m.hostInput.View())
		b.WriteString("\n")

		// Show host dropdown or help
		if m.hostDropdownOpen && len(m.filteredHosts) > 0 {
			for i, h := range m.filteredHosts {
				prefix := "  "
				label := h.ID
				if !h.Connected {
					label += " (disconnected)"
				}
				if i == m.hostSelectedIndex {
					prefix = "> "
					b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + label))
				} else if !h.Connected {
					b.WriteString(disabledLabelStyle.Render(prefix + label))
				} else {
					b.WriteString(helpStyle.Render(prefix + label))
				}
				b.WriteString("\n")
			}
			b.WriteString(helpStyle.Render("  up/down: select  Enter/Tab: confirm"))
		} else if m.focusIndex == 0 {
			b.WriteString(helpStyle.Render("  Type to search hosts..."))
		}
		b.WriteString("\n\n")
	}

	// Session name field
	if m.focusIndex == offset+0 {
		b.WriteString(focusedLabelStyle.Render("Session Name:"))
	} else {
		b.WriteString(labelStyle.Render("Session Name:"))
	}
	b.WriteString(m.nameInput.View())
	b.WriteString("\n\n")

	// Repository field
	if m.focusIndex == offset+1 {
		b.WriteString(focusedLabelStyle.Render("Repository:"))
	} else {
		b.WriteString(labelStyle.Render("Repository:"))
	}
	b.WriteString(m.repoInput.View())
	b.WriteString("\n")

	// Repository dropdown or help
	if m.repoDropdownOpen && len(m.filteredRepos) > 0 {
		for i, repo := range m.filteredRepos {
			prefix := "  "
			if i == m.repoSelectedIndex {
				prefix = "> "
				b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + repo.Name))
			} else {
				b.WriteString(helpStyle.Render(prefix + repo.Name))
			}
			b.WriteString("\n")
		}
		b.WriteString(helpStyle.Render("  up/down: select  Enter/Tab: confirm"))
	} else if len(m.repositories) == 0 {
		b.WriteString(helpStyle.Render("  No repositories registered. Use 'ccvalet repo add' to add one."))
	} else if m.focusIndex == offset+1 {
		b.WriteString(helpStyle.Render("  Type to search repositories..."))
	}
	b.WriteString("\n\n")

	// Worktree field
	if m.focusIndex == offset+2 {
		if m.isNewWorktree {
			b.WriteString(focusedLabelStyle.Render("Worktree Name:"))
		} else {
			b.WriteString(focusedLabelStyle.Render("Worktree:"))
		}
	} else {
		if m.isNewWorktree {
			b.WriteString(labelStyle.Render("Worktree Name:"))
		} else {
			b.WriteString(labelStyle.Render("Worktree:"))
		}
	}
	b.WriteString(m.worktreeInput.View())
	b.WriteString("\n")

	// Worktree dropdown or help
	if m.isNewWorktree {
		b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Render("  [New Worktree Mode]"))
		b.WriteString("\n")
		if m.focusIndex == offset+2 {
			b.WriteString(helpStyle.Render("  Worktree directory name (optional, defaults to branch)"))
		}
	} else if m.worktreeDropdownOpen && len(m.filteredWorktrees) > 0 {
		wtSessionMap := m.buildWorktreeSessionMap()
		for i, wt := range m.filteredWorktrees {
			prefix := "  "
			label := formatWorktreeDisplay(&wt)

			inUseRendered := ""
			if sess, ok := wtSessionMap[wt.Path]; ok {
				inUseRendered = " " + getInUseStyle(sess.Status).Render(formatInUseIndicator(sess, 25))
			}

			if i == m.worktreeSelectedIndex {
				prefix = "> "
				b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + label))
				b.WriteString(inUseRendered)
			} else {
				b.WriteString(helpStyle.Render(prefix + label))
				b.WriteString(inUseRendered)
			}
			b.WriteString("\n")
		}
		b.WriteString(helpStyle.Render("  up/down: select  Enter/Tab: confirm  Ctrl+W: new worktree"))
	} else if m.focusIndex == offset+2 && m.repoInput.Value() == "" {
		b.WriteString(helpStyle.Render("  Select repository first"))
	} else if m.focusIndex == offset+2 {
		b.WriteString(helpStyle.Render("  Type to search or select worktree..."))
	}
	b.WriteString("\n\n")

	// Branch field
	branchLabel := "Branch:"
	if !m.isNewWorktree {
		branchLabel = "Checkout:"
	}
	if m.focusIndex == offset+3 {
		b.WriteString(focusedLabelStyle.Render(branchLabel))
	} else {
		b.WriteString(labelStyle.Render(branchLabel))
	}
	b.WriteString(m.branchInput.View())
	b.WriteString("\n")

	// Branch dropdown or help
	if m.branchDropdownOpen && len(m.filteredBranches) > 0 {
		branchSessionMap := m.buildBranchSessionMap()
		displayCount := len(m.filteredBranches)
		if displayCount > 10 {
			displayCount = 10
		}
		for i := 0; i < displayCount; i++ {
			branch := m.filteredBranches[i]
			prefix := "  "

			inUseRendered := ""
			if sess, ok := branchSessionMap[branch]; ok {
				inUseRendered = " " + getInUseStyle(sess.Status).Render(formatInUseIndicator(sess, 25))
			}

			if i == m.branchSelectedIndex {
				prefix = "> "
				b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + branch))
				b.WriteString(inUseRendered)
			} else {
				b.WriteString(helpStyle.Render(prefix + branch))
				b.WriteString(inUseRendered)
			}
			b.WriteString("\n")
		}
		if len(m.filteredBranches) > 10 {
			b.WriteString(helpStyle.Render(fmt.Sprintf("  ... and %d more", len(m.filteredBranches)-10)))
			b.WriteString("\n")
		}
		b.WriteString(helpStyle.Render("  up/down: select  Enter/Tab: confirm"))
		b.WriteString("\n")
	} else if m.focusIndex == offset+3 {
		if m.newBranchMode {
			b.WriteString(helpStyle.Render("  New branch name (Ctrl+B to toggle mode)"))
		} else {
			if m.isNewWorktree {
				b.WriteString(helpStyle.Render("  Select existing branch (Ctrl+B to toggle mode)"))
			} else {
				b.WriteString(helpStyle.Render("  Branch to checkout (Ctrl+B for new branch)"))
			}
		}
		b.WriteString("\n")
	}

	// New branch mode: show base branch field
	if m.newBranchMode {
		b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Render("  [New Branch Mode]"))
		b.WriteString("\n\n")

		baseBranchFieldIdx := m.getBaseBranchFieldIndex()
		if m.focusIndex == baseBranchFieldIdx {
			b.WriteString(focusedLabelStyle.Render("Base Branch:"))
		} else {
			b.WriteString(labelStyle.Render("Base Branch:"))
		}
		b.WriteString(m.baseBranchInput.View())
		b.WriteString("\n")

		// Base branch dropdown or help
		if m.baseBranchDropdownOpen && len(m.filteredBaseBranches) > 0 {
			displayCount := len(m.filteredBaseBranches)
			if displayCount > 10 {
				displayCount = 10
			}
			for i := 0; i < displayCount; i++ {
				branch := m.filteredBaseBranches[i]
				prefix := "  "
				if i == m.baseBranchSelectedIndex {
					prefix = "> "
					b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + branch))
				} else {
					b.WriteString(helpStyle.Render(prefix + branch))
				}
				b.WriteString("\n")
			}
			if len(m.filteredBaseBranches) > 10 {
				b.WriteString(helpStyle.Render(fmt.Sprintf("  ... and %d more", len(m.filteredBaseBranches)-10)))
				b.WriteString("\n")
			}
			b.WriteString(helpStyle.Render("  up/down: select  Enter/Tab: confirm"))
			b.WriteString("\n")
		} else if m.focusIndex == baseBranchFieldIdx {
			b.WriteString(helpStyle.Render("  Base branch for new branch creation"))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Prompt field
	promptFieldIdx := m.getPromptFieldIndex()
	argsFieldIdx := promptFieldIdx + 1
	if m.focusIndex == promptFieldIdx {
		b.WriteString(focusedLabelStyle.Render("Prompt:"))
	} else {
		b.WriteString(labelStyle.Render("Prompt:"))
	}
	b.WriteString(m.promptInput.View())
	b.WriteString("\n")

	// Prompt dropdown or help
	if m.promptDropdownOpen && len(m.filteredPrompts) > 0 {
		displayCount := len(m.filteredPrompts)
		if displayCount > 5 {
			displayCount = 5
		}
		for i := 0; i < displayCount; i++ {
			p := m.filteredPrompts[i]
			prefix := "  "
			if i == m.promptSelectedIndex {
				prefix = "> "
				b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + p))
			} else {
				b.WriteString(helpStyle.Render(prefix + p))
			}
			b.WriteString("\n")
		}
		if len(m.filteredPrompts) > 5 {
			b.WriteString(helpStyle.Render(fmt.Sprintf("  ... and %d more", len(m.filteredPrompts)-5)))
			b.WriteString("\n")
		}
		b.WriteString(helpStyle.Render("  up/down: select  Enter/Tab: confirm"))
		b.WriteString("\n")
	} else if m.focusIndex == promptFieldIdx && len(m.prompts) == 0 {
		b.WriteString(helpStyle.Render("  No prompts available. Add files to ~/.ccvalet/prompts/"))
		b.WriteString("\n")
	} else if m.focusIndex == promptFieldIdx {
		b.WriteString(helpStyle.Render("  Type to search prompts... (optional)"))
		b.WriteString("\n")
	}

	// Args field (only when prompt is selected)
	if m.promptInput.Value() != "" {
		b.WriteString("\n")
		if m.focusIndex == argsFieldIdx {
			b.WriteString(focusedLabelStyle.Render("Args:"))
		} else {
			b.WriteString(labelStyle.Render("Args:"))
		}
		b.WriteString(m.argsInput.View())
		b.WriteString("\n")
		if m.focusIndex == argsFieldIdx {
			b.WriteString(helpStyle.Render("  Arguments to pass to the prompt (${args})"))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")

	// Help footer
	b.WriteString(helpStyle.Render("Tab: next  Ctrl+R: refresh branches  Ctrl+W: new worktree  Ctrl+B: new branch  Enter: create  Esc: cancel"))

	return b.String()
}

// handleCreateComplete handles the result of async session creation.
func (m CreateFormModel) handleCreateComplete(msg createFormCompleteMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.processingMsg = ""
		m.err = msg.err
		return m, nil
	}

	// Success: set tmux env var so the parent TUI can pick up the new session ID
	tc, err := tmux.NewMgrClient()
	if err == nil {
		_ = tc.SetEnvironment(tmux.SessionName, "CCVALET_CREATED_SESSION", msg.sessionID)
	}

	return m, tea.Quit
}

// handleSubmit validates the form and submits session creation asynchronously.
func (m CreateFormModel) handleSubmit() (tea.Model, tea.Cmd) {
	name := m.nameInput.Value()
	promptName := m.promptInput.Value()
	promptArgs := m.argsInput.Value()

	repoName := m.repoInput.Value()
	branch := m.branchInput.Value()

	if repoName == "" {
		m.err = fmt.Errorf("repository is required")
		return m, nil
	}

	if branch == "" {
		m.err = fmt.Errorf("branch is required")
		return m, nil
	}

	// Validate prompt if specified
	if promptName != "" {
		found := false
		for _, p := range m.prompts {
			if p == promptName {
				found = true
				break
			}
		}
		if !found {
			m.err = fmt.Errorf("prompt '%s' does not exist", promptName)
			return m, nil
		}
	}

	// Show processing indicator
	m.processingMsg = "Creating..."

	// Capture values for the async command closure
	client := m.client
	stateMgr := m.stateMgr

	var opts daemon.NewOptions
	if m.isNewWorktree {
		baseBranch := m.baseBranchInput.Value()
		worktreeName := m.worktreeInput.Value()
		opts = daemon.NewOptions{
			Name:          name,
			Start:         true,
			Async:         true,
			Repository:    repoName,
			Branch:        branch,
			BaseBranch:    baseBranch,
			NewBranch:     m.newBranchMode,
			IsNewWorktree: true,
			WorktreeName:  worktreeName,
			PromptName:    promptName,
			PromptArgs:    promptArgs,
			HostID:        m.selectedHostID,
		}
	} else {
		selectedWt := m.getSelectedWorktree()
		if selectedWt == nil {
			m.processingMsg = ""
			m.err = fmt.Errorf("worktree is required")
			return m, nil
		}

		baseBranch := ""
		if m.newBranchMode {
			baseBranch = m.baseBranchInput.Value()
		}
		opts = daemon.NewOptions{
			Name:          name,
			WorkDir:       selectedWt.Path,
			Start:         true,
			Repository:    repoName,
			Branch:        branch,
			NewBranch:     m.newBranchMode,
			BaseBranch:    baseBranch,
			IsNewWorktree: false,
			PromptName:    promptName,
			PromptArgs:    promptArgs,
			HostID:        m.selectedHostID,
		}
	}

	return m, func() tea.Msg {
		info, err := client.NewWithOptions(opts)
		if err != nil {
			return createFormCompleteMsg{err: err}
		}
		// Remember last used repository on success
		if stateMgr != nil {
			stateMgr.SetLastUsedRepository(repoName)
			_ = stateMgr.Save()
		}
		return createFormCompleteMsg{sessionID: info.ID}
	}
}

// --- Field helpers ---

// hasHostField returns true when the host selection field should be shown.
func (m *CreateFormModel) hasHostField() bool {
	return len(m.hosts) > 1
}

// getHostFieldOffset returns the field index offset caused by the host field.
func (m *CreateFormModel) getHostFieldOffset() int {
	if m.hasHostField() {
		return 1
	}
	return 0
}

// getFieldCount returns the total number of visible fields based on current mode.
func (m *CreateFormModel) getFieldCount() int {
	offset := m.getHostFieldOffset()
	hasArgs := m.promptInput.Value() != ""

	if m.newBranchMode {
		// New branch: [host], name, repo, worktree, branch, base, prompt, [args]
		if hasArgs {
			return offset + 7
		}
		return offset + 6
	}
	// Existing branch: [host], name, repo, worktree, branch, prompt, [args]
	if hasArgs {
		return offset + 6
	}
	return offset + 5
}

// getBaseBranchFieldIndex returns the index of the base branch field.
func (m *CreateFormModel) getBaseBranchFieldIndex() int {
	if m.newBranchMode {
		return m.getHostFieldOffset() + 4
	}
	return -1
}

// getWorktreeNameFieldIndex returns -1 (worktree name is integrated into the worktree field).
func (m *CreateFormModel) getWorktreeNameFieldIndex() int {
	return -1
}

// getPromptFieldIndex returns the index of the prompt field.
func (m *CreateFormModel) getPromptFieldIndex() int {
	offset := m.getHostFieldOffset()
	if m.newBranchMode {
		return offset + 5
	}
	return offset + 4
}

// --- Focus management ---

// updateInputFocus updates input focus based on focusIndex.
func (m *CreateFormModel) updateInputFocus() {
	m.hostInput.Blur()
	m.nameInput.Blur()
	m.repoInput.Blur()
	m.worktreeInput.Blur()
	m.branchInput.Blur()
	m.baseBranchInput.Blur()
	m.promptInput.Blur()
	m.argsInput.Blur()

	offset := m.getHostFieldOffset()
	promptFieldIdx := m.getPromptFieldIndex()
	baseBranchFieldIdx := m.getBaseBranchFieldIndex()
	hasArgs := m.promptInput.Value() != ""
	argsFieldIdx := promptFieldIdx + 1

	fi := m.focusIndex
	if m.hasHostField() && fi == 0 {
		m.hostInput.Focus()
		m.filterHosts()
	} else if fi == offset+0 {
		m.nameInput.Focus()
	} else if fi == offset+1 {
		m.repoInput.Focus()
		m.filterRepositories()
	} else if fi == offset+2 {
		m.worktreeInput.Focus()
		if !m.isNewWorktree {
			m.filterWorktrees()
		}
	} else if fi == offset+3 {
		m.branchInput.Focus()
		m.filterBranches()
	} else if fi == baseBranchFieldIdx && baseBranchFieldIdx >= 0 {
		m.baseBranchInput.Focus()
		m.filterBaseBranches()
	} else if fi == promptFieldIdx {
		m.promptInput.Focus()
		m.filterPrompts()
	} else if hasArgs && fi == argsFieldIdx {
		m.argsInput.Focus()
	}

	// Close prompt dropdown when not focused
	if fi != promptFieldIdx {
		m.promptDropdownOpen = false
	}
}

// updateFocusedInput updates the currently focused input field.
func (m *CreateFormModel) updateFocusedInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd

	offset := m.getHostFieldOffset()
	promptFieldIdx := m.getPromptFieldIndex()
	baseBranchFieldIdx := m.getBaseBranchFieldIndex()
	hasArgs := m.promptInput.Value() != ""
	argsFieldIdx := promptFieldIdx + 1

	fi := m.focusIndex
	if m.hasHostField() && fi == 0 {
		oldHost := m.hostInput.Value()
		m.hostInput, cmd = m.hostInput.Update(msg)
		if oldHost != m.hostInput.Value() {
			m.filterHosts()
		}
	} else if fi == offset+0 {
		m.nameInput, cmd = m.nameInput.Update(msg)
	} else if fi == offset+1 {
		oldRepo := m.repoInput.Value()
		m.repoInput, cmd = m.repoInput.Update(msg)
		if oldRepo != m.repoInput.Value() {
			m.filterRepositories()
		}
	} else if fi == offset+2 {
		oldWorktree := m.worktreeInput.Value()
		m.worktreeInput, cmd = m.worktreeInput.Update(msg)
		if !m.isNewWorktree && oldWorktree != m.worktreeInput.Value() {
			m.filterWorktrees()
		}
	} else if fi == offset+3 {
		oldBranch := m.branchInput.Value()
		m.branchInput, cmd = m.branchInput.Update(msg)
		if oldBranch != m.branchInput.Value() {
			m.filterBranches()
		}
	} else if fi == baseBranchFieldIdx && baseBranchFieldIdx >= 0 {
		oldBaseBranch := m.baseBranchInput.Value()
		m.baseBranchInput, cmd = m.baseBranchInput.Update(msg)
		if oldBaseBranch != m.baseBranchInput.Value() {
			m.filterBaseBranches()
		}
	} else if fi == promptFieldIdx {
		oldPrompt := m.promptInput.Value()
		m.promptInput, cmd = m.promptInput.Update(msg)
		if oldPrompt != m.promptInput.Value() {
			m.filterPrompts()
		}
	} else if hasArgs && fi == argsFieldIdx {
		m.argsInput, cmd = m.argsInput.Update(msg)
	}

	return cmd
}

// --- Dropdown management ---

// closeAllDropdowns closes all dropdown menus.
func (m *CreateFormModel) closeAllDropdowns() {
	m.hostDropdownOpen = false
	m.repoDropdownOpen = false
	m.worktreeDropdownOpen = false
	m.branchDropdownOpen = false
	m.baseBranchDropdownOpen = false
	m.promptDropdownOpen = false
}

// --- Data loaders ---

// loadRepositories loads the repository list from the daemon for the selected host.
func (m *CreateFormModel) loadRepositories() {
	repoInfos, err := m.client.ListRepos(m.selectedHostID)
	if err != nil {
		m.repositories = nil
		m.filteredRepos = nil
		return
	}

	repos := make([]config.RepositoryConfig, 0, len(repoInfos))
	for _, r := range repoInfos {
		repos = append(repos, config.RepositoryConfig{
			Name:       r.Name,
			Path:       r.Path,
			BaseBranch: r.BaseBranch,
		})
	}

	m.repositories = repos
	m.filteredRepos = repos
	m.repoSelectedIndex = 0
}

// loadWorktrees loads the worktree list for the currently selected repository.
func (m *CreateFormModel) loadWorktrees() {
	repoName := m.repoInput.Value()
	if repoName == "" {
		m.worktrees = nil
		m.filteredWorktrees = nil
		return
	}

	wtInfos, err := m.client.ListWorktrees(m.selectedHostID, repoName)
	if err != nil {
		m.worktrees = nil
		m.filteredWorktrees = nil
		return
	}

	wts := make([]worktree.Worktree, 0, len(wtInfos))
	for _, info := range wtInfos {
		wts = append(wts, worktree.Worktree{
			Path:       info.Path,
			Branch:     info.Branch,
			RepoName:   info.RepoName,
			IsMain:     info.IsMain,
			IsManaged:  info.IsManaged,
			IsDetached: info.IsDetached,
		})
	}

	m.worktrees = wts
	m.filteredWorktrees = wts
	m.worktreeSelectedIndex = 0
}

// loadBranches loads the local branch list for the selected repository.
func (m *CreateFormModel) loadBranches() {
	repoName := m.repoInput.Value()
	if repoName == "" {
		m.branches = nil
		m.filteredBranches = nil
		return
	}

	branches, err := m.client.ListBranches(m.selectedHostID, repoName, false)
	if err != nil {
		m.branches = nil
		m.filteredBranches = nil
		return
	}

	m.branches = branches
	m.filteredBranches = branches
	m.branchSelectedIndex = 0
}

// loadAllBranches loads all branches (local + remote) for the selected repository.
func (m *CreateFormModel) loadAllBranches() {
	repoName := m.repoInput.Value()
	if repoName == "" {
		m.allBranches = nil
		m.filteredBaseBranches = nil
		return
	}

	branches, err := m.client.ListBranches(m.selectedHostID, repoName, true)
	if err != nil {
		m.allBranches = nil
		m.filteredBaseBranches = nil
		return
	}

	m.allBranches = branches
	m.filteredBaseBranches = branches
	m.baseBranchSelectedIndex = 0
}

// --- Filters ---

// filterRepositories filters the repository list based on the input value.
func (m *CreateFormModel) filterRepositories() {
	query := strings.ToLower(m.repoInput.Value())
	if query == "" {
		m.filteredRepos = m.repositories
	} else {
		m.filteredRepos = make([]config.RepositoryConfig, 0)
		for _, repo := range m.repositories {
			if strings.Contains(strings.ToLower(repo.Name), query) {
				m.filteredRepos = append(m.filteredRepos, repo)
			}
		}
	}
	m.repoSelectedIndex = 0
	m.repoDropdownOpen = len(m.filteredRepos) > 0
}

// filterHosts filters the host list based on the input value.
func (m *CreateFormModel) filterHosts() {
	query := strings.ToLower(m.hostInput.Value())
	if query == "" {
		m.filteredHosts = m.hosts
	} else {
		m.filteredHosts = make([]daemon.HostInfo, 0)
		for _, h := range m.hosts {
			if strings.Contains(strings.ToLower(h.ID), query) {
				m.filteredHosts = append(m.filteredHosts, h)
			}
		}
	}
	if len(m.filteredHosts) > 0 {
		m.hostDropdownOpen = true
		m.hostSelectedIndex = 0
	} else {
		m.hostDropdownOpen = false
	}
}

// filterBranches filters the branch list based on the input value.
func (m *CreateFormModel) filterBranches() {
	// In new branch mode, free text input; no dropdown
	if m.newBranchMode {
		m.branchDropdownOpen = false
		return
	}

	if len(m.branches) == 0 {
		m.loadBranches()
	}

	query := strings.ToLower(m.branchInput.Value())
	if query == "" {
		m.filteredBranches = m.branches
	} else {
		m.filteredBranches = make([]string, 0)
		for _, branch := range m.branches {
			if strings.Contains(strings.ToLower(branch), query) {
				m.filteredBranches = append(m.filteredBranches, branch)
			}
		}
	}
	m.branchSelectedIndex = 0
	m.branchDropdownOpen = len(m.filteredBranches) > 0
}

// filterBaseBranches filters the base branch list based on the input value.
func (m *CreateFormModel) filterBaseBranches() {
	if len(m.allBranches) == 0 {
		m.loadAllBranches()
	}

	query := strings.ToLower(m.baseBranchInput.Value())
	if query == "" {
		m.filteredBaseBranches = m.allBranches
	} else {
		m.filteredBaseBranches = make([]string, 0)
		for _, branch := range m.allBranches {
			if strings.Contains(strings.ToLower(branch), query) {
				m.filteredBaseBranches = append(m.filteredBaseBranches, branch)
			}
		}
	}
	m.baseBranchSelectedIndex = 0
	m.baseBranchDropdownOpen = len(m.filteredBaseBranches) > 0
}

// filterWorktrees filters the worktree list based on the input value.
func (m *CreateFormModel) filterWorktrees() {
	if m.isNewWorktree {
		m.worktreeDropdownOpen = false
		return
	}

	if len(m.worktrees) == 0 {
		m.loadWorktrees()
	}

	query := strings.ToLower(m.worktreeInput.Value())
	if query == "" {
		m.filteredWorktrees = m.worktrees
	} else {
		m.filteredWorktrees = make([]worktree.Worktree, 0)
		for _, wt := range m.worktrees {
			if strings.Contains(strings.ToLower(wt.Branch), query) ||
				strings.Contains(strings.ToLower(wt.Path), query) {
				m.filteredWorktrees = append(m.filteredWorktrees, wt)
			}
		}
	}
	m.worktreeSelectedIndex = 0
	m.worktreeDropdownOpen = true
}

// filterPrompts filters the prompt list based on the input value.
func (m *CreateFormModel) filterPrompts() {
	query := strings.ToLower(m.promptInput.Value())
	if query == "" {
		m.filteredPrompts = m.prompts
	} else {
		m.filteredPrompts = make([]string, 0)
		for _, p := range m.prompts {
			if strings.Contains(strings.ToLower(p), query) {
				m.filteredPrompts = append(m.filteredPrompts, p)
			}
		}
	}
	m.promptDropdownOpen = len(m.filteredPrompts) > 0
	m.promptSelectedIndex = 0
}

// --- Selectors ---

// selectCurrentHost confirms the currently selected host and resets downstream fields.
func (m *CreateFormModel) selectCurrentHost() {
	if len(m.filteredHosts) > 0 && m.hostSelectedIndex < len(m.filteredHosts) {
		selected := m.filteredHosts[m.hostSelectedIndex]
		m.hostInput.SetValue(selected.ID)
		m.hostDropdownOpen = false
		m.selectedHostID = selected.ID
		// Reset downstream fields
		m.repoInput.Reset()
		m.worktreeInput.Reset()
		m.branchInput.Reset()
		m.baseBranchInput.Reset()
		m.repositories = nil
		m.filteredRepos = nil
		m.worktrees = nil
		m.filteredWorktrees = nil
		m.branches = nil
		m.filteredBranches = nil
		m.allBranches = nil
		// Load repositories for the new host
		m.loadRepositories()
	}
}

// selectCurrentRepo confirms the currently selected repository and loads worktrees/branches.
func (m *CreateFormModel) selectCurrentRepo() {
	if len(m.filteredRepos) > 0 && m.repoSelectedIndex < len(m.filteredRepos) {
		selected := m.filteredRepos[m.repoSelectedIndex]
		m.repoInput.SetValue(selected.Name)
		m.repoDropdownOpen = false
		m.updateBaseBranchDefault()
		m.loadBranches()
		m.loadWorktrees()
		m.allBranches = nil
		// Reset worktree selection
		m.worktreeInput.Reset()
		m.isNewWorktree = false
	}
}

// selectCurrentBranch confirms the currently selected branch.
func (m *CreateFormModel) selectCurrentBranch() {
	if len(m.filteredBranches) > 0 && m.branchSelectedIndex < len(m.filteredBranches) {
		selected := m.filteredBranches[m.branchSelectedIndex]
		m.branchInput.SetValue(selected)
		m.branchDropdownOpen = false
	}
}

// selectCurrentBaseBranch confirms the currently selected base branch.
func (m *CreateFormModel) selectCurrentBaseBranch() {
	if len(m.filteredBaseBranches) > 0 && m.baseBranchSelectedIndex < len(m.filteredBaseBranches) {
		selected := m.filteredBaseBranches[m.baseBranchSelectedIndex]
		m.baseBranchInput.SetValue(selected)
		m.baseBranchDropdownOpen = false
	}
}

// selectCurrentWorktree confirms the currently selected worktree.
func (m *CreateFormModel) selectCurrentWorktree() {
	if len(m.filteredWorktrees) > 0 && m.worktreeSelectedIndex < len(m.filteredWorktrees) {
		selected := m.filteredWorktrees[m.worktreeSelectedIndex]
		m.isNewWorktree = false
		displayText := formatWorktreeDisplay(&selected)
		m.worktreeInput.SetValue(displayText)
		// Set the branch from the selected worktree
		m.branchInput.SetValue(selected.Branch)
		m.newBranchMode = false
	}
	m.worktreeDropdownOpen = false
}

// selectCurrentPrompt confirms the currently selected prompt.
func (m *CreateFormModel) selectCurrentPrompt() {
	if len(m.filteredPrompts) > 0 && m.promptSelectedIndex < len(m.filteredPrompts) {
		selected := m.filteredPrompts[m.promptSelectedIndex]
		m.promptInput.SetValue(selected)
		m.promptDropdownOpen = false
	}
}

// --- Worktree helpers ---

// getSelectedWorktree returns the currently selected worktree (nil in new worktree mode).
func (m *CreateFormModel) getSelectedWorktree() *worktree.Worktree {
	if m.isNewWorktree {
		return nil
	}
	for _, wt := range m.worktrees {
		displayText := formatWorktreeDisplay(&wt)
		if m.worktreeInput.Value() == displayText {
			return &wt
		}
	}
	return nil
}

// updateBaseBranchDefault sets the default base branch from the repository config.
func (m *CreateFormModel) updateBaseBranchDefault() {
	repoName := m.repoInput.Value()
	for _, repo := range m.repositories {
		if repo.Name == repoName && repo.BaseBranch != "" {
			m.baseBranchInput.SetValue(repo.BaseBranch)
			return
		}
	}
	m.baseBranchInput.SetValue("")
}

// --- In-use helpers ---

// buildWorktreeSessionMap builds a map from worktree path to session info
// for the currently selected repository and host.
func (m CreateFormModel) buildWorktreeSessionMap() map[string]session.Info {
	repo := m.repoInput.Value()
	hostID := m.selectedHostID
	if hostID == "" {
		hostID = "local"
	}

	result := make(map[string]session.Info)
	for _, sess := range m.sessions {
		sessHost := sess.HostID
		if sessHost == "" {
			sessHost = "local"
		}
		if sess.Repository == repo && sessHost == hostID && sess.WorkDir != "" {
			if _, exists := result[sess.WorkDir]; !exists {
				result[sess.WorkDir] = sess
			}
		}
	}
	return result
}

// buildBranchSessionMap builds a map from branch name to session info
// for the currently selected repository and host.
func (m CreateFormModel) buildBranchSessionMap() map[string]session.Info {
	repo := m.repoInput.Value()
	hostID := m.selectedHostID
	if hostID == "" {
		hostID = "local"
	}

	result := make(map[string]session.Info)
	for _, sess := range m.sessions {
		sessHost := sess.HostID
		if sessHost == "" {
			sessHost = "local"
		}
		if sess.Repository == repo && sessHost == hostID && sess.Branch != "" {
			if _, exists := result[sess.Branch]; !exists {
				result[sess.Branch] = sess
			}
		}
	}
	return result
}
