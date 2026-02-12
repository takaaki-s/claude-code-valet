package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/notify"
	"github.com/takaaki-s/claude-code-valet/internal/prompt"
	"github.com/takaaki-s/claude-code-valet/internal/status"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/transcript"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
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
	tmuxClient  *tmux.Client // tmux client for session management
	mu          sync.RWMutex
}

// SetTmuxClient sets the tmux client for tmux-based session management.
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
	return s.TmuxWindowName != ""
}

// startSession starts a session's process in a tmux window.
func (m *Manager) startSession(session *Session) error {
	// Try to detect tmux session if not already connected
	// (may trigger recovery which sets session to Running)
	m.ensureTmuxClient()

	// Re-check: recovery in ensureTmuxClient may have found this session alive
	if isProcessRunning(session) {
		return nil
	}

	return m.startSessionTmux(session)
}

// startSessionTmux starts a session in a tmux window.
func (m *Manager) startSessionTmux(session *Session) error {
	// Branch checkout handling
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

	session.Status = StatusStopped
	// Update LastActiveAt for persistence
	if !session.LastOutputTime.IsZero() {
		session.LastActiveAt = session.LastOutputTime
	} else {
		session.LastActiveAt = time.Now()
	}

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

	// Remove from store
	if err := m.store.Delete(id); err != nil {
		return err
	}

	delete(m.sessions, id)
	return nil
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
