package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/prompt"
)

var promptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Manage prompt templates",
	Long: `Manage prompt templates for auto-injection.

Prompts are stored as markdown files in ~/.ccvalet/prompts/
The filename (without .md extension) becomes the prompt name.

Example:
  ~/.ccvalet/prompts/coding-task.md  →  prompt name: coding-task

Available variables for templates:
  ${args}        - User-provided arguments
  ${branch}      - Branch name
  ${repository}  - Repository name
  ${session}     - Session name
  ${workdir}     - Work directory path
  ${base_branch} - Base branch name`,
}

var promptListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List prompt templates",
	Long:    `List all available prompt templates.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := prompt.NewManager(getConfigDir())

		templates, err := mgr.List()
		if err != nil {
			return fmt.Errorf("failed to list prompts: %w", err)
		}

		if len(templates) == 0 {
			promptDir := filepath.Join(getConfigDir(), "prompts")
			fmt.Printf("No prompt templates found.\n")
			fmt.Printf("Place .md files in: %s\n", promptDir)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPATH")
		for _, t := range templates {
			fmt.Fprintf(w, "%s\t%s\n", t.Name, t.Path)
		}
		w.Flush()
		return nil
	},
}

var promptShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show prompt template content",
	Long:  `Show the content of a prompt template.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		mgr := prompt.NewManager(getConfigDir())

		template, err := mgr.Get(name)
		if err != nil {
			return fmt.Errorf("failed to get prompt: %w", err)
		}

		fmt.Printf("# %s\n", template.Name)
		fmt.Printf("# Path: %s\n", template.Path)
		fmt.Println("---")
		fmt.Println(template.Content)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(promptCmd)

	promptCmd.AddCommand(promptListCmd)
	promptCmd.AddCommand(promptShowCmd)
}
