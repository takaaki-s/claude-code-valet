package daemon

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// respondRoundTrip runs Client.Respond against a canned response, so the
// marshal/unmarshal of RespondResponse is exercised rather than assumed. The
// whole reason that type exists is to carry `kind` back to the caller, and
// nothing was checking that it survives the wire.
func respondRoundTrip(t *testing.T, canned Response) (string, error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()

	go func() {
		defer serverConn.Close()
		var req Request
		if err := json.NewDecoder(serverConn).Decode(&req); err != nil {
			return
		}
		canned.ProtocolVersion = ProtocolVersion
		b, _ := json.Marshal(canned)
		_, _ = serverConn.Write(append(b, '\n'))
	}()

	orig := dialDaemon
	dialDaemon = func(string, string, time.Duration) (net.Conn, error) { return clientConn, nil }
	t.Cleanup(func() { dialDaemon = orig })

	return NewClient("/nonexistent.sock").Respond("sess", 1, "")
}

func TestClientRespond_ReturnsKind(t *testing.T) {
	payload, _ := json.Marshal(RespondResponse{Kind: "question"})
	kind, err := respondRoundTrip(t, Response{Success: true, Data: payload})
	if err != nil {
		t.Fatalf("Respond returned err=%v, want nil", err)
	}
	if kind != "question" {
		t.Errorf("kind = %q, want %q", kind, "question")
	}
}

// TestClientRespond_MissingDataIsNotAnError covers a daemon too old to send
// the payload. The answer still landed, and turning that into an error would
// report a failure for work that succeeded.
func TestClientRespond_MissingDataIsNotAnError(t *testing.T) {
	kind, err := respondRoundTrip(t, Response{Success: true})
	if err != nil {
		t.Fatalf("Respond returned err=%v, want nil", err)
	}
	if kind != "" {
		t.Errorf("kind = %q, want empty", kind)
	}
}

// TestClientRespond_NotClearedIsTyped is the other half of the exit-code
// contract: the daemon tags this one failure on the wire and the client turns
// the tag back into a sentinel, so the CLI never has to match message text.
func TestClientRespond_NotClearedIsTyped(t *testing.T) {
	_, err := respondRoundTrip(t, Response{
		Success: false,
		Error:   RespondNotClearedPrefix + "the prompt was still on screen after 10s",
	})
	if err == nil {
		t.Fatal("Respond returned nil, want an error")
	}
	if !errors.Is(err, ErrRespondNotCleared) {
		t.Errorf("error %v does not wrap ErrRespondNotCleared", err)
	}
	if strings.Contains(err.Error(), RespondNotClearedPrefix) {
		t.Errorf("error %q still carries the wire tag; it is not for people to read", err)
	}
}

func TestClientRespond_OtherErrorsAreNotTyped(t *testing.T) {
	_, err := respondRoundTrip(t, Response{Success: false, Error: "session not found: sess"})
	if err == nil {
		t.Fatal("Respond returned nil, want an error")
	}
	if errors.Is(err, ErrRespondNotCleared) {
		t.Error("an unrelated failure was classified as not-cleared")
	}
}
