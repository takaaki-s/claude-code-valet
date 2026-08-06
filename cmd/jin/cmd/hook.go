package cmd

import (
	"encoding/json"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/agentdocs"
	"github.com/takaaki-s/jind-ai/internal/daemon"
	"github.com/takaaki-s/jind-ai/internal/debug"
)

// hookInput represents the JSON input from Claude Code hooks (stdin)
type hookInput struct {
	SessionID        string `json:"session_id"`
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type,omitempty"`
	CWD              string `json:"cwd,omitempty"`
	StopReason       string `json:"stop_reason,omitempty"`
}

// sessionStartEvent is the only event that carries the agent-facing context.
// Injecting on every event would repeat the same paragraph into the agent's
// context window once per turn.
const sessionStartEvent = "SessionStart"

// hookSpecificOutput / hookContextOutput are the stdout JSON that Claude Code
// and Codex both read from a SessionStart hook — the two products document the
// same shape and the same field name, so one struct serves both.
//
// No per-kind branch belongs here, and if a future agent needs a different
// shape, the escape hatch is to give agentdocs.HookContextFlag a value
// (--emit-context=<format>) rather than to switch on the kind: which agent is
// calling is something this command deliberately does not know. What decides
// whether to emit is the adapter that wrote the command line.
//
// opencode is deliberately absent. Its plugin invokes `jin hook` with stdout
// set to "ignore", so nothing written here could reach it; opencode receives
// the same text through its config's instructions list instead.
type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

type hookContextOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

var hookEmitContext bool

var hookLog = debug.NewLogger("hook-debug.log")

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Handle Claude Code hook events (stdin JSON)",
	Long:   "Internal command invoked by Claude Code hooks. Reads JSON from stdin and notifies the daemon.",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Read JSON from stdin
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			hookLog("failed to read stdin: %v", err)
			return nil // Always exit 0
		}

		var input hookInput
		if err := json.Unmarshal(data, &input); err != nil {
			hookLog("failed to parse JSON: %v (data: %s)", err, string(data))
			return nil
		}

		// Emitted before every other check, and before any early return: the
		// context is a static string compiled into this binary, so it can be
		// delivered even when the daemon is down or the session is unknown to
		// jind-ai — exactly the situations where an agent most needs to be
		// told `jin docs` exists.
		if hookEmitContext && input.HookEventName == sessionStartEvent {
			emitAgentContext(cmd.OutOrStdout())
		}

		if input.SessionID == "" || input.HookEventName == "" {
			hookLog("missing required fields: session_id=%q hook_event_name=%q", input.SessionID, input.HookEventName)
			return nil
		}

		// Read jin session ID from environment (set by jin when starting Claude)
		jinSessionID := os.Getenv("JIN_SESSION_ID")
		if jinSessionID == "" {
			return nil // Not a jin-managed session, skip
		}

		hookLog("event=%s cc_session=%s jin_session=%s notification=%s", input.HookEventName, input.SessionID, jinSessionID, input.NotificationType)

		// Send to daemon
		client := daemon.NewClient(getSocketPath())
		if err := client.SendHook(daemon.HookRequest{
			SessionID:        input.SessionID,
			JinSessionID:     jinSessionID,
			HookEventName:    input.HookEventName,
			NotificationType: input.NotificationType,
			CWD:              input.CWD,
			StopReason:       input.StopReason,
		}); err != nil {
			hookLog("SendHook failed: %v", err)
		}

		return nil
	},
}

// emitAgentContext writes the SessionStart context JSON.
//
// Failures are logged and swallowed: a hook that cannot describe jin must
// still let the session start, and the caller keeps its exit-0 contract. The
// payload is one line and nothing else may share stdout — anything extra
// would land in front of the JSON and break the agent's parse.
func emitAgentContext(w io.Writer) {
	payload := hookContextOutput{
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:     sessionStartEvent,
			AdditionalContext: agentdocs.Context(),
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		hookLog("failed to marshal agent context: %v", err)
		return
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		hookLog("failed to write agent context: %v", err)
	}
}

func init() {
	// Registered under the same constant the adapters build their command
	// lines from, so the flag and its callers cannot drift apart.
	hookCmd.Flags().BoolVar(&hookEmitContext, agentdocs.HookContextFlag, false,
		"Print the agent-facing jin context as SessionStart hook output (set by the adapter that wrote the hook command)")
	rootCmd.AddCommand(hookCmd)
}
