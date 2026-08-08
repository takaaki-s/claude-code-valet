package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/session"
)

// respondReq marshals a RespondRequest the way a client would.
func respondReq(t *testing.T, r RespondRequest) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// The validation below is duplicated from the CLI on purpose, and these tests
// are what keep the duplicate honest: this endpoint is reachable without the
// CLI — plugins shell out to jin, but nothing stops a caller speaking the
// protocol straight to the socket — so a guard that lived only in cobra would
// not be a guard at all.
func TestHandleRespond_Validation(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name    string
		req     RespondRequest
		wantErr string
	}{
		{"no id", RespondRequest{Option: 1}, "id is required"},
		{"no answer", RespondRequest{ID: "x"}, "an answer is required"},
		{"both", RespondRequest{ID: "x", Option: 1, Text: "hi"}, "not both"},
		{"option zero with text empty", RespondRequest{ID: "x"}, "an answer is required"},
		{"option too large", RespondRequest{ID: "x", Option: 10}, "between 1 and 9"},
		{"option negative", RespondRequest{ID: "x", Option: -1}, "between 1 and 9"},
		{"whitespace text", RespondRequest{ID: "x", Text: "   "}, "no verifiable content"},
		{"box-drawing text", RespondRequest{ID: "x", Text: "────"}, "no verifiable content"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.handleRespond(respondReq(t, tc.req))
			if resp.Success {
				t.Fatalf("handleRespond succeeded, want the request rejected")
			}
			if !strings.Contains(resp.Error, tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", resp.Error, tc.wantErr)
			}
		})
	}
}

// TestHandleRespond_ValidRequestReachesManager is the negative control for the
// table above: without it, a guard that rejected everything would pass every
// case there. Option 9 is the boundary the bound allows.
func TestHandleRespond_ValidRequestReachesManager(t *testing.T) {
	s := newTestServer(t)

	for _, req := range []RespondRequest{
		{ID: "no-such-session", Option: 1},
		{ID: "no-such-session", Option: 9},
		{ID: "no-such-session", Text: "紫がいい"},
	} {
		resp := s.handleRespond(respondReq(t, req))
		if resp.Success {
			t.Fatalf("handleRespond(%+v) succeeded against a missing session", req)
		}
		// Reaching the manager is the point: only it knows about sessions, so
		// this error can only have come from past the validation guard.
		if !strings.Contains(resp.Error, "session not found") {
			t.Errorf("error = %q, want it to come from the manager (session not found)", resp.Error)
		}
	}
}

func TestHandleRespond_InvalidJSON(t *testing.T) {
	s := newTestServer(t)
	resp := s.handleRespond(json.RawMessage(`{"id":`))
	if resp.Success {
		t.Fatal("handleRespond succeeded on malformed JSON")
	}
}

// TestRespondIsNotReadOnly pins the action out of readOnlyActions. It types
// into a pane, so a client that times out on it must be told the outcome is
// unknown rather than given the plain read-only timeout wording.
func TestRespondIsNotReadOnly(t *testing.T) {
	if readOnlyActions["respond"] {
		t.Error("respond is listed read-only, but it sends keys to a pane")
	}
}

// TestRespondIsDispatched is small and load-bearing. Without it the handler is
// thoroughly tested and completely unreachable: every test above calls
// handleRespond directly, TestActionsAreDispatched only walks readOnlyActions
// (and respond is deliberately not in it), and the CLI tests run against a
// socket that does not exist. Deleting the switch case broke nothing.
func TestRespondIsDispatched(t *testing.T) {
	s := newTestServer(t)
	resp := s.handleRequest(&Request{
		Action: "respond",
		Data:   respondReq(t, RespondRequest{ID: "no-such-session", Option: 1}),
	})
	if strings.Contains(resp.Error, "unknown action") {
		t.Fatalf("respond is not wired into handleRequest: %q", resp.Error)
	}
}

// TestHandleRespond_TextLengthBound covers the one refusal that is about the
// transport rather than the request: past the agent's fold threshold the text
// is hidden from the pane, so the check that guards the submitting keystroke
// could never succeed.
func TestHandleRespond_TextLengthBound(t *testing.T) {
	s := newTestServer(t)

	ok := strings.Repeat("a", RespondMaxTextBytes)
	if resp := s.handleRespond(respondReq(t, RespondRequest{ID: "x", Text: ok})); strings.Contains(resp.Error, "bytes") {
		t.Errorf("a %d-byte answer was rejected for length: %q", len(ok), resp.Error)
	}

	tooBig := strings.Repeat("a", RespondMaxTextBytes+1)
	resp := s.handleRespond(respondReq(t, RespondRequest{ID: "x", Text: tooBig}))
	if resp.Success {
		t.Fatal("an over-long answer was accepted")
	}
	if !strings.Contains(resp.Error, "cannot be verified") {
		t.Errorf("error = %q, want it to say why length matters", resp.Error)
	}
}

// TestTagRespondError pins the daemon half of the exit-code contract. The
// protocol carries errors as plain strings, so this is the only place the
// "prompt did not clear" case can be marked while it is still a typed error —
// and the CLI's exit 4, which the README and the exit-codes doc both promise,
// depends on the mark surviving.
func TestTagRespondError(t *testing.T) {
	notCleared := fmt.Errorf("%w after 10s; attach and look", session.ErrBlockNotCleared)
	got := tagRespondError(notCleared)
	if !strings.HasPrefix(got, RespondNotClearedPrefix) {
		t.Errorf("tagRespondError(not-cleared) = %q, want the %q prefix", got, RespondNotClearedPrefix)
	}
	if !strings.Contains(got, "attach and look") {
		t.Errorf("tagRespondError dropped the message: %q", got)
	}

	other := errors.New("session not found: x")
	if got := tagRespondError(other); strings.HasPrefix(got, RespondNotClearedPrefix) {
		t.Errorf("an unrelated failure was tagged as not-cleared: %q", got)
	}
}
