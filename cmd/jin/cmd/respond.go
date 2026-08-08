package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/daemon"
	"github.com/takaaki-s/jind-ai/internal/exitcode"
	"github.com/takaaki-s/jind-ai/internal/session"
)

type respondResult struct {
	Success bool   `json:"success"`
	Session string `json:"session"`
	// Kind names the sort of prompt that was answered ("tool-permission" or
	// "question"), so a caller knows what it just agreed to without having to
	// read the pane.
	Kind string `json:"kind,omitempty"`
}

// respondMaxOption mirrors the daemon's bound. An answer is one keystroke, so
// a two-digit choice cannot be addressed at all.
const respondMaxOption = 9

var respondCmd = &cobra.Command{
	Use:   "respond <selector> (--option <n> | --text <answer>)",
	Short: "Answer a prompt a session is blocked on",
	Long: `Answer a prompt an agent session is waiting on — a tool-approval dialog, or a
question it asked.

This is not "send". "send" types a prompt into the agent's input line and
proves it arrived by finding it there; a dialog draws none of what you type, so
there is nothing to find. "send" therefore still refuses anything that is not
idle, and this command drives the dialog instead.

  jin session respond fix-login --option 1        # choose the answer numbered 1
  jin session respond fix-login --text "use bun"  # type free text, where offered

The number is the one shown on screen, 1 to 9. To see what the choices are,
read the transcript rather than the pane:

  jin session result fix-login --json

Claude Code sessions only. Other agent kinds are refused with a message saying
so — attach and answer those yourself.

A form asking several questions at once cannot be answered, nor can such a
form's final submit confirmation. Both are recognised and refused without
sending anything: answering one question of a form leaves the form standing, so
jin cannot tell a half-filled form from an answer that never landed.

There is no --wait flag, because there is nothing left to wait for. This
returns only once the prompt is gone from the pane. Exit code 4 means it was
still there — the keys went out, so attach and look before answering again.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]

		option, _ := cmd.Flags().GetInt("option")
		text, _ := cmd.Flags().GetString("text")

		optionSet := cmd.Flags().Changed("option")
		textSet := cmd.Flags().Changed("text")
		switch {
		case optionSet && textSet:
			return usageError(cmd, "pass either --option or --text, not both")
		case !optionSet && !textSet:
			return usageError(cmd, "an answer is required: pass --option <n> or --text <answer>")
		}
		if optionSet && (option < 1 || option > respondMaxOption) {
			return usageError(cmd,
				"--option must be between 1 and %d (an answer is one keystroke)", respondMaxOption)
		}
		// Rejected by the same rule as an unverifiable prompt, and for the
		// same reason: RespondToBlock confirms free text rendered before it
		// presses the key that submits it, and text that normalizes to
		// nothing would pass that check without any evidence.
		if textSet && !session.PromptVerifiable(text) {
			return fmt.Errorf("--text has no verifiable content (only whitespace or box-drawing characters)")
		}
		if !optionSet {
			option = 0
		}
		if !textSet {
			text = ""
		}

		client := daemon.NewClient(getSocketPath())

		sessionID, sessionName, err := resolveSession(client, nameOrID)
		if err != nil {
			return err
		}

		kind, err := client.Respond(sessionID, option, text)
		if err != nil {
			// The daemon reports a prompt that never cleared in the words
			// RespondToBlock chose; map it to the timeout code so a caller can
			// branch on "the outcome is unknown" without matching text.
			if strings.Contains(err.Error(), "still on screen") {
				return exitcode.Errorf(exitcode.Timeout, "%s", err.Error())
			}
			return err
		}

		result := respondResult{Success: true, Session: sessionName, Kind: kind}
		if jsonOutput {
			return renderRespondResultJSON(os.Stdout, result)
		}
		fmt.Printf("Answered %s prompt on session: %s\n", displayBlockKind(kind), sessionName)
		return nil
	},
}

// displayBlockKind turns the wire value into something a person reads. An
// unknown kind is printed as-is rather than guessed at, so a newer daemon
// talking to an older CLI says something true.
func displayBlockKind(kind string) string {
	switch kind {
	case "tool-permission":
		return "the permission"
	case "question":
		return "the question"
	case "":
		return "the"
	default:
		return kind
	}
}

func renderRespondResultJSON(w io.Writer, result respondResult) error {
	return writeJSON(w, result)
}

func init() {
	sessionCmd.AddCommand(respondCmd)
	respondCmd.Flags().Int("option", 0,
		"Answer by choosing the option with this on-screen number (1-9).")
	respondCmd.Flags().String("text", "",
		"Answer with free text, for a prompt that offers a free-text entry.")
}
