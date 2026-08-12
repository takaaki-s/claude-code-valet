package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/daemon"
	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/transcript"
)

// The two ways `session output` can come up empty. They used to share one
// message on the default path and no message at all on the --last path;
// separating them lets a caller tell "I am looking at the wrong session" from
// "the agent has not spoken yet".
var errNoTranscriptFound = errors.New("no transcript file found for this session; it may not have started yet, or its agent session ID may be stale")

const noReadableMessagesHint = "no plain-text messages found; the session may not have produced any text yet — try 'jin session result --json' to inspect tool_use / tool_result activity"

var outputCmd = &cobra.Command{
	Use:   "output <selector>",
	Short: "Get the output of a session",
	Long: `Get the conversation output from a Claude Code session.

By default, shows the newest plain-text message: usually the agent's reply, but
the prompt itself while the agent is still working on it. Use --last N to get
the last N exchanges, an exchange being a prompt plus the agent's whole reply to
it. One exchange can span many messages, so N does not bound how much comes back.
The selector may be an ID prefix or a description substring (case-insensitive).

Examples:
  jin session output my-session
  jin session output my-session --last 3
  jin session output my-session --last 3 --json`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := args[0]
		lastN, _ := cmd.Flags().GetInt("last")

		// 0 is the flag's zero value and cannot be told apart from "not
		// passed", so it keeps meaning the default path; a negative count is
		// unambiguous and is refused the way `session result` refuses it.
		if lastN < 0 {
			return fmt.Errorf("--last must be >= 0")
		}

		client := daemon.NewClient(getSocketPath())

		sessionID, _, err := resolveSession(client, nameOrID)
		if err != nil {
			return err
		}

		info, err := client.Get(sessionID)
		if err != nil {
			return err
		}

		if info.AgentSessionID == "" {
			return fmt.Errorf("session has no agent session ID (session may not have started yet)")
		}

		return runOutput(os.Stdout, os.Stderr, transcript.NewReader(),
			transcriptWorkDir(info), info.AgentSessionID, lastN, jsonOutput)
	},
}

// conversationReader is the slice of *transcript.Reader this command needs.
// It lives next to its consumer rather than in internal/transcript, the same
// way send.go declares statusGetter for daemon.Client, so runOutput can be
// exercised without a transcript on disk.
type conversationReader interface {
	GetConversation(workDir, sessionID string, lastN int) ([]transcript.Message, error)
	GetLastMessage(workDir, sessionID string) (*transcript.Message, error)
}

// runOutput decides what `session output` prints and writes it. RunE keeps the
// flag parsing and the daemon lookup; everything downstream of "which
// transcript, and how much of it" lives here, because that is the part with
// branches worth pinning — which reader call is made, how a failure is worded,
// and which stream each half of the output goes to.
func runOutput(out, errOut io.Writer, r conversationReader, workDir, sessionID string, lastN int, jsonOut bool) error {
	if lastN > 0 {
		msgs, err := r.GetConversation(workDir, sessionID, lastN)
		if err != nil {
			return mapTranscriptErr(err)
		}
		return renderConversation(out, errOut, msgs, jsonOut)
	}

	msg, err := r.GetLastMessage(workDir, sessionID)
	if err != nil {
		return mapTranscriptErr(err)
	}
	return renderLastMessage(out, msg, jsonOut)
}

// renderLastMessage writes the default output: the newest plain-text message.
// Nothing readable is an error here, unlike on the --last path, because a
// single message is the entire answer — printing an empty line and exiting 0
// would look exactly like a session whose transcript is missing.
func renderLastMessage(out io.Writer, msg *transcript.Message, jsonOut bool) error {
	if msg == nil {
		// The transcript exists but every user/assistant entry so far consists
		// only of tool_use / thinking blocks with no plain text. A missing
		// transcript is a separate error, so this message no longer has to
		// cover both.
		return errors.New(noReadableMessagesHint)
	}
	if jsonOut {
		return renderOutputJSON(out, msg)
	}
	fmt.Fprintln(out, msg.Content)
	return nil
}

// mapTranscriptErr turns a transcript read failure into the message the user
// gets. A missing transcript is the one failure a caller can act on — wrong
// session, or too early — so it keeps its own wording instead of surfacing as
// a read error.
func mapTranscriptErr(err error) error {
	if errors.Is(err, transcript.ErrNoTranscript) {
		return errNoTranscriptFound
	}
	return fmt.Errorf("failed to read transcript: %w", err)
}

// renderConversation writes the --last output. A transcript that exists but
// holds nothing readable is a state every session passes through, so it stays
// a success — the note goes to stderr precisely because stdout is being piped
// somewhere, and an empty stdout used to be indistinguishable from a missing
// transcript.
func renderConversation(out, errOut io.Writer, msgs []transcript.Message, jsonOut bool) error {
	if len(msgs) == 0 {
		fmt.Fprintln(errOut, noReadableMessagesHint)
	}

	if jsonOut {
		// Ensure JSON outputs [] instead of null for empty results
		if msgs == nil {
			msgs = []transcript.Message{}
		}
		return renderOutputJSON(out, msgs)
	}

	for _, msg := range msgs {
		fmt.Fprintf(out, "[%s] %s\n", msg.Type, msg.Content)
	}
	return nil
}

// transcriptWorkDir picks the directory to feed to the transcript reader.
// CurrentWorkDir (tracked by daemon polling) reflects where the agent actually
// is right now — after `cd` into a subdir or worktree — while WorkDir is the
// directory the session was launched in. Claude Code writes its JSONL under a
// projects/<encoded-workdir>/ tree, so the two can point at different files.
func transcriptWorkDir(info *session.Info) string {
	if info.CurrentWorkDir != "" {
		return info.CurrentWorkDir
	}
	return info.WorkDir
}

func renderOutputJSON(w io.Writer, v any) error {
	return writeJSON(w, v)
}

func init() {
	sessionCmd.AddCommand(outputCmd)

	outputCmd.Flags().Int("last", 0, "Number of exchanges to show — a prompt plus the agent's whole reply; one exchange can span many messages")
}
