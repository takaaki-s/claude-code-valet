package config

import (
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// State はアプリケーションの状態を表す（設定ではない一時的な状態）
type State struct {
	LastUsedRepository string `yaml:"last_used_repository,omitempty"`
}

// StateManager は状態ファイルの読み書きを管理する
type StateManager struct {
	mu       sync.RWMutex
	state    *State
	filePath string
}

// NewStateManager は新しい状態マネージャを作成する
func NewStateManager(dataDir string) (*StateManager, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	m := &StateManager{
		filePath: filepath.Join(dataDir, "state.yaml"),
		state:    &State{},
	}

	if err := m.load(); err != nil {
		// ファイルが存在しない場合は空の状態を使用
		if !os.IsNotExist(err) {
			return nil, err
		}
	}

	return m, nil
}

// load は状態ファイルを読み込む
func (m *StateManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return err
	}

	state := &State{}
	if err := yaml.Unmarshal(data, state); err != nil {
		return err
	}

	m.state = state
	return nil
}

// Save は状態をファイルに保存する
func (m *StateManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := yaml.Marshal(m.state)
	if err != nil {
		return err
	}

	return os.WriteFile(m.filePath, data, 0644)
}

// GetLastUsedRepository は前回使用したリポジトリ名を返す
func (m *StateManager) GetLastUsedRepository() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.LastUsedRepository
}

// SetLastUsedRepository は前回使用したリポジトリ名を設定する
func (m *StateManager) SetLastUsedRepository(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.LastUsedRepository = name
}
