package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests for SendPrompt's paste transport — the path an adapter selects by
// returning a placeholder from PastePlaceholder. Why it exists, and the
// numbers behind it, are in docs/gotchas.md "Session send".

// opencodePlaceholder imitates the real adapter closely enough to exercise the
// manager: a summary line carrying a count derived from the prompt.
//
// It cannot be shared with internal/agent/opencode — that package imports
// session, so importing it back would cycle. Keep the two in step by hand;
// the real one is pinned by its own package's tests.
func opencodePlaceholder(prompt string) string {
	return fmt.Sprintf("[Pasted ~%d lines]", strings.Count(prompt, "\n")+1)
}

// newPasteSession wires a manager whose "claude" adapter takes the paste
// transport, plus an idle session on a pane named after the test.
func newPasteSession(t *testing.T, name string, budget time.Duration) (*Manager, *mockTmuxRunner, *Session, string) {
	t.Helper()
	mgr, mock, _ := newTestManager(t)
	withShortSendVerify(t, budget, time.Millisecond, time.Millisecond)
	fakeClaudeAgent(t, mgr).pasteFn = opencodePlaceholder
	pane := "%" + name
	return mgr, mock, newIdleSessionWithPane(t, mgr, "/tmp/send-"+name, name, pane), pane
}

func TestSendPrompt_PastesInOnePieceWhenAdapterFolds(t *testing.T) {
	mgr, mock, sess, pane := newPasteSession(t, "paste", 2*time.Second)
	withSmallChunks(t, 10) // would force many chunks on the keystroke path

	prompt := "line one\nline two\nline three"
	mock.capturedSequence[pane] = []string{
		"$ ",
		"$ [Pasted ~3 lines]",
	}

	if err := mgr.SendPrompt(sess.ID, prompt); err != nil {
		t.Fatalf("SendPrompt returned err=%v, want nil", err)
	}

	// The whole prompt goes over in a single buffer. This is the property
	// that removes the argv limit and the leading-dash chunk hazard at once.
	got, ok := mock.pastedContent(sendPasteBufferName)
	if !ok {
		t.Fatal("prompt was never loaded into a tmux buffer")
	}
	if got != prompt {
		t.Errorf("buffer content = %q, want %q", got, prompt)
	}
	if n := countCalls(mock, "PasteBuffer", pane); n != 1 {
		t.Errorf("PasteBuffer called %d times, want exactly 1", n)
	}
	if n := countCalls(mock, "SendKeysLiteral", pane); n != 0 {
		t.Errorf("SendKeysLiteral called %d times — the prompt must not also be typed", n)
	}
}

// TestSendPrompt_PasteVerifyShapes covers what the paste path will and will
// not accept as evidence that the prompt arrived. All three cases are the
// same three statements over different pane content, so they are a table.
func TestSendPrompt_PasteVerifyShapes(t *testing.T) {
	stale := "$ earlier message [Pasted ~2 lines] and its reply"
	cases := []struct {
		name     string
		prompt   string
		captures []string
		wantErr  bool
		why      string
	}{
		{
			name: "wrong count is rejected", prompt: "a\nb\nc",
			captures: []string{"$ ", "$ [Pasted ~9 lines]"}, wantErr: true,
			why: "the count is the whole check once the text is folded out of sight",
		},
		{
			name: "placeholder predating the send is rejected", prompt: "a\nb",
			captures: []string{stale, stale}, wantErr: true,
			why: "an earlier turn must not vouch for a prompt that never arrived",
		},
		{
			name: "unfolded paste falls back to the tail", prompt: "hi there",
			captures: []string{"$ ", "$ hi there"}, wantErr: false,
			why: "OpenCode inserts small pastes as text; that is still a landed paste",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, mock, sess, pane := newPasteSession(t, "shape", 300*time.Millisecond)
			mock.capturedSequence[pane] = tc.captures

			err := mgr.SendPrompt(sess.ID, tc.prompt)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SendPrompt returned nil, want an error — %s", tc.why)
				}
				if n := countCallsWithArgs(mock, "SendKeys", pane, "Enter"); n != 0 {
					t.Error("Enter was pressed despite verify failing")
				}
				return
			}
			if err != nil {
				t.Fatalf("SendPrompt returned err=%v, want nil — %s", err, tc.why)
			}
		})
	}
}

func TestSendPrompt_PasteDoesNotNudge(t *testing.T) {
	// The nudge exists to walk a cursor through text that is off screen. A
	// folded paste has no such text, and sending keys into it could only
	// disturb what we are about to submit.
	mgr, mock, sess, pane := newPasteSession(t, "pastenudge", 2*time.Second)
	mock.capturedSequence[pane] = []string{"$ ", "$ [Pasted ~2 lines]"}

	if err := mgr.SendPrompt(sess.ID, "a\nb"); err != nil {
		t.Fatalf("SendPrompt returned err=%v, want nil", err)
	}
	if n := countCallsWithArgs(mock, "SendKeys", pane, sendNudgeKey); n != 0 {
		t.Errorf("nudge key %q was sent %d times on the paste path", sendNudgeKey, n)
	}
}

func TestSendPrompt_KeystrokePathUnchangedWhenNoPlaceholder(t *testing.T) {
	// The negative control: adapters that did not opt in must still be typed
	// to, or this change would silently reroute Claude Code and Codex.
	mgr, mock, _ := newTestManager(t)
	withShortSendVerify(t, 2*time.Second, time.Millisecond, time.Millisecond)

	const pane = "%nopaste"
	prompt := "plain prompt"
	sess := newIdleSessionWithPane(t, mgr, "/tmp/send-nopaste", "nopaste", pane)
	mock.capturedSequence[pane] = []string{"$ ", "$ " + prompt}

	if err := mgr.SendPrompt(sess.ID, prompt); err != nil {
		t.Fatalf("SendPrompt returned err=%v, want nil", err)
	}
	if _, ok := mock.pastedContent(sendPasteBufferName); ok {
		t.Error("prompt was pasted even though the adapter returned no placeholder")
	}
	if n := countCalls(mock, "SendKeysLiteral", pane); n == 0 {
		t.Error("prompt was never typed on the keystroke path")
	}
}

// TestSendPrompt_PasteReportsTransportErrors covers both tmux calls the paste
// path makes. Each names its own step so a failure says which one broke.
func TestSendPrompt_PasteReportsTransportErrors(t *testing.T) {
	cases := []struct {
		name    string
		inject  func(*mockTmuxRunner)
		wantMsg string
	}{
		{"load fails", func(m *mockTmuxRunner) { m.loadBufferErr = errTmuxTransport }, "load paste buffer"},
		{"paste fails", func(m *mockTmuxRunner) { m.pasteBufferErr = errTmuxTransport }, "paste prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, mock, sess, pane := newPasteSession(t, "pasteerr", 300*time.Millisecond)
			tc.inject(mock)
			mock.capturedSequence[pane] = []string{"$ ", "$ [Pasted ~1 lines]"}

			err := mgr.SendPrompt(sess.ID, "boom")
			if err == nil {
				t.Fatalf("SendPrompt returned nil despite the tmux call failing")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %v, want it to name the failing step (%q)", err, tc.wantMsg)
			}
		})
	}
}

// errTmuxTransport is a sentinel so the transport-failure tests assert on the
// wrapping, not on an error string invented at the call site.
var errTmuxTransport = errors.New("tmux: no space left")

// TestSendPrompt_PasteClampsClearRepeats pins the clamp that makes a large
// paste fast. Without it a 64KB prompt spends 512 tmux processes clearing a
// single row of residue, which measured as the bulk of the send (1.12s vs
// 114ms). The keystroke path must keep the unclamped count, where residue
// really can be the whole prompt typed out.
func TestSendPrompt_PasteClampsClearRepeats(t *testing.T) {
	prompt := strings.Repeat("x", 64*1024)

	t.Run("paste path is clamped", func(t *testing.T) {
		mgr, mock, sess, pane := newPasteSession(t, "clamp", 5*time.Second)
		installClearKeys(t, mgr, []string{"C-u"})
		mock.capturedSequence[pane] = []string{"$ ", "$ [Pasted ~1 lines]"}

		if err := mgr.SendPrompt(sess.ID, prompt); err != nil {
			t.Fatalf("SendPrompt returned err=%v, want nil", err)
		}
		got := countCallsWithArgs(mock, "SendKeys", pane, "C-u")
		want := sendClearRepeats(sendChunkMaxBytes)
		if got != want {
			t.Errorf("clear pressed %d times, want %d (clamped to the fold-threshold bound)", got, want)
		}
	})

	t.Run("keystroke path is not clamped", func(t *testing.T) {
		mgr, mock, _ := newTestManager(t)
		withShortSendVerify(t, 5*time.Second, time.Millisecond, time.Millisecond)
		installClearKeys(t, mgr, []string{"C-u"})

		const pane = "%noclamp"
		sess := newIdleSessionWithPane(t, mgr, "/tmp/send-noclamp", "noclamp", pane)
		mock.capturedSequence[pane] = []string{"$ ", "$ " + prompt}

		if err := mgr.SendPrompt(sess.ID, prompt); err != nil {
			t.Fatalf("SendPrompt returned err=%v, want nil", err)
		}
		got := countCallsWithArgs(mock, "SendKeys", pane, "C-u")
		if want := sendClearRepeats(len(prompt)); got != want {
			t.Errorf("clear pressed %d times, want %d (unclamped)", got, want)
		}
	})
}
