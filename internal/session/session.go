package session

import (
	"time"
)

// Status represents the session status
type Status string

const (
	StatusCreating   Status = "creating"   // CC起動中
	StatusStopped    Status = "stopped"    // プロセス停止
	StatusRunning    Status = "running"    // 実行中（詳細不明）
	StatusIdle       Status = "idle"       // 入力待ち（Stop hook）
	StatusThinking   Status = "thinking"   // 処理中（UserPromptSubmit hook）
	StatusPermission Status = "permission" // 許可待ち（Notification hook）
)

// Session represents a Claude Code session
type Session struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	WorkDir   string    `json:"work_dir"`
	CreatedAt time.Time `json:"created_at"`
	Status    Status    `json:"status"`

	// 最終アクティブ時刻（永続化）
	LastActiveAt time.Time `json:"last_active_at,omitempty"`

	// エラー情報
	ErrorMessage string `json:"error_message,omitempty"` // エラー時のメッセージ

	// Claude Code セッションID（復元用）
	ClaudeSessionID      string `json:"claude_session_id,omitempty"`
	ClaudeSessionStarted bool   `json:"claude_session_started,omitempty"` // CCセッションが一度でも起動されたか

	// ホスト情報（マルチホスト対応）
	HostID string `json:"host_id,omitempty"` // ホスト識別子 ("local", "ec2", "docker-dev" 等)

	// tmux integration
	TmuxWindowName string `json:"tmux_window_name,omitempty"` // tmux window name for this session
	TmuxPaneID     string `json:"tmux_pane_id,omitempty"`     // CC pane ID (e.g., "%42") for capture-pane

	// Runtime fields (not persisted)
	LastOutputTime time.Time `json:"-"` // 最後にPTY出力を受信した時刻（idle安定性検出用）
	StartedAt      time.Time `json:"-"` // プロセス起動時刻（起動直後のエラー誤検出防止用）
	SSHAuthSock    string    `json:"-"` // SSH_AUTH_SOCK（git操作用、永続化しない）

	// Tracked runtime fields (not persisted, updated by daemon polling)
	CurrentWorkDir string `json:"-"` // 現在のワークディレクトリ（tmux pane_current_path）
	CurrentBranch  string `json:"-"` // 現在のgitブランチ
	IsGitRepo      bool   `json:"-"` // CurrentWorkDirがgitリポジトリ内か
}

// Info returns session information for display
type Info struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	WorkDir         string    `json:"work_dir"`
	Status          Status    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	LastActiveAt    time.Time `json:"last_active_at,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"` // Claude Code session ID for transcript lookup
	TmuxWindowName  string    `json:"tmux_window_name,omitempty"` // tmux window name
	HostID          string    `json:"host_id,omitempty"`          // ホスト識別子

	// Tracked fields (dynamic, from daemon polling)
	CurrentWorkDir string `json:"current_work_dir,omitempty"` // 現在のワークディレクトリ
	CurrentBranch  string `json:"current_branch,omitempty"`   // 現在のgitブランチ

	// Last messages from transcript
	LastUserMessage      string `json:"last_user_message,omitempty"`      // Last user message content (truncated)
	LastAssistantMessage string `json:"last_assistant_message,omitempty"` // Last assistant message content (truncated)
}

// ToInfo converts Session to Info
func (s *Session) ToInfo() Info {
	return Info{
		ID:              s.ID,
		Name:            s.Name,
		WorkDir:         s.WorkDir,
		Status:          s.Status,
		CreatedAt:       s.CreatedAt,
		LastActiveAt:    s.LastActiveAt,
		ErrorMessage:    s.ErrorMessage,
		ClaudeSessionID: s.ClaudeSessionID,
		TmuxWindowName:  s.TmuxWindowName,
		HostID:          s.HostID,
		CurrentWorkDir:  s.CurrentWorkDir,
		CurrentBranch:   s.CurrentBranch,
	}
}
