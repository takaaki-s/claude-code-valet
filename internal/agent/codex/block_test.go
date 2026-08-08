package codex

import (
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// TestBlockOptsOut pins the opt-out. Codex's approval dialog has not been
// measured, and the failure a guess would produce is not a quiet one: a
// literal that matches the wrong screen makes DetectBlock claim a block that
// is not there, after which RespondToBlock types a digit into whatever the
// pane really holds.
//
// So this test does not assert "codex support is missing". It asserts that
// the adapter keeps saying so until someone measures the dialog — at which
// point this test is the thing that has to be rewritten deliberately.
func TestBlockOptsOut(t *testing.T) {
	a := New()
	captures := []string{
		"",
		"Do you want to proceed?\n› 1. Yes\n  2. No\n",
		"Allow command?\nPress enter to confirm or esc to go back\n",
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
