package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
)

var socketPathFlag string

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the daemon server",
	Long:  `Start the ccvalet daemon server. This must be running for session management.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check if daemon is already running
		client := daemon.NewClient(getSocketPath())
		if client.IsRunning() {
			return fmt.Errorf("daemon is already running. Use 'ccvalet daemon stop' to stop it first")
		}

		server, err := daemon.NewServer(getSocketPath(), getDataDir(), getConfigDir())
		if err != nil {
			return err
		}
		return server.Start()
	},
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon in the background",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := daemon.NewClient(getSocketPath())
		if client.IsRunning() {
			fmt.Println("Daemon is already running")
			return nil
		}

		// Start daemon in background
		exe, _ := os.Executable()
		daemonArgs := []string{"daemon"}
		if socketPathFlag != "" {
			daemonArgs = append(daemonArgs, "--socket", socketPathFlag)
		}
		daemonCmd := exec.Command(exe, daemonArgs...)
		daemonCmd.Env = os.Environ() // Inherit environment variables
		daemonCmd.Stdout = nil
		daemonCmd.Stderr = nil
		daemonCmd.Stdin = nil

		if err := daemonCmd.Start(); err != nil {
			return err
		}

		fmt.Printf("Daemon started (PID: %d)\n", daemonCmd.Process.Pid)
		return nil
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := daemon.NewClient(getSocketPath())
		if client.IsRunning() {
			fmt.Println("Daemon is running")
		} else {
			fmt.Println("Daemon is not running")
		}
		return nil
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := daemon.NewClient(getSocketPath())
		if !client.IsRunning() {
			fmt.Println("Daemon is not running")
			return nil
		}

		if err := client.Stop(); err != nil {
			return err
		}
		// Wait for daemon to actually exit (handleStop calls os.Exit in a goroutine)
		for i := 0; i < 30; i++ {
			if !client.IsRunning() {
				fmt.Println("Daemon stopped")
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Println("Daemon stopped")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonStopCmd)

	// --socket フラグ: Slaveモードでカスタムソケットパスを指定
	daemonCmd.PersistentFlags().StringVar(&socketPathFlag, "socket", "", "custom socket path (for slave mode)")
}

func getSocketPath() string {
	if socketPathFlag != "" {
		return socketPathFlag
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccvalet", "run", "daemon.sock")
}
