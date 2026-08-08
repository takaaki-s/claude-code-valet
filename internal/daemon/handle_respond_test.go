package daemon

import (
	"encoding/json"
	"strings"
	"testing"
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
