package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

// supportedDetachKeys はサポートされるdetachキーの一覧
var supportedDetachKeys = []string{"ctrl+^", "ctrl+]", "ctrl+\\", "ctrl+g"}

// ValidateDetachKey はdetachキーがサポートされているか検証する
// サポート外の場合はサポートキー一覧を含むerrorを返す
func ValidateDetachKey(key string) error {
	for _, k := range supportedDetachKeys {
		if key == k {
			return nil
		}
	}
	return fmt.Errorf("unsupported detach key %q: supported keys are %s",
		key, strings.Join(supportedDetachKeys, ", "))
}

// KeybindingsConfig はキーバインド設定を表す
type KeybindingsConfig struct {
	// セッション一覧画面
	Up            []string `mapstructure:"up,omitempty"`
	Down          []string `mapstructure:"down,omitempty"`
	Attach        []string `mapstructure:"attach,omitempty"`
	New           []string `mapstructure:"new,omitempty"`
	Kill          []string `mapstructure:"kill,omitempty"`
	Delete        []string `mapstructure:"delete,omitempty"`
	Refresh       []string `mapstructure:"refresh,omitempty"`
	Quit          []string `mapstructure:"quit,omitempty"`
	Help          []string `mapstructure:"help,omitempty"`
	Search        []string `mapstructure:"search,omitempty"`
	Vscode        []string `mapstructure:"vscode,omitempty"`
	Notifications []string `mapstructure:"notifications,omitempty"`

	// セッション作成フォーム
	NextField      []string `mapstructure:"next_field,omitempty"`
	PrevField      []string `mapstructure:"prev_field,omitempty"`
	Submit []string `mapstructure:"submit,omitempty"`
	CancelForm     []string `mapstructure:"cancel_form,omitempty"`

	// アタッチ中のキー
	Detach []string `mapstructure:"detach,omitempty"`
}

// HostConfig はリモートホストの設定を表す
type HostConfig struct {
	ID         string   `mapstructure:"id"`                    // ホスト識別子 (例: "ec2", "docker-dev")
	Type       string   `mapstructure:"type"`                  // "ssh" or "docker"
	Host       string   `mapstructure:"host,omitempty"`        // SSH接続先 (例: "ec2-host")
	SSHOpts    []string `mapstructure:"ssh_opts,omitempty"`    // 追加SSHオプション
	Container  string   `mapstructure:"container,omitempty"`   // Dockerコンテナ名/ID
	SocketPath string   `mapstructure:"socket_path,omitempty"` // リモート側ソケットパス (デフォルト: ~/.ccvalet/run/daemon.sock)
}

// Config はアプリケーション全体の設定を表す
type Config struct {
	Keybindings KeybindingsConfig `mapstructure:"keybindings,omitempty"` // キーバインド設定
	Hosts       []HostConfig      `mapstructure:"hosts,omitempty"`       // リモートホスト設定
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
	return &Config{}
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

// Reload は設定ファイルを再読み込みする
func (m *Manager) Reload() error {
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

	return m.v.WriteConfigAs(m.filePath)
}

// Get は現在の設定を返す
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cfg := *m.config
	return &cfg
}

// GetHosts はリモートホスト一覧を返す
func (m *Manager) GetHosts() []HostConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	hosts := make([]HostConfig, len(m.config.Hosts))
	copy(hosts, m.config.Hosts)
	return hosts
}

// GetHost は指定したIDのホストを返す
func (m *Manager) GetHost(id string) *HostConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, h := range m.config.Hosts {
		if h.ID == id {
			hc := h
			return &hc
		}
	}
	return nil
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
		Up:            []string{"up", "k"},
		Down:          []string{"down", "j"},
		Attach:        []string{"enter"},
		New:           []string{"n"},
		Kill:          []string{"x"},
		Delete:        []string{"d"},
		Refresh:       []string{"r"},
		Quit:          []string{"q", "ctrl+c"},
		Help:          []string{"?"},
		Search:        []string{"/"},
		Vscode:        []string{"v"},
		Notifications: []string{"!"},

		// セッション作成フォーム
		NextField:      []string{"tab"},
		PrevField:      []string{"shift+tab"},
		Submit: []string{"enter"},
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
	if len(cfg.Refresh) == 0 {
		cfg.Refresh = defaults.Refresh
	}
	if len(cfg.Quit) == 0 {
		cfg.Quit = defaults.Quit
	}
	if len(cfg.Help) == 0 {
		cfg.Help = defaults.Help
	}
	if len(cfg.Search) == 0 {
		cfg.Search = defaults.Search
	}
	if len(cfg.Vscode) == 0 {
		cfg.Vscode = defaults.Vscode
	}
	if len(cfg.Notifications) == 0 {
		cfg.Notifications = defaults.Notifications
	}
	if len(cfg.NextField) == 0 {
		cfg.NextField = defaults.NextField
	}
	if len(cfg.PrevField) == 0 {
		cfg.PrevField = defaults.PrevField
	}
	if len(cfg.Submit) == 0 {
		cfg.Submit = defaults.Submit
	}
	if len(cfg.CancelForm) == 0 {
		cfg.CancelForm = defaults.CancelForm
	}
	if len(cfg.Detach) == 0 {
		cfg.Detach = defaults.Detach
	} else {
		// サポート外のキーをフィルタ
		var valid []string
		for _, k := range cfg.Detach {
			if err := ValidateDetachKey(k); err != nil {
				log.Printf("WARNING: %v", err)
			} else {
				valid = append(valid, k)
			}
		}
		if len(valid) == 0 {
			cfg.Detach = defaults.Detach
		} else {
			cfg.Detach = valid
		}
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

// formatKeyForTmux はキー文字列をtmuxのbind-key形式に変換する
func formatKeyForTmux(key string) string {
	switch key {
	case "ctrl+^":
		return "C-^"
	case "ctrl+]":
		return "C-]"
	case "ctrl+\\":
		return "C-\\"
	case "ctrl+g":
		return "C-g"
	default:
		return "C-]"
	}
}

// GetDetachKeyTmux はデタッチキーのtmux bind-key形式文字列を返す
func (m *Manager) GetDetachKeyTmux() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	detachKeys := m.config.Keybindings.Detach
	if len(detachKeys) == 0 {
		detachKeys = DefaultKeybindings().Detach
	}

	return formatKeyForTmux(detachKeys[0])
}
