package daemon

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/agent/agenttest"
	"github.com/takaaki-s/jind-ai/internal/agent/opencode"
	"github.com/takaaki-s/jind-ai/internal/procgroup"
	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/transcript"
)

// recordingSource is a TranscriptSource that answers with fixed entries and
// remembers what it was asked for, so a test can pin the arguments handleResult
// forwards as well as the result it returns.
type recordingSource struct {
	entries []transcript.Entry
	err     error

	calls           int
	workDir, sessID string
	since           string
}

// CheapEnoughToPoll makes the stub stand in for a file-backed reader, which is
// what the preview path requires of a source before it will call it. Without
// this, every preview assertion would pass for the wrong reason — see
// internal/session's TestAttachLastMessages_SkipsAReaderThatIsNotCheapToPoll.
func (s *recordingSource) CheapEnoughToPoll() {}

func (s *recordingSource) ReadEntries(workDir, sessionID, since string) ([]transcript.Entry, error) {
	s.calls++
	s.workDir, s.sessID, s.since = workDir, sessionID, since
	return s.entries, s.err
}

// reserveSession registers a session of the given kind without spawning
// anything. ReserveCreation is pure state: no tmux, no goroutines.
func reserveSession(t *testing.T, s *Server, kind string) session.Info {
	t.Helper()
	_, info, err := s.manager.ReserveCreation(session.CreateOptions{
		AgentKind: kind,
		WorkDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ReserveCreation: %v", err)
	}
	return info
}

func resultFor(t *testing.T, s *Server, req ResultRequest) Response {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return s.handleResult(data)
}

func decodeResult(t *testing.T, resp Response) ResultResponse {
	t.Helper()
	if !resp.Success {
		t.Fatalf("Success=false: %s", resp.Error)
	}
	var out ResultResponse
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out
}

// TestHandleResult_UsesTheSessionsOwnAdapter is the wiring this change exists
// for. handleResult used to construct the Claude Code reader unconditionally,
// so every session was read as if it were a Claude session regardless of its
// kind. Two kinds are registered with different sources; the answer must come
// from the one the session actually runs.
func TestHandleResult_UsesTheSessionsOwnAdapter(t *testing.T) {
	s := newAsyncTestServer(t)

	mine := &recordingSource{entries: []transcript.Entry{
		{Type: "assistant", Timestamp: "2026-07-11T07:00:00.000Z",
			Blocks: []transcript.Block{{Kind: "text", Text: "from the right adapter"}}},
	}}
	theirs := &recordingSource{entries: []transcript.Entry{
		{Type: "assistant", Blocks: []transcript.Block{{Kind: "text", Text: "from the wrong adapter"}}},
	}}
	agent.Register(&agenttest.StubAgent{KindStr: "mine", TranscriptSrc: mine})
	agent.Register(&agenttest.StubAgent{KindStr: "theirs", TranscriptSrc: theirs})

	info := reserveSession(t, s, "mine")
	got := decodeResult(t, resultFor(t, s, ResultRequest{ID: info.ID, Since: "2026-07-11T06:00:00.000Z"}))

	if theirs.calls != 0 {
		t.Errorf("the other kind's reader was called %d times, want 0", theirs.calls)
	}
	if mine.calls != 1 {
		t.Fatalf("the session's own reader was called %d times, want 1", mine.calls)
	}
	if mine.sessID != info.AgentSessionID {
		t.Errorf("sessionID = %q, want %q", mine.sessID, info.AgentSessionID)
	}
	if mine.workDir != info.WorkDir {
		t.Errorf("workDir = %q, want %q", mine.workDir, info.WorkDir)
	}
	if mine.since != "2026-07-11T06:00:00.000Z" {
		t.Errorf("since = %q, want it forwarded verbatim", mine.since)
	}
	if len(got.Entries) != 1 || got.Entries[0].Blocks[0].Text != "from the right adapter" {
		t.Errorf("entries = %+v, want the ones the session's adapter returned", got.Entries)
	}
}

// TestHandleResult_PrefersTheSessionsCurrentWorkDir pins behaviour this change
// had to carry over untouched. A session's directory moves when the agent
// changes it, and the Claude Code reader locates a transcript by encoding that
// directory into a path — hand it the original and it looks in the wrong
// place, then reports the conversation as empty.
func TestHandleResult_PrefersTheSessionsCurrentWorkDir(t *testing.T) {
	s := newAsyncTestServer(t)
	src := &recordingSource{}
	agent.Register(&agenttest.StubAgent{KindStr: "readable", TranscriptSrc: src})

	info := reserveSession(t, s, "readable")
	sess, ok := s.manager.Get(info.ID)
	if !ok {
		t.Fatalf("session %s vanished", info.ID)
	}
	moved := t.TempDir()
	sess.CurrentWorkDir = moved

	decodeResult(t, resultFor(t, s, ResultRequest{ID: info.ID}))
	if src.workDir != moved {
		t.Errorf("workDir = %q, want the current directory %q, not the original %q",
			src.workDir, moved, info.WorkDir)
	}
}

// TestHandleResult_UnreadableAdapterFails pins the deliberate breaking change.
// An adapter with no transcript reader used to yield zero entries and success,
// which reads exactly like a child agent that ran and said nothing. An
// orchestrator cannot tell those apart, so the honest answer is a failure that
// names the kind.
func TestHandleResult_UnreadableAdapterFails(t *testing.T) {
	s := newAsyncTestServer(t)
	agent.Register(&agenttest.StubAgent{KindStr: "unreadable"}) // TranscriptSrc nil

	info := reserveSession(t, s, "unreadable")
	resp := resultFor(t, s, ResultRequest{ID: info.ID})

	if resp.Success {
		t.Fatalf("Success=true with data=%s, want a failure", resp.Data)
	}
	if !strings.Contains(resp.Error, "unreadable") {
		t.Errorf("Error = %q, want it to name the agent kind", resp.Error)
	}
}

// TestHandleResult_NotStartedYetStaysEmptyAndSuccessful guards the other side
// of the same line. A session whose agent has never been launched has no
// transcript to read, and that is not a failure — it is the state every
// session begins in. The check runs against the unreadable kind on purpose:
// "not started" must win over "cannot read", so this fails if the two branches
// are ever reordered.
func TestHandleResult_NotStartedYetStaysEmptyAndSuccessful(t *testing.T) {
	s := newAsyncTestServer(t)
	agent.Register(&agenttest.StubAgent{KindStr: "unreadable"})

	info := reserveSession(t, s, "unreadable")
	sess, ok := s.manager.Get(info.ID)
	if !ok {
		t.Fatalf("session %s vanished", info.ID)
	}
	sess.AgentSessionID = ""

	got := decodeResult(t, resultFor(t, s, ResultRequest{ID: info.ID}))
	if len(got.Entries) != 0 {
		t.Errorf("entries = %+v, want none", got.Entries)
	}
}

func TestHandleResult_UnknownKindFails(t *testing.T) {
	s := newAsyncTestServer(t)

	info := reserveSession(t, s, "never-registered")
	resp := resultFor(t, s, ResultRequest{ID: info.ID})

	if resp.Success {
		t.Fatalf("Success=true with data=%s, want a failure", resp.Data)
	}
	if !strings.Contains(resp.Error, "never-registered") {
		t.Errorf("Error = %q, want it to name the unknown kind", resp.Error)
	}
}

func TestHandleResult_ReadErrorPropagates(t *testing.T) {
	s := newAsyncTestServer(t)
	src := &recordingSource{err: errors.New("disk went away")}
	agent.Register(&agenttest.StubAgent{KindStr: "flaky", TranscriptSrc: src})

	info := reserveSession(t, s, "flaky")
	resp := resultFor(t, s, ResultRequest{ID: info.ID})

	if resp.Success {
		t.Fatalf("Success=true with data=%s, want the read error surfaced", resp.Data)
	}
	if !strings.Contains(resp.Error, "disk went away") {
		t.Errorf("Error = %q, want the reader's message", resp.Error)
	}
}

func TestHandleResult_LastTruncatesFromTheEnd(t *testing.T) {
	s := newAsyncTestServer(t)
	src := &recordingSource{entries: []transcript.Entry{
		{Type: "user", Blocks: []transcript.Block{{Kind: "text", Text: "one"}}},
		{Type: "assistant", Blocks: []transcript.Block{{Kind: "text", Text: "two"}}},
		{Type: "user", Blocks: []transcript.Block{{Kind: "text", Text: "three"}}},
	}}
	agent.Register(&agenttest.StubAgent{KindStr: "readable", TranscriptSrc: src})

	info := reserveSession(t, s, "readable")
	got := decodeResult(t, resultFor(t, s, ResultRequest{ID: info.ID, Last: 2}))

	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(got.Entries) != 2 || got.Entries[0].Blocks[0].Text != "two" {
		t.Errorf("entries = %+v, want the last two", got.Entries)
	}
}

// TestHandleResult_EntriesIsAlwaysAnArray pins the wire shape.
//
// A reader that found nothing returns nil, and filterResultEntries passes nil
// straight through when neither filter is set, so `entries` marshalled as
// `null`. The agent-facing docs tell an orchestrator to run
// `jq '.entries[] | select(.type=="system")'` to tell "recorded nothing" from
// "lost the conversation" — and jq dies on null at exactly that moment.
func TestHandleResult_EntriesIsAlwaysAnArray(t *testing.T) {
	for _, tt := range []struct {
		name string
		req  ResultRequest
	}{
		{"no filters", ResultRequest{}},
		{"tool filter", ResultRequest{Tool: "Bash"}},
		{"errors only", ResultRequest{ErrorsOnly: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newAsyncTestServer(t)
			// A source that succeeds and returns nil — "the agent has not
			// written anything", which is a state, not a failure.
			kind := "nilsource-" + tt.name
			agent.Register(&agenttest.StubAgent{KindStr: kind, TranscriptSrc: &recordingSource{entries: nil}})
			info := reserveSession(t, srv, kind)

			tt.req.ID = info.ID
			data, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			resp := srv.handleResult(data)
			if !resp.Success {
				t.Fatalf("handleResult failed: %s", resp.Error)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(resp.Data, &raw); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := string(raw["entries"]); got != "[]" {
				t.Errorf("entries = %s, want [] — jq cannot iterate over null", got)
			}
		})
	}
}

// TestExportTimeoutFitsInsideTheClientBudget holds a relationship that two
// comments in two packages assert to each other and nothing enforces.
//
// `session result` on an opencode session runs a subprocess bounded by the
// adapter's own timeout. If that ever grows past what the client waits for,
// the client reports a timeout while the daemon is still working and may yet
// answer — the failure mode the client's own budget comment was written to
// avoid. The margin covers the teardown: a process that ignores SIGTERM is
// killed a grace period later, and Wait can hold on a little past that.
func TestExportTimeoutFitsInsideTheClientBudget(t *testing.T) {
	worst := opencode.ExportTimeout + procgroup.GracePeriod
	if worst >= defaultRequestTimeout {
		t.Errorf("an opencode result can take up to %s (export %s + teardown %s), but the client gives up at %s",
			worst, opencode.ExportTimeout, procgroup.GracePeriod, defaultRequestTimeout)
	}
}
