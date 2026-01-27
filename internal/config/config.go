package config

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/viper"
)

// RepositoryConfig はリポジトリの設定を表す
type RepositoryConfig struct {
	Path       string   `mapstructure:"path"`
	Name       string   `mapstructure:"name"`
	BaseBranch string   `mapstructure:"basebranch,omitempty"`
	Setup      []string `mapstructure:"setup,omitempty"`
}

// ParallelConfig は並列管理の設定を表す
type ParallelConfig struct {
	MaxParallel int `mapstructure:"max_parallel"`
}

// KeybindingsConfig はキーバインド設定を表す
type KeybindingsConfig struct {
	// セッション一覧画面
	Up      []string `mapstructure:"up,omitempty"`
	Down    []string `mapstructure:"down,omitempty"`
	Attach  []string `mapstructure:"attach,omitempty"`
	New     []string `mapstructure:"new,omitempty"`
	Kill    []string `mapstructure:"kill,omitempty"`
	Delete  []string `mapstructure:"delete,omitempty"`
	Cancel  []string `mapstructure:"cancel,omitempty"`
	Refresh []string `mapstructure:"refresh,omitempty"`
	Resume  []string `mapstructure:"resume,omitempty"`
	Quit    []string `mapstructure:"quit,omitempty"`
	Help    []string `mapstructure:"help,omitempty"`

	// セッション作成フォーム
	NextField      []string `mapstructure:"next_field,omitempty"`
	PrevField      []string `mapstructure:"prev_field,omitempty"`
	ToggleWorktree []string `mapstructure:"toggle_worktree,omitempty"`
	ToggleBranch   []string `mapstructure:"toggle_branch,omitempty"`
	Submit         []string `mapstructure:"submit,omitempty"`
	CancelForm     []string `mapstructure:"cancel_form,omitempty"`

	// アタッチ中のキー
	Detach []string `mapstructure:"detach,omitempty"`
}

// Config はアプリケーション全体の設定を表す
type Config struct {
	Parallel     ParallelConfig     `mapstructure:"parallel"`
	Repositories []RepositoryConfig `mapstructure:"repositories"`
	Keybindings  KeybindingsConfig  `mapstructure:"keybindings,omitempty"` // キーバインド設定
}

// Manager は設定ファイルの読み書きを管理する
type Manager struct {
	v        *viper.Viper
	mu       sync.RWMutex
	config   *Config
	filePath string
}

// NewManager は新しい設定マネージャを作成する
func NewManager(dataDir string) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(dataDir)

	m := &Manager{
		v:        v,
		filePath: filepath.Join(dataDir, "config.yaml"),
		config:   defaultConfig(),
	}

	if err := m.load(); err != nil {
		// ファイルが存在しない場合はデフォルト設定を使用
		if !os.IsNotExist(err) {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, err
			}
		}
	}

	return m, nil
}

// defaultConfig はデフォルト設定を返す
func defaultConfig() *Config {
	return &Config{
		Parallel: ParallelConfig{
			MaxParallel: 3, // デフォルト並列数
		},
		Repositories: []RepositoryConfig{},
	}
}

// load は設定ファイルを読み込む
func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.v.ReadInConfig(); err != nil {
		return err
	}

	cfg := &Config{}
	if err := m.v.Unmarshal(cfg); err != nil {
		return err
	}

	m.config = cfg
	return nil
}

// Save は設定をファイルに保存する
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// viperのSetは構造体のmapstructureタグを無視するため、明示的にキーを設定
	m.v.Set("parallel.max_parallel", m.config.Parallel.MaxParallel)
	m.v.Set("repositories", m.config.Repositories)

	return m.v.WriteConfigAs(m.filePath)
}

// Get は現在の設定を返す
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// コピーを返す
	cfg := *m.config
	repos := make([]RepositoryConfig, len(m.config.Repositories))
	copy(repos, m.config.Repositories)
	cfg.Repositories = repos
	return &cfg
}

// GetRepositories はリポジトリ一覧を返す
func (m *Manager) GetRepositories() []RepositoryConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	repos := make([]RepositoryConfig, len(m.config.Repositories))
	copy(repos, m.config.Repositories)
	return repos
}

// GetRepository は指定した名前のリポジトリを返す
func (m *Manager) GetRepository(name string) *RepositoryConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, repo := range m.config.Repositories {
		if repo.Name == name {
			r := repo
			return &r
		}
	}
	return nil
}

// AddRepository はリポジトリを追加する
func (m *Manager) AddRepository(repo RepositoryConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 重複チェック
	for _, r := range m.config.Repositories {
		if r.Name == repo.Name {
			return ErrRepositoryExists
		}
	}

	m.config.Repositories = append(m.config.Repositories, repo)
	return nil
}

// RemoveRepository はリポジトリを削除する
func (m *Manager) RemoveRepository(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, repo := range m.config.Repositories {
		if repo.Name == name {
			m.config.Repositories = append(
				m.config.Repositories[:i],
				m.config.Repositories[i+1:]...,
			)
			return nil
		}
	}
	return ErrRepositoryNotFound
}

// UpdateRepository はリポジトリを更新する
func (m *Manager) UpdateRepository(name string, repo RepositoryConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.config.Repositories {
		if r.Name == name {
			m.config.Repositories[i] = repo
			return nil
		}
	}
	return ErrRepositoryNotFound
}

// GetWorktreeDir はworktreeの配置ディレクトリを返す
func (m *Manager) GetWorktreeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccvalet", "worktrees")
}

// GetMaxParallel は最大並列数を返す
func (m *Manager) GetMaxParallel() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config.Parallel.MaxParallel <= 0 {
		return 3 // デフォルト値
	}
	return m.config.Parallel.MaxParallel
}

// GetShell はClaude Code起動時のシェルを返す
// 環境変数$SHELLを使用、未設定時は/bin/sh
func (m *Manager) GetShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// DefaultKeybindings はデフォルトのキーバインドを返す
func DefaultKeybindings() KeybindingsConfig {
	return KeybindingsConfig{
		// セッション一覧画面
		Up:      []string{"up", "k"},
		Down:    []string{"down", "j"},
		Attach:  []string{"enter"},
		New:     []string{"n"},
		Kill:    []string{"s", "x"},
		Delete:  []string{"d"},
		Cancel:  []string{"c"},
		Refresh: []string{"r"},
		Resume:  []string{"R"},
		Quit:    []string{"q", "ctrl+c"},
		Help:    []string{"?"},

		// セッション作成フォーム
		NextField:      []string{"tab"},
		PrevField:      []string{"shift+tab"},
		ToggleWorktree: []string{"ctrl+w"},
		ToggleBranch:   []string{"ctrl+b"},
		Submit:         []string{"enter"},
		CancelForm:     []string{"esc"},

		// アタッチ中のキー
		Detach: []string{"ctrl+]"},
	}
}

// GetKeybindings はキーバインド設定を返す（未設定の項目はデフォルト値を使用）
func (m *Manager) GetKeybindings() KeybindingsConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	defaults := DefaultKeybindings()
	cfg := m.config.Keybindings

	// 未設定の項目はデフォルト値を使用
	if len(cfg.Up) == 0 {
		cfg.Up = defaults.Up
	}
	if len(cfg.Down) == 0 {
		cfg.Down = defaults.Down
	}
	if len(cfg.Attach) == 0 {
		cfg.Attach = defaults.Attach
	}
	if len(cfg.New) == 0 {
		cfg.New = defaults.New
	}
	if len(cfg.Kill) == 0 {
		cfg.Kill = defaults.Kill
	}
	if len(cfg.Delete) == 0 {
		cfg.Delete = defaults.Delete
	}
	if len(cfg.Cancel) == 0 {
		cfg.Cancel = defaults.Cancel
	}
	if len(cfg.Refresh) == 0 {
		cfg.Refresh = defaults.Refresh
	}
	if len(cfg.Resume) == 0 {
		cfg.Resume = defaults.Resume
	}
	if len(cfg.Quit) == 0 {
		cfg.Quit = defaults.Quit
	}
	if len(cfg.Help) == 0 {
		cfg.Help = defaults.Help
	}
	if len(cfg.NextField) == 0 {
		cfg.NextField = defaults.NextField
	}
	if len(cfg.PrevField) == 0 {
		cfg.PrevField = defaults.PrevField
	}
	if len(cfg.ToggleWorktree) == 0 {
		cfg.ToggleWorktree = defaults.ToggleWorktree
	}
	if len(cfg.ToggleBranch) == 0 {
		cfg.ToggleBranch = defaults.ToggleBranch
	}
	if len(cfg.Submit) == 0 {
		cfg.Submit = defaults.Submit
	}
	if len(cfg.CancelForm) == 0 {
		cfg.CancelForm = defaults.CancelForm
	}
	if len(cfg.Detach) == 0 {
		cfg.Detach = defaults.Detach
	}

	return cfg
}

// GetDetachKey はアタッチ中のデタッチキーのバイト値を返す
func (m *Manager) GetDetachKey() byte {
	m.mu.RLock()
	defer m.mu.RUnlock()

	detachKeys := m.config.Keybindings.Detach
	if len(detachKeys) == 0 {
		detachKeys = DefaultKeybindings().Detach
	}

	return parseKeyToByte(detachKeys[0])
}

// GetDetachKeyHint はデタッチキーの表示用文字列を返す
func (m *Manager) GetDetachKeyHint() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	detachKeys := m.config.Keybindings.Detach
	if len(detachKeys) == 0 {
		detachKeys = DefaultKeybindings().Detach
	}

	return formatKeyHint(detachKeys[0])
}

// GetDetachKeyCSIu はデタッチキーのCSI uシーケンスを返す
func (m *Manager) GetDetachKeyCSIu() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()

	detachKeys := m.config.Keybindings.Detach
	if len(detachKeys) == 0 {
		detachKeys = DefaultKeybindings().Detach
	}

	return parseKeyToCSIu(detachKeys[0])
}

// parseKeyToByte はキー文字列をバイト値に変換する
func parseKeyToByte(key string) byte {
	switch key {
	case "ctrl+^":
		return 0x1e
	case "ctrl+]":
		return 0x1d
	case "ctrl+\\":
		return 0x1c
	case "ctrl+g":
		return 0x07
	default:
		return 0x1d // デフォルト: ctrl+]
	}
}

// parseKeyToCSIu はキー文字列をCSI uシーケンスに変換する
// iTerm2等のCSI uモード対応ターミナル用
func parseKeyToCSIu(key string) []byte {
	switch key {
	case "ctrl+^":
		// Ctrl+Shift+6: keycode=54('6'), modifiers=6(Ctrl+Shift)
		return []byte("\x1b[54;6u")
	case "ctrl+]":
		// Ctrl+]: keycode=93(']'), modifiers=5(Ctrl)
		return []byte("\x1b[93;5u")
	case "ctrl+\\":
		// Ctrl+\: keycode=92('\'), modifiers=5(Ctrl)
		return []byte("\x1b[92;5u")
	case "ctrl+g":
		// Ctrl+G: keycode=103('g'), modifiers=5(Ctrl)
		return []byte("\x1b[103;5u")
	default:
		return []byte("\x1b[93;5u") // デフォルト: ctrl+]
	}
}

// formatKeyHint はキー文字列を表示用にフォーマットする
func formatKeyHint(key string) string {
	switch key {
	case "ctrl+^":
		return "Ctrl+^"
	case "ctrl+]":
		return "Ctrl+]"
	case "ctrl+\\":
		return "Ctrl+\\"
	case "ctrl+g":
		return "Ctrl+G"
	default:
		return "Ctrl+]"
	}
}
