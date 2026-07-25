package cmd

import (
	"testing"
)

// pushFocusSession's own behaviour is covered alongside the other popup result
// writers in TestPushPopupResult (util_test.go): they share one helper.

// TestSessionFilterPopupCmd_Registered guards the cobra wiring: init() must
// attach the hidden subcommand under rootCmd so `jin session-filter-popup`
// resolves. This is the minimum shape check that catches "forgot to
// AddCommand" regressions without needing to run the bubbletea program.
func TestSessionFilterPopupCmd_Registered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"session-filter-popup"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Name() != "session-filter-popup" {
		t.Fatalf("session-filter-popup not registered: %+v", cmd)
	}
}

// TestSessionFilterPopupCmd_Hidden regresses the "internal use only" flag.
// The command must remain absent from `jin --help` so users don't invoke it
// directly; the tmux popup shells out to it.
func TestSessionFilterPopupCmd_Hidden(t *testing.T) {
	if !sessionFilterPopupCmd.Hidden {
		t.Errorf("session-filter-popup should be Hidden")
	}
}

// TestSessionFilterPopupCmd_RunE confirms RunE is wired. Without it, cobra
// would silently print help text on invocation instead of running the
// picker. Testing the closure body itself needs interface-ification of
// daemon.Client / tea.Program (design §11.3 says out of scope for this PR).
func TestSessionFilterPopupCmd_RunE(t *testing.T) {
	if sessionFilterPopupCmd.RunE == nil {
		t.Errorf("session-filter-popup.RunE = nil, want a runner")
	}
}
