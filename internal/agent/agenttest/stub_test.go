package agenttest

import (
	"testing"

	"github.com/takaaki-s/jind-ai/internal/session"
)

// TestStubDefaultsOptOut pins the zero-value stub's answers for the two
// capabilities SendPrompt reads before it presses Enter.
//
// Neither default is cosmetic. Every SendPrompt test that does not configure
// them is, implicitly, asserting the pre-fix key sequence — so a stub that
// started returning keys of its own would leave those tests passing while
// describing a send nobody performs.
func TestStubDefaultsOptOut(t *testing.T) {
	var s session.Agent = &StubAgent{}

	if got := s.DismissOverlayKeys("list @internal/agent"); got != nil {
		t.Errorf("DismissOverlayKeys on a zero-value stub = %v, want nil", got)
	}
	if got := s.ClearInputKeys(); got != nil {
		t.Errorf("ClearInputKeys on a zero-value stub = %v, want nil", got)
	}
}

// TestStubDismissFnReceivesPrompt checks the stub forwards the prompt rather
// than deciding on its own, so a test can express "keys for this prompt only".
func TestStubDismissFnReceivesPrompt(t *testing.T) {
	var seen string
	s := &StubAgent{DismissFn: func(p string) []string {
		seen = p
		return []string{"Escape"}
	}}

	got := s.DismissOverlayKeys("list @internal/agent")
	if seen != "list @internal/agent" {
		t.Errorf("DismissFn received %q, want the prompt", seen)
	}
	if len(got) != 1 || got[0] != "Escape" {
		t.Errorf("DismissOverlayKeys = %v, want [Escape]", got)
	}
}
