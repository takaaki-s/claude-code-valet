package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
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

		editorCmd := exec.Command(editor, ".")
		editorCmd.Dir = workDir
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		return editorCmd.Run()
	},
}

func init() {
	sessionCmd.AddCommand(editCmd)
}
