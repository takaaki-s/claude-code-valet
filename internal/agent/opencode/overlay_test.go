package opencode

import "testing"

// TestDismissOverlayKeysOptsOut pins the opt-out.
//
// Claude Code was measured to hold a completion overlay open for prompts that
// end in an in-progress token, and to let that overlay eat the Enter
// SendPrompt presses. Whether opencode does the same is unknown: the local
// install has no provider configured and never reaches a usable prompt.
//
// This adapter also takes the paste transport, where the prompt arrives as
// one bracketed write rather than as typing, so it is not even clear a
// completion would be triggered. Both point the same way — opt out, stay
// byte-identical to the pre-fix key sequence, and measure before sending a
// key that is destructive when it misfires.
func TestDismissOverlayKeysOptsOut(t *testing.T) {
	a := &Agent{}
	for _, prompt := range []string{
		"list @internal/agent",
		"/help",
		"say pong only",
		"",
	} {
		if got := a.DismissOverlayKeys(prompt); got != nil {
			t.Errorf("DismissOverlayKeys(%q) = %v, want nil (unmeasured, must opt out)", prompt, got)
		}
	}
}
