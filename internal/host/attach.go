package host

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/takaaki-s/claude-code-valet/internal/config"
)

// sshControlPath returns the ControlMaster socket path for a host.
// Each host gets a unique socket so connections don't interfere with each other.
func sshControlPath(hostID string) string {
	return filepath.Join("/tmp", "ccvalet-ssh-ctrl-"+hostID)
}

// EnsureSSHMaster ensures a background ControlMaster SSH connection exists for the host.
// If a master is already running, this is a no-op.
// The master runs with -N (no remote command) and -f (background after auth),
// so it persists independently of any tmux pane processes.
func EnsureSSHMaster(hostConfig config.HostConfig) error {
	if hostConfig.Type != "ssh" {
		return nil
	}
	ctrlPath := sshControlPath(hostConfig.ID)

	// Check if master is already running
	check := exec.Command("ssh", "-o", "ControlPath="+ctrlPath, "-O", "check", hostConfig.Host)
	if check.Run() == nil {
		return nil // Master already running
	}

	// Start a new background master
	// -o ControlMaster=yes: this process IS the master
	// -o ControlPersist=300: master stays alive 300s after last slave disconnects
	// -N: no remote command (just keep the connection open)
	// -f: fork to background after authentication
	args := []string{
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=300",
		"-o", "ControlPath=" + ctrlPath,
		"-o", "ClearAllForwardings=yes",
	}
	args = append(args, hostConfig.SSHOpts...)
	args = append(args, "-N", "-f", hostConfig.Host)

	return exec.Command("ssh", args...).Run()
}

// AttachCommand はホストタイプに応じたtmux attachコマンドを生成する
// tmuxTarget は "ccvalet:sess-xxx" のようなtmuxターゲット文字列
func AttachCommand(hostConfig config.HostConfig, tmuxTarget string) *exec.Cmd {
	switch hostConfig.Type {
	case "ssh":
		return sshAttachCommand(hostConfig, tmuxTarget)
	case "docker":
		return dockerAttachCommand(hostConfig, tmuxTarget)
	default:
		// ローカル: 直接tmuxのselect-windowを使う
		return exec.Command("tmux", "-L", "ccvalet", "select-window", "-t", tmuxTarget)
	}
}

// AttachCommandString はホストタイプに応じたtmux attachコマンド文字列を生成する
// respawn-pane で実行するためのコマンド文字列を返す
func AttachCommandString(hostConfig config.HostConfig, tmuxTarget string) string {
	switch hostConfig.Type {
	case "ssh":
		remoteCmd := fmt.Sprintf("tmux -L ccvalet attach -t %s", tmuxTarget)
		ctrlPath := sshControlPath(hostConfig.ID)
		// ControlMaster=no: スレーブ専用（マスターはEnsureSSHMasterで事前に起動済み）
		// ControlPath: マスターのソケットを参照してSSH多重化（接続がほぼ即座になる）
		// ClearAllForwardings=yes: ssh_configのLocalForward/RemoteForwardのポート競合を回避
		cmd := fmt.Sprintf("ssh -o ControlMaster=no -o ControlPath=%s -o ClearAllForwardings=yes", ctrlPath)
		for _, opt := range hostConfig.SSHOpts {
			cmd += " " + opt
		}
		cmd += " -t " + hostConfig.Host + " '" + remoteCmd + "'"
		return cmd
	case "docker":
		return fmt.Sprintf("docker exec -it %s tmux -L ccvalet attach -t %s", hostConfig.Container, tmuxTarget)
	default:
		return fmt.Sprintf("tmux -L ccvalet attach -t %s", tmuxTarget)
	}
}

func sshAttachCommand(hostConfig config.HostConfig, tmuxTarget string) *exec.Cmd {
	remoteCmd := fmt.Sprintf("tmux -L ccvalet attach -t %s", tmuxTarget)
	ctrlPath := sshControlPath(hostConfig.ID)
	args := make([]string, 0, len(hostConfig.SSHOpts)+9)
	args = append(args, "-o", "ControlMaster=no",
		"-o", "ControlPath="+ctrlPath, "-o", "ClearAllForwardings=yes")
	args = append(args, hostConfig.SSHOpts...)
	args = append(args, "-t", hostConfig.Host, remoteCmd)
	return exec.Command("ssh", args...)
}

func dockerAttachCommand(hostConfig config.HostConfig, tmuxTarget string) *exec.Cmd {
	return exec.Command("docker", "exec", "-it", hostConfig.Container, "tmux", "-L", "ccvalet", "attach", "-t", tmuxTarget)
}
