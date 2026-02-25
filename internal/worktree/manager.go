package worktree

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/takaaki-s/claude-code-valet/internal/config"
)

// debugEnabled controls debug logging output
var debugEnabled = os.Getenv("CCVALET_DEBUG") == "1"

func debugLog(format string, args ...interface{}) {
	if debugEnabled {
		log.Printf(format, args...)
	}
}

// Worktree はgit worktreeの情報を表す
type Worktree struct {
	Path       string // worktreeのパス
	Branch     string // ブランチ名
	RepoName   string // リポジトリ名
	RepoPath   string // 元リポジトリのパス
	IsManaged  bool   // ccvaletで管理されているか
	IsCurrent  bool   // HEADか
	IsDetached bool   // detached HEAD か
	IsMain     bool   // メインのworktree（リポジトリ本体）か
}

// Manager はgit worktreeの管理を行う
type Manager struct {
	configMgr   *config.Manager
	worktreeDir string
	configDir   string // 設定ディレクトリ（~/.ccvalet）
}

// NewManager は新しいWorktreeマネージャを作成する
func NewManager(configMgr *config.Manager) *Manager {
	home, _ := os.UserHomeDir()
	return &Manager{
		configMgr:   configMgr,
		worktreeDir: configMgr.GetWorktreeDir(),
		configDir:   filepath.Join(home, ".ccvalet"),
	}
}

// CreateOptions contains options for creating a worktree
type CreateOptions struct {
	RepoName     string // リポジトリ名（必須）
	Branch       string // ブランチ名（必須）
	NewBranch    bool   // 新しいブランチを作成するか
	BaseBranch   string // 新規ブランチ作成時のベースブランチ
	WorktreeName string // worktree名（省略時はブランチ名から自動生成）
	SSHAuthSock  string // SSH_AUTH_SOCK（git fetch用、空の場合は環境変数を継承）
}

// CreateWithOptions は新しいworktreeを作成する（v2 API）
func (m *Manager) CreateWithOptions(opts CreateOptions) (*Worktree, string, error) {
	repo := m.configMgr.GetRepository(opts.RepoName)
	if repo == nil {
		return nil, "", fmt.Errorf("repository '%s' not found", opts.RepoName)
	}

	// ブランチ存在チェック
	branchExists := m.BranchExists(repo.Path, opts.Branch)
	if opts.NewBranch && branchExists {
		return nil, "", fmt.Errorf("branch '%s' already exists", opts.Branch)
	}
	if !opts.NewBranch && !branchExists {
		return nil, "", fmt.Errorf("branch '%s' does not exist", opts.Branch)
	}

	// worktree名を決定（省略時はブランチ名から自動生成、重複時はサフィックス付与）
	worktreeName := opts.WorktreeName
	if worktreeName == "" {
		worktreeName = m.generateWorktreeName(opts.RepoName, opts.Branch)
	} else {
		// 明示指定でも重複チェック
		worktreeName = m.ensureUniqueWorktreeName(opts.RepoName, worktreeName)
	}

	// worktreeパスを決定
	worktreePath := m.getWorktreePathByName(opts.RepoName, worktreeName)

	// 新規ブランチ作成時はgit fetchを試行（失敗しても続行）
	if opts.NewBranch {
		fetchCmd := exec.Command("git", "-C", repo.Path, "fetch", "origin")
		sshSock := opts.SSHAuthSock
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
		if output, err := fetchCmd.CombinedOutput(); err != nil {
			debugLog("[WORKTREE] git fetch origin failed (continuing): %s: %v", string(output), err)
		}
	}

	// ベースブランチを決定
	effectiveBaseBranch := opts.BaseBranch
	if effectiveBaseBranch == "" {
		effectiveBaseBranch = repo.BaseBranch
	}

	// git worktree add を実行
	args := []string{"-C", repo.Path, "worktree", "add"}
	if opts.NewBranch {
		args = append(args, "-b", opts.Branch)
		args = append(args, worktreePath)
		if effectiveBaseBranch != "" {
			// origin/ プレフィックスが既にある場合は追加しない
			baseRef := effectiveBaseBranch
			if !strings.HasPrefix(effectiveBaseBranch, "origin/") && !strings.HasPrefix(effectiveBaseBranch, "refs/") {
				baseRef = "origin/" + effectiveBaseBranch
			}
			args = append(args, baseRef)
		}
	} else {
		args = append(args, worktreePath, opts.Branch)
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create worktree: %s: %w", string(output), err)
	}

	wt := &Worktree{
		Path:      worktreePath,
		Branch:    opts.Branch,
		RepoName:  opts.RepoName,
		RepoPath:  repo.Path,
		IsManaged: true,
	}

	// セットアップを実行
	if err := m.runSetup(wt, repo); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: setup failed: %v\n", err)
	}

	return wt, worktreeName, nil
}

// Create は新しいworktreeを作成する（後方互換用）
// path: worktreeを作成するパス（空の場合はデフォルトパスを使用）
// repoName: リポジトリ名
// branch: ブランチ名
// newBranch: 新しいブランチを作成するかどうか
// baseBranch: 新規ブランチ作成時のベースブランチ（空の場合はリポジトリ設定のbase_branchを使用）
func (m *Manager) Create(path, repoName, branch string, newBranch bool, baseBranch string) (*Worktree, error) {
	repo := m.configMgr.GetRepository(repoName)
	if repo == nil {
		return nil, fmt.Errorf("repository '%s' not found", repoName)
	}

	// worktreeのパスを決定
	worktreePath := path
	if worktreePath == "" {
		worktreePath = m.getWorktreePath(repoName, branch)
	}

	// 絶対パスに変換
	if !filepath.IsAbs(worktreePath) {
		absPath, err := filepath.Abs(worktreePath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve path: %w", err)
		}
		worktreePath = absPath
	}

	// 既存のworktreeがあるか確認
	if _, err := os.Stat(worktreePath); err == nil {
		return nil, fmt.Errorf("worktree already exists: %s", worktreePath)
	}

	// worktree用ディレクトリを作成
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory: %w", err)
	}

	// 新規ブランチ作成時はgit fetchを実行
	if newBranch {
		fetchCmd := exec.Command("git", "-C", repo.Path, "fetch", "origin")
		if output, err := fetchCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to fetch origin: %s: %w", string(output), err)
		}
	}

	// ベースブランチを決定（引数 > リポジトリ設定 > デフォルト）
	effectiveBaseBranch := baseBranch
	if effectiveBaseBranch == "" {
		effectiveBaseBranch = repo.BaseBranch
	}

	// git worktree add を実行
	args := []string{"-C", repo.Path, "worktree", "add"}
	if newBranch {
		args = append(args, "-b", branch)
		args = append(args, worktreePath)
		// ベースブランチが指定されている場合は origin/<base> から作成
		if effectiveBaseBranch != "" {
			// origin/ プレフィックスが既にある場合は追加しない
			baseRef := effectiveBaseBranch
			if !strings.HasPrefix(effectiveBaseBranch, "origin/") && !strings.HasPrefix(effectiveBaseBranch, "refs/") {
				baseRef = "origin/" + effectiveBaseBranch
			}
			args = append(args, baseRef)
		}
	} else {
		args = append(args, worktreePath, branch)
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree: %s: %w", string(output), err)
	}

	wt := &Worktree{
		Path:      worktreePath,
		Branch:    branch,
		RepoName:  repoName,
		RepoPath:  repo.Path,
		IsManaged: m.isManagedWorktree(worktreePath, repoName),
	}

	// セットアップを実行
	if err := m.runSetup(wt, repo); err != nil {
		// セットアップ失敗時もworktreeは残す（警告のみ）
		fmt.Fprintf(os.Stderr, "Warning: setup failed: %v\n", err)
	}

	return wt, nil
}

// Delete はworktreeを削除する
// repoName: リポジトリ名
// worktreePath: 削除するworktreeのパス
// force: 強制削除
func (m *Manager) Delete(repoName, worktreePath string, force bool) error {
	repo := m.configMgr.GetRepository(repoName)
	if repo == nil {
		return fmt.Errorf("repository '%s' not found", repoName)
	}

	// worktreeが存在するか確認
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return fmt.Errorf("worktree does not exist: %s", worktreePath)
	}

	// git worktree remove を実行
	args := []string{"-C", repo.Path, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove worktree: %s: %w", string(output), err)
	}

	return nil
}

// List は指定リポジトリのworktree一覧を返す
func (m *Manager) List(repoName string) ([]Worktree, error) {
	repo := m.configMgr.GetRepository(repoName)
	if repo == nil {
		return nil, fmt.Errorf("repository '%s' not found", repoName)
	}

	return m.listWorktrees(repo.Path, repoName)
}

// ListAll は全リポジトリのworktree一覧を返す
func (m *Manager) ListAll() ([]Worktree, error) {
	repos := m.configMgr.GetRepositories()
	var allWorktrees []Worktree

	for _, repo := range repos {
		wts, err := m.listWorktrees(repo.Path, repo.Name)
		if err != nil {
			continue // エラーがあってもスキップして続行
		}
		allWorktrees = append(allWorktrees, wts...)
	}

	return allWorktrees, nil
}

// listWorktrees はgit worktree listを実行してworktree一覧を取得する
func (m *Manager) listWorktrees(repoPath, repoName string) ([]Worktree, error) {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return m.parseWorktreeList(string(output), repoName, repoPath)
}

// parseWorktreeList はgit worktree list --porcelainの出力をパースする
func (m *Manager) parseWorktreeList(output string, repoName, repoPath string) ([]Worktree, error) {
	var worktrees []Worktree
	var current *Worktree
	isFirst := true

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "worktree ") {
			if current != nil {
				worktrees = append(worktrees, *current)
			}
			path := strings.TrimPrefix(line, "worktree ")
			current = &Worktree{
				Path:      path,
				RepoName:  repoName,
				RepoPath:  repoPath,
				IsManaged: m.isManagedWorktree(path, repoName),
				IsMain:    isFirst, // 最初のworktreeはメイン（リポジトリ本体）
			}
			isFirst = false
		} else if current != nil {
			if strings.HasPrefix(line, "branch ") {
				branch := strings.TrimPrefix(line, "branch ")
				// refs/heads/を除去
				branch = strings.TrimPrefix(branch, "refs/heads/")
				current.Branch = branch
			} else if line == "HEAD" {
				current.IsCurrent = true
			} else if line == "detached" {
				current.IsDetached = true
			}
		}
	}

	if current != nil {
		worktrees = append(worktrees, *current)
	}

	return worktrees, nil
}

// GetWorktreePath は指定されたリポジトリとブランチのworktreeパスを返す
func (m *Manager) GetWorktreePath(repoName, branch string) string {
	return m.getWorktreePath(repoName, branch)
}

// getWorktreePath は内部用のworktreeパス取得関数
func (m *Manager) getWorktreePath(repoName, branch string) string {
	// ブランチ名の/を_に置換（ディレクトリ階層を避けるため）
	safeBranch := strings.ReplaceAll(branch, "/", "_")
	return filepath.Join(m.worktreeDir, repoName, safeBranch)
}

// isManagedWorktree はworktreeがccvaletで管理されているか確認する
func (m *Manager) isManagedWorktree(path, repoName string) bool {
	expectedPrefix := filepath.Join(m.worktreeDir, repoName)
	return strings.HasPrefix(path, expectedPrefix)
}

// runSetup はworktree作成後のセットアップを実行する
// 優先順位: 1. config.yamlのsetup（コマンドリスト） 2. Convention-basedスクリプト
func (m *Manager) runSetup(wt *Worktree, repo *config.RepositoryConfig) error {
	debugLog("[SETUP] runSetup called for worktree: %s, repo: %s", wt.Path, repo.Name)
	debugLog("[SETUP] Setup commands: %v", repo.Setup)

	// 1. config.yamlのsetupコマンドリスト
	if len(repo.Setup) > 0 {
		debugLog("[SETUP] Running %d setup commands", len(repo.Setup))
		return m.runSetupCommands(wt, repo.Setup)
	}

	// 2. Convention-based スクリプト: ~/.ccvalet/setup-scripts/{repo-name}.sh
	conventionScript := m.GetSetupScriptPath(repo.Name)
	if _, err := os.Stat(conventionScript); err == nil {
		debugLog("[SETUP] Found convention-based script: %s", conventionScript)
		return m.runSetupScript(wt, conventionScript)
	}

	debugLog("[SETUP] No setup configured for repository %s", repo.Name)
	return nil
}

// GetSetupScriptPath はリポジトリのConvention-basedセットアップスクリプトのパスを返す
func (m *Manager) GetSetupScriptPath(repoName string) string {
	return filepath.Join(m.configDir, "setup-scripts", repoName+".sh")
}

// runSetupScript はセットアップスクリプトを実行する
func (m *Manager) runSetupScript(wt *Worktree, scriptPath string) error {
	// 相対パスの場合は元リポジトリからの相対パスとして解決
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(wt.RepoPath, scriptPath)
	}

	cmd := exec.Command(scriptPath)
	cmd.Dir = wt.Path
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = m.getSetupEnv(wt)

	return cmd.Run()
}

// runSetupCommands はセットアップコマンドを順に実行する
func (m *Manager) runSetupCommands(wt *Worktree, commands []string) error {
	env := m.getSetupEnv(wt)
	for _, cmdStr := range commands {
		cmd := exec.Command("sh", "-c", cmdStr)
		cmd.Dir = wt.Path
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = env

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("setup command failed '%s': %w", cmdStr, err)
		}
	}
	return nil
}

// getSetupEnv はセットアップスクリプト/コマンド用の環境変数を返す
func (m *Manager) getSetupEnv(wt *Worktree) []string {
	env := os.Environ()
	env = append(env,
		"CCVALET_SOURCE_REPO="+wt.RepoPath,
		"CCVALET_WORKTREE="+wt.Path,
		"CCVALET_BRANCH="+wt.Branch,
		"CCVALET_REPO_NAME="+wt.RepoName,
	)
	return env
}

// Exists はworktreeが存在するか確認する
func (m *Manager) Exists(repoName, branch string) bool {
	path := m.getWorktreePath(repoName, branch)
	_, err := os.Stat(path)
	return err == nil
}

// Get は指定されたworktreeの情報を返す（管理されているworktreeのみ）
func (m *Manager) Get(repoName, branch string) (*Worktree, error) {
	worktrees, err := m.List(repoName)
	if err != nil {
		return nil, err
	}

	for _, wt := range worktrees {
		if wt.Branch == branch && wt.IsManaged {
			return &wt, nil
		}
	}

	return nil, fmt.Errorf("worktree not found: %s/%s", repoName, branch)
}

// GetByBranch はブランチ名で指定されたworktreeの情報を返す（すべてのworktree対象）
func (m *Manager) GetByBranch(repoName, branch string) (*Worktree, error) {
	worktrees, err := m.List(repoName)
	if err != nil {
		return nil, err
	}

	for _, wt := range worktrees {
		if wt.Branch == branch {
			return &wt, nil
		}
	}

	return nil, fmt.Errorf("worktree not found for branch: %s/%s", repoName, branch)
}

// GetByName はworktree名（パスのディレクトリ名）で指定されたworktreeの情報を返す
func (m *Manager) GetByName(repoName, worktreeName string) (*Worktree, error) {
	worktrees, err := m.List(repoName)
	if err != nil {
		return nil, err
	}

	for _, wt := range worktrees {
		// パスの最後のディレクトリ名がworktree名
		name := filepath.Base(wt.Path)
		if name == worktreeName {
			return &wt, nil
		}
	}

	return nil, fmt.Errorf("worktree not found: %s/%s", repoName, worktreeName)
}

// BranchExists はブランチが存在するか確認する（ローカル + リモート）
func (m *Manager) BranchExists(repoPath, branch string) bool {
	// ローカルブランチをチェック
	cmd := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err := cmd.Run(); err == nil {
		return true
	}

	// リモートブランチをチェック
	cmd = exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return cmd.Run() == nil
}

// ListBranches はローカル+リモートブランチの一覧を返す
func (m *Manager) ListBranches(repoPath string) []string {
	var branches []string

	// ローカルブランチ
	cmd := exec.Command("git", "-C", repoPath, "branch", "--format=%(refname:short)")
	if output, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" {
				branches = append(branches, line)
			}
		}
	}

	// リモートブランチ（origin/を除去）
	cmd = exec.Command("git", "-C", repoPath, "branch", "-r", "--format=%(refname:short)")
	if output, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" && strings.HasPrefix(line, "origin/") {
				branch := strings.TrimPrefix(line, "origin/")
				if branch != "HEAD" {
					branches = append(branches, branch)
				}
			}
		}
	}

	// 重複を除去
	seen := make(map[string]bool)
	var unique []string
	for _, b := range branches {
		if !seen[b] {
			seen[b] = true
			unique = append(unique, b)
		}
	}

	return unique
}

// getWorktreePathByName はworktree名からworktreeパスを返す
func (m *Manager) getWorktreePathByName(repoName, worktreeName string) string {
	// worktree名の/を_に置換（ディレクトリ階層を避けるため）
	safeName := strings.ReplaceAll(worktreeName, "/", "_")
	return filepath.Join(m.worktreeDir, repoName, safeName)
}

// generateWorktreeName はブランチ名からworktree名を生成する（重複時はサフィックス付与）
func (m *Manager) generateWorktreeName(repoName, branch string) string {
	// ブランチ名をベースに
	baseName := branch
	return m.ensureUniqueWorktreeName(repoName, baseName)
}

// ensureUniqueWorktreeName は重複しないworktree名を返す
func (m *Manager) ensureUniqueWorktreeName(repoName, baseName string) string {
	name := baseName
	suffix := 2

	for {
		path := m.getWorktreePathByName(repoName, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%d", baseName, suffix)
		suffix++
	}
}

// HasUncommittedChanges は未コミットの変更があるか確認する（untracked filesは除外）
func HasUncommittedChanges(workDir string) (bool, error) {
	cmd := exec.Command("git", "-C", workDir, "status", "--porcelain", "-uno")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check git status: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// GetCurrentBranch は現在のブランチ名を取得する
func GetCurrentBranch(workDir string) (string, error) {
	cmd := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// CheckoutBranch は指定されたブランチにチェックアウトする
func CheckoutBranch(workDir, branch string) error {
	cmd := exec.Command("git", "-C", workDir, "checkout", branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to checkout branch %s: %s", branch, string(output))
	}
	return nil
}

// CreateAndCheckoutBranch は新しいブランチを作成してチェックアウトする
func CreateAndCheckoutBranch(workDir, branch, baseBranch string) error {
	var cmd *exec.Cmd
	if baseBranch != "" {
		// origin/ プレフィックスがない場合は追加
		base := baseBranch
		if !strings.HasPrefix(baseBranch, "origin/") && !strings.HasPrefix(baseBranch, "refs/") {
			// Check if it's a remote branch
			checkCmd := exec.Command("git", "-C", workDir, "rev-parse", "--verify", "origin/"+baseBranch)
			if checkCmd.Run() == nil {
				base = "origin/" + baseBranch
			}
		}
		cmd = exec.Command("git", "-C", workDir, "checkout", "-b", branch, base)
	} else {
		// baseBranchが指定されていない場合は現在のHEADから作成
		cmd = exec.Command("git", "-C", workDir, "checkout", "-b", branch)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create and checkout branch %s: %s", branch, string(output))
	}
	return nil
}

// IsBranchCheckedOutElsewhere は指定されたブランチが別のworktreeでチェックアウトされているか確認する
func (m *Manager) IsBranchCheckedOutElsewhere(repoName, branch, currentWorkDir string) (bool, string, error) {
	worktrees, err := m.List(repoName)
	if err != nil {
		return false, "", err
	}

	for _, wt := range worktrees {
		if wt.Branch == branch && wt.Path != currentWorkDir {
			return true, wt.Path, nil
		}
	}

	return false, "", nil
}

// FetchRemoteBranch はリモートブランチをfetchする
func FetchRemoteBranch(workDir, branch string) error {
	cmd := exec.Command("git", "-C", workDir, "fetch", "origin", branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to fetch branch %s: %s", branch, string(output))
	}
	return nil
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
