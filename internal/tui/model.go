package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"github.com/takaaki-s/claude-code-valet/internal/host"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

// maxTUIWidth is the maximum width (columns) for the TUI pane.
// When the terminal is maximized, the TUI pane is resized to this width
// so the display pane gets the extra space.
const maxTUIWidth = 50

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
	keys         KeyMap      // キーバインド設定

	// Config manager (used for remote session attach)
	configMgr *config.Manager

	// Pagination
	currentPage int // 現在のページ（0-indexed）

	// Delete confirmation
	confirmDelete    bool   // 削除確認中かどうか
	deleteTargetID     string // 削除対象のセッションID
	deleteTargetName   string // 削除対象のセッション名（表示用）
	deleteTargetHostID string // 削除対象のホストID

	// Kill confirmation
	confirmKill      bool   // Kill確認中かどうか
	killTargetID     string // Kill対象のセッションID
	killTargetName   string // Kill対象のセッション名（表示用）
	killTargetHostID string // Kill対象のホストID

	// Focus tracking (for visual focus indicator)
	focused bool // true when TUI pane has focus (changes border/title color)

	// tmux integration
	tmuxClient       *tmux.Client // outer tmux client (-L ccvalet-mgr, nil in legacy mode)
	tuiPaneID        string       // TUIペインの固有ID (例: "%42") in outer tmux
	displayPaneID    string       // 右ペインの固有ID (セッション表示用) in outer tmux
	currentSessionID string       // 現在右ペインに表示中のセッションID
	switchSeq        int          // カーソル移動デバウンス用シーケンス番号

	// Focus after create
	focusSessionID string // 作成後にフォーカスするセッションID

	// Reswitch after delete/kill
	needsReswitch bool // 削除/Kill後にカーソル位置のセッションに再接続

	// Processing indicator
	processingMsg    string // 処理中メッセージ（空でない時はオーバーレイ表示）
	waitingForResize bool   // WindowSizeMsg到着を待っている（ZoomPane後のリサイズ完了待ち）
}

// NewModel creates a new TUI model
func NewModel(client *daemon.Client) Model {
	// Initialize config manager
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".ccvalet")
	configMgr, _ := config.NewManager(configDir)

	// Initialize keybindings
	var keybindings config.KeybindingsConfig
	if configMgr != nil {
		keybindings = configMgr.GetKeybindings()
	} else {
		keybindings = config.DefaultKeybindings()
	}
	keys := NewKeyMap(keybindings)

	return Model{
		client:    client,
		keys:      keys,
		focused:   true,
		configMgr: configMgr,
	}
}

// NewModelWithTmux creates a new TUI model with tmux integration.
// The outer tmux (-L ccvalet-mgr) has a fixed 2-pane layout:
// left pane (TUI) + right pane (session display via RespawnPane).
func NewModelWithTmux(client *daemon.Client, tc *tmux.Client, tuiPaneID, displayPaneID string) Model {
	m := NewModel(client)
	m.tmuxClient = tc
	m.tuiPaneID = tuiPaneID
	m.displayPaneID = displayPaneID
	// Restore which session was displayed (for reattach)
	m.currentSessionID = tc.GetEnvironment(tmux.SessionName, "CCVALET_CURRENT_SESSION")
	return m
}

// getItemsPerPage calculates how many items fit on one page
func (m *Model) getItemsPerPage() int {
	// Subtract header lines (title, stats, separator, footer)
	// Header: 3 lines, Footer: 2 lines (page info + help)
	availableLines := m.height - 8
	if availableLines < 4 {
		availableLines = 4
	}
	// Each session takes ~4 lines (name + status + meta + time)
	items := availableLines / 4
	if items < 1 {
		items = 1
	}
	return items
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

// cursorSettledMsg is sent after a debounce delay when the cursor stops moving.
type cursorSettledMsg struct {
	seq int
}

func cursorSettledCmd(seq int) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return cursorSettledMsg{seq: seq}
	})
}

// resizeSettledMsg is sent after a delay to allow WindowSizeMsg to arrive
// after tmux pane operations (ZoomPane).
type resizeSettledMsg struct{}

func resizeSettledCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return resizeSettledMsg{}
	})
}

// switchToSession displays the given session in the right pane via RespawnPane.
// For local sessions, attaches to the inner tmux session (-L ccvalet).
// For remote sessions, runs SSH attach command.
// For stopped/error sessions, shows a placeholder with session info.
func (m *Model) switchToSession(sessionID string) {
	if m.tmuxClient == nil || m.displayPaneID == "" || sessionID == "" {
		return
	}

	// Already displaying this session
	if m.currentSessionID == sessionID {
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
	if sess == nil {
		return
	}

	// Stopped/error sessions: show placeholder in right pane (no TmuxWindowName needed)
	if !isSessionAlive(sess.Status) {
		var placeholderCmd string
		if sess.ErrorMessage != "" {
			placeholderCmd = fmt.Sprintf(
				"printf '\\n  Session: %s\\n  Status:  %s\\n\\n  Error:\\n%s\\n'; tail -f /dev/null",
				sess.Name, sess.Status, sess.ErrorMessage,
			)
		} else {
			placeholderCmd = fmt.Sprintf(
				"printf '\\n  Session: %s\\n  Status:  %s\\n\\n  Press Enter to restart\\n'; tail -f /dev/null",
				sess.Name, sess.Status,
			)
		}
		m.tmuxClient.RespawnPane(m.displayPaneID, placeholderCmd)
		m.currentSessionID = sessionID
		m.tmuxClient.SetEnvironment(tmux.SessionName, "CCVALET_CURRENT_SESSION", sessionID)
		m.tmuxClient.SetPaneOption(m.displayPaneID, "@session_name", sess.Name)
		return
	}

	// Running sessions require TmuxWindowName for inner tmux attach
	if sess.TmuxWindowName == "" {
		return
	}

	// Remote session: use SSH attach command
	if sess.HostID != "" && sess.HostID != "local" {
		m.switchToRemoteSession(sess)
		m.tmuxClient.SetPaneOption(m.displayPaneID, "@session_name", sess.Name)
		return
	}

	// Local: respawn right pane with inner tmux attach
	attachCmd := fmt.Sprintf("tmux -L %s attach -t %s", tmux.SocketName, sess.TmuxWindowName)
	m.tmuxClient.RespawnPane(m.displayPaneID, attachCmd)

	m.currentSessionID = sessionID
	m.tmuxClient.SetEnvironment(tmux.SessionName, "CCVALET_CURRENT_SESSION", sessionID)
	m.tmuxClient.SetPaneOption(m.displayPaneID, "@session_name", sess.Name)
}

// isSessionAlive returns true if the session status indicates an active process.
func isSessionAlive(status session.Status) bool {
	switch status {
	case session.StatusRunning, session.StatusThinking, session.StatusIdle,
		session.StatusPermission, session.StatusConfirm, session.StatusCreating:
		return true
	}
	return false
}

// switchToRemoteSession displays a remote session in the right pane via RespawnPane.
func (m *Model) switchToRemoteSession(sess *session.Info) {
	if m.configMgr == nil {
		return
	}

	hostConfig := m.configMgr.GetHost(sess.HostID)
	if hostConfig == nil {
		return
	}

	// Ensure a background ControlMaster SSH connection exists for this host.
	// This is separate from the tmux pane process, so RespawnPane won't kill it.
	// Subsequent SSH connections (slaves) reuse the master for near-instant connection.
	host.EnsureSSHMaster(*hostConfig)

	// Generate SSH attach command string (slave connection via ControlMaster)
	attachCmd := host.AttachCommandString(*hostConfig, sess.TmuxWindowName)
	m.tmuxClient.RespawnPane(m.displayPaneID, attachCmd)

	m.currentSessionID = sess.ID
	m.tmuxClient.SetEnvironment(tmux.SessionName, "CCVALET_CURRENT_SESSION", sess.ID)
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
		// Cap TUI pane width: resize via tmux so the display pane gets the extra space.
		if m.width > maxTUIWidth && m.tmuxClient != nil && m.tuiPaneID != "" && m.displayPaneID != "" {
			m.tmuxClient.ResizePaneWidth(m.tuiPaneID, maxTUIWidth)
		}
		// ZoomPane後のリサイズ完了を検知
		// WindowSizeMsgが届いた = ペインサイズが確定した → processingMsgをクリアして再描画
		if m.waitingForResize {
			m.waitingForResize = false
			m.processingMsg = ""
			return m, tea.ClearScreen
		}
	}

	// Handle focus events (from tmux focus-events + tea.WithReportFocus)
	if _, ok := msg.(tea.FocusMsg); ok {
		m.focused = true
		return m, nil
	}
	if _, ok := msg.(tea.BlurMsg); ok {
		m.focused = false
		return m, nil
	}

	// 処理中はキー入力を無視し、完了メッセージのみ処理
	if m.processingMsg != "" {
		switch msg.(type) {
		case tea.KeyMsg:
			return m, nil
		}
	}

	return m.updateListMode(msg)
}

func (m Model) updateListMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// 削除確認モード中の処理
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y", "enter":
				m.processingMsg = "Deleting..."
				m.confirmDelete = false
				m.needsReswitch = true

				deleteID := m.deleteTargetID
				deleteHostID := m.deleteTargetHostID
				m.deleteTargetID = ""
				m.deleteTargetName = ""
				m.deleteTargetHostID = ""

				client := m.client

				return m, func() tea.Msg {
					if err := client.Delete(deleteID, deleteHostID); err != nil {
						return errMsg(fmt.Errorf("delete failed: %w", err))
					}
					sessions, err := client.List()
					if err != nil {
						return errMsg(err)
					}
					return sessionsMsg(sessions)
				}
			case "n", "N", "esc":
				m.confirmDelete = false
				m.deleteTargetID = ""
				m.deleteTargetName = ""
				m.deleteTargetHostID = ""
				return m, nil
			}
			return m, nil
		}

		// Kill確認モード中の処理
		if m.confirmKill {
			switch msg.String() {
			case "y", "Y", "enter":
				m.processingMsg = "Stopping..."
				m.confirmKill = false
				m.needsReswitch = true

				killID := m.killTargetID
				killHostID := m.killTargetHostID
				m.killTargetID = ""
				m.killTargetName = ""
				m.killTargetHostID = ""

				client := m.client

				return m, func() tea.Msg {
					if err := client.Kill(killID, killHostID); err != nil {
						return errMsg(fmt.Errorf("kill failed: %w", err))
					}
					sessions, err := client.List()
					if err != nil {
						return errMsg(err)
					}
					return sessionsMsg(sessions)
				}
			case "n", "N", "esc":
				m.confirmKill = false
				m.killTargetID = ""
				m.killTargetName = ""
				m.killTargetHostID = ""
				return m, nil
			}
			return m, nil
		}

		// Left pane key handling
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			m.switchSeq++
			return m, cursorSettledCmd(m.switchSeq)

		case key.Matches(msg, m.keys.Down):
			pageSessions := m.getPageSessions()
			if m.cursor < len(pageSessions)-1 {
				m.cursor++
			}
			m.switchSeq++
			return m, cursorSettledCmd(m.switchSeq)

		case key.Matches(msg, m.keys.Enter):
			m.switchSeq++ // デバウンス中のswitchをキャンセル
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

				// tmux mode: start session and display in right pane
				if m.tmuxClient != nil {
					needsStart := sess.Status == session.StatusStopped || sess.Status == session.StatusError
					if needsStart {
						if err := m.client.Start(sess.ID, sess.HostID); err != nil {
							m.err = err
							return m, nil
						}
						// Update local session data immediately
						for i := range m.sessions {
							if m.sessions[i].ID == sess.ID {
								if m.sessions[i].TmuxWindowName == "" {
									m.sessions[i].TmuxWindowName = tmux.InnerSessionName(sess.ID)
								}
								m.sessions[i].Status = session.StatusRunning
								break
							}
						}
						// Force switchToSession to run
						m.currentSessionID = ""
					}
					m.switchToSession(sess.ID)
					// Focus right pane (session display)
					if m.displayPaneID != "" {
						m.tmuxClient.SelectPane(m.displayPaneID)
					}
					return m, m.fetchSessions
				}
			}

		case key.Matches(msg, m.keys.New):
			// Open session creation form in a tmux popup
			if m.tmuxClient != nil {
				selfBin, _ := os.Executable()
				m.tmuxClient.DisplayPopup(tmux.DisplayPopupOptions{
					Width:  "80%",
					Height: "80%",
					Cmd:    fmt.Sprintf("'%s' create-popup", selfBin),
					Title:  " New Session ",
				})
			}
			return m, nil

		case key.Matches(msg, m.keys.Kill):
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				// 確認モードに入る
				m.confirmKill = true
				m.killTargetID = sess.ID
				m.killTargetName = sess.Name
				m.killTargetHostID = sess.HostID
				return m, nil
			}

		case key.Matches(msg, m.keys.Delete):
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				// 確認モードに入る
				m.confirmDelete = true
				m.deleteTargetID = sess.ID
				m.deleteTargetName = sess.Name
				m.deleteTargetHostID = sess.HostID
				return m, nil
			}

		case key.Matches(msg, m.keys.Cancel):
			// Cancel only works on queued sessions
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				sess := pageSessions[m.cursor]
				if sess.Status == session.StatusQueued {
					if err := m.client.Delete(sess.ID, sess.HostID); err != nil {
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
			if m.tmuxClient != nil {
				selfBin, _ := os.Executable()
				m.tmuxClient.DisplayPopup(tmux.DisplayPopupOptions{
					Width:  "60%",
					Height: "60%",
					Cmd:    fmt.Sprintf("'%s' help-popup", selfBin),
					Title:  " Shortcuts ",
				})
			}
			return m, nil

		case key.Matches(msg, m.keys.PrevPage):
			if m.currentPage > 0 {
				m.currentPage--
				m.cursor = 0
			}
			return m, nil

		case key.Matches(msg, m.keys.NextPage):
			totalPages := m.getTotalPages()
			if m.currentPage < totalPages-1 {
				m.currentPage++
				m.cursor = 0
			}
			return m, nil
		}

	case sessionsMsg:
		m.sessions = msg
		m.err = nil
		// 作成直後のセッションにフォーカス＋右ペイン切り替え
		if m.focusSessionID != "" {
			itemsPerPage := m.getItemsPerPage()
			for i, s := range m.sessions {
				if s.ID == m.focusSessionID {
					m.currentPage = i / itemsPerPage
					m.cursor = i % itemsPerPage
					m.currentSessionID = "" // 強制リセットで switchToSession を実行
					m.switchToSession(s.ID)
					break
				}
			}
			m.focusSessionID = ""
			return m, nil
		}
		if m.cursor >= len(m.sessions) && m.cursor > 0 {
			m.cursor = len(m.sessions) - 1
		}
		// 削除/Kill後にカーソル位置のセッションに再接続
		if m.needsReswitch {
			m.needsReswitch = false
			m.currentSessionID = "" // 強制リセット
			pageSessions := m.getPageSessions()
			if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
				m.switchToSession(pageSessions[m.cursor].ID)
			} else {
				// セッションがなくなった場合はplaceholderに戻す
				if m.tmuxClient != nil && m.displayPaneID != "" {
					m.tmuxClient.RespawnPane(m.displayPaneID, tmux.PlaceholderCmd)
				}
			}
			m.processingMsg = ""
			return m, nil
		}
		m.processingMsg = ""
		return m, nil

	case resizeSettledMsg:
		// フォールバック: WindowSizeMsgが来なかった場合（ペインサイズ変更なし）
		if m.waitingForResize {
			m.waitingForResize = false
			m.processingMsg = ""
			return m, tea.ClearScreen
		}
		return m, nil

	case cursorSettledMsg:
		if msg.seq != m.switchSeq {
			return m, nil
		}
		pageSessions := m.getPageSessions()
		if len(pageSessions) > 0 && m.cursor < len(pageSessions) {
			sess := pageSessions[m.cursor]
			m.switchToSession(sess.ID)
		}
		return m, nil

	case errMsg:
		m.processingMsg = ""
		m.err = msg

	case tickMsg:
		// Poll for session created via tmux popup
		if m.tmuxClient != nil {
			if id := m.tmuxClient.GetEnvironment(tmux.SessionName, "CCVALET_CREATED_SESSION"); id != "" {
				m.tmuxClient.UnsetEnvironment(tmux.SessionName, "CCVALET_CREATED_SESSION")
				m.focusSessionID = id
			}
		}
		return m, tea.Batch(m.fetchSessions, tickCmd())
	}

	return m, nil
}

// View renders the UI
func (m Model) View() string {
	// 処理中インジケーター
	if m.processingMsg != "" {
		return m.renderProcessingView()
	}

	boxWidth := m.width - 2
	if boxWidth < 20 {
		boxWidth = 20
	}
	boxHeight := m.height - 3
	if boxHeight < 5 {
		boxHeight = 5
	}
	boxStyle := createBoxStyle(boxWidth, boxHeight, m.focused)
	box := boxStyle.Render(m.renderListContent(boxWidth - 4))
	helpLine := m.renderHelpLine()
	return box + "\n" + helpLine
}

// renderProcessingView renders a processing indicator.
// サイズ非依存: ZoomPane/JoinPane後のWindowSizeMsg到着前でも正しく表示される
func (m Model) renderProcessingView() string {
	return "\n  ⟳ " + m.processingMsg
}

// renderListContent renders the session list content
func (m Model) renderListContent(contentWidth int) string {
	var content strings.Builder

	// Header line: title + current time
	ts := titleStyle
	if !m.focused {
		ts = ts.Foreground(secondaryColor)
	}
	title := ts.Render("ccvalet")
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
	content.WriteString(strings.Repeat("-", contentWidth))
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

// renderHelpLine renders the help line at the bottom
func (m Model) renderHelpLine() string {
	if m.confirmKill {
		name := truncateString(m.killTargetName, 20)
		confirmMsg := fmt.Sprintf(" Kill '%s'? y:yes n:no", name)
		return lipgloss.NewStyle().Foreground(warningColor).Bold(true).Render(confirmMsg)
	}
	if m.confirmDelete {
		name := truncateString(m.deleteTargetName, 20)
		confirmMsg := fmt.Sprintf(" Delete '%s'? y:yes n:no", name)
		return lipgloss.NewStyle().Foreground(warningColor).Bold(true).Render(confirmMsg)
	}
	return helpStyle.Render(" ? help")
}

// renderSession renders a single session in 1-line format with optional output preview
// Format: >name (branch)                    STATUS    Last Active
//         details...
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

	// --- Line 1: cursor + session name ---
	availableForName := width - 2 // cursor(2)
	name := truncateString(sess.Name, availableForName)

	if selected {
		b.WriteString(selectedItemStyle.Render(padLine("> "+name, width)))
	} else {
		b.WriteString("  ")
		b.WriteString(sessionNameStyle.Render(name))
	}
	b.WriteString("\n")

	// Build metadata: [host] repo (branch) — fallback to workDir
	var metaParts []string
	if sess.HostID != "" && sess.HostID != "local" {
		metaParts = append(metaParts, "["+sess.HostID+"]")
	}
	if sess.Repository != "" {
		metaParts = append(metaParts, sess.Repository)
	} else if sess.WorkDir != "" {
		workDir := sess.WorkDir
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(workDir, home) {
			workDir = "~" + workDir[len(home):]
		}
		metaParts = append(metaParts, workDir)
	}
	if sess.Branch != "" {
		metaParts = append(metaParts, "("+sess.Branch+")")
	}

	statusStr := statusIcon + " " + statusLabel
	metaStr := strings.Join(metaParts, " ")
	indent := "  ├─ "
	indentWidth := 5

	// Truncate metadata if needed
	availableForMeta := width - indentWidth
	if availableForMeta > 0 && runewidth.StringWidth(metaStr) > availableForMeta {
		metaStr = truncateString(metaStr, availableForMeta)
	}

	// --- Line 2: status (icon + label) ---
	if selected {
		b.WriteString(selectedItemStyle.Render(padLine(indent+statusStr, width)))
	} else {
		b.WriteString(indent)
		b.WriteString(statusStyle.Render(statusStr))
	}
	b.WriteString("\n")

	// Error: show metadata + error message, then return early
	if sess.Status == session.StatusError && sess.ErrorMessage != "" {
		if metaStr != "" {
			if selected {
				b.WriteString(selectedItemStyle.Render(padLine(indent+metaStr, width)))
			} else {
				b.WriteString(indent)
				b.WriteString(helpStyle.Render(metaStr))
			}
			b.WriteString("\n")
		}
		errMsg := truncateString(sess.ErrorMessage, width-indentWidth)
		if selected {
			b.WriteString(selectedItemStyle.Render(padLine(indent+errMsg, width)))
		} else {
			b.WriteString(indent)
			b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(errMsg))
		}
		b.WriteString("\n")
		// Time on last line
		if selected {
			b.WriteString(selectedItemStyle.Render(padLine("  └─ "+timeStr, width)))
		} else {
			b.WriteString("  └─ " + timeStyle.Render(timeStr))
		}
		b.WriteString("\n")
		return b.String()
	}

	// --- Line 3: metadata ([host] repo (branch)) ---
	if metaStr != "" {
		if selected {
			b.WriteString(selectedItemStyle.Render(padLine(indent+metaStr, width)))
		} else {
			b.WriteString(indent)
			b.WriteString(helpStyle.Render(metaStr))
		}
		b.WriteString("\n")
	}

	// --- Line 4: worktree name ---
	if sess.WorktreeName != "" {
		wtDisplay := "📁 " + sess.WorktreeName
		if selected {
			b.WriteString(selectedItemStyle.Render(padLine(indent+wtDisplay, width)))
		} else {
			b.WriteString(indent)
			b.WriteString(helpStyle.Render(wtDisplay))
		}
		b.WriteString("\n")
	}

	// --- Line 3: last user message ---
	if sess.LastUserMessage != "" {
		prefix := "  ├─ 👤 "
		pWidth := lipgloss.Width(prefix)
		msgWidth := width - pWidth
		if msgWidth < 10 {
			msgWidth = 10
		}
		msgStr := truncateString(sess.LastUserMessage, msgWidth)

		if selected {
			b.WriteString(selectedItemStyle.Render(padLine(prefix+msgStr, width)))
		} else {
			b.WriteString("  ├─ " + helpStyle.Render("👤 "+msgStr))
		}
		b.WriteString("\n")
	}

	// --- Line 4: last assistant message ---
	if sess.LastAssistantMessage != "" {
		prefix := "  ├─ 🤖 "
		pWidth := lipgloss.Width(prefix)
		msgWidth := width - pWidth
		if msgWidth < 10 {
			msgWidth = 10
		}
		msgStr := truncateStringFromEnd(sess.LastAssistantMessage, msgWidth)

		if selected {
			b.WriteString(selectedItemStyle.Render(padLine(prefix+msgStr, width)))
		} else {
			b.WriteString("  ├─ " + helpStyle.Render("🤖 "+msgStr))
		}
		b.WriteString("\n")
	}

	// --- Last line: time ---
	if selected {
		b.WriteString(selectedItemStyle.Render(padLine("  └─ "+timeStr, width)))
	} else {
		b.WriteString("  └─ " + timeStyle.Render(timeStr))
	}
	b.WriteString("\n")

	return b.String()
}

// padLine pads a string to the specified width with spaces.
func padLine(s string, width int) string {
	w := lipgloss.Width(s)
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
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

// getInUseStyle returns a style for the "in use" indicator based on session status.
func getInUseStyle(status session.Status) lipgloss.Style {
	switch status {
	case session.StatusThinking, session.StatusPermission, session.StatusConfirm:
		return lipgloss.NewStyle().Foreground(warningColor)
	case session.StatusRunning, session.StatusCreating:
		return lipgloss.NewStyle().Foreground(secondaryColor)
	default:
		return lipgloss.NewStyle().Foreground(dimColor)
	}
}

// formatInUseIndicator returns a formatted "in use" indicator string.
// Format: [status: session-name], truncated to maxWidth.
func formatInUseIndicator(sess session.Info, maxWidth int) string {
	statusStr := string(sess.Status)
	name := sess.Name
	// "[" + status + ": " + name + "]"
	overhead := len(statusStr) + 4 // "[", ": ", "]"
	availableForName := maxWidth - overhead
	if availableForName < 3 {
		return fmt.Sprintf("[%s]", statusStr)
	}
	name = truncateString(name, availableForName)
	return fmt.Sprintf("[%s: %s]", statusStr, name)
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
func buildStatusSummary(sessions []session.Info) string {
	counts := countStatuses(sessions)

	var parts []string
	if counts.thinking > 0 {
		parts = append(parts, thinkingStyle.Render(fmt.Sprintf("*%d Thinking", counts.thinking)))
	}
	if counts.permission > 0 {
		parts = append(parts, permissionStyle.Render(fmt.Sprintf("?%d Permission", counts.permission)))
	}
	if counts.confirm > 0 {
		parts = append(parts, confirmStyle.Render(fmt.Sprintf("!%d Confirm", counts.confirm)))
	}
	if counts.running > 0 {
		parts = append(parts, runningStyle.Render(fmt.Sprintf(">%d Running", counts.running)))
	}
	if counts.creating > 0 {
		parts = append(parts, creatingStyle.Render(fmt.Sprintf("+%d Creating", counts.creating)))
	}
	if counts.idle > 0 {
		parts = append(parts, idleStyle.Render(fmt.Sprintf("o%d Idle", counts.idle)))
	}
	if counts.queued > 0 {
		parts = append(parts, queuedStyle.Render(fmt.Sprintf(".%d Queued", counts.queued)))
	}
	if counts.errorCount > 0 {
		parts = append(parts, errorStatusStyle.Render(fmt.Sprintf("x%d Error", counts.errorCount)))
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
