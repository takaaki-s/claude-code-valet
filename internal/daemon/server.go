package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/worktree"
)

// debugEnabled controls debug logging output
var debugEnabled = os.Getenv("CCVALET_DEBUG") == "1"
var debugLogPath string

func init() {
	if debugEnabled {
		home, _ := os.UserHomeDir()
		debugLogPath = filepath.Join(home, ".ccvalet", "daemon-debug.log")
	}
}

func debugLog(format string, args ...interface{}) {
	if !debugEnabled || debugLogPath == "" {
		return
	}
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
}

// Server is the daemon server
type Server struct {
	socketPath string
	manager    *session.Manager
	configMgr  *config.Manager
	stateMgr   *config.StateManager
	listener   net.Listener
	createMu   sync.Mutex // セッション作成の排他制御用
}

// Message types
type Request struct {
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type Response struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// NewServer creates a new daemon server
func NewServer(socketPath, dataDir, configDir string) (*Server, error) {
	configMgr, err := config.NewManager(configDir)
	if err != nil {
		return nil, err
	}

	stateMgr, err := config.NewStateManager(configDir)
	if err != nil {
		return nil, err
	}

	mgr, err := session.NewManager(dataDir, configDir, configMgr)
	if err != nil {
		return nil, err
	}

	return &Server{
		socketPath: socketPath,
		manager:    mgr,
		configMgr:  configMgr,
		stateMgr:   stateMgr,
	}, nil
}

// Start starts the daemon server
func (s *Server) Start() error {
	// Remove existing socket
	os.Remove(s.socketPath)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0755); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	s.listener = listener

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		s.Stop()
		os.Exit(0)
	}()

	log.Printf("Daemon listening on %s", s.socketPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.listener == nil {
				return nil // Server stopped
			}
			log.Printf("Accept error: %v", err)
			continue
		}
		go s.handleConnection(conn)
	}
}

// Stop stops the daemon server
func (s *Server) Stop() {
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	os.Remove(s.socketPath)
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	var req Request
	if err := decoder.Decode(&req); err != nil {
		if err != io.EOF {
			log.Printf("Decode error: %v", err)
		}
		return
	}

	resp := s.handleRequest(&req, conn)
	encoder.Encode(resp)
}

func (s *Server) handleRequest(req *Request, conn net.Conn) Response {
	switch req.Action {
	case "new":
		return s.handleNew(req.Data)
	case "list":
		return s.handleList()
	case "start":
		return s.handleStart(req.Data)
	case "kill":
		return s.handleKill(req.Data)
	case "delete":
		return s.handleDelete(req.Data)
	case "attach":
		return s.handleAttach(req.Data, conn)
	case "subscribe":
		return s.handleSubscribe(req.Data, conn)
	case "write":
		return s.handleWrite(req.Data)
	case "resize":
		return s.handleResize(req.Data)
	case "stop":
		return s.handleStop()
	default:
		return Response{Success: false, Error: fmt.Sprintf("unknown action: %s", req.Action)}
	}
}

type NewRequest struct {
	Name          string `json:"name"`
	WorkDir       string `json:"work_dir"`
	Start         bool   `json:"start"`
	Async         bool   `json:"async,omitempty"`          // trueの場合、creating状態で即座に返す
	PromptName    string `json:"prompt_name,omitempty"`
	PromptArgs    string `json:"prompt_args,omitempty"`
	Repository    string `json:"repository,omitempty"`
	Branch        string `json:"branch,omitempty"`
	BaseBranch    string `json:"base_branch,omitempty"`
	NewBranch     bool   `json:"new_branch,omitempty"`     // 新規ブランチを作成するか
	IsNewWorktree bool   `json:"is_new_worktree,omitempty"` // 新規worktreeを作成するか
	WorktreeName  string `json:"worktree_name,omitempty"`  // worktree名（ディレクトリ名）
}

func (s *Server) handleNew(data json.RawMessage) Response {
	var req NewRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// 設定を再読み込み（CLI で repo add された場合に対応）
	if s.configMgr != nil {
		_ = s.configMgr.Reload()
	}

	// リポジトリが指定されている場合は存在チェック
	if req.Repository != "" && s.configMgr != nil {
		if s.configMgr.GetRepository(req.Repository) == nil {
			return Response{Success: false, Error: fmt.Sprintf("repository '%s' not found", req.Repository)}
		}
	}

	// Asyncモード + worktreeモードの場合
	if req.Async && req.Repository != "" && req.Branch != "" {
		return s.handleNewAsync(req)
	}

	// 同期モード - 排他制御と並列数チェック
	s.createMu.Lock()

	// Check if we should queue this session
	shouldQueue := false
	if s.configMgr != nil && req.Start {
		maxParallel := s.configMgr.GetMaxParallel()
		activeCount := s.manager.CountActive()
		if activeCount >= maxParallel {
			shouldQueue = true
			debugLog("[QUEUE] Queueing new session (active: %d, max: %d)",
				activeCount, maxParallel)
		}
	}

	sess, err := s.manager.CreateWithOptions(session.CreateOptions{
		Name:          req.Name,
		WorkDir:       req.WorkDir,
		PromptName:    req.PromptName,
		PromptArgs:    req.PromptArgs,
		Repository:    req.Repository,
		Branch:        req.Branch,
		BaseBranch:    req.BaseBranch,
		NewBranch:     req.NewBranch,
		IsNewWorktree: req.IsNewWorktree,
		WorktreeName:  req.WorktreeName,
	})
	if err != nil {
		s.createMu.Unlock()
		return Response{Success: false, Error: err.Error()}
	}

	if shouldQueue {
		s.manager.SetStatus(sess.ID, session.StatusQueued)
		s.createMu.Unlock()
	} else {
		s.createMu.Unlock()

		// Start session in background if requested
		if req.Start {
			if err := s.manager.StartBackground(sess.ID); err != nil {
				return Response{Success: false, Error: err.Error()}
			}
		}
	}

	respData, _ := json.Marshal(sess.ToInfo())
	return Response{Success: true, Data: respData}
}

// handleNewAsync handles async session creation with worktree
func (s *Server) handleNewAsync(req NewRequest) Response {
	// 排他制御：CountActive()とセッション作成を原子的に行う
	s.createMu.Lock()

	// Check if we should queue this session
	shouldQueue := false
	if s.configMgr != nil && req.Start {
		maxParallel := s.configMgr.GetMaxParallel()
		activeCount := s.manager.CountActive()
		if activeCount >= maxParallel {
			shouldQueue = true
			debugLog("[QUEUE] Queueing new session (active: %d, max: %d)",
				activeCount, maxParallel)
		}
	}

	// セッションを作成（WorkDirは後で設定、セッション名は自動生成される）
	sess, err := s.manager.CreateWithOptions(session.CreateOptions{
		Name:          req.Name, // 空の場合は自動生成
		WorkDir:       "",       // 後で設定
		PromptName:    req.PromptName,
		PromptArgs:    req.PromptArgs,
		Repository:    req.Repository,
		Branch:        req.Branch,
		BaseBranch:    req.BaseBranch,
		NewBranch:     req.NewBranch,
		IsNewWorktree: req.IsNewWorktree,
		WorktreeName:  req.WorktreeName,
	})
	if err != nil {
		s.createMu.Unlock()
		return Response{Success: false, Error: err.Error()}
	}

	if shouldQueue {
		// Set to queued status
		s.manager.SetStatus(sess.ID, session.StatusQueued)
	} else {
		// Set to creating status and start immediately
		s.manager.SetStatus(sess.ID, session.StatusCreating)
	}

	s.createMu.Unlock()

	// バックグラウンド処理はロック解放後に開始
	if !shouldQueue {
		go s.createSessionAsync(sess.ID, req)
	}

	respData, _ := json.Marshal(sess.ToInfo())
	return Response{Success: true, Data: respData}
}

// createSessionAsync creates worktree and starts CC in background
func (s *Server) createSessionAsync(sessionID string, req NewRequest) {
	debugLog("[ASYNC] Starting async session creation for %s", sessionID)

	wtMgr := worktree.NewManager(s.configMgr)

	var workDir string
	var worktreeName string
	var wt *worktree.Worktree
	var err error

	if req.IsNewWorktree {
		// 新規worktreeを作成（setup-script実行）
		debugLog("[ASYNC] Creating new worktree for %s/%s (IsNewWorktree=true)", req.Repository, req.Branch)
		wt, worktreeName, err = wtMgr.CreateWithOptions(worktree.CreateOptions{
			RepoName:     req.Repository,
			Branch:       req.Branch,
			NewBranch:    req.NewBranch,
			BaseBranch:   req.BaseBranch,
			WorktreeName: req.WorktreeName,
		})
		if err != nil {
			debugLog("[ASYNC] Failed to create worktree: %v", err)
			s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("worktree creation failed: %v", err))
			// ロールバック：worktree作成に失敗したセッションを削除して再作成可能にする
			if delErr := s.manager.Delete(sessionID); delErr != nil {
				debugLog("[ASYNC] Failed to delete session on rollback: %v", delErr)
			}
			return
		}
		// WorktreeNameを更新
		s.manager.SetWorktreeName(sessionID, worktreeName)
	} else {
		// 既存のworktreeを検索
		wt, err = wtMgr.GetByBranch(req.Repository, req.Branch)
		if err != nil {
			// worktreeが存在しない場合は作成
			debugLog("[ASYNC] Creating worktree for %s/%s (not found)", req.Repository, req.Branch)
			wt, worktreeName, err = wtMgr.CreateWithOptions(worktree.CreateOptions{
				RepoName:     req.Repository,
				Branch:       req.Branch,
				NewBranch:    req.NewBranch,
				BaseBranch:   req.BaseBranch,
				WorktreeName: req.WorktreeName,
			})
			if err != nil {
				debugLog("[ASYNC] Failed to create worktree: %v", err)
				s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("worktree creation failed: %v", err))
				// ロールバック：worktree作成に失敗したセッションを削除して再作成可能にする
				if delErr := s.manager.Delete(sessionID); delErr != nil {
					debugLog("[ASYNC] Failed to delete session on rollback: %v", delErr)
				}
				return
			}
			// WorktreeNameを更新
			s.manager.SetWorktreeName(sessionID, worktreeName)
		}
	}
	workDir = wt.Path
	debugLog("[ASYNC] Worktree ready at %s", workDir)

	// WorkDirを設定（重複チェック付き）
	if err := s.manager.SetWorkDir(sessionID, workDir); err != nil {
		debugLog("[ASYNC] Failed to set WorkDir: %v", err)
		s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("failed to set WorkDir: %v", err))
		// ロールバック：WorkDir設定に失敗したセッションを削除して再作成可能にする
		if delErr := s.manager.Delete(sessionID); delErr != nil {
			debugLog("[ASYNC] Failed to delete session on rollback: %v", delErr)
		}
		return
	}

	// セッションを起動
	if req.Start {
		debugLog("[ASYNC] Starting session %s", sessionID)
		if err := s.manager.StartBackground(sessionID); err != nil {
			debugLog("[ASYNC] Failed to start session: %v", err)
			s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("failed to start session: %v", err))
			return
		}
	}

	// 使用したリポジトリを記憶
	if s.stateMgr != nil {
		s.stateMgr.SetLastUsedRepository(req.Repository)
		_ = s.stateMgr.Save()
	}

	debugLog("[ASYNC] Session %s created successfully", sessionID)
}

func (s *Server) handleList() Response {
	// Try to start queued sessions if there's room
	s.tryStartQueued()

	sessions := s.manager.List()
	data, _ := json.Marshal(sessions)
	return Response{Success: true, Data: data}
}

// tryStartQueued checks if we can start a queued session and starts it
func (s *Server) tryStartQueued() {
	if s.configMgr == nil {
		return
	}

	// 排他制御
	s.createMu.Lock()
	defer s.createMu.Unlock()

	maxParallel := s.configMgr.GetMaxParallel()
	activeCount := s.manager.CountActive()

	// Start queued sessions while we have room
	for activeCount < maxParallel {
		queuedSession, ok := s.manager.GetNextQueued()
		if !ok {
			break // No more queued sessions
		}

		debugLog("[QUEUE] Starting queued session %s (active: %d, max: %d)",
			queuedSession.ID, activeCount, maxParallel)

		// Set to creating status before starting
		s.manager.SetStatus(queuedSession.ID, session.StatusCreating)

		// Start the queued session in background
		go s.startQueuedSession(queuedSession.ID)

		activeCount++ // Increment to prevent starting too many in one call
	}
}

// startQueuedSession starts a queued session (with worktree creation if needed)
// Note: Session status should already be set to StatusCreating by tryStartQueued
func (s *Server) startQueuedSession(sessionID string) {
	sess, ok := s.manager.Get(sessionID)
	if !ok {
		debugLog("[QUEUE] Session %s not found", sessionID)
		return
	}

	// If session has repository/branch, handle worktree creation
	if sess.Repository != "" && sess.Branch != "" {
		wtMgr := worktree.NewManager(s.configMgr)

		var wt *worktree.Worktree
		var err error

		if sess.IsNewWorktree {
			// 新規worktreeを作成（setup-script実行）
			debugLog("[QUEUE] Creating new worktree for %s/%s (IsNewWorktree=true)", sess.Repository, sess.Branch)
			wt, _, err = wtMgr.CreateWithOptions(worktree.CreateOptions{
				RepoName:     sess.Repository,
				Branch:       sess.Branch,
				NewBranch:    sess.NewBranch,
				BaseBranch:   sess.BaseBranch,
				WorktreeName: sess.WorktreeName,
			})
			if err != nil {
				debugLog("[QUEUE] Failed to create worktree: %v", err)
				s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("worktree creation failed: %v", err))
				return
			}
		} else {
			// 既存のworktreeを検索
			wt, err = wtMgr.GetByBranch(sess.Repository, sess.Branch)
			if err != nil {
				// worktreeが存在しない場合は作成
				debugLog("[QUEUE] Creating worktree for %s/%s (not found)", sess.Repository, sess.Branch)
				wt, err = wtMgr.Create("", sess.Repository, sess.Branch, sess.NewBranch, sess.BaseBranch)
				if err != nil {
					debugLog("[QUEUE] Failed to create worktree: %v", err)
					s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("worktree creation failed: %v", err))
					return
				}
			}
		}

		// Update WorkDir（重複チェック付き）
		if err := s.manager.SetWorkDir(sessionID, wt.Path); err != nil {
			debugLog("[QUEUE] Failed to set WorkDir: %v", err)
			s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("failed to set WorkDir: %v", err))
			return
		}
	}

	// Start the session
	if err := s.manager.StartBackground(sessionID); err != nil {
		debugLog("[QUEUE] Failed to start session %s: %v", sessionID, err)
		s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("failed to start session: %v", err))
		return
	}

	debugLog("[QUEUE] Session %s started successfully", sessionID)
}

type IDRequest struct {
	ID string `json:"id"`
}

func (s *Server) handleStart(data json.RawMessage) Response {
	var req IDRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if err := s.manager.StartBackground(req.ID); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	return Response{Success: true}
}

func (s *Server) handleKill(data json.RawMessage) Response {
	var req IDRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if err := s.manager.Kill(req.ID); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	return Response{Success: true}
}

func (s *Server) handleDelete(data json.RawMessage) Response {
	var req IDRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if err := s.manager.Delete(req.ID); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// Try to start queued sessions if a slot became available
	s.tryStartQueued()

	return Response{Success: true}
}

func (s *Server) handleAttach(data json.RawMessage, conn net.Conn) Response {
	var req IDRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// Auto-start stopped sessions
	if err := s.manager.StartBackground(req.ID); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// Wait for session to be fully running (with timeout)
	// This handles the race condition where the session process
	// might not be fully initialized before AttachToConn is called
	sess, ok := s.manager.Get(req.ID)
	if ok {
		for i := 0; i < 30; i++ { // 3 seconds timeout
			if sess.Status != session.StatusStopped && sess.PTY != nil && sess.Broadcaster != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
			sess, _ = s.manager.Get(req.ID)
		}
	}

	// Send success first, then stream PTY data
	resp := Response{Success: true}
	encoder := json.NewEncoder(conn)
	encoder.Encode(resp)

	// Stream PTY I/O over the connection
	if err := s.manager.AttachToConn(req.ID, conn); err != nil {
		log.Printf("Attach error: %v", err)
	}

	return Response{} // Already sent
}

// WriteRequest contains session ID and data to write to PTY
type WriteRequest struct {
	ID   string `json:"id"`
	Data string `json:"data"` // base64 or raw text to write to PTY
}

func (s *Server) handleWrite(data json.RawMessage) Response {
	var req WriteRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if err := s.manager.WriteToSession(req.ID, []byte(req.Data)); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	return Response{Success: true}
}

// ResizeRequest contains session ID and terminal dimensions
type ResizeRequest struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (s *Server) handleResize(data json.RawMessage) Response {
	var req ResizeRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if err := s.manager.ResizePTY(req.ID, req.Cols, req.Rows); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	return Response{Success: true}
}

func (s *Server) handleSubscribe(data json.RawMessage, conn net.Conn) Response {
	var req IDRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	screenData, outputCh, cleanup, err := s.manager.SubscribeOutput(req.ID)
	if err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// Send success response first
	resp := Response{Success: true}
	encoder := json.NewEncoder(conn)
	encoder.Encode(resp)

	// Send current screen buffer content
	if len(screenData) > 0 {
		conn.Write(screenData)
	}

	// If no broadcaster (session not running), we're done
	if outputCh == nil {
		return Response{}
	}
	defer cleanup()

	// Stream new output until connection closes
	for data := range outputCh {
		if _, err := conn.Write(data); err != nil {
			break
		}
	}

	return Response{} // Already sent
}

func (s *Server) handleStop() Response {
	// Stop in a goroutine to allow response to be sent first
	go func() {
		s.Stop()
		os.Exit(0)
	}()
	return Response{Success: true}
}
