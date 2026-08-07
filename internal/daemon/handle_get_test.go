package daemon

import (
	"encoding/json"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/agent/agenttest"
	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/transcript"
)

func getFor(t *testing.T, s *Server, id string) session.Info {
	t.Helper()
	data, err := json.Marshal(IDRequest{ID: id})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp := s.handleGet(data)
	if !resp.Success {
		t.Fatalf("Success=false: %s", resp.Error)
	}
	var out session.Info
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out
}

// TestHandleGet_FillsPreviewsThroughTheSessionsAdapter covers the second call
// site of the same fix. `jin session info` built the Claude Code reader
// directly, so a codex session's two previews were blank for good — and since
// the read error was discarded, blank looked identical to a session that had
// not spoken yet.
//
// Two kinds are registered so this fails both for an implementation that stops
// attaching previews and for one that attaches the wrong adapter's.
func TestHandleGet_FillsPreviewsThroughTheSessionsAdapter(t *testing.T) {
	s := newAsyncTestServer(t)

	mine := &recordingSource{entries: []transcript.Entry{
		{Type: "user", Blocks: []transcript.Block{{Kind: "text", Text: "do the thing"}}},
		{Type: "assistant", Blocks: []transcript.Block{{Kind: "text", Text: "done"}}},
	}}
	theirs := &recordingSource{entries: []transcript.Entry{
		{Type: "assistant", Blocks: []transcript.Block{{Kind: "text", Text: "from the wrong adapter"}}},
	}}
	agent.Register(&agenttest.StubAgent{KindStr: "mine", TranscriptSrc: mine})
	agent.Register(&agenttest.StubAgent{KindStr: "theirs", TranscriptSrc: theirs})

	info := reserveSession(t, s, "mine")
	got := getFor(t, s, info.ID)

	if theirs.calls != 0 {
		t.Errorf("the other kind's reader was called %d times, want 0", theirs.calls)
	}
	if got.LastUserMessage != "do the thing" {
		t.Errorf("LastUserMessage = %q, want it read through the session's adapter", got.LastUserMessage)
	}
	if got.LastAssistantMessage != "done" {
		t.Errorf("LastAssistantMessage = %q, want it read through the session's adapter", got.LastAssistantMessage)
	}
}

// TestHandleGet_UnreadableAdapterStillAnswers pins the deliberate difference
// from `jin session result`, which fails outright when the adapter cannot read
// a conversation. `session info` reports the session itself, so an adapter
// with no reader costs the two previews and nothing else.
func TestHandleGet_UnreadableAdapterStillAnswers(t *testing.T) {
	s := newAsyncTestServer(t)
	agent.Register(&agenttest.StubAgent{KindStr: "unreadable"}) // TranscriptSrc nil

	info := reserveSession(t, s, "unreadable")
	got := getFor(t, s, info.ID)

	if got.ID != info.ID {
		t.Errorf("ID = %q, want the session it asked for", got.ID)
	}
	if got.LastUserMessage != "" || got.LastAssistantMessage != "" {
		t.Errorf("previews = (%q, %q), want both empty", got.LastUserMessage, got.LastAssistantMessage)
	}
}
