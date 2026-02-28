package cmd

import (
	"encoding/json"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/claude-code-valet/internal/daemon"
)

// hookInput represents the JSON input from Claude Code hooks (stdin)
type hookInput struct {
	SessionID        string `json:"session_id"`
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type,omitempty"`
}

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Handle Claude Code hook events (stdin JSON)",
	Long:   "Internal command invoked by Claude Code hooks. Reads JSON from stdin and notifies the daemon.",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Read JSON from stdin
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil // Always exit 0
		}

		var input hookInput
		if err := json.Unmarshal(data, &input); err != nil {
			return nil
		}

		if input.SessionID == "" || input.HookEventName == "" {
			return nil
		}

		// Send to daemon (fire-and-forget, short timeout)
		client := daemon.NewClient(getSocketPath())
		_ = client.SendHook(daemon.HookRequest{
			SessionID:        input.SessionID,
			HookEventName:    input.HookEventName,
			NotificationType: input.NotificationType,
		})

		return nil
	},
}

func init() {
	rootCmd.AddCommand(hookCmd)
}
