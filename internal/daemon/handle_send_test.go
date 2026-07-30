package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// Cover the validation branches of handleSend without spinning a full Server,
// matching the style of handle_pane_test.go. Each case returns before touching
// s.manager, so the zero-value Server{} is safe.

func TestHandleSend_EmptyPrompt(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(SendRequest{ID: "sess-1", Prompt: ""})
	resp := s.handleSend(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "prompt is required") {
		t.Errorf("Error = %q, want to contain 'prompt is required'", resp.Error)
	}
}

func TestHandleSend_UnverifiablePrompt(t *testing.T) {
	// Prompts that Manager.SendPrompt could not verify must be rejected here.
	// Its verify searches the captured pane for the prompt's tail, and a
	// prompt that normalizes to nothing leaves no needle — sendVerifyOK then
	// accepts it trivially, so letting one through would send an unverified
	// Enter.
	//
	// The box-drawing cases are the ones that pin the wiring: a guard written
	// against strings.TrimSpace passes every whitespace case below, so
	// without them this test cannot tell whether handleSend actually defers
	// to session.PromptVerifiable.
	cases := []struct {
		name   string
		prompt string
	}{
		{"space", " "},
		{"newline", "\n"},
		{"tab", "\t"},
		{"mixed-whitespace", "  \n\t  "},
		{"box-drawing-only", "────────"},
		{"box-drawing-and-space", "┃ ─ ┃"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			data, _ := json.Marshal(SendRequest{ID: "sess-1", Prompt: tc.prompt})
			resp := s.handleSend(data)

			if resp.Success {
				t.Fatalf("expected Success=false for prompt=%q", tc.prompt)
			}
			if !strings.Contains(resp.Error, "no verifiable content") {
				t.Errorf("Error = %q, want to contain 'no verifiable content'", resp.Error)
			}
		})
	}
}

// TestHandleSend_VerifiablePromptReachesManager is the negative control for
// the table above: a prompt made of the same box-drawing runes plus one
// ordinary character must clear the guard and reach SendPrompt. Without it,
// tightening the rule to "reject everything" would still satisfy every
// rejection case.
//
// This one needs a real Manager — unlike the validation branches, it is meant
// to get past them — so it borrows newAsyncTestServer instead of the
// zero-value Server{}.
func TestHandleSend_VerifiablePromptReachesManager(t *testing.T) {
	s := newAsyncTestServer(t)

	data, _ := json.Marshal(SendRequest{ID: "no-such-session", Prompt: "┃ ok"})
	resp := s.handleSend(data)

	if resp.Success {
		t.Fatal("expected Success=false — the session does not exist")
	}
	// Reaching "session not found" proves the prompt passed validation:
	// that error can only come from inside SendPrompt.
	if !strings.Contains(resp.Error, "session not found") {
		t.Errorf("Error = %q, want 'session not found' (the prompt should clear "+
			"the validation guard and reach SendPrompt)", resp.Error)
	}
}

func TestHandleSend_InvalidJSON(t *testing.T) {
	s := &Server{}
	resp := s.handleSend(json.RawMessage(`{`))

	if resp.Success {
		t.Fatal("expected Success=false")
	}
}
