package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/notify"
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
	sessions   map[string]*Session
	store      *Store
	notifier   *notify.Notifier
	configMgr  *config.Manager
	tmuxClient *tmux.Client // tmux client for session management
	mu         sync.RWMutex
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
			// Fix stale sessions: active status but no tmux session (from prior recovery bug)
			if session.Status != StatusStopped && session.Status != StatusCreating {
				session.Status = StatusStopped
				m.store.Save(session)
				debugLog("[RECOVER] Session %s has active status but no tmux session, marked stopped", session.Name)
			}
			continue
		}

		// Check if the inner tmux session still exists
		if !m.tmuxClient.HasSession(session.TmuxWindowName) {
			session.TmuxWindowName = ""
			session.Status = StatusStopped
			m.store.Save(session)
			debugLog("[RECOVER] Session %s inner tmux session gone, marked stopped", session.Name)
			continue
		}

		// Use pane ID if available, otherwise get from tmux (avoids base-index issues)
		target := session.TmuxPaneID
		if target == "" {
			if id, err := m.tmuxClient.GetPaneID(session.TmuxWindowName); err == nil {
				target = id
			} else {
				target = session.TmuxWindowName + ":0.0"
			}
		}

		// Check if pane is dead — keep TmuxWindowName (session alive via remain-on-exit)
		if m.tmuxClient.IsPaneDead(target) {
			session.Status = StatusStopped
			m.store.Save(session)
			debugLog("[RECOVER] Session %s tmux pane dead, kept TmuxWindowName (session preserved)", session.Name)
			continue
		}

		// Session exists and pane is alive - resume monitoring
		session.Status = StatusRunning
		session.LastOutputTime = time.Now()
		m.store.Save(session)
		debugLog("[RECOVER] Session %s has live inner tmux session, resuming monitoring", session.Name)

		go m.captureOutputTmux(session)
	}
}

// ensureTmuxClient lazily initializes the inner tmux client (-L ccvalet).
// Each CC session creates its own tmux session, so no shared session is needed.
func (m *Manager) ensureTmuxClient() {
	if m.tmuxClient != nil {
		return
	}
	tc, err := tmux.NewClient() // Uses SocketName = "ccvalet"
	if err != nil {
		return
	}
	m.tmuxClient = tc
	debugLog("[TMUX] Inner tmux client initialized (socket: %s)", tmux.SocketName)
	// Don't call configureInnerTmux here — the inner tmux server may not exist yet.
	// Configuration is applied in startSessionTmux after the first session is created.
	m.recoverTmuxSessionsLocked()
}

// configureInnerTmux applies ccvalet-specific settings to the inner tmux server.
// User's ~/.tmux.conf is automatically loaded by tmux on server startup.
// Only ccvalet-specific settings (remain-on-exit, pane-died hook) are added here.
// Must only be called after the inner tmux server is confirmed to exist (i.e., after
// a session has been created).
// This is called every time a session is started (not just once) because the inner
// tmux server may have exited and restarted between sessions. The overhead is minimal.
func (m *Manager) configureInnerTmux() {
	if m.tmuxClient == nil {
		return
	}
	if err := m.tmuxClient.SetOption("remain-on-exit", "on", true); err != nil {
		debugLog("[TMUX] Inner tmux server not ready: %v", err)
		return
	}
	m.tmuxClient.SetupAutoCleanDeadPanes()
	debugLog("[TMUX] Inner tmux server configured (remain-on-exit, pane-died hook)")
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
		configMgr: configMgr,
	}

	// Load existing sessions
	sessions, err := store.LoadAll()
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		s.Status = StatusStopped // All loaded sessions start as stopped
		m.sessions[s.ID] = s
	}

	return m, nil
}

// CreateOptions contains options for creating a new session
type CreateOptions struct {
	WorkDir string // ワークディレクトリパス
	Name    string // セッション名（省略時: ディレクトリのbasename）
}

// CreateWithOptions creates a new session with full options
func (m *Manager) CreateWithOptions(opts CreateOptions) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// WorkDirは必須
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("work directory is required")
	}

	// 重複ディレクトリチェック
	for _, s := range m.sessions {
		if s.WorkDir == opts.WorkDir {
			return nil, fmt.Errorf("session already exists for directory: %s (session: %s)", opts.WorkDir, s.Name)
		}
	}

	id := uuid.New().String() // Full UUID for Claude Code --session-id compatibility

	// セッション名の決定（デフォルト: ディレクトリのbasename）
	name := opts.Name
	if name == "" {
		name = filepath.Base(opts.WorkDir)
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
		m.store.Save(session)
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

// CountActive returns the number of active sessions (creating, running, thinking, permission)
// Excludes: stopped, idle, error
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
	shellCmd := fmt.Sprintf("env -u TMUX -u TMUX_PANE -u CLAUDECODE TERM=xterm-256color COLORTERM=truecolor FORCE_COLOR=1 %s -ic '%s'",
		shell, claudeCmd)

	innerSessionName := tmux.InnerSessionName(session.ID)

	// Try to revive CC in existing inner tmux session (preserves user panes)
	if session.TmuxWindowName != "" && m.tmuxClient.HasSession(session.TmuxWindowName) {
		target := session.TmuxPaneID
		if target == "" {
			// Fallback: get pane ID from session (avoids base-index issues with ":0.0")
			if id, err := m.tmuxClient.GetPaneID(session.TmuxWindowName); err == nil {
				target = id
			} else {
				target = session.TmuxWindowName + ":0.0"
			}
		}
		if err := m.tmuxClient.RespawnPane(target, shellCmd); err == nil {
			session.Status = StatusRunning
			session.LastOutputTime = time.Now()
			session.StartedAt = time.Now()
			m.store.Save(session)
			debugLog("[TMUX] Session %s CC revived via RespawnPane in inner session", session.Name)
			go m.captureOutputTmux(session)
			return nil
		}
		// Fall through: session gone or respawn failed → create new
		session.TmuxWindowName = ""
	}

	// Kill existing inner session with the same name if it exists (stale from daemon restart)
	m.tmuxClient.KillSession(innerSessionName) // ignore error (session might not exist)

	// Create a new inner tmux session (-L ccvalet) for this CC session
	if err := m.tmuxClient.NewSessionWithCmdInDir(innerSessionName, 200, 50, session.WorkDir, shellCmd); err != nil {
		return fmt.Errorf("failed to create inner tmux session: %w", err)
	}

	// Get the pane's unique ID (%N) — reliable regardless of base-index/pane-base-index.
	// User's ~/.tmux.conf may set base-index=1, making ":0.0" targets invalid.
	paneID, err := m.tmuxClient.GetPaneID(innerSessionName)
	if err != nil {
		debugLog("[TMUX] GetPaneID failed for %s: %v", innerSessionName, err)
		paneID = ""
	}

	// Tag CC pane FIRST — must happen before pane-died hook is active,
	// otherwise a quick process exit triggers kill-pane on the untagged pane.
	if paneID != "" {
		m.tmuxClient.TagManagedPane(paneID)
	}

	// Then apply server config (remain-on-exit + pane-died hook)
	m.configureInnerTmux()

	session.TmuxPaneID = paneID

	session.TmuxWindowName = innerSessionName // Reuse field for inner session name
	session.Status = StatusRunning
	session.LastOutputTime = time.Now()
	session.StartedAt = time.Now()

	// Persist inner session name
	m.store.Save(session)

	// Start status detection via capture-pane polling
	go m.captureOutputTmux(session)

	return nil
}

// captureOutputTmux polls tmux for process death detection and CWD/branch tracking.
// Status detection is handled by Claude Code hooks (see HandleHookEvent).
func (m *Manager) captureOutputTmux(session *Session) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Use pane ID (%N) if available; falls back to window.pane index.
	// Pane IDs are stable even when join-pane reorders pane indices.
	target := session.TmuxPaneID
	if target == "" {
		target = tmux.WindowTarget(session.TmuxWindowName, 0)
	}
	lastTrackedPath := ""

	for range ticker.C {
		m.mu.RLock()
		_, exists := m.sessions[session.ID]
		if !exists || session.Status == StatusStopped {
			m.mu.RUnlock()
			return
		}
		sessionName := session.Name
		m.mu.RUnlock()

		// Check if pane process has exited
		if m.tmuxClient.IsPaneDead(target) {
			m.mu.Lock()
			// セッションが削除済みの場合はsaveせず終了
			if _, exists := m.sessions[session.ID]; !exists {
				m.mu.Unlock()
				debugLog("[TMUX] Session %s pane died but session already deleted, skipping save", sessionName)
				return
			}

			// claude --resume が即座に失敗した場合（起動後10秒以内）、
			// 新しいセッションIDでプレーンな claude を自動リスタートする
			if session.ClaudeSessionStarted && time.Since(session.StartedAt) < 10*time.Second {
				debugLog("[TMUX] Session %s pane died quickly (resume likely failed), retrying with fresh claude", session.Name)
				newSessionID := uuid.New().String()
				session.ClaudeSessionStarted = false
				session.ClaudeSessionID = newSessionID
				m.mu.Unlock()
				m.store.Save(session)

				shell := m.configMgr.GetShell()
				shellCmd := fmt.Sprintf("env -u TMUX -u TMUX_PANE -u CLAUDECODE TERM=xterm-256color COLORTERM=truecolor FORCE_COLOR=1 %s -ic 'claude --session-id %s'",
					shell, newSessionID)
				if err := m.tmuxClient.RespawnPane(target, shellCmd); err == nil {
					m.mu.Lock()
					session.Status = StatusRunning
					session.ClaudeSessionStarted = true
					session.StartedAt = time.Now()
					session.LastOutputTime = time.Now()
					m.mu.Unlock()
					m.store.Save(session)
					debugLog("[TMUX] Session %s restarted with fresh claude (session-id: %s)", session.Name, newSessionID)
					continue
				}
				debugLog("[TMUX] Session %s respawn failed after quick death", session.Name)
				m.mu.Lock()
				if _, exists := m.sessions[session.ID]; !exists {
					m.mu.Unlock()
					return
				}
			}

			session.Status = StatusStopped
			session.LastActiveAt = time.Now()
			// Keep TmuxWindowName: window survives (remain-on-exit), only CC pane is dead.
			// RespawnPane can revive CC while preserving user panes in the same window.
			m.mu.Unlock()
			m.store.Save(session)
			debugLog("[TMUX] Session %s pane died, marked as stopped (window preserved)", sessionName)
			return
		}

		// Track current working directory and git branch
		if currentPath, err := m.tmuxClient.GetPaneCurrentPath(target); err == nil {
			currentPath = strings.TrimSpace(currentPath)
			if currentPath != "" {
				m.mu.Lock()
				session.CurrentWorkDir = currentPath
				m.mu.Unlock()

				// Always check git branch (git rev-parse is lightweight, <5ms)
				// Branch can change without CWD changing (e.g. git checkout)
				if branch, err := worktree.GetCurrentBranch(currentPath); err == nil {
					m.mu.Lock()
					session.CurrentBranch = branch
					session.IsGitRepo = true
					m.mu.Unlock()
				} else if currentPath != lastTrackedPath {
					// Only clear git info when entering a non-git directory
					m.mu.Lock()
					session.CurrentBranch = ""
					session.IsGitRepo = false
					m.mu.Unlock()
				}
				lastTrackedPath = currentPath
			}
		}
	}
}

// FindByClaudeSessionID finds a session by its Claude Code session ID
func (m *Manager) FindByClaudeSessionID(ccSessionID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.ClaudeSessionID == ccSessionID {
			return s, true
		}
	}
	return nil, false
}

// HandleHookEvent processes a Claude Code hook event and updates session status
func (m *Manager) HandleHookEvent(ccSessionID, eventName, notificationType string) {
	session, ok := m.FindByClaudeSessionID(ccSessionID)
	if !ok {
		debugLog("[HOOK] Unknown CC session ID: %s", ccSessionID)
		return
	}

	m.mu.Lock()
	oldStatus := session.Status
	sessionID := session.ID
	sessionName := session.Name

	switch eventName {
	case "UserPromptSubmit":
		session.Status = StatusThinking
		session.LastOutputTime = time.Now()
	case "Stop":
		session.Status = StatusIdle
		session.LastOutputTime = time.Now()
	case "Notification":
		switch notificationType {
		case "permission_prompt", "elicitation_dialog":
			session.Status = StatusPermission
			session.LastOutputTime = time.Now()
		case "idle_prompt":
			session.Status = StatusIdle
			session.LastOutputTime = time.Now()
		default:
			m.mu.Unlock()
			return
		}
	default:
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	// Send notifications based on hook event
	if oldStatus != session.Status {
		debugLog("[HOOK] Session %s: %s -> %s (hook: %s)", sessionName, oldStatus, session.Status, eventName)
	}

	switch eventName {
	case "Stop":
		m.notifier.NotifyTaskComplete(sessionID, sessionName)
	case "Notification":
		if notificationType == "permission_prompt" || notificationType == "elicitation_dialog" {
			m.notifier.NotifyPermission(sessionID, sessionName)
		}
	}
}

// NotificationHistory returns the notification history
func (m *Manager) NotificationHistory() []notify.Entry {
	return m.notifier.NotificationHistory()
}

// Kill terminates a session
func (m *Manager) Kill(id string) error {
	m.mu.Lock()

	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}

	// Kill CC pane in the inner tmux session
	if m.tmuxClient != nil && session.TmuxPaneID != "" {
		m.tmuxClient.KillPane(session.TmuxPaneID)
		session.TmuxPaneID = ""
		session.TmuxWindowName = ""
	} else if m.tmuxClient != nil && session.TmuxWindowName != "" {
		// Fallback: no pane ID, kill the inner tmux session
		m.tmuxClient.KillSession(session.TmuxWindowName)
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

	// Kill the inner tmux session entirely
	if m.tmuxClient != nil && session.TmuxWindowName != "" {
		m.tmuxClient.KillSession(session.TmuxWindowName)
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
