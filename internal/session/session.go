package session

import (
	"os"
	"os/exec"
	"sync"
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

// ScreenBuffer is a ring buffer for storing screen output
type ScreenBuffer struct {
	buf   []byte
	size  int
	start int
	len   int
	mu    sync.Mutex
}

// NewScreenBuffer creates a new screen buffer with the given size
func NewScreenBuffer(size int) *ScreenBuffer {
	return &ScreenBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Write writes data to the buffer
func (b *ScreenBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, c := range p {
		pos := (b.start + b.len) % b.size
		b.buf[pos] = c
		if b.len < b.size {
			b.len++
		} else {
			b.start = (b.start + 1) % b.size
		}
	}
	return len(p), nil
}

// Bytes returns the buffer contents in order
func (b *ScreenBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]byte, b.len)
	for i := 0; i < b.len; i++ {
		result[i] = b.buf[(b.start+i)%b.size]
	}
	return result
}

// LastN returns the last n bytes from the buffer
func (b *ScreenBuffer) LastN(n int) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	if n > b.len {
		n = b.len
	}
	if n == 0 {
		return nil
	}

	result := make([]byte, n)
	startPos := b.len - n
	for i := 0; i < n; i++ {
		result[i] = b.buf[(b.start+startPos+i)%b.size]
	}
	return result
}

// Clear clears the buffer
func (b *ScreenBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.start = 0
	b.len = 0
}

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
	ClaudeSessionID        string `json:"claude_session_id,omitempty"`
	ClaudeSessionStarted   bool   `json:"claude_session_started,omitempty"` // CCセッションが一度でも起動されたか

	// Runtime fields (not persisted)
	PTY            *os.File      `json:"-"`
	Cmd            *exec.Cmd     `json:"-"`
	ScreenBuffer   *ScreenBuffer `json:"-"`
	Broadcaster    *Broadcaster  `json:"-"`
	TrustHandled   bool          `json:"-"` // trust確認を自動応答済みか
	PromptInjected bool          `json:"-"` // プロンプト注入済みか
	LastOutputTime time.Time     `json:"-"` // 最後にPTY出力を受信した時刻（idle安定性検出用）
	StartedAt      time.Time     `json:"-"` // プロセス起動時刻（起動直後のエラー誤検出防止用）
}

// Broadcaster broadcasts PTY output to multiple listeners
type Broadcaster struct {
	listeners map[chan []byte]struct{}
	mu        sync.Mutex
}

// NewBroadcaster creates a new broadcaster
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		listeners: make(map[chan []byte]struct{}),
	}
}

// Subscribe adds a listener and returns a channel to receive data
func (b *Broadcaster) Subscribe() chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []byte, 256)
	b.listeners[ch] = struct{}{}
	return ch
}

// Unsubscribe removes a listener
func (b *Broadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Only close if still in the map (not already closed by Close())
	if _, exists := b.listeners[ch]; exists {
		delete(b.listeners, ch)
		close(ch)
	}
}

// Broadcast sends data to all listeners
func (b *Broadcaster) Broadcast(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.listeners {
		// Non-blocking send
		select {
		case ch <- data:
		default:
			// Drop if buffer is full
		}
	}
}

// Close closes all listener channels
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.listeners {
		close(ch)
	}
	b.listeners = make(map[chan []byte]struct{})
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
	IsNewWorktree bool      `json:"is_new_worktree,omitempty"`
	WorktreeName  string    `json:"worktree_name,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
}

// ToInfo converts Session to Info
func (s *Session) ToInfo() Info {
	return Info{
		ID:            s.ID,
		Name:          s.Name,
		WorkDir:       s.WorkDir,
		Status:        s.Status,
		CreatedAt:     s.CreatedAt,
		LastActiveAt:  s.LastActiveAt,
		Repository:    s.Repository,
		Branch:        s.Branch,
		IsNewWorktree: s.IsNewWorktree,
		WorktreeName:  s.WorktreeName,
		ErrorMessage:  s.ErrorMessage,
	}
}
