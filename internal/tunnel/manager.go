package tunnel

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/takaaki-s/claude-code-valet/internal/config"
)

const (
	// DefaultRemoteSocketPath はリモート側のデフォルトdaemonソケットパス
	DefaultRemoteSocketPath = ".ccvalet/run/daemon.sock"

	// localSocketDir はトンネル用ローカルソケットの配置ディレクトリ
	localSocketDir = "/tmp/ccvalet-tunnels"

	// healthCheckInterval はトンネルのヘルスチェック間隔
	healthCheckInterval = 30 * time.Second
)

// Tunnel はリモートホストへのトンネル接続を表す
type Tunnel struct {
	HostID      string
	HostType    string // "ssh" or "docker"
	LocalSocket string // ローカル側のソケットパス
	process     *os.Process
	cmd         *exec.Cmd
}

// Manager はトンネル接続を管理する
type Manager struct {
	mu      sync.RWMutex
	tunnels map[string]*Tunnel
}

// NewManager は新しいトンネルマネージャを作成する
func NewManager() *Manager {
	return &Manager{
		tunnels: make(map[string]*Tunnel),
	}
}

// Open はホスト設定に基づいてトンネルを開く
func (m *Manager) Open(hostConfig config.HostConfig) (string, error) {
	switch hostConfig.Type {
	case "ssh":
		return m.OpenSSH(hostConfig)
	case "docker":
		return m.OpenDocker(hostConfig)
	default:
		return "", fmt.Errorf("unsupported host type: %s", hostConfig.Type)
	}
}

// OpenSSH はSSHトンネルを開いてリモートのdaemonソケットをフォワードする
// ssh -L {localSocket}:{remoteSocket} -N -f {host}
func (m *Manager) OpenSSH(hostConfig config.HostConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 既に接続中なら既存のソケットを返す
	if t, ok := m.tunnels[hostConfig.ID]; ok {
		if m.isAlive(t) {
			return t.LocalSocket, nil
		}
		// 死んでいたらクリーンアップして再接続
		m.closeLocked(hostConfig.ID)
	}

	// ローカルソケットパスを生成
	localSocket, err := m.prepareLocalSocket(hostConfig.ID)
	if err != nil {
		return "", fmt.Errorf("failed to prepare local socket: %w", err)
	}

	// リモートソケットパスを決定
	// SSH -Lのリモートパスは絶対パスが必要（相対パスはsshdで正しく解決されない）
	remoteSocket := hostConfig.SocketPath
	if remoteSocket == "" {
		// リモートのホームディレクトリを取得して絶対パスを構築
		remoteHome, err := m.getRemoteHome(hostConfig)
		if err != nil {
			os.Remove(localSocket)
			return "", fmt.Errorf("failed to get remote home directory: %w", err)
		}
		remoteSocket = remoteHome + "/" + DefaultRemoteSocketPath
	}

	// SSHコマンドを構築
	// ユーザーのssh_optsの前にオーバーライドを追加（SSHはfirst match winsルール）
	// - ControlMaster=no: トンネル専用の長寿命接続のためControlMasterを使わない
	// - ExitOnForwardFailure=no: ssh_configのLocalForward/RemoteForwardが
	//   ポート競合で失敗してもSSHを中断させない（トンネル用-Lは別途waitForSocketで検証）
	// 注: ClearAllForwardings=yesはコマンドラインの-Lも消すため使えない
	args := make([]string, 0, len(hostConfig.SSHOpts)+10)
	args = append(args, "-o", "ControlMaster=no", "-o", "ExitOnForwardFailure=no")
	args = append(args, hostConfig.SSHOpts...)
	args = append(args,
		"-L", localSocket+":"+remoteSocket,
		"-N",    // リモートコマンドを実行しない
		hostConfig.Host,
	)

	cmd := exec.Command("ssh", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		os.Remove(localSocket)
		return "", fmt.Errorf("failed to start SSH tunnel to %s: %w", hostConfig.Host, err)
	}

	tunnel := &Tunnel{
		HostID:      hostConfig.ID,
		HostType:    "ssh",
		LocalSocket: localSocket,
		process:     cmd.Process,
		cmd:         cmd,
	}
	m.tunnels[hostConfig.ID] = tunnel

	// ソケットが利用可能になるまで待つ
	if err := m.waitForSocket(localSocket, 10*time.Second); err != nil {
		m.closeLocked(hostConfig.ID)
		return "", fmt.Errorf("SSH tunnel to %s started but socket not available: %w", hostConfig.Host, err)
	}

	return localSocket, nil
}

// OpenDocker はDockerコンテナのdaemonソケットへの接続を設定する
// Dockerの場合、ボリュームマウント経由でソケットを共有する想定
func (m *Manager) OpenDocker(hostConfig config.HostConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 既に設定済みなら既存のソケットを返す
	if t, ok := m.tunnels[hostConfig.ID]; ok {
		return t.LocalSocket, nil
	}

	// Dockerの場合、ソケットパスはボリュームマウントで直接アクセス可能な前提
	// SSHと同じ規約でhost IDからローカルソケットパスを自動計算
	// 例: docker run -v /tmp/ccvalet-tunnels/docker-dev:/root/.ccvalet/run container
	//     → /tmp/ccvalet-tunnels/docker-dev/daemon.sock でアクセス
	localSocket := filepath.Join(localSocketDir, hostConfig.ID, "daemon.sock")
	os.MkdirAll(filepath.Dir(localSocket), 0700)

	tunnel := &Tunnel{
		HostID:      hostConfig.ID,
		HostType:    "docker",
		LocalSocket: localSocket,
		process:     nil, // Dockerではプロセス管理不要
		cmd:         nil,
	}
	m.tunnels[hostConfig.ID] = tunnel

	return localSocket, nil
}

// Close は指定したホストのトンネルを閉じる
func (m *Manager) Close(hostID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeLocked(hostID)
}

// CloseAll は全てのトンネルを閉じる
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for hostID := range m.tunnels {
		m.closeLocked(hostID)
	}
}

// LocalSocket はホストIDに対応するローカルソケットパスを返す
func (m *Manager) LocalSocket(hostID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if t, ok := m.tunnels[hostID]; ok {
		return t.LocalSocket
	}
	return ""
}

// IsAlive はトンネルが生存しているかを返す
func (m *Manager) IsAlive(hostID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.tunnels[hostID]
	if !ok {
		return false
	}
	return m.isAlive(t)
}

// closeLocked はロック取得済みの状態でトンネルを閉じる
func (m *Manager) closeLocked(hostID string) error {
	t, ok := m.tunnels[hostID]
	if !ok {
		return nil
	}

	// SSHプロセスを終了
	if t.process != nil {
		t.process.Kill()
		if t.cmd != nil {
			t.cmd.Wait()
		}
	}

	// ローカルソケットファイルを削除
	if t.HostType == "ssh" {
		os.Remove(t.LocalSocket)
	}

	delete(m.tunnels, hostID)
	return nil
}

// isAlive はトンネルが生存しているかを確認する
func (m *Manager) isAlive(t *Tunnel) bool {
	if t.HostType == "docker" {
		// Dockerの場合、ソケットファイルの存在確認
		_, err := os.Stat(t.LocalSocket)
		return err == nil
	}

	// SSHの場合、プロセスの生存確認
	if t.process == nil {
		return false
	}
	// Unixではプロセスにシグナル0を送って生存確認
	err := t.process.Signal(os.Signal(nil))
	return err == nil
}

// prepareLocalSocket はローカルソケットのパスを準備する
func (m *Manager) prepareLocalSocket(hostID string) (string, error) {
	dir := filepath.Join(localSocketDir, hostID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}

	socketPath := filepath.Join(dir, "daemon.sock")

	// 既存のソケットファイルがあれば削除
	os.Remove(socketPath)

	return socketPath, nil
}

// waitForSocket はソケットが利用可能になるまで待つ
func (m *Manager) waitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for socket %s", socketPath)
}

// getRemoteHome はSSH経由でリモートのホームディレクトリを取得する
func (m *Manager) getRemoteHome(hostConfig config.HostConfig) (string, error) {
	args := []string{"-o", "ControlMaster=no", "-o", "ClearAllForwardings=yes"}
	args = append(args, hostConfig.SSHOpts...)
	args = append(args, hostConfig.Host, "echo $HOME")

	cmd := exec.Command("ssh", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	home := strings.TrimSpace(string(out))
	if home == "" {
		return "", fmt.Errorf("empty home directory")
	}
	return home, nil
}
