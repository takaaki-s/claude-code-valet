package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
	"golang.org/x/term"
)

var editCmd = &cobra.Command{
	Use:   "edit <session-name>",
	Short: "Open editor in session's working directory",
	Long: `Open the editor (specified by EDITOR environment variable) in the session's working directory.

If EDITOR is not set, defaults to 'vim'.

Examples:
  ccvalet session edit my-session
  EDITOR=code ccvalet session edit my-session`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]
		client := daemon.NewClient(getSocketPath())

		sessions, err := client.List()
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}

		var workDir string
		for _, s := range sessions {
			if s.Name == nameOrID || s.ID == nameOrID {
				workDir = s.WorkDir
				break
			}
		}

		if workDir == "" {
			return fmt.Errorf("session not found or has no working directory: %s", nameOrID)
		}

		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vim"
		}

		// ターミナル状態を保存（エディタ終了後に復元するため）
		oldState, err := term.GetState(int(os.Stdin.Fd()))
		if err != nil {
			oldState = nil
		}

		editorCmd := exec.Command(editor, ".")
		editorCmd.Dir = workDir
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		runErr := editorCmd.Run()

		// ターミナル状態を復元
		if oldState != nil {
			term.Restore(int(os.Stdin.Fd()), oldState)
		}

		return runErr
	},
}

func init() {
	sessionCmd.AddCommand(editCmd)
}
