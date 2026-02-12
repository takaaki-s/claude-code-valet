package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/notify"
	"github.com/takaaki-s/claude-code-valet/internal/prompt"
	"github.com/takaaki-s/claude-code-valet/internal/status"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/transcript"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
	"golang.org/x/term"
)

// debugEnabled controls debug logging output
var debugEnabled = os.Getenv("CCVALET_DEBUG") == "1"

func debugLog(format string, args ...interface{}) {
	if debugEnabled {
		log.Printf(format, args...)
	}
}

// Manager manages multiple Claude Code sessions
type Manager struct {
	sessions    map[string]*Session
	store       *Store
	notifier    *notify.Notifier
	promptMgr   *prompt.Manager
	configMgr   *config.Manager
	tmuxClient  *tmux.Client // tmux client (nil in legacy/non-tmux mode)
	mu          sync.RWMutex
}

// SetTmuxClient sets the tmux client for tmux-based session management.
// When set, sessions are started in tmux windows instead of PTY directly.
func (m *Manager) SetTmuxClient(tc *tmux.Client) {
	m.tmuxClient = tc
}

// RecoverTmuxSessions checks for sessions with existing tmux windows after daemon restart
// and resumes monitoring for live ones, or clears stale TmuxWindowName for dead ones.
func (m *Manager) RecoverTmuxSessions() {
	if m.tmuxClient == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.recoverTmuxSessionsLocked()
}

// recoverTmuxSessionsLocked is the lock-held version of RecoverTmuxSessions.
// Caller must hold m.mu.
func (m *Manager) recoverTmuxSessionsLocked() {
	if m.tmuxClient == nil {
		return
	}

	for _, session := range m.sessions {
		if session.TmuxWindowName == "" {
			continue
		}

		// Check if the tmux window still exists
		windowTarget := tmux.SessionName + ":" + session.TmuxWindowName
		if _, err := m.tmuxClient.PaneCount(windowTarget); err != nil {
			// Window doesn't exist anymore
			session.TmuxWindowName = ""
			m.store.Save(session)
			debugLog("[RECOVER] Session %s tmux window gone, cleared TmuxWindowName", session.Name)
			continue
		}

		target := tmux.WindowTarget(session.TmuxWindowName, 0)

		// Check if pane is dead — keep TmuxWindowName (window alive via remain-on-exit)
		if m.tmuxClient.IsPaneDead(target) {
			session.Status = StatusStopped
			m.store.Save(session)
			debugLog("[RECOVER] Session %s tmux pane dead, kept TmuxWindowName (window preserved)", session.Name)
			continue
		}

		// Window exists and pane is alive - resume monitoring
		session.Status = StatusRunning
		session.LastOutputTime = time.Now()
		m.store.Save(session)
		debugLog("[RECOVER] Session %s has live tmux window, resuming monitoring", session.Name)

		go m.captureOutputTmux(session)
	}
}

// ensureTmuxClient lazily initializes the tmux client if the ccvalet tmux session exists.
// This handles the case where the daemon starts before the TUI creates the tmux session.
func (m *Manager) ensureTmuxClient() {
	if m.tmuxClient != nil {
		return
	}
	tc, err := tmux.NewClient()
	if err != nil {
		return
	}
	if tc.HasSession(tmux.SessionName) {
		m.tmuxClient = tc
		debugLog("[TMUX] tmux client lazily initialized (session: %s)", tmux.SessionName)
		m.recoverTmuxSessionsLocked()
	}
}

// NewManager creates a new session manager
func NewManager(dataDir, configDir string, configMgr *config.Manager) (*Manager, error) {
	store, err := NewStore(dataDir)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		sessions:  make(map[string]*Session),
		store:     store,
		notifier:  notify.NewNotifier(),
		promptMgr: prompt.NewManager(configDir),
		configMgr: configMgr,
	}

	// Load existing sessions
	sessions, err := store.LoadAll()
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		// 作成に失敗したセッション（一度も起動していない）はスキップして削除
		if s.Status == StatusError && !s.ClaudeSessionStarted {
			debugLog("[SESSION] Removing failed creation session: %s (%s)", s.Name, s.ID)
			store.Delete(s.ID)
			continue
		}
		s.Status = StatusStopped // All loaded sessions start as stopped
		m.sessions[s.ID] = s
	}

	return m, nil
}

// CreateOptions contains options for creating a new session
type CreateOptions struct {
	// 必須項目
	Repository string // リポジトリ名
	WorkDir    string // ワークディレクトリパス（リポジトリ本体/既存worktree/新規worktreeパス）
	Branch     string // ブランチ名

	// オプション
	Name          string // セッション名（省略時: リポジトリ名/ブランチ名）
	NewBranch     bool   // 新規ブランチを作成するか
	BaseBranch    string // ベースブランチ（新規ブランチ時）
	IsNewWorktree bool   // 新規worktreeを作成するか
	WorktreeName  string // worktree名（新規worktree時、省略時はブランチ名）

	// プロンプト
	PromptName string
	PromptArgs string
}

// CreateWithOptions creates a new session with full options
func (m *Manager) CreateWithOptions(opts CreateOptions) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 必須フィールドのバリデーション
	if opts.Repository == "" {
		return nil, fmt.Errorf("repository is required")
	}
	if opts.Branch == "" {
		return nil, fmt.Errorf("branch is required")
	}

	// 重複ディレクトリチェック（WorkDirが指定されている場合）
	if opts.WorkDir != "" {
		for _, s := range m.sessions {
			if s.WorkDir == opts.WorkDir {
				return nil, fmt.Errorf("session already exists for directory: %s (session: %s)", opts.WorkDir, s.Name)
			}
		}
	}

	// 重複ブランチチェック（Repository + Branch の組み合わせ）
	// 同じリポジトリの同じブランチで複数のセッションを作成することを防止
	if opts.Repository != "" && opts.Branch != "" {
		for _, s := range m.sessions {
			if s.Repository == opts.Repository && s.Branch == opts.Branch {
				return nil, fmt.Errorf("session already exists for %s/%s (session: %s)",
					opts.Repository, opts.Branch, s.Name)
			}
		}
	}

	id := uuid.New().String() // Full UUID for Claude Code --session-id compatibility

	// セッション名の決定（デフォルト: リポジトリ名/ブランチ名）
	name := opts.Name
	if name == "" {
		name = fmt.Sprintf("%s/%s", opts.Repository, opts.Branch)
	}

	// セッション名の一意性チェック
	for _, s := range m.sessions {
		if s.Name == name {
			return nil, fmt.Errorf("session with name '%s' already exists. Use --name to specify a different name", name)
		}
	}

	// Generate Claude session ID for session persistence
	claudeSessionID := uuid.New().String()

	session := &Session{
		ID:              id,
		Name:            name,
		WorkDir:         opts.WorkDir,
		CreatedAt:       time.Now(),
		Status:          StatusStopped,
		Repository:      opts.Repository,
		Branch:          opts.Branch,
		BaseBranch:      opts.BaseBranch,
		NewBranch:       opts.NewBranch,
		IsNewWorktree:   opts.IsNewWorktree,
		WorktreeName:    opts.WorktreeName,
		PromptName:      opts.PromptName,
		PromptArgs:      opts.PromptArgs,
		ClaudeSessionID: claudeSessionID,
	}

	m.sessions[id] = session

	// Persist session
	if err := m.store.Save(session); err != nil {
		return nil, err
	}

	return session, nil
}

// List returns all sessions sorted by creation time
func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()

	reader := transcript.NewReader()
	infos := make([]Info, 0, len(m.sessions))
	for _, s := range m.sessions {
		info := s.ToInfo()

		// Fetch last messages from transcript if Claude session exists
		// Use larger limit here; actual truncation happens in TUI based on window width
		if s.ClaudeSessionID != "" && s.WorkDir != "" {
			if msgs, err := reader.GetLastMessages(s.WorkDir, s.ClaudeSessionID); err == nil && msgs != nil {
				if msgs.User != nil {
					info.LastUserMessage = transcript.TruncateMessage(msgs.User.Content, 500)
				}
				if msgs.Assistant != nil {
					// Use TruncateMessageFromEnd for assistant messages
					// Important content (like questions) is often at the end
					info.LastAssistantMessage = transcript.TruncateMessageFromEnd(msgs.Assistant.Content, 500)
				}
			}
		}

		infos = append(infos, info)
	}

	// Sort by CreatedAt (oldest first)
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})

	return infos
}

// Get returns a session by ID
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// SetStatus updates the status of a session
func (m *Manager) SetStatus(id string, status Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, ok := m.sessions[id]; ok {
		session.Status = status
	}
}

// SetStatusWithError updates the status and error message of a session
func (m *Manager) SetStatusWithError(id string, status Status, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, ok := m.sessions[id]; ok {
		session.Status = status
		session.ErrorMessage = errMsg
	}
}

// SetWorkDir updates the work directory of a session
// Returns error if the workDir is already in use by another session
func (m *Manager) SetWorkDir(id string, workDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 重複チェック（Asyncモードでの競合を防ぐ）
	if workDir != "" {
		for _, s := range m.sessions {
			if s.ID != id && s.WorkDir == workDir {
				return fmt.Errorf("WorkDir already in use by session %s", s.Name)
			}
		}
	}

	if session, ok := m.sessions[id]; ok {
		session.WorkDir = workDir
		// Persist the change
		m.store.Save(session)
	}
	return nil
}

// SetWorktreeName updates the worktree name of a session
func (m *Manager) SetWorktreeName(id string, worktreeName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, ok := m.sessions[id]; ok {
		session.WorktreeName = worktreeName
		// Persist the change
		m.store.Save(session)
	}
}

// CountActive returns the number of active sessions (creating, running, thinking, permission)
// These are the sessions that count against the parallel limit
// Excludes: stopped, idle, queued, error
func (m *Manager) CountActive() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, s := range m.sessions {
		switch s.Status {
		case StatusCreating, StatusRunning, StatusThinking, StatusPermission:
			count++
		}
	}
	return count
}

// GetNextQueued returns the oldest queued session (FIFO)
func (m *Manager) GetNextQueued() (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var oldest *Session
	for _, s := range m.sessions {
		if s.Status == StatusQueued {
			if oldest == nil || s.CreatedAt.Before(oldest.CreatedAt) {
				oldest = s
			}
		}
	}
	if oldest != nil {
		return oldest, true
	}
	return nil, false
}

// StartBackground starts a session in the background
func (m *Manager) StartBackground(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}

	if isProcessRunning(session) {
		return nil // Already running
	}

	return m.startSession(session)
}

// isProcessRunning returns true if the session process is running
// (any status except StatusStopped means the process is alive)
func isProcessRunning(s *Session) bool {
	if s.Status == StatusStopped {
		return false
	}
	// tmux mode: process is running if we have a tmux window name
	if s.TmuxWindowName != "" {
		return true
	}
	// Legacy mode: check PTY
	return s.PTY != nil
}

// Attach attaches to a session and runs interactively
func (m *Manager) Attach(id string) error {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}

	// Start the process if not running
	if !isProcessRunning(session) {
		if err := m.startSession(session); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Unlock()

	// Run interactive session
	return m.runInteractive(session)
}

// AttachToConn attaches a session to a network connection
func (m *Manager) AttachToConn(id string, conn io.ReadWriter) error {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}

	if !isProcessRunning(session) {
		m.mu.Unlock()
		return fmt.Errorf("session %s is not running", id)
	}

	// Get references to session resources
	screenBuffer := session.ScreenBuffer
	broadcaster := session.Broadcaster
	ptyFile := session.PTY
	cmd := session.Cmd
	m.mu.Unlock()

	// Subscribe to broadcaster for new output
	if broadcaster == nil {
		debugLog("[ATTACH] broadcaster is nil!")
		return fmt.Errorf("broadcaster is nil")
	}
	outputCh := broadcaster.Subscribe()
	debugLog("[ATTACH] subscribed to broadcaster")
	defer broadcaster.Unsubscribe(outputCh)

	// Send screen buffer to restore the current screen state
	if screenBuffer != nil {
		bufferData := screenBuffer.Bytes()
		debugLog("[ATTACH] screenBuffer size: %d bytes", len(bufferData))
		if len(bufferData) > 0 {
			conn.Write(bufferData)
		}
	}

	done := make(chan struct{}, 2)

	// Resize command markers
	resizePrefix := []byte("\x00\x00RESIZE:")
	resizeSuffix := []byte("\x00\x00")

	// Read from conn and write to PTY (input), handling resize commands
	go func() {
		buf := make([]byte, 4096)
		var pending []byte

		for {
			n, err := conn.Read(buf)
			if err != nil {
				done <- struct{}{}
				return
			}

			data := buf[:n]
			if len(pending) > 0 {
				data = append(pending, data...)
				pending = nil
			}

			// Process data, looking for resize commands
			for len(data) > 0 {
				// Check for resize command prefix
				if idx := bytes.Index(data, resizePrefix); idx >= 0 {
					// Write any data before the resize command
					if idx > 0 {
						ptyFile.Write(data[:idx])
					}

					// Find the end of the resize command
					cmdStart := idx + len(resizePrefix)
					remaining := data[cmdStart:]
					if endIdx := bytes.Index(remaining, resizeSuffix); endIdx >= 0 {
						// Parse resize command: cols:rows
						cmdData := string(remaining[:endIdx])
						if cols, rows, ok := parseResizeCmd(cmdData); ok {
							pty.Setsize(ptyFile, &pty.Winsize{
								Cols: uint16(cols),
								Rows: uint16(rows),
							})
							// Record resize time to prevent false thinking detection
							m.mu.Lock()
							if sess, ok := m.sessions[id]; ok {
								sess.LastResizeTime = time.Now()
							}
							m.mu.Unlock()
							// Send SIGWINCH to trigger TUI redraw
							if cmd != nil && cmd.Process != nil {
								cmd.Process.Signal(syscall.SIGWINCH)
							}
						}
						data = remaining[endIdx+len(resizeSuffix):]
					} else {
						// Incomplete resize command, save for next read
						pending = data[idx:]
						data = nil
					}
				} else {
					// Check if data ends with partial prefix
					partialMatch := false
					for i := 1; i < len(resizePrefix) && i <= len(data); i++ {
						if bytes.HasSuffix(data, resizePrefix[:i]) {
							pending = data[len(data)-i:]
							ptyFile.Write(data[:len(data)-i])
							partialMatch = true
							break
						}
					}
					if !partialMatch {
						ptyFile.Write(data)
					}
					data = nil
				}
			}
		}
	}()

	// Read from broadcaster and write to conn (output)
	go func() {
		for data := range outputCh {
			if _, err := conn.Write(data); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()

	<-done
	return nil
}

// parseResizeCmd parses "cols:rows" format
func parseResizeCmd(cmd string) (cols, rows int, ok bool) {
	parts := bytes.Split([]byte(cmd), []byte(":"))
	if len(parts) != 2 {
		return 0, 0, false
	}
	c, err1 := strconv.Atoi(string(parts[0]))
	r, err2 := strconv.Atoi(string(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return c, r, true
}

// startSession starts a session's process (tmux window or PTY).
func (m *Manager) startSession(session *Session) error {
	// Try to detect tmux session if not already connected
	// (may trigger recovery which sets session to Running)
	m.ensureTmuxClient()

	// Re-check: recovery in ensureTmuxClient may have found this session alive
	if isProcessRunning(session) {
		return nil
	}

	if m.tmuxClient != nil {
		return m.startSessionTmux(session)
	}
	return m.startSessionPTY(session)
}

// startSessionTmux starts a session in a tmux window.
func (m *Manager) startSessionTmux(session *Session) error {
	// Branch checkout handling (same as PTY mode)
	if session.Branch != "" && session.WorkDir != "" && !session.IsNewWorktree {
		currentBranch, err := worktree.GetCurrentBranch(session.WorkDir)
		if err != nil {
			debugLog("[SESSION] Failed to get current branch: %v", err)
		} else if currentBranch != session.Branch {
			if session.ClaudeSessionStarted {
				debugLog("[SESSION] Branch changed externally: %s -> %s", session.Branch, currentBranch)
				session.Branch = currentBranch
				session.NewBranch = false
				m.store.Save(session)
			} else {
				hasChanges, err := worktree.HasUncommittedChanges(session.WorkDir)
				if err != nil {
					return fmt.Errorf("failed to check uncommitted changes: %w", err)
				}
				if hasChanges {
					return fmt.Errorf("cannot checkout branch %s: uncommitted changes exist in %s", session.Branch, session.WorkDir)
				}
				if session.NewBranch {
					if err := worktree.CreateAndCheckoutBranch(session.WorkDir, session.Branch, session.BaseBranch); err != nil {
						return fmt.Errorf("failed to create and checkout branch: %w", err)
					}
				} else {
					if err := worktree.CheckoutBranch(session.WorkDir, session.Branch); err != nil {
						return fmt.Errorf("failed to checkout branch: %w", err)
					}
				}
			}
		}
	}

	// Set trust state
	if err := ensureClaudeTrustState(session.WorkDir); err != nil {
		debugLog("[TRUST] Warning: failed to set trust state: %v", err)
	}

	// Build Claude command
	shell := m.configMgr.GetShell()
	claudeCmd := "claude"
	if session.ClaudeSessionID != "" {
		if session.ClaudeSessionStarted {
			claudeCmd = fmt.Sprintf("claude --resume %s", session.ClaudeSessionID)
			debugLog("[SESSION] Resuming Claude session: %s", session.ClaudeSessionID)
		} else {
			claudeCmd = fmt.Sprintf("claude --session-id %s", session.ClaudeSessionID)
			debugLog("[SESSION] Starting new Claude session with ID: %s", session.ClaudeSessionID)
			session.ClaudeSessionStarted = true
		}
	}

	// Build shell command with environment setup
	// Unset TMUX/TMUX_PANE to prevent nested tmux detection
	shellCmd := fmt.Sprintf("env -u TMUX -u TMUX_PANE TERM=xterm-256color COLORTERM=truecolor FORCE_COLOR=1 %s -ic '%s'",
		shell, claudeCmd)

	// Try to revive CC in existing window (preserves user panes)
	windowName := tmux.WindowName(session.ID)
	if session.TmuxWindowName != "" {
		existingTarget := tmux.SessionName + ":" + session.TmuxWindowName
		if _, err := m.tmuxClient.PaneCount(existingTarget); err == nil {
			// Window exists → RespawnPane on pane 0 to revive CC in place
			target := tmux.WindowTarget(session.TmuxWindowName, 0)
			if err := m.tmuxClient.RespawnPane(target, shellCmd); err == nil {
				session.Status = StatusRunning
				session.LastOutputTime = time.Now()
				session.StartedAt = time.Now()
				m.store.Save(session)
				debugLog("[TMUX] Session %s CC revived via RespawnPane (layout preserved)", session.Name)
				go m.captureOutputTmux(session)
				return nil
			}
		}
		// Fall through: window gone or respawn failed → create new
		session.TmuxWindowName = ""
	}

	// Kill existing window with the same name if it exists (stale from daemon restart)
	existingWindowTarget := tmux.SessionName + ":" + windowName
	m.tmuxClient.KillWindow(existingWindowTarget) // ignore error (window might not exist)

	if err := m.tmuxClient.NewWindowInDir(tmux.SessionName, windowName, session.WorkDir, shellCmd, true); err != nil {
		return fmt.Errorf("failed to create tmux window: %w", err)
	}

	// Tag CC pane so pane-died hook preserves it (user-added panes auto-close)
	target := tmux.WindowTarget(windowName, 0)
	m.tmuxClient.TagManagedPane(target)

	session.TmuxWindowName = windowName
	session.Status = StatusRunning
	session.LastOutputTime = time.Now()
	session.StartedAt = time.Now()

	// Persist tmux window name
	m.store.Save(session)

	// Start status detection via capture-pane polling
	go m.captureOutputTmux(session)

	return nil
}

// captureOutputTmux polls tmux capture-pane for status detection.
func (m *Manager) captureOutputTmux(session *Session) {
	detector := status.NewDetector()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	target := tmux.WindowTarget(session.TmuxWindowName, 0)
	consecutiveErrors := 0

	for range ticker.C {
		m.mu.RLock()
		if session.Status == StatusStopped {
			m.mu.RUnlock()
			return
		}
		sessionID := session.ID
		sessionName := session.Name
		promptName := session.PromptName
		promptArgs := session.PromptArgs
		promptInjected := session.PromptInjected
		repository := session.Repository
		branch := session.Branch
		baseBranch := session.BaseBranch
		workDir := session.WorkDir
		m.mu.RUnlock()

		// Check if pane process has exited
		if m.tmuxClient.IsPaneDead(target) {
			m.mu.Lock()
			session.Status = StatusStopped
			session.LastActiveAt = time.Now()
			// Keep TmuxWindowName: window survives (remain-on-exit), only CC pane is dead.
			// RespawnPane can revive CC while preserving user panes in the same window.
			m.mu.Unlock()
			m.store.Save(session)
			debugLog("[TMUX] Session %s pane died, marked as stopped (window preserved)", sessionName)
			return
		}

		// Capture pane content for status detection
		content, err := m.tmuxClient.CapturePane(target, false)
		if err != nil {
			consecutiveErrors++
			// After 3 consecutive failures, the tmux window/session is likely gone
			// (e.g., user quit ccvalet and the tmux session was destroyed)
			if consecutiveErrors >= 3 {
				m.mu.Lock()
				session.Status = StatusStopped
				session.LastActiveAt = time.Now()
				session.TmuxWindowName = ""
				m.mu.Unlock()
				m.store.Save(session)
				debugLog("[TMUX] Session %s tmux window gone (capture failed %d times), marked as stopped", sessionName, consecutiveErrors)
				return
			}
			continue
		}
		consecutiveErrors = 0

		detected := detector.Detect(content)
		if detected == "" {
			continue
		}

		// Trust dialog detection
		if detected == status.StatusTrust {
			m.mu.Lock()
			oldStatus := session.Status
			session.Status = StatusConfirm
			m.mu.Unlock()
			if oldStatus != StatusConfirm {
				debugLog("[STATUS] Session %s: %s -> %s (tmux)", sessionName, oldStatus, StatusConfirm)
			}
			continue
		}

		newStatus := convertStatus(detected)

		// Apply status stability logic
		m.mu.Lock()
		oldStatus := session.Status
		timeSinceStart := time.Since(session.StartedAt)

		// Skip error detection during startup grace period
		if newStatus == StatusError && timeSinceStart < 5*time.Second {
			newStatus = StatusRunning
		}

		session.Status = newStatus
		session.LastOutputTime = time.Now()
		m.mu.Unlock()

		// Handle status change notifications
		if oldStatus != newStatus {
			m.handleStatusChange(sessionID, sessionName, oldStatus, newStatus)
		}

		// Inject prompt when idle and not yet injected
		if newStatus == StatusIdle && !promptInjected && promptName != "" {
			m.mu.Lock()
			session.PromptInjected = true
			m.mu.Unlock()

			debugLog("[PROMPT] Triggering tmux prompt injection for session %s", sessionID)
			go m.injectPromptTmux(session, promptName, prompt.Variables{
				Args:       promptArgs,
				Branch:     branch,
				Repository: repository,
				Session:    sessionName,
				WorkDir:    workDir,
				BaseBranch: baseBranch,
			})
		}
	}
}

// injectPromptTmux injects a prompt into the session via tmux send-keys.
func (m *Manager) injectPromptTmux(session *Session, promptName string, vars prompt.Variables) {
	expandedPrompt, err := m.promptMgr.GetExpanded(promptName, vars)
	if err != nil {
		debugLog("[PROMPT] Failed to expand prompt template '%s': %v", promptName, err)
		return
	}

	debugLog("[PROMPT] Expanded prompt (%d chars)", len(expandedPrompt))

	// Wait for Claude Code to be fully ready
	time.Sleep(1 * time.Second)

	m.mu.RLock()
	windowName := session.TmuxWindowName
	m.mu.RUnlock()

	if windowName == "" {
		debugLog("[PROMPT] No tmux window, cannot inject prompt")
		return
	}

	target := tmux.WindowTarget(windowName, 0)

	// Send prompt text via send-keys (literal mode)
	if err := m.tmuxClient.SendKeysLiteral(target, expandedPrompt); err != nil {
		debugLog("[PROMPT] Failed to send prompt via tmux: %v", err)
		return
	}

	time.Sleep(200 * time.Millisecond)

	// Send Enter
	if err := m.tmuxClient.SendKeys(target, "Enter"); err != nil {
		debugLog("[PROMPT] Failed to send Enter via tmux: %v", err)
		return
	}

	debugLog("[PROMPT] Tmux injection complete for '%s'", promptName)
}

// startSessionPTY starts a session's PTY process (legacy mode).
func (m *Manager) startSessionPTY(session *Session) error {
	// Branch checkout handling (v3 design)
	if session.Branch != "" && session.WorkDir != "" && !session.IsNewWorktree {
		// Get current branch
		currentBranch, err := worktree.GetCurrentBranch(session.WorkDir)
		if err != nil {
			debugLog("[SESSION] Failed to get current branch: %v", err)
			// Continue anyway - might not be a git repo
		} else if currentBranch != session.Branch {
			if session.ClaudeSessionStarted {
				// 既に一度起動済み: セッション内でブランチが変更された
				// チェックアウトせず、現在のブランチ情報でセッションを更新
				debugLog("[SESSION] Branch changed externally: %s -> %s (skipping checkout, using current branch)",
					session.Branch, currentBranch)
				session.Branch = currentBranch
				session.NewBranch = false
				m.store.Save(session)
			} else {
				// 初回起動: 従来通りチェックアウトを実行
				hasChanges, err := worktree.HasUncommittedChanges(session.WorkDir)
				if err != nil {
					return fmt.Errorf("failed to check uncommitted changes: %w", err)
				}
				if hasChanges {
					return fmt.Errorf("cannot checkout branch %s: uncommitted changes exist in %s", session.Branch, session.WorkDir)
				}

				if session.NewBranch {
					// Create and checkout a new branch
					debugLog("[SESSION] Creating and checking out new branch %s from %s in %s", session.Branch, session.BaseBranch, session.WorkDir)
					if err := worktree.CreateAndCheckoutBranch(session.WorkDir, session.Branch, session.BaseBranch); err != nil {
						return fmt.Errorf("failed to create and checkout branch: %w", err)
					}
				} else {
					// Checkout existing branch
					debugLog("[SESSION] Checking out branch %s in %s", session.Branch, session.WorkDir)
					if err := worktree.CheckoutBranch(session.WorkDir, session.Branch); err != nil {
						return fmt.Errorf("failed to checkout branch: %w", err)
					}
				}
			}
		}
	}

	// Set trust state in Claude settings to skip trust confirmation dialog
	if err := ensureClaudeTrustState(session.WorkDir); err != nil {
		debugLog("[TRUST] Warning: failed to set trust state: %v", err)
		// Continue anyway - trust confirmation will appear but session can still work
	}

	// Create command: <shell> -ic "claude [--session-id|--resume <uuid>]"
	shell := m.configMgr.GetShell()
	claudeCmd := "claude"

	if session.ClaudeSessionID != "" {
		if session.ClaudeSessionStarted {
			// Resume existing Claude session
			claudeCmd = fmt.Sprintf("claude --resume %s", session.ClaudeSessionID)
			debugLog("[SESSION] Resuming Claude session: %s", session.ClaudeSessionID)
		} else {
			// First start: create new session with specific ID
			claudeCmd = fmt.Sprintf("claude --session-id %s", session.ClaudeSessionID)
			debugLog("[SESSION] Starting new Claude session with ID: %s", session.ClaudeSessionID)
			session.ClaudeSessionStarted = true
		}
	}

	c := exec.Command(shell, "-ic", claudeCmd)
	c.Dir = session.WorkDir
	c.Env = m.buildEnv()

	// Get terminal size
	cols, rows := 80, 24
	if term.IsTerminal(int(os.Stdin.Fd())) {
		w, h, err := term.GetSize(int(os.Stdin.Fd()))
		if err == nil {
			cols, rows = w, h
		}
	}

	// Start with PTY
	ptmx, err := pty.StartWithSize(c, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		return err
	}

	session.PTY = ptmx
	session.Cmd = c
	session.Status = StatusRunning
	session.LastOutputTime = time.Now()
	session.StartedAt = time.Now()

	// Initialize screen buffer (256KB) and broadcaster
	session.ScreenBuffer = NewScreenBuffer(256 * 1024)
	session.Broadcaster = NewBroadcaster()

	// Start goroutine to capture PTY output
	go m.captureOutput(session)

	// Start goroutine for idle stability detection
	go m.checkIdleStability(session)

	return nil
}

// captureOutput reads from PTY and broadcasts to listeners and buffer
func (m *Manager) captureOutput(session *Session) {
	buf := make([]byte, 4096)
	detector := status.NewDetector()

	for {
		n, err := session.PTY.Read(buf)
		if err != nil {
			// Session ended - clean up resources
			m.mu.Lock()
			session.Status = StatusStopped
			// Update LastActiveAt for persistence
			if !session.LastOutputTime.IsZero() {
				session.LastActiveAt = session.LastOutputTime
			} else {
				session.LastActiveAt = time.Now()
			}
			if session.PTY != nil {
				session.PTY.Close()
				session.PTY = nil
			}
			session.Cmd = nil
			session.ScreenBuffer = nil
			// Close broadcaster to notify all listeners
			if session.Broadcaster != nil {
				session.Broadcaster.Close()
				session.Broadcaster = nil
			}
			sessionToSave := session
			m.mu.Unlock()
			// Persist LastActiveAt
			m.store.Save(sessionToSave)
			return
		}
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// 前回の出力からの経過時間を取得（idle安定性検出用）
			m.mu.Lock()
			timeSinceLastOutput := time.Since(session.LastOutputTime)
			// 出力を受信した時刻を更新
			session.LastOutputTime = time.Now()
			m.mu.Unlock()

			// Write to screen buffer first
			if session.ScreenBuffer != nil {
				session.ScreenBuffer.Write(data)
			}

			// Detect status from recent buffer content (last 4KB)
			// detector.go filters and uses only last 30 content lines
			if session.ScreenBuffer != nil {
				recentContent := session.ScreenBuffer.LastN(4096)
				if detected := detector.Detect(string(recentContent)); detected != "" {
					// Trust確認を検出した場合、ステータスをconfirmに更新
					if detected == status.StatusTrust {
						m.mu.Lock()
						oldStatus := session.Status
						session.Status = StatusConfirm
						if !session.TrustHandled {
							session.TrustHandled = true
							sessionName := session.Name
							m.mu.Unlock()
							debugLog("[TRUST] Trust dialog detected for session %s - manual intervention required", sessionName)
							// TODO: デスクトップ通知を送信
						} else {
							m.mu.Unlock()
						}
						// ステータス変更をログ出力
						if oldStatus != StatusConfirm {
							debugLog("[STATUS] Session %s: %s -> %s", session.Name, oldStatus, StatusConfirm)
						}
						// Broadcast to all listeners (Trust画面もクライアントに送信)
						if session.Broadcaster != nil {
							session.Broadcaster.Broadcast(data)
						}
						continue
					}

					newStatus := convertStatus(detected)

					// 出力安定性ベースのステータス検出
					// permission: 即時反映
					// thinking: idleからの遷移は出力安定後のみ（リサイズ時の誤検出防止）
					// idle: 出力が一定時間安定している場合のみ反映
					// error: 起動直後は無視
					const stabilityTime = 1 * time.Second
					const startupGracePeriod = 5 * time.Second // 起動直後のエラー誤検出防止
					m.mu.Lock()
					oldStatus := session.Status
					timeSinceStart := time.Since(session.StartedAt)

					// 起動直後はエラー検出を無視（誤検出防止）
					if newStatus == StatusError && timeSinceStart < startupGracePeriod {
						// 起動直後のエラーは無視 - runningを維持
						newStatus = StatusRunning
					}

					// thinking検出時はリサイズ直後かチェック（idleからの遷移時のみ）
					// リサイズ時の一時的な再描画でthinkingと誤検出されることを防止
					const resizeGracePeriod = 500 * time.Millisecond
					if newStatus == StatusThinking && oldStatus == StatusIdle {
						timeSinceResize := time.Since(session.LastResizeTime)
						if timeSinceResize < resizeGracePeriod {
							// リサイズ直後 - idleを維持
							newStatus = StatusIdle
						}
					}

					// idle検出時は出力安定性をチェック
					if newStatus == StatusIdle {
						if timeSinceLastOutput < stabilityTime {
							// まだ出力が安定していない - 現在のステータスを維持
							// ただし、初回起動時（ステータスがrunning）の場合はidleに遷移可能
							if oldStatus != StatusRunning {
								newStatus = oldStatus
							}
						}
					}

					session.Status = newStatus
					sessionName := session.Name
					sessionID := session.ID
					promptName := session.PromptName
					promptArgs := session.PromptArgs
					promptInjected := session.PromptInjected
					ptyFile := session.PTY
					repository := session.Repository
					branch := session.Branch
					baseBranch := session.BaseBranch
					workDir := session.WorkDir
					m.mu.Unlock()

					// Send notifications on status change
					if oldStatus != newStatus {
						m.handleStatusChange(sessionID, sessionName, oldStatus, newStatus)
					}

					// Inject prompt when idle and not yet injected
					if newStatus == StatusIdle && !promptInjected && promptName != "" && ptyFile != nil {
						// Mark as injected immediately to prevent duplicate
						m.mu.Lock()
						session.PromptInjected = true
						m.mu.Unlock()

						debugLog("[PROMPT] Triggering prompt injection for session %s, prompt=%s", sessionID, promptName)

						// Run injection in separate goroutine to avoid blocking capture loop
						go m.injectPrompt(session, promptName, prompt.Variables{
							Args:       promptArgs,
							Branch:     branch,
							Repository: repository,
							Session:    sessionName,
							WorkDir:    workDir,
							BaseBranch: baseBranch,
						})
					}
				}
			}

			// Broadcast to all listeners
			if session.Broadcaster != nil {
				session.Broadcaster.Broadcast(data)
			}
		}
	}
}

// convertStatus converts status.DetectedStatus to session.Status
func convertStatus(detected status.DetectedStatus) Status {
	switch detected {
	case status.StatusPermission:
		return StatusPermission
	case status.StatusThinking:
		return StatusThinking
	case status.StatusIdle:
		return StatusIdle
	case status.StatusError:
		return StatusError
	default:
		return StatusRunning
	}
}

// checkIdleStability periodically checks if the session should transition to idle
// This handles the case where PTY output stops completely (true idle state)
func (m *Manager) checkIdleStability(session *Session) {
	detector := status.NewDetector()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	const idleStabilityTime = 3 * time.Second

	for range ticker.C {
		m.mu.Lock()
		// セッションが停止していたら終了
		if session.Status == StatusStopped || session.PTY == nil {
			m.mu.Unlock()
			return
		}

		// 既にidleなら何もしない
		if session.Status == StatusIdle {
			m.mu.Unlock()
			continue
		}

		// 出力が安定しているかチェック
		timeSinceLastOutput := time.Since(session.LastOutputTime)
		if timeSinceLastOutput < idleStabilityTime {
			m.mu.Unlock()
			continue
		}

		// バッファからidleパターンをチェック
		screenBuffer := session.ScreenBuffer
		if screenBuffer == nil {
			m.mu.Unlock()
			continue
		}
		recentContent := screenBuffer.LastN(4096)
		m.mu.Unlock()

		detected := detector.Detect(string(recentContent))
		// 出力が安定していて、thinking/permission/error/trustが検出されない → idleに遷移
		shouldTransitionToIdle := detected == status.StatusIdle || detected == ""
		if shouldTransitionToIdle {
			m.mu.Lock()
			oldStatus := session.Status
			session.Status = StatusIdle
			sessionID := session.ID
			sessionName := session.Name
			promptName := session.PromptName
			promptArgs := session.PromptArgs
			promptInjected := session.PromptInjected
			ptyFile := session.PTY
			repository := session.Repository
			branch := session.Branch
			baseBranch := session.BaseBranch
			workDir := session.WorkDir
			m.mu.Unlock()

			// 通知を送信
			if oldStatus != StatusIdle {
				m.handleStatusChange(sessionID, sessionName, oldStatus, StatusIdle)
			}

			// プロンプト注入（未注入の場合）
			if !promptInjected && promptName != "" && ptyFile != nil {
				m.mu.Lock()
				session.PromptInjected = true
				m.mu.Unlock()

				debugLog("[PROMPT] Triggering prompt injection (stability check) for session %s, prompt=%s", sessionID, promptName)

				go m.injectPrompt(session, promptName, prompt.Variables{
					Args:       promptArgs,
					Branch:     branch,
					Repository: repository,
					Session:    sessionName,
					WorkDir:    workDir,
					BaseBranch: baseBranch,
				})
			}
		}
	}
}

// handleStatusChange sends notifications based on status transitions
func (m *Manager) handleStatusChange(sessionID, sessionName string, oldStatus, newStatus Status) {
	// Notify on permission request (from any state)
	if newStatus == StatusPermission {
		m.notifier.NotifyPermission(sessionID, sessionName)
		return
	}

	// Notify on task completion (thinking -> idle)
	if oldStatus == StatusThinking && newStatus == StatusIdle {
		m.notifier.NotifyTaskComplete(sessionID, sessionName)
		return
	}

	// Notify on error
	if newStatus == StatusError {
		m.notifier.NotifyError(sessionID, sessionName)
		return
	}
}

// injectPrompt injects a prompt template into the session's PTY
// This should be called in a separate goroutine to avoid blocking the capture loop
func (m *Manager) injectPrompt(session *Session, promptName string, vars prompt.Variables) {
	debugLog("[PROMPT] Starting injection, promptName=%s, args=%s", promptName, vars.Args)

	// Get expanded prompt
	expandedPrompt, err := m.promptMgr.GetExpanded(promptName, vars)
	if err != nil {
		debugLog("[PROMPT] Failed to expand prompt template '%s': %v", promptName, err)
		return
	}

	debugLog("[PROMPT] Expanded prompt (%d chars): %s", len(expandedPrompt), expandedPrompt[:min(100, len(expandedPrompt))])

	// Wait for Claude Code to be fully ready for input
	// Initial idle detection can happen before the UI is fully rendered
	time.Sleep(1 * time.Second)

	// Get PTY file with lock
	m.mu.RLock()
	ptyFile := session.PTY
	m.mu.RUnlock()

	if ptyFile == nil {
		debugLog("[PROMPT] PTY is nil, cannot inject prompt")
		return
	}

	debugLog("[PROMPT] Writing prompt to PTY...")

	// Write the prompt to PTY character by character with small delays
	// This simulates typing and ensures Claude Code receives the input properly
	for _, r := range expandedPrompt {
		_, err = ptyFile.Write([]byte(string(r)))
		if err != nil {
			debugLog("[PROMPT] Failed to write prompt to PTY: %v", err)
			return
		}
		time.Sleep(5 * time.Millisecond) // Small delay between characters
	}

	// Wait a bit before sending Enter
	time.Sleep(200 * time.Millisecond)

	// Send Enter to submit the prompt
	debugLog("[PROMPT] Sending Enter...")
	ptyFile.Write([]byte("\r"))

	debugLog("[PROMPT] Injection complete for '%s'", promptName)
}

// buildEnv builds environment variables for the session
func (m *Manager) buildEnv() []string {
	env := os.Environ()
	envMap := make(map[string]bool)
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				envMap[e[:i]] = true
				break
			}
		}
	}

	// Ensure TERM is set
	if !envMap["TERM"] {
		env = append(env, "TERM=xterm-256color")
	}
	if !envMap["COLORTERM"] {
		env = append(env, "COLORTERM=truecolor")
	}
	if !envMap["FORCE_COLOR"] {
		env = append(env, "FORCE_COLOR=1")
	}

	// Remove problematic variables
	removeVars := map[string]bool{
		"TMUX": true, "TMUX_PANE": true, "STY": true,
		"WINDOW": true, "WINDOWID": true, "TERMCAP": true,
		"COLUMNS": true, "LINES": true, "CI": true,
	}

	result := make([]string, 0, len(env))
	for _, e := range env {
		varName := ""
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				varName = e[:i]
				break
			}
		}
		if !removeVars[varName] {
			result = append(result, e)
		}
	}

	return result
}

// runInteractive runs an interactive session
func (m *Manager) runInteractive(session *Session) error {
	// Handle PTY size
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, session.PTY); err != nil {
				debugLog("error resizing pty: %s", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH
	defer func() { signal.Stop(ch); close(ch) }()

	// Set stdin in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Copy stdin to PTY
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				session.PTY.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// Copy PTY to stdout
	_, _ = io.Copy(os.Stdout, session.PTY)

	// Session ended
	m.mu.Lock()
	session.Status = StatusStopped
	session.PTY = nil
	session.Cmd = nil
	m.mu.Unlock()

	return nil
}

// Kill terminates a session
func (m *Manager) Kill(id string) error {
	m.mu.Lock()

	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}

	// Kill tmux window if exists
	if m.tmuxClient != nil && session.TmuxWindowName != "" {
		windowTarget := tmux.SessionName + ":" + session.TmuxWindowName
		m.tmuxClient.KillWindow(windowTarget)
		session.TmuxWindowName = ""
	}

	if session.Cmd != nil && session.Cmd.Process != nil {
		session.Cmd.Process.Kill()
	}
	if session.PTY != nil {
		session.PTY.Close()
	}

	session.Status = StatusStopped
	// Update LastActiveAt for persistence
	if !session.LastOutputTime.IsZero() {
		session.LastActiveAt = session.LastOutputTime
	} else {
		session.LastActiveAt = time.Now()
	}
	session.PTY = nil
	session.Cmd = nil

	m.mu.Unlock()
	// Persist LastActiveAt
	m.store.Save(session)

	return nil
}

// Delete removes a session completely
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}

	// Kill tmux window if exists
	if m.tmuxClient != nil && session.TmuxWindowName != "" {
		windowTarget := tmux.SessionName + ":" + session.TmuxWindowName
		m.tmuxClient.KillWindow(windowTarget)
	}

	// Kill if running
	if session.Cmd != nil && session.Cmd.Process != nil {
		session.Cmd.Process.Kill()
	}
	if session.PTY != nil {
		session.PTY.Close()
	}

	// Remove from store
	if err := m.store.Delete(id); err != nil {
		return err
	}

	delete(m.sessions, id)
	return nil
}

// WriteToSession writes data to a session's PTY (e.g., user prompt input).
func (m *Manager) WriteToSession(id string, data []byte) error {
	m.mu.RLock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("session %s not found", id)
	}

	if !isProcessRunning(session) {
		m.mu.RUnlock()
		return fmt.Errorf("session %s is not running", id)
	}

	ptyFile := session.PTY
	m.mu.RUnlock()

	if ptyFile == nil {
		return fmt.Errorf("session %s has no PTY", id)
	}

	_, err := ptyFile.Write(data)
	return err
}

// ResizePTY resizes a session's PTY to the given dimensions.
func (m *Manager) ResizePTY(id string, cols, rows int) error {
	m.mu.RLock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("session %s not found", id)
	}

	if !isProcessRunning(session) {
		m.mu.RUnlock()
		return fmt.Errorf("session %s is not running", id)
	}

	ptyFile := session.PTY
	m.mu.RUnlock()

	if ptyFile == nil {
		return fmt.Errorf("session %s has no PTY", id)
	}

	return pty.Setsize(ptyFile, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

// SubscribeOutput subscribes to a session's PTY output (read-only, no input).
// It sends the current screen buffer contents, then streams new output via the writer.
// The done channel is closed when the caller should stop (e.g., session ended).
// Returns the broadcaster channel and a cleanup function.
func (m *Manager) SubscribeOutput(id string) (screenData []byte, outputCh chan []byte, cleanup func(), err error) {
	m.mu.RLock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		return nil, nil, nil, fmt.Errorf("session %s not found", id)
	}

	broadcaster := session.Broadcaster
	screenBuffer := session.ScreenBuffer
	m.mu.RUnlock()

	// Get current screen buffer content
	if screenBuffer != nil {
		screenData = screenBuffer.Bytes()
	}

	// Subscribe to broadcaster for new output
	if broadcaster != nil {
		outputCh = broadcaster.Subscribe()
		cleanup = func() {
			broadcaster.Unsubscribe(outputCh)
		}
	} else {
		// No broadcaster (session not running) - return nil channel
		outputCh = nil
		cleanup = func() {}
	}

	return screenData, outputCh, cleanup, nil
}

// ClaudeSettings represents the structure of ~/.claude/settings.local.json
type ClaudeSettings struct {
	Projects map[string]ClaudeProjectSettings `json:"projects,omitempty"`
}

// ClaudeProjectSettings represents project-specific settings in Claude
type ClaudeProjectSettings struct {
	HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted,omitempty"`
}

// ensureClaudeTrustState sets hasTrustDialogAccepted=true in ~/.claude/settings.local.json
// Claude Code checks this setting to skip the trust confirmation dialog
func ensureClaudeTrustState(workDir string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// Get absolute path of workDir
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.local.json")

	// Ensure .claude directory exists
	claudeDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// Read existing settings or create new
	var settings ClaudeSettings
	data, err := os.ReadFile(settingsPath)
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			// If parsing fails, start fresh but preserve the raw JSON
			settings = ClaudeSettings{}
		}
	}

	// Initialize projects map if nil
	if settings.Projects == nil {
		settings.Projects = make(map[string]ClaudeProjectSettings)
	}

	// Check if already trusted
	if projectSettings, exists := settings.Projects[absWorkDir]; exists && projectSettings.HasTrustDialogAccepted {
		return nil // Already trusted
	}

	// Set trust state for this project
	settings.Projects[absWorkDir] = ClaudeProjectSettings{
		HasTrustDialogAccepted: true,
	}

	// Write back to file
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	debugLog("[TRUST] Set hasTrustDialogAccepted=true for %s", absWorkDir)
	return nil
}
