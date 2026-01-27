package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/prompt"
	"github.com/takaaki-s/claude-code-valet/internal/repository"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

// Mode represents the current TUI mode
type Mode int

const (
	ModeList Mode = iota
	ModeCreate
)

// KeyMap defines key bindings
type KeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Enter   key.Binding
	New      key.Binding
	Kill     key.Binding
	Delete   key.Binding
	Cancel   key.Binding
	Refresh  key.Binding
	Resume   key.Binding
	Quit     key.Binding
	Help     key.Binding
	PrevPage key.Binding
	NextPage key.Binding

	// セッション作成フォーム
	NextField      key.Binding
	PrevField      key.Binding
	ToggleWorktree key.Binding
	ToggleBranch   key.Binding
	Submit         key.Binding
	CancelForm     key.Binding
}

// NewKeyMap creates a KeyMap from config
func NewKeyMap(cfg config.KeybindingsConfig) KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys(cfg.Up...),
			key.WithHelp(strings.Join(cfg.Up, "/"), "up"),
		),
		Down: key.NewBinding(
			key.WithKeys(cfg.Down...),
			key.WithHelp(strings.Join(cfg.Down, "/"), "down"),
		),
		Enter: key.NewBinding(
			key.WithKeys(cfg.Attach...),
			key.WithHelp(strings.Join(cfg.Attach, "/"), "attach"),
		),
		New: key.NewBinding(
			key.WithKeys(cfg.New...),
			key.WithHelp(strings.Join(cfg.New, "/"), "new session"),
		),
		Kill: key.NewBinding(
			key.WithKeys(cfg.Kill...),
			key.WithHelp(strings.Join(cfg.Kill, "/"), "kill"),
		),
		Delete: key.NewBinding(
			key.WithKeys(cfg.Delete...),
			key.WithHelp(strings.Join(cfg.Delete, "/"), "delete"),
		),
		Cancel: key.NewBinding(
			key.WithKeys(cfg.Cancel...),
			key.WithHelp(strings.Join(cfg.Cancel, "/"), "cancel queued"),
		),
		Refresh: key.NewBinding(
			key.WithKeys(cfg.Refresh...),
			key.WithHelp(strings.Join(cfg.Refresh, "/"), "refresh"),
		),
		Resume: key.NewBinding(
			key.WithKeys(cfg.Resume...),
			key.WithHelp(strings.Join(cfg.Resume, "/"), "resume"),
		),
		Quit: key.NewBinding(
			key.WithKeys(cfg.Quit...),
			key.WithHelp(strings.Join(cfg.Quit, "/"), "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys(cfg.Help...),
			key.WithHelp(strings.Join(cfg.Help, "/"), "help"),
		),
		PrevPage: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/h", "prev page"),
		),
		NextPage: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→/l", "next page"),
		),
		NextField: key.NewBinding(
			key.WithKeys(cfg.NextField...),
			key.WithHelp(strings.Join(cfg.NextField, "/"), "next field"),
		),
		PrevField: key.NewBinding(
			key.WithKeys(cfg.PrevField...),
			key.WithHelp(strings.Join(cfg.PrevField, "/"), "prev field"),
		),
		ToggleWorktree: key.NewBinding(
			key.WithKeys(cfg.ToggleWorktree...),
			key.WithHelp(strings.Join(cfg.ToggleWorktree, "/"), "toggle worktree"),
		),
		ToggleBranch: key.NewBinding(
			key.WithKeys(cfg.ToggleBranch...),
			key.WithHelp(strings.Join(cfg.ToggleBranch, "/"), "toggle branch"),
		),
		Submit: key.NewBinding(
			key.WithKeys(cfg.Submit...),
			key.WithHelp(strings.Join(cfg.Submit, "/"), "submit"),
		),
		CancelForm: key.NewBinding(
			key.WithKeys(cfg.CancelForm...),
			key.WithHelp(strings.Join(cfg.CancelForm, "/"), "cancel"),
		),
	}
}

// Model is the TUI model
type Model struct {
	client       *daemon.Client
	sessions     []session.Info
	cursor       int
	width        int
	height       int
	err          error
	showHelp     bool
	attachSignal chan string // Signal to attach to a session
	keys         KeyMap      // キーバインド設定

	// Create mode
	mode       Mode
	nameInput  textinput.Model
	focusIndex int // フォーカス中のフィールドインデックス

	// リポジトリ選択
	repoInput         textinput.Model
	configMgr         *config.Manager
	stateMgr          *config.StateManager
	repositories      []config.RepositoryConfig
	filteredRepos     []config.RepositoryConfig // フィルタリングされたリポジトリ
	repoSelectedIndex int                       // 選択中のリポジトリインデックス
	repoDropdownOpen  bool                      // リポジトリドロップダウンが開いているか

	// Worktree選択
	worktreeInput           textinput.Model
	worktrees               []worktree.Worktree // 現在のリポジトリのworktree一覧
	filteredWorktrees       []worktree.Worktree // フィルタリングされたworktree
	worktreeSelectedIndex   int                 // 選択中のworktreeインデックス
	worktreeDropdownOpen    bool                // worktreeドロップダウンが開いているか
	isNewWorktree           bool                // 新規worktree作成モードか
	worktreeNameInput       textinput.Model     // worktree名（新規作成時）

	// ブランチ選択
	branchInput         textinput.Model
	branches            []string // 現在のリポジトリのブランチ一覧
	filteredBranches    []string // フィルタリングされたブランチ
	branchSelectedIndex int      // 選択中のブランチインデックス
	branchDropdownOpen  bool     // ブランチドロップダウンが開いているか
	newBranchMode       bool     // 新規ブランチ作成モード

	// ベースブランチ選択（新規ブランチモード用）
	baseBranchInput         textinput.Model
	allBranches             []string // 全ブランチ一覧（ローカル＋リモート）
	filteredBaseBranches    []string // フィルタリングされたベースブランチ
	baseBranchSelectedIndex int      // 選択中のベースブランチインデックス
	baseBranchDropdownOpen  bool     // ベースブランチドロップダウンが開いているか

	// プロンプト選択
	promptInput         textinput.Model
	argsInput           textinput.Model
	prompts             []string // 利用可能なプロンプト一覧
	filteredPrompts     []string // フィルタリングされたプロンプト
	promptSelectedIndex int      // 選択中のプロンプトインデックス
	promptDropdownOpen  bool     // プロンプトドロップダウンが開いているか

	// Pagination
	currentPage int // 現在のページ（0-indexed）

	// Delete confirmation
	confirmDelete    bool   // 削除確認中かどうか
	deleteTargetID   string // 削除対象のセッションID
	deleteTargetName string // 削除対象のセッション名（表示用）
}

// NewModel creates a new TUI model
func NewModel(client *daemon.Client) Model {
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

	// Worktree name input (for new worktree)
	worktreeNameInput := textinput.New()
	worktreeNameInput.Placeholder = "worktree name (optional, defaults to branch)"
	worktreeNameInput.CharLimit = 100

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

	// Initialize keybindings
	var keybindings config.KeybindingsConfig
	if configMgr != nil {
		keybindings = configMgr.GetKeybindings()
	} else {
		keybindings = config.DefaultKeybindings()
	}
	keys := NewKeyMap(keybindings)

	var repos []config.RepositoryConfig
	if configMgr != nil {
		repos = configMgr.GetRepositories()
	}

	// 前回使用したリポジトリをデフォルト値として設定
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

	return Model{
		client:            client,
		attachSignal:      make(chan string, 1),
		keys:              keys,
		mode:              ModeList,
		nameInput:         nameInput,
		repoInput:         repoInput,
		worktreeInput:     worktreeInput,
		worktreeNameInput: worktreeNameInput,
		branchInput:       branchInput,
		baseBranchInput:   baseBranchInput,
		promptInput:       promptInput,
		argsInput:         argsInput,
		configMgr:         configMgr,
		stateMgr:          stateMgr,
		repositories:      repos,
		filteredRepos:     repos,
		prompts:           promptNames,
		filteredPrompts:   promptNames,
	}
}

// AttachSignal returns the attach signal channel
func (m *Model) AttachSignal() <-chan string {
	return m.attachSignal
}

// getItemsPerPage calculates how many items fit on one page
func (m *Model) getItemsPerPage() int {
	// Subtract header lines (title, stats, separator, footer)
	// Header: 3 lines, Footer: 2 lines (page info + help)
	availableLines := m.height - 8
	if availableLines < 3 {
		availableLines = 3
	}
	// Each session takes 1 line (2 if error)
	return availableLines
}

// getTotalPages calculates the total number of pages
func (m *Model) getTotalPages() int {
	if len(m.sessions) == 0 {
		return 1
	}
	itemsPerPage := m.getItemsPerPage()
	totalPages := (len(m.sessions) + itemsPerPage - 1) / itemsPerPage
	if totalPages < 1 {
		totalPages = 1
	}
	return totalPages
}

// getPageSessions returns sessions for the current page
func (m *Model) getPageSessions() []session.Info {
	if len(m.sessions) == 0 {
		return nil
	}
	itemsPerPage := m.getItemsPerPage()
	start := m.currentPage * itemsPerPage
	end := start + itemsPerPage
	if start >= len(m.sessions) {
		start = 0
		m.currentPage = 0
		end = itemsPerPage
	}
	if end > len(m.sessions) {
		end = len(m.sessions)
	}
	return m.sessions[start:end]
}

// Messages
type sessionsMsg []session.Info
type errMsg error
type tickMsg time.Time

// Commands
func (m *Model) fetchSessions() tea.Msg {
	sessions, err := m.client.List()
	if err != nil {
		return errMsg(err)
	}
	return sessionsMsg(sessions)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchSessions,
		tickCmd(),
	)
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle window size for all modes
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = msg.Width
		m.height = msg.Height
	}

	// Mode-specific handling
	switch m.mode {
	case ModeCreate:
		return m.updateCreateMode(msg)
	default:
		return m.updateListMode(msg)
	}
}

func (m Model) updateListMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// 削除確認モード中の処理
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y", "enter":
				// 削除実行
				if err := m.client.Delete(m.deleteTargetID); err != nil {
					m.err = err
				}
				m.confirmDelete = false
				m.deleteTargetID = ""
				m.deleteTargetName = ""
				// Reset cursor if needed
				newPageSessions := m.getPageSessions()
				if m.cursor >= len(newPageSessions) && m.cursor > 0 {
					m.cursor--
				}
				return m, m.fetchSessions
			case "n", "N", "esc":
				// キャンセル
				m.confirmDelete = false
				m.deleteTargetID = ""
				m.deleteTargetName = ""
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}

		case key.Matches(msg, m.keys.Down):
			// Limit cursor to current page
			pageSessions := m.getPageSessions()
			if m.cursor < len(pageSessions)-1 {
				m.cursor++
			}

		case key.Matches(msg, m.keys.Enter):
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				// Don't attach to queued or creating sessions
				if sess.Status == session.StatusQueued {
					m.err = fmt.Errorf("cannot attach to queued session")
					return m, nil
				}
				if sess.Status == session.StatusCreating {
					m.err = fmt.Errorf("cannot attach to creating session")
					return m, nil
				}
				m.attachSignal <- sess.ID
				return m, tea.Quit
			}

		case key.Matches(msg, m.keys.New):
			// Switch to create mode
			m.mode = ModeCreate
			m.nameInput.Reset()
			m.worktreeInput.Reset()
			m.worktreeNameInput.Reset()
			m.branchInput.Reset()
			m.baseBranchInput.Reset()
			m.promptInput.Reset()
			m.argsInput.Reset()
			m.isNewWorktree = false // デフォルトはworktree選択モード（ドロップダウンが開く）
			m.newBranchMode = true  // デフォルトは新規ブランチ作成
			m.worktrees = nil
			m.filteredWorktrees = nil
			m.focusIndex = 0
			m.closeAllDropdowns()
			m.nameInput.Focus()
			return m, textinput.Blink

		case key.Matches(msg, m.keys.Kill):
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				if err := m.client.Kill(sess.ID); err != nil {
					m.err = err
				}
				return m, m.fetchSessions
			}

		case key.Matches(msg, m.keys.Delete):
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				// 確認モードに入る
				m.confirmDelete = true
				m.deleteTargetID = sess.ID
				m.deleteTargetName = sess.Name
				return m, nil
			}

		case key.Matches(msg, m.keys.Cancel):
			// Cancel only works on queued sessions
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				if sess.Status == session.StatusQueued {
					if err := m.client.Delete(sess.ID); err != nil {
						m.err = err
					}
					newPageSessions := m.getPageSessions()
					if m.cursor >= len(newPageSessions) && m.cursor > 0 {
						m.cursor--
					}
					return m, m.fetchSessions
				} else {
					m.err = fmt.Errorf("can only cancel queued sessions")
				}
			}

		case key.Matches(msg, m.keys.Refresh):
			return m, m.fetchSessions

		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp

		case key.Matches(msg, m.keys.PrevPage):
			if m.currentPage > 0 {
				m.currentPage--
				m.cursor = 0 // Reset cursor to first item on new page
			}

		case key.Matches(msg, m.keys.NextPage):
			totalPages := m.getTotalPages()
			if m.currentPage < totalPages-1 {
				m.currentPage++
				m.cursor = 0 // Reset cursor to first item on new page
			}
		}

	case sessionsMsg:
		m.sessions = msg
		m.err = nil
		if m.cursor >= len(m.sessions) && m.cursor > 0 {
			m.cursor = len(m.sessions) - 1
		}

	case errMsg:
		m.err = msg

	case tickMsg:
		return m, tea.Batch(m.fetchSessions, tickCmd())
	}

	return m, nil
}

func (m Model) updateCreateMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	// Calculate field count based on mode
	// フィールド構成:
	// - 0: name
	// - 1: repo
	// - 2: worktree
	// - 3: branch
	// 既存worktree: name(0), repo(1), worktree(2), branch(3), prompt(4), args(5) = 6
	// 新規worktree + 既存ブランチ: name(0), repo(1), worktree(2), branch(3), wtname(4), prompt(5), args(6) = 7
	// 新規worktree + 新規ブランチ: name(0), repo(1), worktree(2), branch(3), base(4), wtname(5), prompt(6), args(7) = 8
	fieldCount := m.getFieldCount()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			// Cancel and return to list mode
			m.mode = ModeList
			return m, nil

		case "ctrl+b":
			// Toggle new branch mode (available for both existing and new worktree)
			m.newBranchMode = !m.newBranchMode
			m.branchInput.Reset()
			m.baseBranchInput.Reset()
			m.baseBranchDropdownOpen = false
			if m.newBranchMode {
				// 新規ブランチモード: ドロップダウンを閉じる
				m.branchDropdownOpen = false
			} else {
				// 既存ブランチモード: ドロップダウンを開く
				m.filterBranches()
			}
			return m, nil

		case "ctrl+w":
			// Toggle worktree mode (new worktree vs select existing)
			m.isNewWorktree = !m.isNewWorktree
			m.worktreeInput.Reset()
			m.branchInput.Reset()
			m.branchDropdownOpen = false
			if m.isNewWorktree {
				// 新規worktreeモード: ドロップダウンを閉じる
				m.worktreeDropdownOpen = false
			} else {
				// 既存worktree選択モード: ドロップダウンを開く
				m.filterWorktrees()
			}
			return m, nil

		case "tab":
			// ドロップダウンが開いている場合は選択を確定（必須フィールドのみ）
			if m.focusIndex == 1 && m.repoDropdownOpen {
				m.selectCurrentRepo()
			}
			if m.focusIndex == 2 && m.worktreeDropdownOpen {
				m.selectCurrentWorktree()
			}
			if m.focusIndex == 3 && m.branchDropdownOpen {
				m.selectCurrentBranch()
			}
			if m.focusIndex == m.getBaseBranchFieldIndex() && m.baseBranchDropdownOpen {
				m.selectCurrentBaseBranch()
			}
			// プロンプトはオプショナルなのでTabでは自動選択しない（ドロップダウンを閉じるだけ）
			// Move focus to next input
			m.focusIndex = (m.focusIndex + 1) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "down":
			// ドロップダウンが開いている場合は候補を移動
			if m.focusIndex == 1 && m.repoDropdownOpen && len(m.filteredRepos) > 0 {
				m.repoSelectedIndex = (m.repoSelectedIndex + 1) % len(m.filteredRepos)
				return m, nil
			}
			if m.focusIndex == 2 && m.worktreeDropdownOpen && len(m.filteredWorktrees) > 0 {
				m.worktreeSelectedIndex = (m.worktreeSelectedIndex + 1) % len(m.filteredWorktrees)
				return m, nil
			}
			if m.focusIndex == 3 && m.branchDropdownOpen && len(m.filteredBranches) > 0 {
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
			// 通常の動作：次のフィールドへ
			m.focusIndex = (m.focusIndex + 1) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "shift+tab":
			// Move focus to previous input
			m.focusIndex = (m.focusIndex - 1 + fieldCount) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "up":
			// ドロップダウンが開いている場合は候補を移動
			if m.focusIndex == 1 && m.repoDropdownOpen && len(m.filteredRepos) > 0 {
				m.repoSelectedIndex = (m.repoSelectedIndex - 1 + len(m.filteredRepos)) % len(m.filteredRepos)
				return m, nil
			}
			if m.focusIndex == 2 && m.worktreeDropdownOpen && len(m.filteredWorktrees) > 0 {
				m.worktreeSelectedIndex = (m.worktreeSelectedIndex - 1 + len(m.filteredWorktrees)) % len(m.filteredWorktrees)
				return m, nil
			}
			if m.focusIndex == 3 && m.branchDropdownOpen && len(m.filteredBranches) > 0 {
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
			// 通常の動作：前のフィールドへ
			m.focusIndex = (m.focusIndex - 1 + fieldCount) % fieldCount
			m.closeAllDropdowns()
			m.updateInputFocus()
			return m, textinput.Blink

		case "enter":
			// ドロップダウンが開いている場合は選択を確定
			if m.focusIndex == 1 && m.repoDropdownOpen {
				m.selectCurrentRepo()
				m.repoDropdownOpen = false
				return m, nil
			}
			if m.focusIndex == 2 && m.worktreeDropdownOpen {
				m.selectCurrentWorktree()
				m.worktreeDropdownOpen = false
				return m, nil
			}
			if m.focusIndex == 3 && m.branchDropdownOpen {
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
			return m.handleCreateSubmit()
		}
	}

	// Update the focused input
	cmd = m.updateFocusedInput(msg)

	return m, cmd
}

// getFieldCount returns the number of fields based on current mode
func (m *Model) getFieldCount() int {
	// argsはpromptが指定されている場合のみ表示
	hasArgs := m.promptInput.Value() != ""

	if !m.isNewWorktree {
		if m.newBranchMode {
			// 既存worktree + 新規ブランチ: name(0), repo(1), worktree(2), branch(3), base(4), prompt(5), [args(6)]
			if hasArgs {
				return 7
			}
			return 6
		}
		// 既存worktree + 既存ブランチ: name(0), repo(1), worktree(2), branch(3), prompt(4), [args(5)]
		if hasArgs {
			return 6
		}
		return 5
	}
	if m.newBranchMode {
		// 新規worktree + 新規ブランチ: name(0), repo(1), worktree(2), branch(3), base(4), prompt(5), [args(6)]
		// worktree名はworktreeフィールドに統合
		if hasArgs {
			return 7
		}
		return 6
	}
	// 新規worktree + 既存ブランチ: name(0), repo(1), worktree(2), branch(3), prompt(4), [args(5)]
	if hasArgs {
		return 6
	}
	return 5
}

// getBaseBranchFieldIndex returns the index of base branch field (only valid for new branch mode)
func (m *Model) getBaseBranchFieldIndex() int {
	if m.newBranchMode {
		return 4 // 新規ブランチモード時はbase branchは常にindex 4
	}
	return -1 // not applicable
}

// getWorktreeNameFieldIndex returns the index of worktree name field
// 新規worktreeモードではworktreeフィールドに統合されたので常に-1を返す
func (m *Model) getWorktreeNameFieldIndex() int {
	return -1 // worktree名はworktreeフィールドに統合
}

// updateInputFocus updates input focus based on current mode and focusIndex
func (m *Model) updateInputFocus() {
	m.nameInput.Blur()
	m.repoInput.Blur()
	m.worktreeInput.Blur()
	m.branchInput.Blur()
	m.baseBranchInput.Blur()
	m.promptInput.Blur()
	m.argsInput.Blur()

	promptFieldIdx := m.getPromptFieldIndex()
	baseBranchFieldIdx := m.getBaseBranchFieldIndex()
	hasArgs := m.promptInput.Value() != ""
	argsFieldIdx := promptFieldIdx + 1

	switch m.focusIndex {
	case 0:
		m.nameInput.Focus()
	case 1:
		m.repoInput.Focus()
		m.filterRepositories()
	case 2:
		m.worktreeInput.Focus()
		// 新規worktreeモードではドロップダウンを開かない（テキスト入力）
		if !m.isNewWorktree {
			m.filterWorktrees()
		}
	case 3:
		m.branchInput.Focus()
		m.filterBranches()
	default:
		// 動的フィールド
		if m.focusIndex == baseBranchFieldIdx {
			m.baseBranchInput.Focus()
			m.filterBaseBranches()
		} else if m.focusIndex == promptFieldIdx {
			m.promptInput.Focus()
			m.filterPrompts()
		} else if hasArgs && m.focusIndex == argsFieldIdx {
			m.argsInput.Focus()
		}
	}

	// フォーカスがpromptでない場合はドロップダウンを閉じる
	if m.focusIndex != promptFieldIdx {
		m.promptDropdownOpen = false
	}
}

// updateFocusedInput updates the currently focused input
func (m *Model) updateFocusedInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd

	promptFieldIdx := m.getPromptFieldIndex()
	baseBranchFieldIdx := m.getBaseBranchFieldIndex()
	hasArgs := m.promptInput.Value() != ""
	argsFieldIdx := promptFieldIdx + 1

	switch m.focusIndex {
	case 0:
		m.nameInput, cmd = m.nameInput.Update(msg)
	case 1:
		oldRepo := m.repoInput.Value()
		m.repoInput, cmd = m.repoInput.Update(msg)
		if oldRepo != m.repoInput.Value() {
			m.filterRepositories()
		}
	case 2:
		oldWorktree := m.worktreeInput.Value()
		m.worktreeInput, cmd = m.worktreeInput.Update(msg)
		// 既存worktreeモードでのみフィルタリング
		if !m.isNewWorktree && oldWorktree != m.worktreeInput.Value() {
			m.filterWorktrees()
		}
	case 3:
		oldBranch := m.branchInput.Value()
		m.branchInput, cmd = m.branchInput.Update(msg)
		if oldBranch != m.branchInput.Value() {
			m.filterBranches()
		}
	default:
		// 動的フィールド
		if m.focusIndex == baseBranchFieldIdx {
			oldBaseBranch := m.baseBranchInput.Value()
			m.baseBranchInput, cmd = m.baseBranchInput.Update(msg)
			if oldBaseBranch != m.baseBranchInput.Value() {
				m.filterBaseBranches()
			}
		} else if m.focusIndex == promptFieldIdx {
			oldPrompt := m.promptInput.Value()
			m.promptInput, cmd = m.promptInput.Update(msg)
			if oldPrompt != m.promptInput.Value() {
				m.filterPrompts()
			}
		} else if hasArgs && m.focusIndex == argsFieldIdx {
			m.argsInput, cmd = m.argsInput.Update(msg)
		}
	}

	return cmd
}

// updateBaseBranchDefault はリポジトリ設定からデフォルトのbaseBranchを設定
func (m *Model) updateBaseBranchDefault() {
	repoName := m.repoInput.Value()
	for _, repo := range m.repositories {
		if repo.Name == repoName && repo.BaseBranch != "" {
			m.baseBranchInput.SetValue(repo.BaseBranch)
			return
		}
	}
	// リポジトリが見つからないか、baseBranchが設定されていない場合はクリア
	m.baseBranchInput.SetValue("")
}

// filterRepositories は入力値でリポジトリをフィルタリング
func (m *Model) filterRepositories() {
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
	// インデックスをリセット
	m.repoSelectedIndex = 0
	// 候補があればドロップダウンを開く
	m.repoDropdownOpen = len(m.filteredRepos) > 0
}

// selectCurrentRepo は現在選択中のリポジトリを確定
func (m *Model) selectCurrentRepo() {
	if len(m.filteredRepos) > 0 && m.repoSelectedIndex < len(m.filteredRepos) {
		selected := m.filteredRepos[m.repoSelectedIndex]
		m.repoInput.SetValue(selected.Name)
		m.repoDropdownOpen = false
		m.updateBaseBranchDefault()
		m.loadBranches()    // リポジトリ確定時にローカルブランチ一覧を取得
		m.loadWorktrees()   // リポジトリ確定時にworktree一覧を取得
		m.allBranches = nil // 全ブランチ一覧をクリア（新規ブランチモード用に再取得させる）
		// worktree選択をリセット
		m.worktreeInput.Reset()
		m.isNewWorktree = false // worktree選択モードに戻す
	}
}

// loadBranches は選択中のリポジトリのローカルブランチ一覧を取得（既存ブランチモード用）
func (m *Model) loadBranches() {
	repoName := m.repoInput.Value()
	if repoName == "" || m.configMgr == nil {
		m.branches = nil
		m.filteredBranches = nil
		return
	}

	registry := repository.NewRegistry(m.configMgr)
	// 既存ブランチモード用：ローカルブランチのみ取得
	branches, err := registry.GetLocalBranches(repoName)
	if err != nil {
		m.branches = nil
		m.filteredBranches = nil
		return
	}

	m.branches = branches
	m.filteredBranches = branches
	m.branchSelectedIndex = 0
}

// filterBranches は入力値でブランチをフィルタリング（既存ブランチモード用）
func (m *Model) filterBranches() {
	// 新規ブランチモードでは自由入力なのでドロップダウンを開かない
	if m.newBranchMode {
		m.branchDropdownOpen = false
		return
	}

	// ブランチ一覧がなければロード
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
	// インデックスをリセット
	m.branchSelectedIndex = 0
	// 候補があればドロップダウンを開く
	m.branchDropdownOpen = len(m.filteredBranches) > 0
}

// selectCurrentBranch は現在選択中のブランチを確定
func (m *Model) selectCurrentBranch() {
	if len(m.filteredBranches) > 0 && m.branchSelectedIndex < len(m.filteredBranches) {
		selected := m.filteredBranches[m.branchSelectedIndex]
		m.branchInput.SetValue(selected)
		m.branchDropdownOpen = false
	}
}

// filterBaseBranches は入力値でベースブランチをフィルタリング（新規ブランチモード用）
func (m *Model) filterBaseBranches() {
	// 全ブランチ一覧がなければロード（ローカル＋リモート）
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
	// インデックスをリセット
	m.baseBranchSelectedIndex = 0
	// 候補があればドロップダウンを開く
	m.baseBranchDropdownOpen = len(m.filteredBaseBranches) > 0
}

// selectCurrentBaseBranch は現在選択中のベースブランチを確定
func (m *Model) selectCurrentBaseBranch() {
	if len(m.filteredBaseBranches) > 0 && m.baseBranchSelectedIndex < len(m.filteredBaseBranches) {
		selected := m.filteredBaseBranches[m.baseBranchSelectedIndex]
		m.baseBranchInput.SetValue(selected)
		m.baseBranchDropdownOpen = false
	}
}

// closeAllDropdowns は全てのドロップダウンを閉じる
func (m *Model) closeAllDropdowns() {
	m.repoDropdownOpen = false
	m.worktreeDropdownOpen = false
	m.branchDropdownOpen = false
	m.baseBranchDropdownOpen = false
	m.promptDropdownOpen = false
}

// loadWorktrees は選択中のリポジトリのworktree一覧を取得
func (m *Model) loadWorktrees() {
	repoName := m.repoInput.Value()
	if repoName == "" || m.configMgr == nil {
		m.worktrees = nil
		m.filteredWorktrees = nil
		return
	}

	wtMgr := worktree.NewManager(m.configMgr)
	wts, err := wtMgr.List(repoName)
	if err != nil {
		m.worktrees = nil
		m.filteredWorktrees = nil
		return
	}

	m.worktrees = wts
	m.filteredWorktrees = wts
	m.worktreeSelectedIndex = 0
}

// filterWorktrees は入力値でworktreeをフィルタリング
func (m *Model) filterWorktrees() {
	// 新規worktreeモードではドロップダウンを開かない
	if m.isNewWorktree {
		m.worktreeDropdownOpen = false
		return
	}

	// worktree一覧がなければロード
	if len(m.worktrees) == 0 {
		m.loadWorktrees()
	}

	query := strings.ToLower(m.worktreeInput.Value())
	if query == "" {
		m.filteredWorktrees = m.worktrees
	} else {
		m.filteredWorktrees = make([]worktree.Worktree, 0)
		for _, wt := range m.worktrees {
			// ブランチ名またはパスでフィルタリング
			if strings.Contains(strings.ToLower(wt.Branch), query) ||
				strings.Contains(strings.ToLower(wt.Path), query) {
				m.filteredWorktrees = append(m.filteredWorktrees, wt)
			}
		}
	}
	// インデックスをリセット
	m.worktreeSelectedIndex = 0
	// ドロップダウンを開く
	m.worktreeDropdownOpen = true
}

// selectCurrentWorktree は現在選択中のworktreeを確定
func (m *Model) selectCurrentWorktree() {
	if len(m.filteredWorktrees) > 0 && m.worktreeSelectedIndex < len(m.filteredWorktrees) {
		selected := m.filteredWorktrees[m.worktreeSelectedIndex]
		m.isNewWorktree = false
		// 表示形式: worktree_name (branch) または [main] path
		displayText := formatWorktreeDisplay(&selected)
		m.worktreeInput.SetValue(displayText)
		// 既存worktreeの場合、現在のブランチをデフォルト値として設定
		m.branchInput.SetValue(selected.Branch)
		m.newBranchMode = false // 既存worktreeの場合は既存ブランチモードがデフォルト
	}
	m.worktreeDropdownOpen = false
}

// getSelectedWorktree は選択中のworktreeを返す（既存worktree選択時のみ有効）
func (m *Model) getSelectedWorktree() *worktree.Worktree {
	if m.isNewWorktree {
		return nil
	}
	// worktreeInput.Value()から選択したworktreeを特定
	for _, wt := range m.worktrees {
		displayText := formatWorktreeDisplay(&wt)
		if m.worktreeInput.Value() == displayText {
			return &wt
		}
	}
	return nil
}

// formatWorktreeDisplay はworktreeの表示形式を返す
// リポジトリ本体: [main] /path/to/repo
// 通常のworktree: worktree_name (branch)
func formatWorktreeDisplay(wt *worktree.Worktree) string {
	if wt.IsMain {
		return fmt.Sprintf("[main] %s", wt.Path)
	}
	// worktree名はパスの最後のディレクトリ名
	wtName := filepath.Base(wt.Path)
	return fmt.Sprintf("%s (%s)", wtName, wt.Branch)
}

// getPromptFieldIndex はプロンプトフィールドのインデックスを返す
func (m *Model) getPromptFieldIndex() int {
	// worktree名はworktreeフィールドに統合されたので、フィールド構成が簡略化
	if m.newBranchMode {
		// 新規ブランチモード: name(0), repo(1), worktree(2), branch(3), base(4), prompt(5)
		return 5
	}
	// 既存ブランチモード: name(0), repo(1), worktree(2), branch(3), prompt(4)
	return 4
}

// filterPrompts は入力値でプロンプトをフィルタリング
func (m *Model) filterPrompts() {
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
	// 候補があればドロップダウンを開く
	m.promptDropdownOpen = len(m.filteredPrompts) > 0
	// インデックスをリセット
	m.promptSelectedIndex = 0
}

// selectCurrentPrompt は現在選択中のプロンプトを確定
func (m *Model) selectCurrentPrompt() {
	if len(m.filteredPrompts) > 0 && m.promptSelectedIndex < len(m.filteredPrompts) {
		selected := m.filteredPrompts[m.promptSelectedIndex]
		m.promptInput.SetValue(selected)
		m.promptDropdownOpen = false
	}
}

// loadAllBranches は選択中のリポジトリの全ブランチ一覧を取得（ローカル＋リモート）
func (m *Model) loadAllBranches() {
	repoName := m.repoInput.Value()
	if repoName == "" || m.configMgr == nil {
		m.allBranches = nil
		m.filteredBaseBranches = nil
		return
	}

	registry := repository.NewRegistry(m.configMgr)
	branches, err := registry.GetBranches(repoName)
	if err != nil {
		m.allBranches = nil
		m.filteredBaseBranches = nil
		return
	}

	m.allBranches = branches
	m.filteredBaseBranches = branches
	m.baseBranchSelectedIndex = 0
}

// handleCreateSubmit handles session creation
func (m Model) handleCreateSubmit() (tea.Model, tea.Cmd) {
	name := m.nameInput.Value()
	promptName := m.promptInput.Value()
	promptArgs := m.argsInput.Value()

	// v3 design: always require repository
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

	// プロンプトが入力されている場合、存在するかチェック
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

	if m.isNewWorktree {
		// 新規worktree作成モード
		baseBranch := m.baseBranchInput.Value()
		// worktree名はworktreeフィールドの入力値を使用（空の場合はブランチ名がデフォルト）
		worktreeName := m.worktreeInput.Value()

		// Use async session creation (worktree creation happens in background)
		_, err := m.client.NewWithOptions(daemon.NewOptions{
			Name:          name,
			Start:         true,
			Async:         true, // Returns immediately with creating status
			Repository:    repoName,
			Branch:        branch,
			BaseBranch:    baseBranch,
			NewBranch:     m.newBranchMode,
			IsNewWorktree: true,
			WorktreeName:  worktreeName,
			PromptName:    promptName,
			PromptArgs:    promptArgs,
		})
		if err != nil {
			m.err = err
		} else {
			// 成功時に使用したリポジトリを記憶
			if m.stateMgr != nil {
				m.stateMgr.SetLastUsedRepository(repoName)
				_ = m.stateMgr.Save()
			}
		}
	} else {
		// 既存worktree使用モード
		selectedWt := m.getSelectedWorktree()
		if selectedWt == nil {
			m.err = fmt.Errorf("worktree is required")
			return m, nil
		}

		baseBranch := ""
		if m.newBranchMode {
			baseBranch = m.baseBranchInput.Value()
		}

		_, err := m.client.NewWithOptions(daemon.NewOptions{
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
		})
		if err != nil {
			m.err = err
		} else {
			// 成功時に使用したリポジトリを記憶
			if m.stateMgr != nil {
				m.stateMgr.SetLastUsedRepository(repoName)
				_ = m.stateMgr.Save()
			}
		}
	}

	m.mode = ModeList
	return m, tea.Batch(m.fetchSessions, tickCmd())
}

// View renders the UI
func (m Model) View() string {
	// Handle create mode separately
	if m.mode == ModeCreate {
		return m.viewCreateMode()
	}

	// Calculate box width based on terminal size
	boxWidth := m.width - 2
	if boxWidth < 60 {
		boxWidth = 60
	}
	contentWidth := boxWidth - 4 // Account for border and padding

	var content strings.Builder

	// Header line: title + current time
	title := titleStyle.Render("ccvalet")
	currentTime := time.Now().Format("15:04:05")
	timeDisplay := fmt.Sprintf("[ %s ]", currentTime)

	// Calculate spacing for right-aligned time
	titleLen := lipgloss.Width(title)
	timeLen := len(timeDisplay)
	headerSpacing := contentWidth - titleLen - timeLen
	if headerSpacing < 2 {
		headerSpacing = 2
	}

	content.WriteString(title)
	content.WriteString(strings.Repeat(" ", headerSpacing))
	content.WriteString(timeStyle.Render(timeDisplay))
	content.WriteString("\n")

	// STATS line: status summary
	statusSummary := buildStatusSummary(m.sessions)
	if statusSummary != "" {
		content.WriteString("STATS: ")
		content.WriteString(statusSummary)
		content.WriteString("\n")
	}

	// Separator line
	content.WriteString(strings.Repeat("─", contentWidth))
	content.WriteString("\n")

	// Error message
	if m.err != nil {
		content.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(fmt.Sprintf("Error: %v", m.err)))
		content.WriteString("\n\n")
	}

	// Separate sessions into active and queued
	var activeSessions, queuedSessions []session.Info
	for _, sess := range m.sessions {
		if sess.Status == session.StatusQueued {
			queuedSessions = append(queuedSessions, sess)
		} else {
			activeSessions = append(activeSessions, sess)
		}
	}

	// Sessions list with pagination
	if len(m.sessions) == 0 {
		content.WriteString("\n")
		content.WriteString(helpStyle.Render("No sessions. Press 'n' to create one."))
		content.WriteString("\n")
	} else {
		// Get sessions for current page
		pageSessions := m.getPageSessions()

		// Render sessions (1-line format)
		for i, sess := range pageSessions {
			content.WriteString(m.renderSession(sess, i == m.cursor, contentWidth))
		}

		// Render queued sessions note if any
		if len(queuedSessions) > 0 {
			content.WriteString("\n")
			queueNote := fmt.Sprintf("(%d queued)", len(queuedSessions))
			content.WriteString(queueHeaderStyle.Render(queueNote))
			content.WriteString("\n")
		}
	}

	// Page info line
	totalPages := m.getTotalPages()
	if totalPages > 1 {
		content.WriteString("\n")
		pageInfo := fmt.Sprintf("Page %d/%d", m.currentPage+1, totalPages)
		pageInfoStyled := helpStyle.Render(pageInfo)
		// Center the page info
		pageInfoLen := lipgloss.Width(pageInfoStyled)
		leftPad := (contentWidth - pageInfoLen) / 2
		if leftPad > 0 {
			content.WriteString(strings.Repeat(" ", leftPad))
		}
		content.WriteString(pageInfoStyled)
	}

	// Calculate box height based on terminal size
	// Terminal height - border (2 lines) - help line (1 line)
	boxHeight := m.height - 3
	if boxHeight < 10 {
		boxHeight = 10
	}

	// Build the box
	boxStyle := createBoxStyle(boxWidth, boxHeight)
	box := boxStyle.Render(content.String())

	// Help line (outside box)
	var helpLine string
	if m.confirmDelete {
		// 削除確認メッセージ
		confirmMsg := fmt.Sprintf(" Delete session '%s'? [y/Enter] yes  [n/Esc] no", m.deleteTargetName)
		helpLine = lipgloss.NewStyle().Foreground(warningColor).Bold(true).Render(confirmMsg)
	} else if m.showHelp {
		helpLine = helpStyle.Render(" [↑/k] up [↓/j] down [←/h] prev [→/l] next [enter] attach [n] new [s] kill [d] delete [q] quit")
	} else {
		helpLine = helpStyle.Render(" [n] new [s] kill [d] del [enter] attach [←→] page [q] quit [?] help")
	}

	return box + "\n" + helpLine
}

// renderSession renders a single session in 1-line format with optional output preview
// Format: >name (branch)                    STATUS    Last Active
//         └─ output preview...
func (m Model) renderSession(sess session.Info, selected bool, width int) string {
	var b strings.Builder

	statusIcon, statusLabel, statusStyle := getStatusDisplay(sess.Status)

	// Use LastActiveAt if available, otherwise CreatedAt
	var lastActiveTime time.Time
	if !sess.LastActiveAt.IsZero() {
		lastActiveTime = sess.LastActiveAt
	} else {
		lastActiveTime = sess.CreatedAt
	}
	timeStr := timeAgo(lastActiveTime)

	// Build name with branch: "name (branch)"
	nameWithBranch := sess.Name
	if sess.Branch != "" {
		nameWithBranch += " (" + sess.Branch + ")"
	}

	// Fixed width columns from right:
	// - Last Active: 10 chars
	// - Status label: 12 chars
	// - Status icon: 2 chars
	// - Remaining: name with branch
	statusColWidth := 12
	timeColWidth := 10

	// Calculate available width for name
	cursorWidth := 2 // "> " or "  "
	availableForName := width - cursorWidth - 2 - statusColWidth - 1 - timeColWidth

	// Truncate name if needed
	if len(nameWithBranch) > availableForName {
		nameWithBranch = truncateString(nameWithBranch, availableForName)
	}

	// Pad name to fill available space
	namePadded := nameWithBranch + strings.Repeat(" ", availableForName-len(nameWithBranch))

	// Format status and time columns
	statusCol := fmt.Sprintf("%s %-10s", statusIcon, statusLabel)
	timeCol := fmt.Sprintf("%10s", timeStr)

	if selected {
		// Selected: highlight with background
		cursor := "› "
		line := cursor + namePadded + statusCol + " " + timeCol
		// Pad to full width
		if lipgloss.Width(line) < width {
			line += strings.Repeat(" ", width-lipgloss.Width(line))
		}
		b.WriteString(selectedItemStyle.Render(line))
	} else {
		// Not selected: use styled text
		cursor := "  "
		b.WriteString(cursor)
		b.WriteString(sessionNameStyle.Render(namePadded))
		b.WriteString(statusStyle.Render(statusCol))
		b.WriteString(" ")
		b.WriteString(timeStyle.Render(timeCol))
	}
	b.WriteString("\n")

	// Show error message on second line if error status
	if sess.Status == session.StatusError && sess.ErrorMessage != "" {
		errMsg := truncateString(sess.ErrorMessage, width-4)
		if selected {
			errLine := "  └─ " + errMsg
			errPadding := width - lipgloss.Width(errLine)
			if errPadding > 0 {
				errLine += strings.Repeat(" ", errPadding)
			}
			b.WriteString(selectedItemStyle.Render(errLine))
		} else {
			b.WriteString("  └─ " + lipgloss.NewStyle().Foreground(errorColor).Render(errMsg))
		}
		b.WriteString("\n")
	} else {
		// Show repository and work directory on second line
		// (Branch is already shown in the session name on the first line)
		var details []string
		if sess.Repository != "" {
			details = append(details, sess.Repository)
		}
		if sess.WorkDir != "" {
			// Shorten home directory to ~
			workDir := sess.WorkDir
			if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(workDir, home) {
				workDir = "~" + workDir[len(home):]
			}
			details = append(details, workDir)
		}
		if len(details) > 0 {
			detailStr := strings.Join(details, " │ ")
			// Use │ instead of └─ if we have last messages to show
			lineChar := "└─"
			if sess.LastUserMessage != "" || sess.LastAssistantMessage != "" {
				lineChar = "├─"
			}
			detailStr = truncateString(detailStr, width-6)
			if selected {
				detailLine := "  " + lineChar + " " + detailStr
				detailPadding := width - lipgloss.Width(detailLine)
				if detailPadding > 0 {
					detailLine += strings.Repeat(" ", detailPadding)
				}
				b.WriteString(selectedItemStyle.Render(detailLine))
			} else {
				b.WriteString("  " + lineChar + " " + helpStyle.Render(detailStr))
			}
			b.WriteString("\n")
		}

		// Show last user message (line 3)
		if sess.LastUserMessage != "" {
			// Determine line prefix: ├─ if assistant message follows, └─ if last line
			linePrefix := "└─"
			if sess.LastAssistantMessage != "" {
				linePrefix = "├─"
			}

			// Calculate prefix width using lipgloss for accurate Unicode width
			prefix := "  " + linePrefix + " 👤 "
			prefixWidth := lipgloss.Width(prefix)
			msgWidth := width - prefixWidth
			if msgWidth < 10 {
				msgWidth = 10
			}
			msgStr := truncateString(sess.LastUserMessage, msgWidth)

			if selected {
				msgLine := prefix + msgStr
				msgPadding := width - lipgloss.Width(msgLine)
				if msgPadding > 0 {
					msgLine += strings.Repeat(" ", msgPadding)
				}
				b.WriteString(selectedItemStyle.Render(msgLine))
			} else {
				b.WriteString("  " + linePrefix + " " + helpStyle.Render("👤 "+msgStr))
			}
			b.WriteString("\n")
		}

		// Show last assistant message (line 4)
		// Truncate from end because important content (like questions) is often at the end
		if sess.LastAssistantMessage != "" {
			// Calculate prefix width using lipgloss for accurate Unicode width
			prefix := "  └─ 🤖 "
			prefixWidth := lipgloss.Width(prefix)
			msgWidth := width - prefixWidth
			if msgWidth < 10 {
				msgWidth = 10
			}
			msgStr := truncateStringFromEnd(sess.LastAssistantMessage, msgWidth)

			if selected {
				msgLine := prefix + msgStr
				msgPadding := width - lipgloss.Width(msgLine)
				if msgPadding > 0 {
					msgLine += strings.Repeat(" ", msgPadding)
				}
				b.WriteString(selectedItemStyle.Render(msgLine))
			} else {
				b.WriteString("  └─ " + helpStyle.Render("🤖 "+msgStr))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// truncateString truncates a string to fit within maxWidth display width from the beginning
func truncateString(s string, maxWidth int) string {
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return truncateToWidth(s, maxWidth)
	}
	return truncateToWidth(s, maxWidth-3) + "..."
}

// truncateStringFromEnd truncates a string, keeping the last maxWidth display width
func truncateStringFromEnd(s string, maxWidth int) string {
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return truncateFromEndToWidth(s, maxWidth)
	}
	return "..." + truncateFromEndToWidth(s, maxWidth-3)
}

// truncateToWidth truncates a string from the beginning to fit within maxWidth
func truncateToWidth(s string, maxWidth int) string {
	var result []rune
	width := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if width+w > maxWidth {
			break
		}
		result = append(result, r)
		width += w
	}
	return string(result)
}

// truncateFromEndToWidth truncates a string from the end, keeping the last maxWidth
func truncateFromEndToWidth(s string, maxWidth int) string {
	runes := []rune(s)
	width := 0
	startIdx := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := runewidth.RuneWidth(runes[i])
		if width+w > maxWidth {
			break
		}
		startIdx = i
		width += w
	}
	return string(runes[startIdx:])
}

// timeAgo returns a human-readable relative time string
func timeAgo(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	default:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// countStatuses counts sessions by status category for summary
type statusCounts struct {
	thinking   int
	permission int
	confirm    int
	running    int
	creating   int
	idle       int
	queued     int
	stopped    int
	errorCount int
}

func countStatuses(sessions []session.Info) statusCounts {
	var counts statusCounts
	for _, s := range sessions {
		switch s.Status {
		case session.StatusThinking:
			counts.thinking++
		case session.StatusPermission:
			counts.permission++
		case session.StatusConfirm:
			counts.confirm++
		case session.StatusRunning:
			counts.running++
		case session.StatusCreating:
			counts.creating++
		case session.StatusIdle:
			counts.idle++
		case session.StatusQueued:
			counts.queued++
		case session.StatusStopped:
			counts.stopped++
		case session.StatusError:
			counts.errorCount++
		}
	}
	return counts
}

// buildStatusSummary builds the status summary string for header
// Format: STATS: ⚡ 1 Thinking ? 1 Permission ▶ 2 Running ○ 2 Idle
func buildStatusSummary(sessions []session.Info) string {
	counts := countStatuses(sessions)

	var parts []string
	if counts.thinking > 0 {
		parts = append(parts, thinkingStyle.Render(fmt.Sprintf("⚡%d Thinking", counts.thinking)))
	}
	if counts.permission > 0 {
		parts = append(parts, permissionStyle.Render(fmt.Sprintf("?%d Permission", counts.permission)))
	}
	if counts.confirm > 0 {
		parts = append(parts, confirmStyle.Render(fmt.Sprintf("⚠%d Confirm", counts.confirm)))
	}
	if counts.running > 0 {
		parts = append(parts, runningStyle.Render(fmt.Sprintf("▶%d Running", counts.running)))
	}
	if counts.creating > 0 {
		parts = append(parts, creatingStyle.Render(fmt.Sprintf("+%d Creating", counts.creating)))
	}
	if counts.idle > 0 {
		parts = append(parts, idleStyle.Render(fmt.Sprintf("○%d Idle", counts.idle)))
	}
	if counts.queued > 0 {
		parts = append(parts, queuedStyle.Render(fmt.Sprintf("…%d Queued", counts.queued)))
	}
	if counts.errorCount > 0 {
		parts = append(parts, errorStatusStyle.Render(fmt.Sprintf("✗%d Error", counts.errorCount)))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// getStatusDisplay returns icon, label, and style for a given status
func getStatusDisplay(status session.Status) (icon, label string, style lipgloss.Style) {
	switch status {
	case session.StatusThinking:
		return "⚡", "THINKING", thinkingStyle
	case session.StatusPermission:
		return "?", "PERMISSION", permissionStyle
	case session.StatusRunning:
		return "▶", "RUNNING", runningStyle
	case session.StatusCreating:
		return "+", "CREATING", creatingStyle
	case session.StatusQueued:
		return "…", "QUEUED", queuedStyle
	case session.StatusIdle:
		return "○", "IDLE", idleStyle
	case session.StatusStopped:
		return "■", "STOPPED", stoppedStyle
	case session.StatusConfirm:
		return "⚠", "CONFIRM", confirmStyle
	case session.StatusError:
		return "✗", "ERROR", errorStatusStyle
	default:
		return "?", "UNKNOWN", stoppedStyle
	}
}

// viewCreateMode renders the create session form
func (m Model) viewCreateMode() string {
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

	// Session name field (always shown)
	if m.focusIndex == 0 {
		b.WriteString(focusedLabelStyle.Render("Session Name:"))
	} else {
		b.WriteString(labelStyle.Render("Session Name:"))
	}
	b.WriteString(m.nameInput.View())
	b.WriteString("\n\n")

	// Repository field
	if m.focusIndex == 1 {
		b.WriteString(focusedLabelStyle.Render("Repository:"))
	} else {
		b.WriteString(labelStyle.Render("Repository:"))
	}
	b.WriteString(m.repoInput.View())
	b.WriteString("\n")

	// Show repository dropdown or help
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
		b.WriteString(helpStyle.Render("  ↑↓: select  Enter/Tab: confirm"))
	} else if len(m.repositories) == 0 {
		b.WriteString(helpStyle.Render("  No repositories registered. Use 'ccvalet repo add' to add one."))
	} else if m.focusIndex == 1 {
		b.WriteString(helpStyle.Render("  Type to search repositories..."))
	}
	b.WriteString("\n\n")

	// Worktree field
	if m.focusIndex == 2 {
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

	// Show worktree dropdown or help
	if m.isNewWorktree {
		// 新規worktreeモード: テキスト入力のみ
		b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Render("  [New Worktree Mode]"))
		b.WriteString("\n")
		if m.focusIndex == 2 {
			b.WriteString(helpStyle.Render("  Worktree directory name (optional, defaults to branch)"))
		}
	} else if m.worktreeDropdownOpen && len(m.filteredWorktrees) > 0 {
		// 既存worktree一覧
		for i, wt := range m.filteredWorktrees {
			prefix := "  "
			label := formatWorktreeDisplay(&wt)
			if i == m.worktreeSelectedIndex {
				prefix = "> "
				b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + label))
			} else {
				b.WriteString(helpStyle.Render(prefix + label))
			}
			b.WriteString("\n")
		}
		b.WriteString(helpStyle.Render("  ↑↓: select  Enter/Tab: confirm  Ctrl+W: new worktree"))
	} else if m.focusIndex == 2 && m.repoInput.Value() == "" {
		b.WriteString(helpStyle.Render("  Select repository first"))
	} else if m.focusIndex == 2 {
		b.WriteString(helpStyle.Render("  Type to search or select worktree..."))
	}
	b.WriteString("\n\n")

	// Branch field
	branchLabel := "Branch:"
	if !m.isNewWorktree {
		branchLabel = "Checkout:"
	}
	if m.focusIndex == 3 {
		b.WriteString(focusedLabelStyle.Render(branchLabel))
	} else {
		b.WriteString(labelStyle.Render(branchLabel))
	}
	b.WriteString(m.branchInput.View())
	b.WriteString("\n")

	// Show branch dropdown or help
	if m.branchDropdownOpen && len(m.filteredBranches) > 0 {
		displayCount := len(m.filteredBranches)
		if displayCount > 10 {
			displayCount = 10
		}
		for i := 0; i < displayCount; i++ {
			branch := m.filteredBranches[i]
			prefix := "  "
			if i == m.branchSelectedIndex {
				prefix = "> "
				b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render(prefix + branch))
			} else {
				b.WriteString(helpStyle.Render(prefix + branch))
			}
			b.WriteString("\n")
		}
		if len(m.filteredBranches) > 10 {
			b.WriteString(helpStyle.Render(fmt.Sprintf("  ... and %d more", len(m.filteredBranches)-10)))
			b.WriteString("\n")
		}
		b.WriteString(helpStyle.Render("  ↑↓: select  Enter/Tab: confirm"))
		b.WriteString("\n")
	} else if m.focusIndex == 3 {
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

	// New branch mode indicator (for both existing and new worktree)
	if m.newBranchMode {
		b.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Render("  [New Branch Mode]"))
		b.WriteString("\n\n")

		// Base branch field
		baseBranchFieldIdx := m.getBaseBranchFieldIndex()
		if m.focusIndex == baseBranchFieldIdx {
			b.WriteString(focusedLabelStyle.Render("Base Branch:"))
		} else {
			b.WriteString(labelStyle.Render("Base Branch:"))
		}
		b.WriteString(m.baseBranchInput.View())
		b.WriteString("\n")

		// Show base branch dropdown or help
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
			b.WriteString(helpStyle.Render("  ↑↓: select  Enter/Tab: confirm"))
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

	// Show prompt dropdown or help
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
		b.WriteString(helpStyle.Render("  ↑↓: select  Enter/Tab: confirm"))
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

	// Help text
	b.WriteString(helpStyle.Render("Tab: next  Ctrl+W: toggle new worktree  Ctrl+B: toggle new branch  Enter: create  Esc: cancel"))

	return b.String()
}
