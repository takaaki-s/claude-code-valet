package host

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/takaaki-s/claude-code-valet/internal/config"
)

const (
	// slaveStartTimeout はSlave起動コマンドのタイムアウト
	slaveStartTimeout = 30 * time.Second

	// defaultRemoteSocketPath はリモート側のデフォルトdaemonソケットパス
	defaultRemoteSocketPath = "~/.ccvalet/run/daemon.sock"
)

// StartSlaveCommand はリモートのSlaveデーモンを起動するコマンドを生成する
func StartSlaveCommand(hostConfig config.HostConfig) *exec.Cmd {
	socketPath := hostConfig.SocketPath
	if socketPath == "" {
		socketPath = defaultRemoteSocketPath
	}

	remoteCmd := fmt.Sprintf("ccvalet daemon start --socket %s", socketPath)

	switch hostConfig.Type {
	case "ssh":
		// ユーザーのssh_optsの前にオーバーライドを追加（SSHはfirst match winsルール）
		// - ControlMaster=no: 既存ControlMasterとの競合を回避
		// - ClearAllForwardings=yes: ssh_configのLocalForward/RemoteForwardを抑止
		//   （bootstrapはワンショットコマンド実行のためフォワーディング不要）
		args := make([]string, 0, len(hostConfig.SSHOpts)+6)
		args = append(args, "-o", "ControlMaster=no", "-o", "ClearAllForwardings=yes")
		args = append(args, hostConfig.SSHOpts...)
		args = append(args, hostConfig.Host, remoteCmd)
		return exec.Command("ssh", args...)
	case "docker":
		return exec.Command("docker", "exec", hostConfig.Container, "sh", "-c", remoteCmd)
	default:
		return nil
	}
}

// StartSlave はリモートのSlaveデーモンを起動し、結果を返す
// ccvaletが未インストールの場合は ErrNotInstalled を返す
func StartSlave(hostConfig config.HostConfig) error {
	cmd := StartSlaveCommand(hostConfig)
	if cmd == nil {
		return fmt.Errorf("unsupported host type: %s", hostConfig.Type)
	}

	ctx, cancel := context.WithTimeout(context.Background(), slaveStartTimeout)
	defer cancel()
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(output))

		// ccvaletが未インストールの場合を検出
		if isNotInstalled(outStr, err) {
			return fmt.Errorf("ccvalet is not installed on host '%s'. Install it first: go install github.com/takaaki-s/claude-code-valet/cmd/ccvalet@latest", hostConfig.ID)
		}

		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout starting slave on host '%s' (waited %s)", hostConfig.ID, slaveStartTimeout)
		}

		return fmt.Errorf("failed to start slave on host '%s': %s (output: %s)", hostConfig.ID, err, outStr)
	}

	return nil
}

// isNotInstalled はコマンドの出力からccvaletが未インストールかどうかを判定する
func isNotInstalled(output string, err error) bool {
	lower := strings.ToLower(output)
	// "ccvalet: command not found" や "ccvalet: not found" 等のシェルエラーを検出
	// SSHのインフラエラー（ControlPath等）の "not found" と区別するため、
	// "ccvalet" を含む行のみをチェックする
	for _, line := range strings.Split(lower, "\n") {
		if !strings.Contains(line, "ccvalet") {
			continue
		}
		if strings.Contains(line, "command not found") ||
			strings.Contains(line, "not found") ||
			strings.Contains(line, "no such file or directory") {
			return true
		}
	}
	// exit code 127 = command not found
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 127 {
			return true
		}
	}
	return false
}
