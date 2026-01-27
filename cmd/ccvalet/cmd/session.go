package cmd

import (
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:     "session",
	Aliases: []string{"sess"},
	Short:   "Manage Claude Code sessions",
	Long:    `Create, list, attach, and manage Claude Code sessions.`,
}

func init() {
	rootCmd.AddCommand(sessionCmd)
}
