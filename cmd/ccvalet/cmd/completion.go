package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
)

// completeSessionNames returns session names for shell completion
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	client := daemon.NewClient(getSocketPath())
	sessions, err := client.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, toComplete) {
			names = append(names, s.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
