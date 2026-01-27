package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ccvalet",
	Short: "LLM session manager for Claude Code",
	Long:  `A CLI tool to manage multiple Claude Code sessions with attach/detach support.`,
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Global flags can be added here
}
