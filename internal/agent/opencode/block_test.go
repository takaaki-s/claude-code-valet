package opencode

import (
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// TestBlockOptsOut pins the opt-out for an adapter whose dialog HAS been
// measured — see DetectBlock's comment for the table.
//
// It is opted out because driving that dialog means reading which button is
// currently lit and moving from there, and a misread moves onto a different
// button and confirms it. On a permission dialog that is an approval nobody
// gave. The Claude Code path needs no position at all, so the two do not
// belong behind one flag.
//
// Whoever implements it replaces this test rather than deleting it: what has
// to keep holding is that a real permission capture never yields a kind while
// AnswerBlockKeys still refuses, because that combination is what would put
// keys into the dialog with nothing to plan them.
func TestBlockOptsOut(t *testing.T) {
	a := New()
	captures := []string{
		"",
		"△ Permission required\n  # Shell command\n\n$ sha256sum /etc/hostname\n\n Allow once   Allow always   Reject    ⇆ select  enter confirm\n",
	}
	for _, c := range captures {
		if got := a.DetectBlock(c); got != agent.BlockNone {
			t.Errorf("DetectBlock(%q) = %q, want %q", c, got, agent.BlockNone)
		}
	}

	steps, err := a.AnswerBlockKeys(agent.BlockPermission, captures[1], agent.BlockAnswer{Option: 1})
	if err == nil {
		t.Fatalf("AnswerBlockKeys returned nil error and steps=%+v, want a refusal", steps)
	}
	if steps != nil {
		t.Errorf("steps = %+v on a refusal, want nil", steps)
	}
}
