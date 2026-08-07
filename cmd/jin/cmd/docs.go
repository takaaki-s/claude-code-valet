package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/agentdocs"
	"github.com/takaaki-s/jind-ai/internal/exitcode"
)

// docsListPayload is the `--json` shape of `jin docs list`.
//
// The list is wrapped in an object rather than returned as a bare array so
// later additions (a schema version, say) do not have to break every caller's
// jq expression, and so an empty set serialises as {"docs":[]} rather than
// null.
type docsListPayload struct {
	Docs []agentdocs.Doc `json:"docs"`
}

var docsCmd = &cobra.Command{
	Use:   "docs",
	Short: "Show the documentation for driving jin from an agent",
	Long: `Show the documentation jin ships for the AI agents that drive it.

The documents are compiled into the binary, so they always describe the jin
you are actually running. Both subcommands work without the daemon.

  jin docs list             List available documents with a summary of each
  jin docs show <name>      Print one document

Running 'jin docs' on its own is the same as 'jin docs list'.`,
	// A bare `jin docs` lists rather than printing help. An agent reaching for
	// this command wants the catalogue, and making it guess a subcommand first
	// spends a round trip to learn something the listing would have shown.
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDocsList(cmd)
	},
}

var docsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available agent-facing documents",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runDocsList(cmd)
	},
}

var docsShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Print one agent-facing document",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		doc, err := agentdocs.Get(args[0])
		if err != nil {
			// The error already names every available doc, so a caller that
			// guessed wrong can correct itself without a second command.
			return exitcode.Errorf(exitcode.GeneralError, "%s", err.Error())
		}
		fmt.Fprint(cmd.OutOrStdout(), doc.Body)
		return nil
	},
}

func runDocsList(cmd *cobra.Command) error {
	docs := agentdocs.List()

	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), docsListPayload{Docs: docs})
	}

	renderDocsList(cmd.OutOrStdout(), docs)
	return nil
}

// renderDocsList writes the human-readable listing: each name on its own line
// with its description indented under it.
//
// Descriptions are a sentence long, so a two-column table would either wrap
// them mid-word or force a terminal wider than most. One entry per pair of
// lines stays readable at any width.
func renderDocsList(w io.Writer, docs []agentdocs.Doc) {
	for _, d := range docs {
		fmt.Fprintf(w, "%s\n    %s\n", d.Name, d.Description)
	}
	fmt.Fprintf(w, "\nRead one with: jin docs show <name>\n")
}

func init() {
	docsCmd.AddCommand(docsListCmd)
	docsCmd.AddCommand(docsShowCmd)
	rootCmd.AddCommand(docsCmd)
}
