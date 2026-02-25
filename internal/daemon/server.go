package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/host"
	"github.com/takaaki-s/claude-code-valet/internal/repository"
	"github.com/takaaki-s/claude-code-valet/internal/session"
	"github.com/takaaki-s/claude-code-valet/internal/tmux"
	"github.com/takaaki-s/claude-code-valet/internal/tunnel"
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
	socketPath    string
	manager       *session.Manager
	configMgr     *config.Manager
	stateMgr      *config.StateManager
	listener      net.Listener
	createMu      sync.Mutex         // セッション作成の排他制御用
	hostRegistry  *host.Registry     // マルチホスト管理
	tunnelMgr     *tunnel.Manager    // SSHトンネル管理
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

	// Set up tmux client if tmux is available and ccvalet tmux session exists
	if tc, err := tmux.NewClient(); err == nil {
		if tc.HasSession(tmux.SessionName) {
			mgr.SetTmuxClient(tc)
			mgr.RecoverTmuxSessions()
			debugLog("tmux client initialized (session: %s)", tmux.SessionName)
		}
	}

	s := &Server{
		socketPath: socketPath,
		manager:    mgr,
		configMgr:  configMgr,
		stateMgr:   stateMgr,
	}

	// マルチホスト対応の初期化
	hosts := configMgr.GetHosts()
	if len(hosts) > 0 {
		s.tunnelMgr = tunnel.NewManager()
		s.hostRegistry = host.NewRegistry(hosts)
		s.initRemoteSlaves()
	}

	return s, nil
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
	// トンネルをクリーンアップ
	if s.tunnelMgr != nil {
		s.tunnelMgr.CloseAll()
	}

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

	resp := s.handleRequest(&req)
	encoder.Encode(resp)
}

func (s *Server) handleRequest(req *Request) Response {
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
	case "stop":
		return s.handleStop()
	case "list-hosts":
		return s.handleListHosts()
	case "list-repos":
		return s.handleListRepos(req.Data)
	case "list-branches":
		return s.handleListBranches(req.Data)
	case "fetch-repo":
		return s.handleFetchRepo(req.Data)
	case "list-worktrees":
		return s.handleListWorktrees(req.Data)
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
	HostID        string `json:"host_id,omitempty"`        // 対象ホスト（空="local"）
	SSHAuthSock   string `json:"ssh_auth_sock,omitempty"`  // SSH_AUTH_SOCK（git操作用）
}

func (s *Server) handleNew(data json.RawMessage) Response {
	var req NewRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// リモートホスト宛の場合、該当slaveに転送
	if req.HostID != "" && req.HostID != "local" {
		// Clear SSH_AUTH_SOCK: local socket path doesn't exist on remote host.
		// The slave uses its own SSH_AUTH_SOCK from the SSH tunnel.
		req.SSHAuthSock = ""
		forwardData, _ := json.Marshal(req)
		return s.forwardToSlave(req.HostID, Request{Action: "new", Data: forwardData})
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
			SSHAuthSock:  req.SSHAuthSock,
		})
		if err != nil {
			debugLog("[ASYNC] Failed to create worktree: %v", err)
			s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("worktree creation failed: %v", err))
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
				SSHAuthSock:  req.SSHAuthSock,
			})
			if err != nil {
				debugLog("[ASYNC] Failed to create worktree: %v", err)
				s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("worktree creation failed: %v", err))
				return
			}
			// WorktreeNameを更新
			s.manager.SetWorktreeName(sessionID, worktreeName)
		} else {
			// 既存worktreeが見つかった場合もworktree名を設定
			s.manager.SetWorktreeName(sessionID, filepath.Base(wt.Path))
		}
	}
	workDir = wt.Path
	debugLog("[ASYNC] Worktree ready at %s", workDir)

	// WorkDirを設定（重複チェック付き）
	if err := s.manager.SetWorkDir(sessionID, workDir); err != nil {
		debugLog("[ASYNC] Failed to set WorkDir: %v", err)
		s.manager.SetStatusWithError(sessionID, session.StatusError, fmt.Sprintf("failed to set WorkDir: %v", err))
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

	// ローカルセッション一覧を取得
	localSessions := s.manager.List()
	for i := range localSessions {
		if localSessions[i].HostID == "" {
			localSessions[i].HostID = "local"
		}
	}

	// リモートホストがない場合はローカルのみ返す
	if s.hostRegistry == nil {
		data, _ := json.Marshal(localSessions)
		return Response{Success: true, Data: data}
	}

	// リモートセッション一覧を並列取得して統合
	allSessions := localSessions
	remotes := s.hostRegistry.Remotes()

	if len(remotes) > 0 {
		type remoteResult struct {
			sessions []session.Info
			err      error
			hostID   string
		}

		results := make(chan remoteResult, len(remotes))
		for _, h := range remotes {
			go func(rh *host.Host) {
				if rh.Client == nil {
					results <- remoteResult{hostID: rh.ID, err: fmt.Errorf("not connected")}
					return
				}
				sessions, err := rh.Client.ListWithHostID()
				results <- remoteResult{sessions: sessions, err: err, hostID: rh.ID}
			}(h)
		}

		for range remotes {
			result := <-results
			if result.err != nil {
				debugLog("[REMOTE] Failed to list from %s: %v", result.hostID, result.err)
				continue
			}
			allSessions = append(allSessions, result.sessions...)
		}
	}

	data, _ := json.Marshal(allSessions)
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
	ID     string `json:"id"`
	HostID string `json:"host_id,omitempty"` // 対象ホスト（空="local"）
}

func (s *Server) handleStart(data json.RawMessage) Response {
	var req IDRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// リモートホスト宛の場合、該当slaveに転送
	if req.HostID != "" && req.HostID != "local" {
		return s.forwardToSlave(req.HostID, Request{Action: "start", Data: data})
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

	// リモートホスト宛の場合、該当slaveに転送
	if req.HostID != "" && req.HostID != "local" {
		return s.forwardToSlave(req.HostID, Request{Action: "kill", Data: data})
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

	// リモートホスト宛の場合、該当slaveに転送
	if req.HostID != "" && req.HostID != "local" {
		return s.forwardToSlave(req.HostID, Request{Action: "delete", Data: data})
	}

	if err := s.manager.Delete(req.ID); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	// Try to start queued sessions if a slot became available
	s.tryStartQueued()

	return Response{Success: true}
}

func (s *Server) handleStop() Response {
	// Stop in a goroutine to allow response to be sent first
	go func() {
		s.Stop()
		os.Exit(0)
	}()
	return Response{Success: true}
}

// --- マルチホスト対応 ---

// initRemoteSlaves はリモートホストのSlaveデーモンを起動し、トンネルを確立し、daemon clientを設定する
func (s *Server) initRemoteSlaves() {
	if s.hostRegistry == nil || s.tunnelMgr == nil {
		return
	}

	for _, h := range s.hostRegistry.Remotes() {
		// Step 1: Slaveデーモンを自動起動（冪等: 既に起動済みなら何もしない）
		if err := host.StartSlave(h.Config); err != nil {
			debugLog("[REMOTE] Failed to start slave on %s: %v", h.ID, err)
			continue
		}
		debugLog("[REMOTE] Slave started on %s", h.ID)

		// Step 2: SSHトンネル/Docker接続を確立
		localSocket, err := s.tunnelMgr.Open(h.Config)
		if err != nil {
			debugLog("[REMOTE] Failed to open tunnel to %s: %v", h.ID, err)
			continue
		}

		// Step 3: RemoteClientを作成して登録
		client := NewRemoteClient(localSocket, h.ID)
		s.hostRegistry.SetClient(h.ID, client)
		debugLog("[REMOTE] Connected to slave %s via %s", h.ID, localSocket)
	}
}

// forwardToSlave はリクエストをリモートslaveに転送する
func (s *Server) forwardToSlave(hostID string, req Request) Response {
	if s.hostRegistry == nil {
		return Response{Success: false, Error: "host registry not initialized"}
	}

	h, ok := s.hostRegistry.Get(hostID)
	if !ok {
		return Response{Success: false, Error: fmt.Sprintf("unknown host: %s", hostID)}
	}

	if h.Client == nil {
		return Response{Success: false, Error: fmt.Sprintf("host %s not connected", hostID)}
	}

	// SlaveClientインターフェースから*Clientにキャスト
	client, ok := h.Client.(*Client)
	if !ok {
		return Response{Success: false, Error: fmt.Sprintf("host %s has incompatible client type", hostID)}
	}

	// host_idをリクエストデータから除去してスレーブに転送する
	// スレーブがhost_idを見て再度forwardしようとするのを防ぐ
	if req.Data != nil {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(req.Data, &m); err == nil {
			delete(m, "host_id")
			req.Data, _ = json.Marshal(m)
		}
	}

	resp, err := client.send(req)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to forward to %s: %v", hostID, err)}
	}

	return *resp
}

// --- ホスト・リポジトリ情報クエリ ---

// HostInfo はホスト情報を表す
type HostInfo struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Connected bool   `json:"connected"`
}

// RepositoryInfo はリポジトリ情報を表す
type RepositoryInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// WorktreeInfo はworktree情報を表す
type WorktreeInfo struct {
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	RepoName   string `json:"repo_name"`
	IsMain     bool   `json:"is_main"`
	IsManaged  bool   `json:"is_managed"`
	IsDetached bool   `json:"is_detached"`
}

// ListReposRequest はリポジトリ一覧取得リクエスト
type ListReposRequest struct {
	HostID string `json:"host_id,omitempty"`
}

// ListBranchesRequest はブランチ一覧取得リクエスト
type ListBranchesRequest struct {
	HostID     string `json:"host_id,omitempty"`
	Repository string `json:"repository"`
	All        bool   `json:"all,omitempty"` // true=ローカル+リモート
}

// ListWorktreesRequest はworktree一覧取得リクエスト
type ListWorktreesRequest struct {
	HostID     string `json:"host_id,omitempty"`
	Repository string `json:"repository"`
}

func (s *Server) handleListHosts() Response {
	hosts := []HostInfo{
		{ID: "local", Type: "local", Connected: true},
	}

	if s.hostRegistry != nil {
		for _, h := range s.hostRegistry.Remotes() {
			connected := h.Client != nil && h.Client.IsRunning()
			hosts = append(hosts, HostInfo{
				ID:        h.ID,
				Type:      h.Type,
				Connected: connected,
			})
		}
	}

	data, _ := json.Marshal(hosts)
	return Response{Success: true, Data: data}
}

func (s *Server) handleListRepos(data json.RawMessage) Response {
	var req ListReposRequest
	if data != nil {
		if err := json.Unmarshal(data, &req); err != nil {
			return Response{Success: false, Error: err.Error()}
		}
	}

	if req.HostID != "" && req.HostID != "local" {
		return s.forwardToSlave(req.HostID, Request{Action: "list-repos", Data: data})
	}

	repos := s.configMgr.GetRepositories()
	infos := make([]RepositoryInfo, 0, len(repos))
	for _, r := range repos {
		infos = append(infos, RepositoryInfo{
			Name:       r.Name,
			Path:       r.Path,
			BaseBranch: r.BaseBranch,
		})
	}

	respData, _ := json.Marshal(infos)
	return Response{Success: true, Data: respData}
}

// FetchRepoRequest はリポジトリのgit fetchリクエスト
type FetchRepoRequest struct {
	HostID      string `json:"host_id,omitempty"`
	Repository  string `json:"repository"`
	SSHAuthSock string `json:"ssh_auth_sock,omitempty"`
}

func (s *Server) handleFetchRepo(data json.RawMessage) Response {
	var req FetchRepoRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if req.HostID != "" && req.HostID != "local" {
		// Clear SSH_AUTH_SOCK before forwarding: the client's local socket path
		// doesn't exist on the remote host. The slave will use its own SSH_AUTH_SOCK
		// (inherited from the SSH tunnel with ForwardAgent).
		req.SSHAuthSock = ""
		forwardData, _ := json.Marshal(req)
		return s.forwardToSlave(req.HostID, Request{Action: "fetch-repo", Data: forwardData})
	}

	repo := s.configMgr.GetRepository(req.Repository)
	if repo == nil {
		return Response{Success: false, Error: fmt.Sprintf("repository '%s' not found", req.Repository)}
	}

	fetchCmd := exec.Command("git", "-C", repo.Path, "fetch", "origin")
	// SSH_AUTH_SOCK の優先順位:
	// 1. リクエストで指定された値
	// 2. トンネル経由のForwardAgent安定シンボリックリンク (~/.ccvalet/ssh-agent.sock)
	// 3. デーモン自身の環境変数
	sshSock := req.SSHAuthSock
	if sshSock == "" {
		home, _ := os.UserHomeDir()
		stableAgent := filepath.Join(home, ".ccvalet", "ssh-agent.sock")
		if target, err := os.Readlink(stableAgent); err == nil && target != "" {
			if _, err := os.Stat(stableAgent); err == nil {
				sshSock = stableAgent
			}
		}
	}
	if sshSock != "" {
		fetchCmd.Env = replaceEnv(os.Environ(), "SSH_AUTH_SOCK", sshSock)
	}
	debugLog("[FETCH] repo=%s ssh_auth_sock=%q (req=%q, daemon=%q)",
		req.Repository, sshSock, req.SSHAuthSock, os.Getenv("SSH_AUTH_SOCK"))
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("git fetch failed: %s: %v", strings.TrimSpace(string(output)), err)}
	}

	return Response{Success: true}
}

// replaceEnv replaces or appends an environment variable in the given env slice.
func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func (s *Server) handleListBranches(data json.RawMessage) Response {
	var req ListBranchesRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if req.HostID != "" && req.HostID != "local" {
		return s.forwardToSlave(req.HostID, Request{Action: "list-branches", Data: data})
	}

	registry := repository.NewRegistry(s.configMgr)
	var branches []string
	var err error
	if req.All {
		branches, err = registry.GetBranches(req.Repository)
	} else {
		branches, err = registry.GetLocalBranches(req.Repository)
	}
	if err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	respData, _ := json.Marshal(branches)
	return Response{Success: true, Data: respData}
}

func (s *Server) handleListWorktrees(data json.RawMessage) Response {
	var req ListWorktreesRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	if req.HostID != "" && req.HostID != "local" {
		return s.forwardToSlave(req.HostID, Request{Action: "list-worktrees", Data: data})
	}

	wtMgr := worktree.NewManager(s.configMgr)
	wts, err := wtMgr.List(req.Repository)
	if err != nil {
		return Response{Success: false, Error: err.Error()}
	}

	infos := make([]WorktreeInfo, 0, len(wts))
	for _, wt := range wts {
		infos = append(infos, WorktreeInfo{
			Path:       wt.Path,
			Branch:     wt.Branch,
			RepoName:   wt.RepoName,
			IsMain:     wt.IsMain,
			IsManaged:  wt.IsManaged,
			IsDetached: wt.IsDetached,
		})
	}

	respData, _ := json.Marshal(infos)
	return Response{Success: true, Data: respData}
}
