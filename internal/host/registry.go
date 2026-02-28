package host

import (
	"sync"

	"github.com/takaaki-s/claude-code-valet/internal/config"
	"github.com/takaaki-s/claude-code-valet/internal/notify"
	"github.com/takaaki-s/claude-code-valet/internal/session"
)

// SlaveClient はリモートslaveデーモンとの通信インターフェース
type SlaveClient interface {
	IsRunning() bool
	ListWithHostID() ([]session.Info, error)
	NotificationHistoryWithHostID() ([]notify.Entry, error)
}

// Host はホストとそのslave clientのペア
type Host struct {
	ID     string            // "local", "ec2", "docker-dev" 等
	Type   string            // "local", "ssh", "docker"
	Config config.HostConfig // ホスト設定
	Client SlaveClient       // slave daemonへの接続（インターフェース）
}

// Registry はホストの一覧を管理する
type Registry struct {
	mu    sync.RWMutex
	hosts map[string]*Host
	local *Host
}

// NewRegistry は設定からHostRegistryを構築する
func NewRegistry(hostConfigs []config.HostConfig) *Registry {
	r := &Registry{
		hosts: make(map[string]*Host),
	}

	// ローカルホストは常に登録
	r.local = &Host{
		ID:     "local",
		Type:   "local",
		Config: config.HostConfig{ID: "local", Type: "local"},
	}
	r.hosts["local"] = r.local

	// 設定ファイルのホストを登録（Clientはトンネル確立後に設定）
	for _, hc := range hostConfigs {
		r.hosts[hc.ID] = &Host{
			ID:     hc.ID,
			Type:   hc.Type,
			Config: hc,
			Client: nil,
		}
	}

	return r
}

// Get はホストIDからHostを取得する
func (r *Registry) Get(hostID string) (*Host, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if hostID == "" || hostID == "local" {
		return r.local, true
	}
	h, ok := r.hosts[hostID]
	return h, ok
}

// Local はローカルホストを返す
func (r *Registry) Local() *Host {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.local
}

// All は全ホストを返す（ローカルが先頭）
func (r *Registry) All() []*Host {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := []*Host{r.local}
	for _, h := range r.hosts {
		if h.ID != "local" {
			result = append(result, h)
		}
	}
	return result
}

// Remotes はリモートホストのみを返す
func (r *Registry) Remotes() []*Host {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Host
	for _, h := range r.hosts {
		if h.ID != "local" {
			result = append(result, h)
		}
	}
	return result
}

// AllIDs は全ホストIDを返す
func (r *Registry) AllIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := []string{"local"}
	for id := range r.hosts {
		if id != "local" {
			ids = append(ids, id)
		}
	}
	return ids
}

// SetClient はホストのslave clientを設定する（トンネル確立後に呼ぶ）
func (r *Registry) SetClient(hostID string, client SlaveClient) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h, ok := r.hosts[hostID]; ok {
		h.Client = client
	}
}

// IsConnected はホストが接続済みかどうかを返す
func (r *Registry) IsConnected(hostID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	h, ok := r.hosts[hostID]
	if !ok {
		return false
	}
	return h.Client != nil && h.Client.IsRunning()
}
