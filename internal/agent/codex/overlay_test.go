package codex

import "testing"

// TestDismissOverlayKeysOptsOut pins the opt-out.
//
// Claude Code was measured to hold a completion overlay open for prompts that
// end in an in-progress token, and to let that overlay eat the Enter
// SendPrompt presses. Codex has slash commands too, so it may share the
// defect — but the measurement could not be made (the account hit its API
// usage limit and the pane dies before a prompt can be typed).
//
// Until it is, this adapter must send no extra keys: Escape is destructive on
// a running turn, and returning it on a guess is the same class of mistake
// that produced the bug. Change this test only alongside a real measurement.
func TestDismissOverlayKeysOptsOut(t *testing.T) {
	a := &Agent{}
	for _, prompt := range []string{
		"list @internal/agent",
		"/status",
		"say pong only",
		"",
	} {
		if got := a.DismissOverlayKeys(prompt); got != nil {
			t.Errorf("DismissOverlayKeys(%q) = %v, want nil (unmeasured, must opt out)", prompt, got)
		}
	}
}
