package repository

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/takaaki-s/claude-code-valet/internal/config"
)

// Registry はリポジトリの登録・管理を行う
type Registry struct {
	configMgr *config.Manager
}

// NewRegistry は新しいレジストリを作成する
func NewRegistry(configMgr *config.Manager) *Registry {
	return &Registry{
		configMgr: configMgr,
	}
}

// Add はリポジトリを登録する。登録した名前を返す。
func (r *Registry) Add(path string, name string, baseBranch string, setup []string) (string, error) {
	// パスを絶対パスに変換
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	// ディレクトリの存在確認
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("path does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", absPath)
	}

	// gitリポジトリか確認
	if !isGitRepository(absPath) {
		return "", fmt.Errorf("path is not a git repository: %s", absPath)
	}

	// 名前が指定されていない場合はgitから自動取得
	if name == "" {
		name = getRepoNameFromGit(absPath)
	}

	repo := config.RepositoryConfig{
		Path:       absPath,
		Name:       name,
		BaseBranch: baseBranch,
		Setup:      setup,
	}

	if err := r.configMgr.AddRepository(repo); err != nil {
		return "", err
	}

	if err := r.configMgr.Save(); err != nil {
		return "", err
	}

	return name, nil
}

// List は登録されているリポジトリ一覧を返す
func (r *Registry) List() []config.RepositoryConfig {
	return r.configMgr.GetRepositories()
}

// Get は指定した名前のリポジトリを返す
func (r *Registry) Get(name string) *config.RepositoryConfig {
	return r.configMgr.GetRepository(name)
}

// Remove はリポジトリを削除する
func (r *Registry) Remove(name string) error {
	if err := r.configMgr.RemoveRepository(name); err != nil {
		return err
	}
	return r.configMgr.Save()
}

// Update はリポジトリ設定を更新する
func (r *Registry) Update(name string, baseBranch string, setup []string) error {
	repo := r.configMgr.GetRepository(name)
	if repo == nil {
		return config.ErrRepositoryNotFound
	}

	repo.BaseBranch = baseBranch
	repo.Setup = setup

	if err := r.configMgr.UpdateRepository(name, *repo); err != nil {
		return err
	}
	return r.configMgr.Save()
}

// ValidatePath はパスが有効なgitリポジトリか確認する
func (r *Registry) ValidatePath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("path does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", absPath)
	}

	if !isGitRepository(absPath) {
		return fmt.Errorf("path is not a git repository: %s", absPath)
	}

	return nil
}

// GetRemoteURL はリポジトリのリモートURLを取得する
func (r *Registry) GetRemoteURL(name string) (string, error) {
	repo := r.configMgr.GetRepository(name)
	if repo == nil {
		return "", config.ErrRepositoryNotFound
	}

	cmd := exec.Command("git", "-C", repo.Path, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get remote url: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// GetBranches はリポジトリのブランチ一覧を取得する（ローカル＋リモート）
func (r *Registry) GetBranches(name string) ([]string, error) {
	repo := r.configMgr.GetRepository(name)
	if repo == nil {
		return nil, config.ErrRepositoryNotFound
	}

	cmd := exec.Command("git", "-C", repo.Path, "branch", "-a", "--format=%(refname:short)")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get branches: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	branches := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}

	return branches, nil
}

// GetLocalBranches はリポジトリのローカルブランチ一覧を取得する
func (r *Registry) GetLocalBranches(name string) ([]string, error) {
	repo := r.configMgr.GetRepository(name)
	if repo == nil {
		return nil, config.ErrRepositoryNotFound
	}

	cmd := exec.Command("git", "-C", repo.Path, "branch", "--format=%(refname:short)")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get local branches: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	branches := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}

	return branches, nil
}

// isGitRepository は指定したパスがgitリポジトリかどうかを確認する
func isGitRepository(path string) bool {
	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// getRepoNameFromGit はgitリポジトリから名前を取得する
// リモートURLから取得し、取得できない場合はディレクトリ名を使用
func getRepoNameFromGit(path string) string {
	// リモートURLから取得を試みる
	cmd := exec.Command("git", "-C", path, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err == nil {
		url := strings.TrimSpace(string(output))
		if name := extractRepoNameFromURL(url); name != "" {
			return name
		}
	}

	// リモートがない場合はディレクトリ名を使用
	return filepath.Base(path)
}

// extractRepoNameFromURL はURLからリポジトリ名を抽出する
// 例: git@github.com:user/repo.git -> repo
//     https://github.com/user/repo.git -> repo
func extractRepoNameFromURL(url string) string {
	// .git サフィックスを除去
	url = strings.TrimSuffix(url, ".git")

	// SSH形式: git@github.com:user/repo
	if strings.Contains(url, ":") && strings.Contains(url, "@") {
		parts := strings.Split(url, ":")
		if len(parts) == 2 {
			pathParts := strings.Split(parts[1], "/")
			if len(pathParts) > 0 {
				return pathParts[len(pathParts)-1]
			}
		}
	}

	// HTTPS形式: https://github.com/user/repo
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}

	return ""
}
