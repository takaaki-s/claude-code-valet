package tui

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/vt"
	"github.com/mattn/go-runewidth"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/prompt"
	"github.com/takaaki-s/claude-code-valet/internal/repository"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

// Mode represents the current TUI mode
type Mode int

const (
	ModeList Mode = iota
	ModeCreate
)

// FocusPane represents which pane has focus in 2-pane layout
type FocusPane int

const (
	FocusLeft  FocusPane = iota // Session list pane
	FocusRight                  // Preview/action pane
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

	// Preview pane (2-pane layout)
	previewLines      []string // ANSIストリップ済みのプレビュー行（creating用）
	previewSessionID  string   // プレビュー中のセッションID
	previewConn       net.Conn // daemon への subscribe 接続
	previewBuffered   io.Reader // JSON decoder のバッファデータ
	previewStopCh     chan struct{} // プレビュー停止シグナル

	// VT emulator (2-pane terminal)
	vtEmulator  *vt.SafeEmulator // VT 端末エミュレータ（スレッドセーフ）
	vtWidth     int              // VT エミュレータの幅
	vtHeight    int              // VT エミュレータの高さ

	// Focus management (2-pane layout)
	focusPane    FocusPane       // 現在フォーカスされているペイン
	promptField  textinput.Model // 右ペインのプロンプト入力フィールド
	sendSuccess  bool            // プロンプト送信成功フラグ（一時表示用）
	sendSuccessAt time.Time      // 送信成功時刻

	// tmux integration
	tmuxClient           *tmux.Client // tmux client (nil in legacy mode)
	tuiPaneID            string       // TUIペインの固有ID (例: "%42")
	currentSessionWindow string       // TUIが現在いるセッションwindow名 ("" = UIウィンドウ)
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

	// Prompt input field for right pane
	promptField := textinput.New()
	promptField.Placeholder = "Send a message to CC..."
	promptField.CharLimit = 2000

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
		focusPane:         FocusLeft,
		promptField:       promptField,
	}
}

// NewModelWithTmux creates a new TUI model with tmux integration.
// In tmux mode, the TUI pane moves between session windows as a floating sidebar.
func NewModelWithTmux(client *daemon.Client, tc *tmux.Client, tuiPaneID string) Model {
	m := NewModel(client)
	m.tmuxClient = tc
	m.tuiPaneID = tuiPaneID
	// Restore which session window TUI was in (for reattach)
	m.currentSessionWindow = tc.GetEnvironment(tmux.SessionName, "CCVALET_CURRENT_WINDOW")
	return m
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
type previewDataMsg struct {
	lines []string
}
type vtOutputMsg struct {
	data []byte
}

// ansiStripRegex strips ANSI escape sequences from text
var ansiStripRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b\[.*?[hl]|\x1b[()][AB012]|\x1b\[\?[0-9;]*[a-zA-Z]`)

// stripANSI removes ANSI escape sequences from a string
func stripANSI(s string) string {
	return ansiStripRegex.ReplaceAllString(s, "")
}

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

// startPreview starts subscribing to a session's output (legacy ANSI-stripped mode for creating)
func (m *Model) startPreview(sessionID string) tea.Cmd {
	// Stop any existing preview
	m.stopPreview()

	m.previewSessionID = sessionID
	m.previewLines = nil

	conn, buffered, err := m.client.Subscribe(sessionID)
	if err != nil {
		return nil
	}

	m.previewConn = conn
	m.previewBuffered = buffered
	m.previewStopCh = make(chan struct{})

	stopCh := m.previewStopCh

	return func() tea.Msg {
		buf := make([]byte, 4096)
		var accumulated strings.Builder

		if buffered != nil {
			n, _ := buffered.Read(buf)
			if n > 0 {
				accumulated.Write(buf[:n])
			}
		}

		for {
			select {
			case <-stopCh:
				return nil
			default:
			}

			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if n > 0 {
				accumulated.Write(buf[:n])
				raw := accumulated.String()
				stripped := stripANSI(raw)
				allLines := strings.Split(stripped, "\n")

				maxLines := 100
				if len(allLines) > maxLines {
					allLines = allLines[len(allLines)-maxLines:]
				}

				return previewDataMsg{lines: allLines}
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return nil
			}
		}
	}
}

// continuePreview continues reading from the preview connection (legacy mode)
func (m *Model) continuePreview() tea.Cmd {
	if m.previewConn == nil || m.previewStopCh == nil {
		return nil
	}

	conn := m.previewConn
	stopCh := m.previewStopCh
	existingLines := m.previewLines

	return func() tea.Msg {
		buf := make([]byte, 4096)
		var accumulated strings.Builder

		for {
			select {
			case <-stopCh:
				return nil
			default:
			}

			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if n > 0 {
				accumulated.Write(buf[:n])
				raw := accumulated.String()
				stripped := stripANSI(raw)
				newLines := strings.Split(stripped, "\n")

				allLines := append(existingLines, newLines...)
				maxLines := 100
				if len(allLines) > maxLines {
					allLines = allLines[len(allLines)-maxLines:]
				}

				return previewDataMsg{lines: allLines}
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return nil
			}
		}
	}
}

// startVT starts VT emulator mode for an active session
func (m *Model) startVT(sessionID string, width, height int) tea.Cmd {
	m.stopPreview()

	m.previewSessionID = sessionID

	conn, buffered, err := m.client.Subscribe(sessionID)
	if err != nil {
		return nil
	}

	m.previewConn = conn
	m.previewBuffered = buffered
	m.previewStopCh = make(chan struct{})

	// Create VT emulator with right pane dimensions
	m.vtWidth = width
	m.vtHeight = height
	m.vtEmulator = vt.NewSafeEmulator(width, height)

	// Resize the PTY to match the right pane dimensions
	_ = m.client.Resize(sessionID, width, height)

	stopCh := m.previewStopCh

	return func() tea.Msg {
		buf := make([]byte, 4096)

		// Read buffered data first
		if buffered != nil {
			n, _ := buffered.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				return vtOutputMsg{data: data}
			}
		}

		// Read from connection
		for {
			select {
			case <-stopCh:
				return nil
			default:
			}

			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				return vtOutputMsg{data: data}
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return nil
			}
		}
	}
}

// continueVT continues reading VT output
func (m *Model) continueVT() tea.Cmd {
	if m.previewConn == nil || m.previewStopCh == nil {
		return nil
	}

	conn := m.previewConn
	stopCh := m.previewStopCh

	return func() tea.Msg {
		buf := make([]byte, 4096)

		for {
			select {
			case <-stopCh:
				return nil
			default:
			}

			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				return vtOutputMsg{data: data}
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return nil
			}
		}
	}
}

// stopPreview stops the current preview subscription and VT emulator
func (m *Model) stopPreview() {
	if m.previewStopCh != nil {
		close(m.previewStopCh)
		m.previewStopCh = nil
	}
	if m.previewConn != nil {
		m.previewConn.Close()
		m.previewConn = nil
	}
	if m.vtEmulator != nil {
		m.vtEmulator.Close()
		m.vtEmulator = nil
	}
	m.previewSessionID = ""
	m.previewLines = nil
	m.previewBuffered = nil
	m.vtWidth = 0
	m.vtHeight = 0
}

// switchToSession moves the TUI sidebar to the given session's tmux window.
// Each session has its own tmux window; the TUI pane floats between them via join-pane.
func (m *Model) switchToSession(sessionID string) {
	if m.tmuxClient == nil || m.tuiPaneID == "" || sessionID == "" {
		return
	}

	// Find session info
	var sess *session.Info
	for i := range m.sessions {
		if m.sessions[i].ID == sessionID {
			sess = &m.sessions[i]
			break
		}
	}
	if sess == nil || sess.TmuxWindowName == "" {
		return
	}

	// Already in this session's window
	if m.currentSessionWindow == sess.TmuxWindowName {
		return
	}

	// join-pane: move TUI to session window as left sidebar (25%)
	target := tmux.WindowTarget(sess.TmuxWindowName, 0)
	if err := m.tmuxClient.JoinPane(m.tuiPaneID, target, true, 25, true, true); err != nil {
		// Join failed — window might not exist
		return
	}

	m.currentSessionWindow = sess.TmuxWindowName
	m.tmuxClient.SetEnvironment(tmux.SessionName, "CCVALET_CURRENT_WINDOW", sess.TmuxWindowName)
}

// isActiveSession returns true if the session is in an active state where VT display is useful
func isActiveSession(status session.Status) bool {
	switch status {
	case session.StatusCreating, session.StatusRunning, session.StatusThinking,
		session.StatusIdle, session.StatusPermission, session.StatusConfirm:
		return true
	}
	return false
}

// updatePreviewForCurrentSession starts/updates preview for the currently selected session
func (m *Model) updatePreviewForCurrentSession() tea.Cmd {
	// tmux sidebar mode: no preview switching on cursor move
	// The TUI pane only moves to a session window on Enter
	if m.tmuxClient != nil {
		return nil
	}

	// Only in 2-pane mode
	if m.width < minTwoPaneWidth {
		return nil
	}

	pageSessions := m.getPageSessions()
	if len(pageSessions) == 0 || m.cursor >= len(pageSessions) {
		m.stopPreview()
		return nil
	}

	sess := pageSessions[m.cursor]

	// Already previewing this session - check if mode needs to change
	if sess.ID == m.previewSessionID {
		// If session became active and we don't have VT emulator, start it
		if isActiveSession(sess.Status) && m.vtEmulator == nil {
			vtW, vtH := m.calcVTDimensions()
			if vtW > 0 && vtH > 0 {
				return m.startVT(sess.ID, vtW, vtH)
			}
		}
		// If session became inactive and we have VT emulator, stop it
		if !isActiveSession(sess.Status) && m.vtEmulator != nil {
			m.stopPreview()
			m.previewSessionID = sess.ID
		}
		return nil
	}

	// Active sessions: use VT emulator for live terminal display
	if isActiveSession(sess.Status) {
		vtW, vtH := m.calcVTDimensions()
		if vtW > 0 && vtH > 0 {
			return m.startVT(sess.ID, vtW, vtH)
		}
	}

	// For stopped/queued/error: stop any existing stream and just set the session ID
	m.stopPreview()
	m.previewSessionID = sess.ID
	return nil
}

// calcVTDimensions calculates the VT emulator dimensions based on the right pane size
func (m *Model) calcVTDimensions() (width, height int) {
	totalAvailable := m.width
	gap := 1
	borderOverhead := 4 // 2 per pane

	usableWidth := totalAvailable - gap - borderOverhead
	if usableWidth < 60 {
		usableWidth = 60
	}

	leftContentWidth := usableWidth * 25 / 100
	rightContentWidth := usableWidth - leftContentWidth

	if leftContentWidth < 28 {
		leftContentWidth = 28
	}
	if rightContentWidth < 30 {
		rightContentWidth = 30
	}

	totalNeeded := leftContentWidth + rightContentWidth + borderOverhead + gap
	if totalNeeded > m.width {
		excess := totalNeeded - m.width
		rightContentWidth -= excess
		if rightContentWidth < 20 {
			rightContentWidth = 20
		}
	}

	boxHeight := m.height - 3
	if boxHeight < 10 {
		boxHeight = 10
	}

	// VT dimensions are the content area of the right pane (inside border)
	return rightContentWidth, boxHeight
}

// minTwoPaneWidth is the minimum terminal width for 2-pane layout
const minTwoPaneWidth = 100

// minPaneWidth is the minimum width for each pane
const minPaneWidth = 40

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
		// Resize VT emulator if active
		if m.vtEmulator != nil && m.previewSessionID != "" {
			newW, newH := m.calcVTDimensions()
			if newW > 0 && newH > 0 && (newW != m.vtWidth || newH != m.vtHeight) {
				m.vtWidth = newW
				m.vtHeight = newH
				m.vtEmulator.Resize(newW, newH)
				_ = m.client.Resize(m.previewSessionID, newW, newH)
			}
		}
	}

	// Mode-specific handling
	switch m.mode {
	case ModeCreate:
		return m.updateCreateMode(msg)
	default:
		return m.updateListMode(msg)
	}
}

// sendSuccessMsg is sent after a delay to clear the success message
type sendSuccessMsg struct{}

func (m Model) updateListMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle sendSuccessMsg to clear the success indicator
	if _, ok := msg.(sendSuccessMsg); ok {
		m.sendSuccess = false
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// 削除確認モード中の処理
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y", "enter":
				// If TUI is in the session's window being deleted, evacuate to UI window first
				if m.tmuxClient != nil && m.tuiPaneID != "" {
					for _, s := range m.sessions {
						if s.ID == m.deleteTargetID && s.TmuxWindowName != "" && m.currentSessionWindow == s.TmuxWindowName {
							m.tmuxClient.BreakPane(m.tuiPaneID, true, tmux.UIWindowName)
							m.currentSessionWindow = ""
							m.tmuxClient.UnsetEnvironment(tmux.SessionName, "CCVALET_CURRENT_WINDOW")
							break
						}
					}
				}
				if err := m.client.Delete(m.deleteTargetID); err != nil {
					m.err = err
				}
				m.confirmDelete = false
				m.deleteTargetID = ""
				m.deleteTargetName = ""
				newPageSessions := m.getPageSessions()
				if m.cursor >= len(newPageSessions) && m.cursor > 0 {
					m.cursor--
				}
				return m, m.fetchSessions
			case "n", "N", "esc":
				m.confirmDelete = false
				m.deleteTargetID = ""
				m.deleteTargetName = ""
				return m, nil
			}
			return m, nil
		}

		// Tab key: in tmux sidebar mode, move focus to the right workspace pane
		if msg.String() == "tab" && m.tmuxClient != nil && m.currentSessionWindow != "" {
			// Select the pane to the right of TUI (the workspace)
			m.tmuxClient.SelectPaneRight()
			return m, nil
		}

		// Tab key: switch focus from left to right pane (only in 2-pane mode)
		// Right→Left is handled by Ctrl+] in updateRightPane (for VT mode compatibility)
		if msg.String() == "tab" && m.width >= minTwoPaneWidth && m.focusPane == FocusLeft {
			m.focusPane = FocusRight
			// Only focus prompt field if not in VT mode
			if m.vtEmulator == nil {
				m.promptField.Focus()
				return m, textinput.Blink
			}
			return m, nil
		}

		// Route to pane-specific handler
		if m.focusPane == FocusRight && m.width >= minTwoPaneWidth {
			return m.updateRightPane(msg)
		}

		// Left pane key handling (original logic)
		switch {
		case key.Matches(msg, m.keys.Quit):
			m.stopPreview()
			return m, tea.Quit

		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
				cmd := m.updatePreviewForCurrentSession()
				return m, cmd
			}

		case key.Matches(msg, m.keys.Down):
			pageSessions := m.getPageSessions()
			if m.cursor < len(pageSessions)-1 {
				m.cursor++
				cmd := m.updatePreviewForCurrentSession()
				return m, cmd
			}

		case key.Matches(msg, m.keys.Enter):
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				if sess.Status == session.StatusQueued {
					m.err = fmt.Errorf("cannot attach to queued session")
					return m, nil
				}
				if sess.Status == session.StatusCreating {
					m.err = fmt.Errorf("cannot attach to creating session")
					return m, nil
				}

				// tmux sidebar mode: start session and move TUI to session window
				if m.tmuxClient != nil {
					needsStart := sess.Status == session.StatusStopped || sess.Status == session.StatusError
					if needsStart {
						if err := m.client.Start(sess.ID); err != nil {
							m.err = err
							return m, nil
						}
						// Update local session data immediately (daemon has already created the tmux window)
						for i := range m.sessions {
							if m.sessions[i].ID == sess.ID {
								if m.sessions[i].TmuxWindowName == "" {
									m.sessions[i].TmuxWindowName = tmux.WindowName(sess.ID)
								}
								m.sessions[i].Status = session.StatusRunning
								break
							}
						}
						// Force switchToSession to run (clear current window tracking)
						m.currentSessionWindow = ""
					}
					m.switchToSession(sess.ID)
					return m, m.fetchSessions
				}

				// Legacy mode: signal attach
				m.stopPreview()
				m.attachSignal <- sess.ID
				return m, tea.Quit
			}

		case key.Matches(msg, m.keys.New):
			// Switch to create mode
			m.stopPreview()
			m.mode = ModeCreate
			// tmuxモード: TUIペインをzoomしてフルスクリーン化
			if m.tmuxClient != nil && m.tuiPaneID != "" {
				m.tmuxClient.ZoomPane(m.tuiPaneID)
			}
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
				m.cursor = 0
				cmd := m.updatePreviewForCurrentSession()
				return m, cmd
			}

		case key.Matches(msg, m.keys.NextPage):
			totalPages := m.getTotalPages()
			if m.currentPage < totalPages-1 {
				m.currentPage++
				m.cursor = 0
				cmd := m.updatePreviewForCurrentSession()
				return m, cmd
			}
		}

	case sessionsMsg:
		m.sessions = msg
		m.err = nil
		if m.cursor >= len(m.sessions) && m.cursor > 0 {
			m.cursor = len(m.sessions) - 1
		}
		// Start or update preview for current selection
		cmd := m.updatePreviewForCurrentSession()
		return m, cmd

	case previewDataMsg:
		m.previewLines = msg.lines
		return m, m.continuePreview()

	case vtOutputMsg:
		if m.vtEmulator != nil {
			m.vtEmulator.Write(msg.data)
		}
		return m, m.continueVT()

	case errMsg:
		m.err = msg

	case tickMsg:
		return m, tea.Batch(m.fetchSessions, tickCmd())
	}

	return m, nil
}

// updateRightPane handles key events when the right pane has focus
func (m Model) updateRightPane(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+] or Esc: always return focus to left pane regardless of state
	if msg.Type == tea.KeyCtrlCloseBracket || msg.Type == tea.KeyEscape {
		m.focusPane = FocusLeft
		m.promptField.Blur()
		return m, nil
	}

	// Get current session
	pageSessions := m.getPageSessions()
	if len(pageSessions) == 0 || m.cursor >= len(pageSessions) {
		return m, nil
	}
	sess := pageSessions[m.cursor]

	// VT mode: active sessions with VT emulator running
	if m.vtEmulator != nil && isActiveSession(sess.Status) {
		// Forward all other key events to PTY
		rawBytes := keyMsgToBytes(msg)
		if len(rawBytes) > 0 {
			if err := m.client.Write(sess.ID, string(rawBytes)); err != nil {
				m.err = err
			}
		}
		return m, nil
	}

	// Non-VT mode: info panel with action hints (stopped/queued/error)

	// q (when input is empty): quit
	if msg.String() == "q" && m.promptField.Value() == "" {
		m.stopPreview()
		return m, tea.Quit
	}

	switch sess.Status {
	case session.StatusStopped:
		if msg.String() == "enter" || msg.String() == "r" {
			if err := m.client.Start(sess.ID); err != nil {
				m.err = err
			}
			return m, m.fetchSessions
		}
		if msg.String() == "a" {
			m.stopPreview()
			m.attachSignal <- sess.ID
			return m, tea.Quit
		}

	case session.StatusError:
		if msg.String() == "r" {
			if err := m.client.Start(sess.ID); err != nil {
				m.err = err
			}
			return m, m.fetchSessions
		}
	}

	return m, nil
}

// keyMsgToBytes converts a Bubble Tea KeyMsg to raw terminal bytes for PTY forwarding
func keyMsgToBytes(msg tea.KeyMsg) []byte {
	// Alt modifier: prepend ESC
	prefix := ""
	if msg.Alt {
		prefix = "\x1b"
	}

	switch msg.Type {
	case tea.KeyRunes:
		return []byte(prefix + string(msg.Runes))
	case tea.KeySpace:
		return []byte(prefix + " ")
	case tea.KeyEnter:
		return []byte(prefix + "\r")
	case tea.KeyTab:
		return []byte(prefix + "\t")
	case tea.KeyBackspace:
		return []byte(prefix + "\x7f")
	case tea.KeyEscape:
		return []byte("\x1b")
	case tea.KeyUp:
		return []byte(prefix + "\x1b[A")
	case tea.KeyDown:
		return []byte(prefix + "\x1b[B")
	case tea.KeyRight:
		return []byte(prefix + "\x1b[C")
	case tea.KeyLeft:
		return []byte(prefix + "\x1b[D")
	case tea.KeyHome:
		return []byte(prefix + "\x1b[H")
	case tea.KeyEnd:
		return []byte(prefix + "\x1b[F")
	case tea.KeyPgUp:
		return []byte(prefix + "\x1b[5~")
	case tea.KeyPgDown:
		return []byte(prefix + "\x1b[6~")
	case tea.KeyDelete:
		return []byte(prefix + "\x1b[3~")
	case tea.KeyInsert:
		return []byte(prefix + "\x1b[2~")
	case tea.KeyShiftTab:
		return []byte(prefix + "\x1b[Z")
	case tea.KeyF1:
		return []byte(prefix + "\x1bOP")
	case tea.KeyF2:
		return []byte(prefix + "\x1bOQ")
	case tea.KeyF3:
		return []byte(prefix + "\x1bOR")
	case tea.KeyF4:
		return []byte(prefix + "\x1bOS")
	case tea.KeyF5:
		return []byte(prefix + "\x1b[15~")
	case tea.KeyF6:
		return []byte(prefix + "\x1b[17~")
	case tea.KeyF7:
		return []byte(prefix + "\x1b[18~")
	case tea.KeyF8:
		return []byte(prefix + "\x1b[19~")
	case tea.KeyF9:
		return []byte(prefix + "\x1b[20~")
	case tea.KeyF10:
		return []byte(prefix + "\x1b[21~")
	case tea.KeyF11:
		return []byte(prefix + "\x1b[23~")
	case tea.KeyF12:
		return []byte(prefix + "\x1b[24~")

	// Ctrl+key combinations (they map to control characters)
	case tea.KeyCtrlA:
		return []byte{0x01}
	case tea.KeyCtrlB:
		return []byte{0x02}
	case tea.KeyCtrlC:
		return []byte{0x03}
	case tea.KeyCtrlD:
		return []byte{0x04}
	case tea.KeyCtrlE:
		return []byte{0x05}
	case tea.KeyCtrlF:
		return []byte{0x06}
	case tea.KeyCtrlG:
		return []byte{0x07}
	case tea.KeyCtrlH:
		return []byte{0x08}
	// KeyCtrlI = Tab, KeyCtrlJ = LF, KeyCtrlM = Enter (handled above)
	case tea.KeyCtrlK:
		return []byte{0x0b}
	case tea.KeyCtrlL:
		return []byte{0x0c}
	case tea.KeyCtrlN:
		return []byte{0x0e}
	case tea.KeyCtrlO:
		return []byte{0x0f}
	case tea.KeyCtrlP:
		return []byte{0x10}
	case tea.KeyCtrlQ:
		return []byte{0x11}
	case tea.KeyCtrlR:
		return []byte{0x12}
	case tea.KeyCtrlS:
		return []byte{0x13}
	case tea.KeyCtrlT:
		return []byte{0x14}
	case tea.KeyCtrlU:
		return []byte{0x15}
	case tea.KeyCtrlV:
		return []byte{0x16}
	case tea.KeyCtrlW:
		return []byte{0x17}
	case tea.KeyCtrlX:
		return []byte{0x18}
	case tea.KeyCtrlY:
		return []byte{0x19}
	case tea.KeyCtrlZ:
		return []byte{0x1a}
	case tea.KeyCtrlBackslash:
		return []byte{0x1c}
	// KeyCtrlCloseBracket (0x1d) is handled as detach key above
	case tea.KeyCtrlCaret:
		return []byte{0x1e}
	case tea.KeyCtrlUnderscore:
		return []byte{0x1f}
	}

	return nil
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
			// tmuxモード: zoom解除
			if m.tmuxClient != nil && m.tuiPaneID != "" {
				m.tmuxClient.ZoomPane(m.tuiPaneID)
			}
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
				// 新規worktreeの場合は新規ブランチモードをデフォルトにする
				m.newBranchMode = true
				m.baseBranchInput.Reset()
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

	// エラー発生時は ModeCreate を維持してエラーを表示
	if m.err != nil {
		return m, nil
	}

	m.mode = ModeList
	// tmuxモード: zoom解除
	if m.tmuxClient != nil && m.tuiPaneID != "" {
		m.tmuxClient.ZoomPane(m.tuiPaneID)
	}
	return m, tea.Batch(m.fetchSessions, tickCmd())
}

// View renders the UI
func (m Model) View() string {
	// Determine if we should use 2-pane layout
	// In tmux mode, always use single pane (tmux manages the right pane)
	twoPaneMode := m.width >= minTwoPaneWidth && m.tmuxClient == nil

	var bg string
	if twoPaneMode {
		bg = m.viewTwoPane()
	} else {
		bg = m.viewSinglePane()
	}

	// Create mode
	if m.mode == ModeCreate {
		if m.tmuxClient != nil {
			return m.viewCreateMode() // tmux: ズームしてフルスクリーン表示
		}
		return m.viewCreatePopup(bg) // 非tmux: ポップアップオーバーレイ
	}

	return bg
}

// viewSinglePane renders the original 1-pane layout
func (m Model) viewSinglePane() string {
	// Calculate box width based on terminal size
	boxWidth := m.width - 2
	if boxWidth < 60 {
		boxWidth = 60
	}

	listContent := m.renderListContent(boxWidth - 4)

	// Calculate box height based on terminal size
	boxHeight := m.height - 3
	if boxHeight < 10 {
		boxHeight = 10
	}

	boxStyle := createBoxStyle(boxWidth, boxHeight)
	box := boxStyle.Render(listContent)

	helpLine := m.renderHelpLine()
	return box + "\n" + helpLine
}

// viewTwoPane renders the 2-pane layout (left: session list, right: preview)
func (m Model) viewTwoPane() string {
	// Total available width for both panes + gap
	// Border takes 2 chars per pane (left+right), gap is 1 char
	totalAvailable := m.width
	gap := 1
	borderOverhead := 2 + 2 // 2 per pane (left border + right border)

	// Distribute width: left 25%, right 75%
	usableWidth := totalAvailable - gap - borderOverhead
	if usableWidth < 60 {
		usableWidth = 60
	}

	leftContentWidth := usableWidth * 25 / 100
	rightContentWidth := usableWidth - leftContentWidth

	if leftContentWidth < 28 {
		leftContentWidth = 28
	}
	if rightContentWidth < 30 {
		rightContentWidth = 30
	}

	// Ensure total doesn't exceed terminal width
	totalNeeded := leftContentWidth + rightContentWidth + borderOverhead + gap
	if totalNeeded > m.width {
		// Scale down proportionally
		excess := totalNeeded - m.width
		rightContentWidth -= excess
		if rightContentWidth < 20 {
			rightContentWidth = 20
			leftContentWidth = m.width - rightContentWidth - borderOverhead - gap
		}
	}

	// Box dimensions (lipgloss Width is content width, border is added on top)
	leftBoxWidth := leftContentWidth
	rightBoxWidth := rightContentWidth

	// Calculate box height
	boxHeight := m.height - 3 // help line + margin
	if boxHeight < 10 {
		boxHeight = 10
	}

	// Render left pane (session list)
	listContent := m.renderListContent(leftContentWidth)
	leftStyle := createPaneStyle(leftBoxWidth, boxHeight, m.focusPane == FocusLeft)
	leftPane := leftStyle.Render(listContent)

	// Render right pane (preview)
	previewContent := m.renderPreviewContent(rightContentWidth, boxHeight-2)
	rightStyle := createPaneStyle(rightBoxWidth, boxHeight, m.focusPane == FocusRight)
	rightPane := rightStyle.Render(previewContent)

	// Join panes horizontally with a gap
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, strings.Repeat(" ", gap), rightPane)

	helpLine := m.renderHelpLine()
	return panes + "\n" + helpLine
}

// renderListContent renders the session list content (shared between 1-pane and 2-pane)
func (m Model) renderListContent(contentWidth int) string {
	var content strings.Builder

	// Header line: title + current time
	title := titleStyle.Render("ccvalet")
	currentTime := time.Now().Format("15:04:05")
	timeDisplay := fmt.Sprintf("[ %s ]", currentTime)

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

	// STATS line
	statusSummary := buildStatusSummary(m.sessions)
	if statusSummary != "" {
		content.WriteString("STATS: ")
		content.WriteString(statusSummary)
		content.WriteString("\n")
	}

	// Separator
	content.WriteString(strings.Repeat("─", contentWidth))
	content.WriteString("\n")

	// Error message
	if m.err != nil {
		content.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(fmt.Sprintf("Error: %v", m.err)))
		content.WriteString("\n\n")
	}

	// Queued sessions count
	var queuedSessions []session.Info
	for _, sess := range m.sessions {
		if sess.Status == session.StatusQueued {
			queuedSessions = append(queuedSessions, sess)
		}
	}

	// Sessions list
	if len(m.sessions) == 0 {
		content.WriteString("\n")
		content.WriteString(helpStyle.Render("No sessions. Press 'n' to create one."))
		content.WriteString("\n")
	} else {
		pageSessions := m.getPageSessions()
		for i, sess := range pageSessions {
			content.WriteString(m.renderSession(sess, i == m.cursor, contentWidth))
		}

		if len(queuedSessions) > 0 {
			content.WriteString("\n")
			queueNote := fmt.Sprintf("(%d queued)", len(queuedSessions))
			content.WriteString(queueHeaderStyle.Render(queueNote))
			content.WriteString("\n")
		}
	}

	// Page info
	totalPages := m.getTotalPages()
	if totalPages > 1 {
		content.WriteString("\n")
		pageInfo := fmt.Sprintf("Page %d/%d", m.currentPage+1, totalPages)
		pageInfoStyled := helpStyle.Render(pageInfo)
		pageInfoLen := lipgloss.Width(pageInfoStyled)
		leftPad := (contentWidth - pageInfoLen) / 2
		if leftPad > 0 {
			content.WriteString(strings.Repeat(" ", leftPad))
		}
		content.WriteString(pageInfoStyled)
	}

	return content.String()
}

// renderPreviewContent renders the right pane content
func (m Model) renderPreviewContent(contentWidth, contentHeight int) string {
	// Get the currently selected session
	pageSessions := m.getPageSessions()
	if len(pageSessions) == 0 || m.cursor >= len(pageSessions) {
		return "\n" + previewDimStyle.Render("  No session selected")
	}

	sess := pageSessions[m.cursor]

	// VT mode: render live terminal output for active sessions
	if m.vtEmulator != nil && m.previewSessionID == sess.ID && isActiveSession(sess.Status) {
		return m.renderVTContent(contentWidth, contentHeight)
	}

	// Info panel mode: for stopped/queued/error sessions
	return m.renderInfoPanel(sess, contentWidth, contentHeight)
}

// renderVTContent renders the VT emulator output for the right pane
func (m Model) renderVTContent(contentWidth, contentHeight int) string {
	if m.vtEmulator == nil {
		return ""
	}

	// Get rendered output from VT emulator (ANSI color codes included)
	rendered := m.vtEmulator.Render()

	// Split into lines and ensure we don't exceed pane height
	lines := strings.Split(rendered, "\n")
	if len(lines) > contentHeight {
		lines = lines[:contentHeight]
	}

	// Truncate lines that exceed content width (ANSI-aware)
	for i, line := range lines {
		visibleWidth := runewidth.StringWidth(stripANSI(line))
		if visibleWidth > contentWidth {
			lines[i] = truncateANSI(line, contentWidth)
		}
	}

	return strings.Join(lines, "\n")
}

// truncateANSI truncates an ANSI-encoded string to maxWidth visible characters,
// preserving escape sequences and appending a reset at the end.
func truncateANSI(s string, maxWidth int) string {
	var result strings.Builder
	width := 0
	inEscape := false

	for i := 0; i < len(s); {
		b := s[i]

		// Start of escape sequence
		if b == '\x1b' {
			inEscape = true
			result.WriteByte(b)
			i++
			continue
		}

		// Inside escape sequence: pass through until terminator
		if inEscape {
			result.WriteByte(b)
			// CSI sequences end with a letter (0x40-0x7e)
			// OSC sequences end with ST (\x1b\\) or BEL (\x07)
			if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '~' {
				inEscape = false
			}
			i++
			continue
		}

		// Decode rune for width calculation
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if width+rw > maxWidth {
			break
		}
		result.WriteString(s[i : i+size])
		width += rw
		i += size
	}

	// Reset styling at truncation point
	result.WriteString("\x1b[0m")
	return result.String()
}

// renderInfoPanel renders the info panel for stopped/queued/error sessions
func (m Model) renderInfoPanel(sess session.Info, contentWidth, contentHeight int) string {
	var content strings.Builder

	// Preview header: session name + status
	headerTitle := previewTitleStyle.Render(truncateString(sess.Name, contentWidth/2))
	statusIcon, statusLabel, statusSty := getStatusDisplay(sess.Status)
	statusStr := statusSty.Render(statusIcon + " " + statusLabel)
	headerSpacing := contentWidth - lipgloss.Width(headerTitle) - lipgloss.Width(statusStr)
	if headerSpacing < 2 {
		headerSpacing = 2
	}
	content.WriteString(headerTitle)
	content.WriteString(strings.Repeat(" ", headerSpacing))
	content.WriteString(statusStr)
	content.WriteString("\n")
	content.WriteString(strings.Repeat("─", contentWidth))
	content.WriteString("\n")

	// Session metadata section
	if sess.Repository != "" || sess.Branch != "" {
		if sess.Repository != "" {
			content.WriteString(previewDimStyle.Render("  Repo: "))
			content.WriteString(truncateString(sess.Repository, contentWidth-8))
			content.WriteString("\n")
		}
		if sess.Branch != "" {
			content.WriteString(previewDimStyle.Render("  Branch: "))
			content.WriteString(truncateString(sess.Branch, contentWidth-10))
			content.WriteString("\n")
		}
	}
	if sess.WorkDir != "" {
		workDir := sess.WorkDir
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(workDir, home) {
			workDir = "~" + workDir[len(home):]
		}
		content.WriteString(previewDimStyle.Render("  Dir: "))
		content.WriteString(truncateString(workDir, contentWidth-7))
		content.WriteString("\n")
	}
	content.WriteString("\n")

	isFocused := m.focusPane == FocusRight

	switch sess.Status {
	case session.StatusStopped:
		content.WriteString(previewStoppedStyle.Render("  ■ Session stopped"))
		content.WriteString("\n\n")
		m.renderLastMessages(&content, sess, contentWidth)
		content.WriteString("\n")
		if isFocused {
			content.WriteString(lipgloss.NewStyle().Foreground(successColor).Render("  [enter/r] Resume  [a] Attach"))
		} else {
			content.WriteString(previewDimStyle.Render("  [tab] to focus this pane"))
		}
		content.WriteString("\n")

	case session.StatusQueued:
		content.WriteString(previewDimStyle.Render("  … Waiting in queue..."))
		content.WriteString("\n")

	case session.StatusError:
		content.WriteString(lipgloss.NewStyle().Foreground(errorColor).Bold(true).Render("  ✗ Error"))
		content.WriteString("\n\n")
		if sess.ErrorMessage != "" {
			errLines := wrapText(sess.ErrorMessage, contentWidth-4)
			for _, line := range errLines {
				content.WriteString("  " + lipgloss.NewStyle().Foreground(errorColor).Render(line))
				content.WriteString("\n")
			}
		}
		content.WriteString("\n")
		if isFocused {
			content.WriteString(lipgloss.NewStyle().Foreground(warningColor).Render("  [r] Retry"))
		}
		content.WriteString("\n")

	default:
		// Fallback for any other state without VT emulator
		m.renderLastMessages(&content, sess, contentWidth)
		content.WriteString("\n")
		if isFocused {
			content.WriteString(previewDimStyle.Render("  [a] Attach"))
		}
		content.WriteString("\n")
	}

	return content.String()
}

// renderLastMessages renders the last user and assistant messages in the preview pane
func (m Model) renderLastMessages(content *strings.Builder, sess session.Info, contentWidth int) {
	if sess.LastUserMessage == "" && sess.LastAssistantMessage == "" {
		return
	}

	content.WriteString("\n")
	content.WriteString(strings.Repeat("─", contentWidth))
	content.WriteString("\n")

	if sess.LastUserMessage != "" {
		content.WriteString(lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("  User:"))
		content.WriteString("\n")
		msgLines := wrapText(sess.LastUserMessage, contentWidth-4)
		maxLines := 5
		if len(msgLines) > maxLines {
			msgLines = msgLines[:maxLines]
			msgLines = append(msgLines, "...")
		}
		for _, line := range msgLines {
			content.WriteString("  " + line)
			content.WriteString("\n")
		}
	}

	if sess.LastAssistantMessage != "" {
		content.WriteString("\n")
		content.WriteString(lipgloss.NewStyle().Foreground(successColor).Bold(true).Render("  Assistant:"))
		content.WriteString("\n")
		msgLines := wrapText(sess.LastAssistantMessage, contentWidth-4)
		maxLines := 10
		if len(msgLines) > maxLines {
			msgLines = msgLines[:maxLines]
			msgLines = append(msgLines, "...")
		}
		for _, line := range msgLines {
			content.WriteString("  " + line)
			content.WriteString("\n")
		}
	}
}

// cleanPreviewLine removes control characters from a preview line
func cleanPreviewLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 32 || r == '\t' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wrapText wraps text to fit within the specified width
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	var lines []string
	// First split by existing newlines
	rawLines := strings.Split(text, "\n")
	for _, rawLine := range rawLines {
		if runewidth.StringWidth(rawLine) <= width {
			lines = append(lines, rawLine)
			continue
		}
		// Wrap long lines
		runes := []rune(rawLine)
		current := 0
		for current < len(runes) {
			end := current
			lineWidth := 0
			for end < len(runes) && lineWidth < width {
				w := runewidth.RuneWidth(runes[end])
				if lineWidth+w > width {
					break
				}
				lineWidth += w
				end++
			}
			if end == current {
				end++ // Avoid infinite loop for very wide characters
			}
			lines = append(lines, string(runes[current:end]))
			current = end
		}
	}
	return lines
}

// renderHelpLine renders the help line (shared between 1-pane and 2-pane)
func (m Model) renderHelpLine() string {
	if m.confirmDelete {
		confirmMsg := fmt.Sprintf(" Delete session '%s'? [y/Enter] yes  [n/Esc] no", m.deleteTargetName)
		return lipgloss.NewStyle().Foreground(warningColor).Bold(true).Render(confirmMsg)
	}

	// In 2-pane mode, show pane-aware help
	if m.width >= minTwoPaneWidth {
		if m.focusPane == FocusRight {
			if m.vtEmulator != nil {
				return helpStyle.Render(" [ctrl+]] left pane  (terminal mode)")
			}
			return helpStyle.Render(" [ctrl+]] left pane [esc] left pane [q] quit")
		}
		if m.showHelp {
			return helpStyle.Render(" [↑/k] up [↓/j] down [tab] right pane [enter] attach [n] new [s] kill [d] del [q] quit")
		}
		return helpStyle.Render(" [tab] right pane [enter] attach [n] new [s] kill [d] del [←→] page [q] quit [?] help")
	}

	if m.showHelp {
		return helpStyle.Render(" [↑/k] up [↓/j] down [←/h] prev [→/l] next [enter] attach [n] new [s] kill [d] delete [q] quit")
	}
	return helpStyle.Render(" [n] new [s] kill [d] del [enter] attach [←→] page [q] quit [?] help")
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

// viewCreatePopup renders the create form as a centered popup over the background.
func (m Model) viewCreatePopup(bg string) string {
	formContent := m.viewCreateMode()

	// Popup dimensions: 70% width, fit content height up to 80% of terminal
	popupWidth := m.width * 70 / 100
	if popupWidth < 60 {
		popupWidth = 60
	}
	if popupWidth > m.width-4 {
		popupWidth = m.width - 4
	}

	// Count content lines to size the popup
	contentLines := strings.Count(formContent, "\n") + 1
	popupHeight := contentLines + 2 // +2 for border padding
	maxHeight := m.height * 80 / 100
	if popupHeight > maxHeight {
		popupHeight = maxHeight
	}
	if popupHeight < 10 {
		popupHeight = 10
	}

	popupStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(primaryColor).
		Width(popupWidth).
		Height(popupHeight).
		Padding(0, 1)

	popup := popupStyle.Render(formContent)

	return overlayCenter(bg, popup, m.width, m.height)
}

// overlayCenter places the foreground string centered on top of the background string.
func overlayCenter(bg, fg string, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	// Pad background to fill the terminal
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}

	// Calculate vertical and horizontal offset for centering
	fgHeight := len(fgLines)
	fgWidth := 0
	for _, line := range fgLines {
		if w := runewidth.StringWidth(stripAnsi(line)); w > fgWidth {
			fgWidth = w
		}
	}

	topOffset := (height - fgHeight) / 2
	if topOffset < 0 {
		topOffset = 0
	}
	leftOffset := (width - fgWidth) / 2
	if leftOffset < 0 {
		leftOffset = 0
	}

	// Overlay foreground onto background
	result := make([]string, len(bgLines))
	for i, bgLine := range bgLines {
		fgIdx := i - topOffset
		if fgIdx >= 0 && fgIdx < len(fgLines) && fgLines[fgIdx] != "" {
			fgLine := fgLines[fgIdx]
			result[i] = overlayLine(bgLine, fgLine, leftOffset, width)
		} else {
			result[i] = bgLine
		}
	}

	return strings.Join(result, "\n")
}

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return re.ReplaceAllString(s, "")
}

// overlayLine overlays a foreground line onto a background line at the given column offset.
func overlayLine(bgLine, fgLine string, leftOffset, totalWidth int) string {
	// Convert to rune-width aware operations
	bgPlain := stripAnsi(bgLine)
	bgWidth := runewidth.StringWidth(bgPlain)

	// Pad background if needed
	if bgWidth < totalWidth {
		bgLine += strings.Repeat(" ", totalWidth-bgWidth)
	}

	fgLineWidth := runewidth.StringWidth(stripAnsi(fgLine))

	// Build: [bg left part] + [fg line] + [bg right part]
	var result strings.Builder

	// Extract left portion of background (leftOffset columns)
	result.WriteString(truncateAnsiToWidth(bgLine, leftOffset))

	// Write the foreground line
	result.WriteString(fgLine)

	// Extract right portion of background
	rightStart := leftOffset + fgLineWidth
	if rightStart < totalWidth {
		result.WriteString(sliceFromWidth(bgLine, rightStart, totalWidth))
	}

	return result.String()
}

// truncateAnsiToWidth returns the prefix of an ANSI-styled string that fits within maxWidth display columns,
// padding with spaces to exactly fill the width.
func truncateAnsiToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	w := 0
	inEsc := false
	var result strings.Builder
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			result.WriteRune(r)
			continue
		}
		if inEsc {
			result.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		rw := runewidth.RuneWidth(r)
		if w+rw > maxWidth {
			break
		}
		result.WriteRune(r)
		w += rw
	}
	// Pad to exact width
	for w < maxWidth {
		result.WriteByte(' ')
		w++
	}
	return result.String()
}

// sliceFromWidth returns the portion of s from startCol to endCol in display columns.
func sliceFromWidth(s string, startCol, endCol int) string {
	if startCol >= endCol {
		return ""
	}
	w := 0
	inEsc := false
	started := false
	var result strings.Builder
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			if started {
				result.WriteRune(r)
			}
			continue
		}
		if inEsc {
			if started {
				result.WriteRune(r)
			}
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		rw := runewidth.RuneWidth(r)
		if w+rw > startCol && !started {
			started = true
		}
		if started {
			if w >= endCol {
				break
			}
			result.WriteRune(r)
		}
		w += rw
	}
	return result.String()
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
