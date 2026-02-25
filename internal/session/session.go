package session

import (
	"time"
)

// Status represents the session status
type Status string

const (
	StatusQueued     Status = "queued"     // キュー待機中
	StatusCreating   Status = "creating"   // worktree作成中/CC起動中
	StatusStopped    Status = "stopped"    // プロセス停止
	StatusRunning    Status = "running"    // 実行中（詳細不明）
	StatusIdle       Status = "idle"       // 入力待ち
	StatusThinking   Status = "thinking"   // 処理中
	StatusPermission Status = "permission" // 許可待ち
	StatusConfirm    Status = "confirm"    // Trust確認待ち
	StatusError      Status = "error"      // エラー
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

	// Worktree関連
	Repository    string `json:"repository,omitempty"`      // リポジトリ名
	Branch        string `json:"branch,omitempty"`          // ブランチ名
	BaseBranch    string `json:"base_branch,omitempty"`     // ベースブランチ名
	NewBranch     bool   `json:"new_branch,omitempty"`      // 新規ブランチを作成するか
	IsNewWorktree bool   `json:"is_new_worktree,omitempty"` // 新規worktreeを作成するか
	WorktreeName  string `json:"worktree_name,omitempty"`   // worktree名（ディレクトリ名）

	// プロンプト関連
	PromptName string `json:"prompt_name,omitempty"` // プロンプトテンプレート名
	PromptArgs string `json:"prompt_args,omitempty"` // プロンプト引数

	// Claude Code セッションID（復元用）
	ClaudeSessionID      string `json:"claude_session_id,omitempty"`
	ClaudeSessionStarted bool   `json:"claude_session_started,omitempty"` // CCセッションが一度でも起動されたか

	// ホスト情報（マルチホスト対応）
	HostID string `json:"host_id,omitempty"` // ホスト識別子 ("local", "ec2", "docker-dev" 等)

	// tmux integration
	TmuxWindowName string `json:"tmux_window_name,omitempty"` // tmux window name for this session
	TmuxPaneID     string `json:"tmux_pane_id,omitempty"`     // CC pane ID (e.g., "%42") for capture-pane

	// Runtime fields (not persisted)
	PromptInjected bool      `json:"-"` // プロンプト注入済みか
	LastOutputTime time.Time `json:"-"` // 最後にPTY出力を受信した時刻（idle安定性検出用）
	StartedAt      time.Time `json:"-"` // プロセス起動時刻（起動直後のエラー誤検出防止用）
	SSHAuthSock    string    `json:"-"` // SSH_AUTH_SOCK（git操作用、永続化しない）
}

// Info returns session information for display
type Info struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	WorkDir       string    `json:"work_dir"`
	Status        Status    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	LastActiveAt  time.Time `json:"last_active_at,omitempty"`
	Repository    string    `json:"repository,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	IsNewWorktree    bool   `json:"is_new_worktree,omitempty"`
	WorktreeName     string `json:"worktree_name,omitempty"`
	ErrorMessage     string `json:"error_message,omitempty"`
	ClaudeSessionID  string `json:"claude_session_id,omitempty"` // Claude Code session ID for transcript lookup
	TmuxWindowName   string `json:"tmux_window_name,omitempty"` // tmux window name
	HostID           string `json:"host_id,omitempty"`          // ホスト識別子

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
		Repository:      s.Repository,
		Branch:          s.Branch,
		IsNewWorktree:   s.IsNewWorktree,
		WorktreeName:    s.WorktreeName,
		ErrorMessage:    s.ErrorMessage,
		ClaudeSessionID: s.ClaudeSessionID,
		TmuxWindowName:  s.TmuxWindowName,
		HostID:          s.HostID,
	}
}
