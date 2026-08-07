package claude

import (
	"reflect"
	"strings"
	"testing"
)

// TestOpensCompletionOverlay pins the rule that decides whether Claude Code
// still has a completion overlay open once the prompt has been typed.
//
// The rule is not a guess about the TUI: each shape below was driven through
// a throwaway tmux server against Claude Code 2.1.224, three runs apiece. The
// cases marked "measured" are the ones with a recorded outcome; the rest
// follow from the same rule and exist so a refactor cannot quietly widen or
// narrow it.
func TestOpensCompletionOverlay(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   bool
	}{
		// Measured: the overlay stayed open and ate Enter, leaving the input
		// rewritten to "list @internal/agentdocs/" and unsent (3/3).
		{"file-ref at end", "list @internal/agent", true},
		// Measured: no overlay, submitted verbatim (3/3). The picker closes
		// as soon as the caret leaves the token.
		{"file-ref followed by words", "list @internal/agent and say ok", false},
		// Measured: nothing was drawn, but the selection was live — the nudge
		// walked it and /my-01-spec ran instead of /my-03-work (3/3).
		{"bare slash command", "/my-0", true},
		// Measured: ran as sent (3/3). Still returns true: the overlay is
		// open either way, and one candidate today can become two tomorrow.
		{"bare slash command, unique", "/my-03-work", true},
		// Measured: no overlay, submitted verbatim (3/3). A slash that is not
		// the first character is just text.
		{"slash inside a sentence", "explain the fix/send-deadlock branch", false},

		{"plain text", "say pong only", false},
		{"empty", "", false},
		{"whitespace only", "   \n\t ", false},
		// Trailing whitespace is trimmed before the check, so these read the
		// same as their untrimmed forms. Erring toward true is deliberate: an
		// unnecessary Escape was measured harmless (3/3), a missed one leaves
		// the defect in place.
		{"file-ref with trailing space", "list @internal/agent ", true},
		{"bare slash with trailing newline", "/my-03-work\n", true},
		// Fields splits on newlines too, so the final token of the whole
		// prompt is what counts — not the final token of the first line.
		{"multiline ending in file-ref", "do this:\nthen read @internal/session", true},
		{"multiline ending in prose", "read @internal/session\nthen summarise it", false},
		// A slash command is the entire input or it is not one at all.
		{"slash command with an argument", "/my-03-work now please", false},
		{"lone at-sign", "@", true},
		{"lone slash", "/", true},

		// Measured, because "@ at the start of the final token" is a stricter
		// rule than "the final token mentions @" and the difference decides
		// prompts an agent writes constantly. A closing delimiter puts the
		// caret outside the token, and the overlay goes with it: none of these
		// drew one, and all submitted verbatim.
		{"backtick-wrapped file-ref", "read `@internal/agent`", false}, // 3/3
		{"double-quoted file-ref", `read "@internal/agent"`, false},    // 2/2
		{"at-sign inside a word", "mail me at foo@example.com", false}, // 1/1
		// Measured to draw no completion overlay (3/3). What its Enter does
		// was NOT measured — `#` opens a save-destination dialog and finding
		// out costs a write to a memory file. False here means "no overlay",
		// not "no hazard"; see 06_followup.
		{"hash prefix", "#remember this", false}, // 3/3, overlay only
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := opensCompletionOverlay(tc.prompt); got != tc.want {
				t.Errorf("opensCompletionOverlay(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

// TestDismissOverlayKeys checks the adapter returns Escape exactly when the
// predicate says an overlay is open, and nothing otherwise. The key is not
// free — Escape interrupts a running turn (2/3; the third run's turn ended
// first) — so "nothing otherwise" is as much of the contract as the key is.
func TestDismissOverlayKeys(t *testing.T) {
	a := &Agent{}

	for _, prompt := range []string{"list @internal/agent", "/my-03-work", "@"} {
		if got := a.DismissOverlayKeys(prompt); !reflect.DeepEqual(got, []string{"Escape"}) {
			t.Errorf("DismissOverlayKeys(%q) = %v, want [Escape]", prompt, got)
		}
	}
	for _, prompt := range []string{"say pong only", "list @internal/agent and say ok", "", "  "} {
		if got := a.DismissOverlayKeys(prompt); got != nil {
			t.Errorf("DismissOverlayKeys(%q) = %v, want nil", prompt, got)
		}
	}
}

// TestDismissOverlayKeysNeverReturnsDestructiveKeys guards the one mistake
// that would turn this fix into a worse bug than the one it closes. C-c
// terminates the agent and C-u empties the input; either would be committed
// blind by the Enter that follows. Escape is the only key measured to close
// the overlay while leaving the prompt intact (3/3).
func TestDismissOverlayKeysNeverReturnsDestructiveKeys(t *testing.T) {
	a := &Agent{}
	banned := map[string]string{
		"C-c":   "terminates the agent",
		"C-u":   "clears the input line",
		"C-k":   "clears to end of line",
		"C-a":   "moves the caret, does not dismiss",
		"Enter": "commits — the very thing we are deferring",
	}
	prompts := []string{
		"list @internal/agent", "/my-03-work", "/my-0", "@", "/",
		"say pong only", "list @internal/agent and say ok", "",
	}
	for _, prompt := range prompts {
		for _, k := range a.DismissOverlayKeys(prompt) {
			if why, bad := banned[k]; bad {
				t.Errorf("DismissOverlayKeys(%q) returned %q, which %s", prompt, k, why)
			}
			if strings.TrimSpace(k) == "" {
				t.Errorf("DismissOverlayKeys(%q) returned an empty key", prompt)
			}
		}
	}
}
