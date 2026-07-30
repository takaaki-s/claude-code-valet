package opencode

import (
	"strings"
	"testing"
)

// TestPasteLineCount pins the rule opencode uses when it summarises a paste.
// Every expectation is a measurement against a real opencode pane, not a
// guess: two of these cases are the reason the count is not
// strings.Count(s, "\n")+1, and getting either wrong makes SendPrompt reject
// prompts that in fact landed.
func TestPasteLineCount(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   int
	}{
		{"single line", "hello", 1},
		{"empty", "", 0},
		{"two lines", "a\nb", 2},
		{"blank line in middle counts", "a\n\nb", 3},
		{"trailing newline dropped", "a\nb\nc\n", 3},
		{"two trailing newlines dropped", "a\nb\nc\n\n", 3},
		{"trailing whitespace-only line dropped", "a\nb\nc\n   ", 3},
		{"trailing spaces on the last line kept", "a\nb\nc   ", 3},
		{"CRLF breaks twice", "a\r\nb\r\nc", 5},
		{"lone CR breaks", "a\rb", 2},
		{"leading blank line counts", "\na", 2},
		{"only newlines", "\n\n\n", 0},
		{"only spaces", "   ", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pasteLineCount(tc.prompt); got != tc.want {
				t.Errorf("pasteLineCount(%q) = %d, want %d", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestPasteLineCount_LargePrompt(t *testing.T) {
	// The count must not depend on prompt size: a 62KB paste reported its
	// lines exactly, so the arithmetic has to hold at that scale too.
	prompt := strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 60)+"\n", 1000), "\n")
	if got := pasteLineCount(prompt); got != 1000 {
		t.Errorf("pasteLineCount(1000-line prompt) = %d, want 1000", got)
	}
}

// TestPastePlaceholder pins the exact wording SendPrompt searches the pane
// for. A drift here — losing the "~", or pluralising "lines" correctly for a
// count of one — would make every paste fail verification, so the literal is
// asserted rather than rebuilt from the same format string.
func TestPastePlaceholder(t *testing.T) {
	a := New()
	cases := []struct {
		prompt string
		want   string
	}{
		{"one line", "[Pasted ~1 lines]"}, // "lines" even for one — measured
		{"a\nb", "[Pasted ~2 lines]"},
		{"a\nb\nc\n", "[Pasted ~3 lines]"},
		{strings.Repeat("x\n", 2500), "[Pasted ~2500 lines]"},
	}
	for _, tc := range cases {
		if got := a.PastePlaceholder(tc.prompt); got != tc.want {
			t.Errorf("PastePlaceholder(%.20q) = %q, want %q", tc.prompt, got, tc.want)
		}
	}
}

// TestPastePlaceholder_OptsIn is the guard on the transport choice itself:
// opencode must never fall back to the keystroke path, where an 8KB prompt
// takes 88s and cannot verify inside the budget at all.
func TestPastePlaceholder_OptsIn(t *testing.T) {
	if New().PastePlaceholder("anything") == "" {
		t.Error(`PastePlaceholder returned "" — opencode would be typed to, not pasted`)
	}
}
