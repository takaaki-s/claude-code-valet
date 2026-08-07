package session

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/transcript"
)

// stubTranscript answers with fixed entries and records what it was asked for.
// Manager.List reads its rows concurrently, so one stub serves several
// goroutines and the bookkeeping is guarded — without the mutex the recording
// itself would be the only race in the test.
type stubTranscript struct {
	entries []transcript.Entry
	err     error

	mu      sync.Mutex
	calls   int
	workDir string
	sessID  string
	since   string
}

func (s *stubTranscript) ReadEntries(workDir, sessionID, since string) ([]transcript.Entry, error) {
	s.mu.Lock()
	s.calls++
	s.workDir, s.sessID, s.since = workDir, sessionID, since
	s.mu.Unlock()
	return s.entries, s.err
}

func (s *stubTranscript) seen() (calls int, workDir, sessID, since string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.workDir, s.sessID, s.since
}

// installTranscript gives the manager's already-installed claude adapter a
// transcript reader, through the same accessor the other capability helpers
// use so nothing else a test set up is discarded.
func installTranscript(t *testing.T, mgr *Manager, src TranscriptSource) {
	t.Helper()
	fakeClaudeAgent(t, mgr).transcriptSrc = src
}

func said(kind, text string) transcript.Entry {
	return transcript.Entry{Type: kind, Blocks: []transcript.Block{{Kind: "text", Text: text}}}
}

// readySession is a session far enough along to have a conversation: the kind
// installTranscript registers, plus the two fields AttachLastMessages needs.
func readySession() Info {
	return Info{AgentKind: "claude", AgentSessionID: "s", WorkDir: "/tmp/x"}
}

// TestAttachLastMessages_ReadsThroughTheAdapter is the wiring this change
// exists for. The previews used to come from the Claude Code reader whatever
// kind the session ran, so a codex or opencode row stayed blank forever.
func TestAttachLastMessages_ReadsThroughTheAdapter(t *testing.T) {
	src := &stubTranscript{entries: []transcript.Entry{said("user", "do the thing"), said("assistant", "done")}}
	mgr, _, _ := newTestManager(t)
	installTranscript(t, mgr, src)

	info := Info{AgentKind: "claude", AgentSessionID: "sess-1", WorkDir: "/tmp/example"}
	mgr.AttachLastMessages(&info)

	calls, workDir, sessID, since := src.seen()
	if calls != 1 {
		t.Fatalf("adapter reader called %d times, want 1", calls)
	}
	if sessID != "sess-1" || workDir != "/tmp/example" {
		t.Errorf("read args = (%q, %q), want the session's own", workDir, sessID)
	}
	if since != "" {
		t.Errorf("since = %q, want empty — the previews always want the whole conversation", since)
	}
	if info.LastUserMessage != "do the thing" {
		t.Errorf("LastUserMessage = %q", info.LastUserMessage)
	}
	if info.LastAssistantMessage != "done" {
		t.Errorf("LastAssistantMessage = %q", info.LastAssistantMessage)
	}
}

// TestAttachLastMessages_ReadsTheKindTheSessionRuns is the defect itself,
// stated as a test. The previews came from the Claude Code reader no matter
// what the session ran, so this fails for any implementation that resolves a
// fixed kind instead of the session's own — which is exactly what the old code
// did and what a refactor could quietly restore.
func TestAttachLastMessages_ReadsTheKindTheSessionRuns(t *testing.T) {
	claude := &stubTranscript{entries: []transcript.Entry{said("assistant", "from the claude reader")}}
	codex := &stubTranscript{entries: []transcript.Entry{said("assistant", "from the codex reader")}}
	mgr, _, _ := newTestManager(t)
	mgr.SetAgentResolver(&fakeAgentResolver{agents: map[string]Agent{
		"claude": &fakeAgent{transcriptSrc: claude},
		"codex":  &fakeAgent{transcriptSrc: codex},
	}})

	info := Info{AgentKind: "codex", AgentSessionID: "s", WorkDir: "/tmp/x"}
	mgr.AttachLastMessages(&info)

	if calls, _, _, _ := claude.seen(); calls != 0 {
		t.Errorf("the other kind's reader was called %d times, want 0", calls)
	}
	if info.LastAssistantMessage != "from the codex reader" {
		t.Errorf("LastAssistantMessage = %q, want the session's own adapter", info.LastAssistantMessage)
	}
}

// TestAttachLastMessages_TruncatesTowardWhatMatters pins the two ends apart.
// A prompt is identified by how it opens, so the user preview keeps its head;
// an agent's closing question is what an operator needs to see, so the
// assistant preview keeps its tail. Swap them and both rows still fill with
// plausible-looking text.
func TestAttachLastMessages_TruncatesTowardWhatMatters(t *testing.T) {
	userMsg := "HEAD " + strings.Repeat("u", 600) + " USERTAIL"
	astMsg := "ASTHEAD " + strings.Repeat("a", 600) + " TAIL"
	src := &stubTranscript{entries: []transcript.Entry{said("user", userMsg), said("assistant", astMsg)}}
	mgr, _, _ := newTestManager(t)
	installTranscript(t, mgr, src)

	info := readySession()
	mgr.AttachLastMessages(&info)

	if !strings.HasPrefix(info.LastUserMessage, "HEAD ") || !strings.HasSuffix(info.LastUserMessage, "...") {
		t.Errorf("LastUserMessage = %q..., want the opening kept and the tail dropped", info.LastUserMessage[:20])
	}
	if !strings.HasPrefix(info.LastAssistantMessage, "...") || !strings.HasSuffix(info.LastAssistantMessage, " TAIL") {
		t.Errorf("LastAssistantMessage = %q, want the closing kept and the opening dropped",
			info.LastAssistantMessage[:20])
	}
}

// TestList_AttachesPreviewsToEveryRow covers the call site rather than the
// function: the previews are what the list rows and the TUI's second line
// render, and a List that stopped attaching them would leave every unit test
// above green while the screen went blank.
func TestList_AttachesPreviewsToEveryRow(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	installTranscript(t, mgr, &stubTranscript{entries: []transcript.Entry{
		said("user", "do the thing"), said("assistant", "done"),
	}})
	for _, dir := range []string{"/tmp/row-1", "/tmp/row-2"} {
		if _, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: dir}); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	infos := mgr.List()
	if len(infos) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(infos))
	}
	for i, info := range infos {
		if info.LastUserMessage != "do the thing" || info.LastAssistantMessage != "done" {
			t.Errorf("row %d previews = (%q, %q), want both filled",
				i, info.LastUserMessage, info.LastAssistantMessage)
		}
	}
}

// TestAttachLastMessages_SilentOnEveryFailure pins the difference from
// `jin session result`. This decorates a row that has to render either way, so
// an unreadable transcript leaves the previews empty rather than failing the
// whole list.
func TestAttachLastMessages_SilentOnEveryFailure(t *testing.T) {
	tests := []struct {
		name string
		src  TranscriptSource
		info Info
	}{
		{"adapter cannot read", nil, readySession()},
		{"read failed", &stubTranscript{err: errors.New("disk went away")}, readySession()},
		// A reader may hand back what it managed to parse alongside the error.
		// Those entries stop at whatever line broke, so the last message in
		// them is not the last message in the conversation — showing it would
		// pin the row to a stale turn with no sign anything was wrong.
		{"partial read", &stubTranscript{
			entries: []transcript.Entry{said("user", "half a conversation")},
			err:     errors.New("truncated"),
		}, readySession()},
		{"nothing said yet", &stubTranscript{}, readySession()},
		{"agent not started", &stubTranscript{entries: []transcript.Entry{said("user", "x")}}, Info{AgentKind: "claude", WorkDir: "/tmp/x"}},
		{"no workdir", &stubTranscript{entries: []transcript.Entry{said("user", "x")}}, Info{AgentKind: "claude", AgentSessionID: "s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, _, _ := newTestManager(t)
			installTranscript(t, mgr, tt.src)
			info := tt.info
			mgr.AttachLastMessages(&info)
			if info.LastUserMessage != "" || info.LastAssistantMessage != "" {
				t.Errorf("previews = (%q, %q), want both empty", info.LastUserMessage, info.LastAssistantMessage)
			}
		})
	}
}

// TestAttachLastMessages_ExcludesInjected proves the shared view's filtering
// survives the wiring — a preview built from raw entries would put the body of
// an injected skill into the row, which is where an absolute path would end up
// on screen.
func TestAttachLastMessages_ExcludesInjected(t *testing.T) {
	injected := said("user", "injected context nobody typed")
	injected.Injected = true
	src := &stubTranscript{entries: []transcript.Entry{said("user", "the real prompt"), injected}}
	mgr, _, _ := newTestManager(t)
	installTranscript(t, mgr, src)

	info := readySession()
	mgr.AttachLastMessages(&info)
	if info.LastUserMessage != "the real prompt" {
		t.Errorf("LastUserMessage = %q, want the operator's own words", info.LastUserMessage)
	}
}
