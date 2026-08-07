package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/transcript"
)

func TestMapTranscriptErr(t *testing.T) {
	readFailure := errors.New("token too long")

	tests := []struct {
		name       string
		err        error
		want       error // non-nil when the result must be this exact sentinel
		wantWraps  error // non-nil when the result must still unwrap to this
		wantSubstr string
	}{
		{
			name: "missing transcript becomes the actionable message",
			err:  transcript.ErrNoTranscript,
			want: errNoTranscriptFound,
		},
		{
			name: "missing transcript is recognised through a wrapper",
			err:  fmt.Errorf("reading %s: %w", "sess-1", transcript.ErrNoTranscript),
			want: errNoTranscriptFound,
		},
		{
			name:       "any other failure keeps its cause and gains context",
			err:        readFailure,
			wantWraps:  readFailure,
			wantSubstr: "failed to read transcript",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapTranscriptErr(tc.err)
			if tc.want != nil && !errors.Is(got, tc.want) {
				t.Fatalf("mapTranscriptErr = %v, want %v", got, tc.want)
			}
			if tc.wantWraps != nil {
				if !errors.Is(got, tc.wantWraps) {
					t.Errorf("mapTranscriptErr = %v, want it to wrap %v", got, tc.wantWraps)
				}
				if errors.Is(got, errNoTranscriptFound) {
					t.Errorf("mapTranscriptErr = %v, want it kept apart from the missing-transcript case", got)
				}
			}
			if tc.wantSubstr != "" && !strings.Contains(got.Error(), tc.wantSubstr) {
				t.Errorf("mapTranscriptErr = %q, want it to mention %q", got, tc.wantSubstr)
			}
		})
	}
}

// TestOutputCmd_NegativeLastRejected verifies that a negative --last is refused
// locally, before the daemon client is built — the same shape as
// TestDeleteCmd_ForceWorktreeRequiresWorktree. Without the guard the value
// reaches GetConversation, whose own "lastN must be >= 1" check is worded for
// the library caller, not for someone at the CLI.
func TestOutputCmd_NegativeLastRejected(t *testing.T) {
	flags := outputCmd.Flags()
	if err := flags.Set("last", "-1"); err != nil {
		t.Fatalf("Set(last): %v", err)
	}
	t.Cleanup(func() {
		_ = flags.Set("last", "0")
	})

	err := outputCmd.RunE(outputCmd, []string{"some-session"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "--last must be >= 0") {
		t.Errorf("error message = %q, want to contain %q", err.Error(), "--last must be >= 0")
	}
}

// fakeConversationReader stands in for *transcript.Reader. It records which
// path was taken so a test can pin the reader call runOutput made, not only
// what came out the other end.
type fakeConversationReader struct {
	conversation    []transcript.Message
	conversationErr error
	lastMessage     *transcript.Message
	lastMessageErr  error

	conversationCalls int
	lastMessageCalls  int
	gotWorkDir        string
	gotSessionID      string
	gotLastN          int
}

func (f *fakeConversationReader) GetConversation(workDir, sessionID string, lastN int) ([]transcript.Message, error) {
	f.conversationCalls++
	f.gotWorkDir, f.gotSessionID, f.gotLastN = workDir, sessionID, lastN
	return f.conversation, f.conversationErr
}

func (f *fakeConversationReader) GetLastMessage(workDir, sessionID string) (*transcript.Message, error) {
	f.lastMessageCalls++
	f.gotWorkDir, f.gotSessionID = workDir, sessionID
	return f.lastMessage, f.lastMessageErr
}

func TestRunOutput(t *testing.T) {
	const workDir = "/actual/worktree"
	const sessionID = "agent-sess-1"

	reply := &transcript.Message{Type: "assistant", Content: "the reply", Timestamp: "2025-01-01T00:00:01Z"}
	exchange := []transcript.Message{
		{Type: "user", Content: "Hi", Timestamp: "2025-01-01T00:00:00Z"},
		{Type: "assistant", Content: "Hello!", Timestamp: "2025-01-01T00:00:01Z"},
	}
	readFailure := errors.New("token too long")

	tests := []struct {
		name    string
		lastN   int
		jsonOut bool
		reader  fakeConversationReader

		wantStdout    string
		wantStderr    string
		wantErrIs     error
		wantErrSubstr string
		wantConvCalls int
		wantLastCalls int
	}{
		{
			name:          "no --last reads the newest message alone",
			lastN:         0,
			reader:        fakeConversationReader{lastMessage: reply},
			wantStdout:    "the reply\n",
			wantLastCalls: 1,
		},
		{
			name:          "--last N asks the reader for that many exchanges",
			lastN:         2,
			reader:        fakeConversationReader{conversation: exchange},
			wantStdout:    "[user] Hi\n[assistant] Hello!\n",
			wantConvCalls: 1,
		},
		{
			name:          "no --last, missing transcript becomes the actionable error",
			lastN:         0,
			reader:        fakeConversationReader{lastMessageErr: transcript.ErrNoTranscript},
			wantErrIs:     errNoTranscriptFound,
			wantLastCalls: 1,
		},
		{
			name:          "--last N, missing transcript becomes the actionable error",
			lastN:         1,
			reader:        fakeConversationReader{conversationErr: transcript.ErrNoTranscript},
			wantErrIs:     errNoTranscriptFound,
			wantConvCalls: 1,
		},
		{
			name:          "no --last, a read failure keeps its cause and gains context",
			lastN:         0,
			reader:        fakeConversationReader{lastMessageErr: readFailure},
			wantErrIs:     readFailure,
			wantErrSubstr: "failed to read transcript",
			wantLastCalls: 1,
		},
		{
			name:          "--last N, a read failure keeps its cause and gains context",
			lastN:         1,
			reader:        fakeConversationReader{conversationErr: readFailure},
			wantErrIs:     readFailure,
			wantErrSubstr: "failed to read transcript",
			wantConvCalls: 1,
		},
		{
			// The state commit 4a25c19 split apart: a transcript that exists
			// but has only tool activity in it. Printing an empty line and
			// exiting 0 would put it back where a missing transcript is.
			name:          "no --last, nothing readable is an error rather than an empty line",
			lastN:         0,
			reader:        fakeConversationReader{lastMessage: nil},
			wantErrSubstr: noReadableMessagesHint,
			wantLastCalls: 1,
		},
		{
			// Same state, opposite verdict: --last is a window over a
			// conversation, and an empty window is a phase every session goes
			// through. The note goes to stderr so stdout stays pipeable.
			name:          "--last N, nothing readable is a success with a note on stderr",
			lastN:         1,
			reader:        fakeConversationReader{conversation: nil},
			wantStdout:    "",
			wantStderr:    noReadableMessagesHint + "\n",
			wantConvCalls: 1,
		},
		{
			name:    "no --last with --json emits one object",
			lastN:   0,
			jsonOut: true,
			reader:  fakeConversationReader{lastMessage: reply},
			wantStdout: `{
  "Type": "assistant",
  "Content": "the reply",
  "Timestamp": "2025-01-01T00:00:01Z"
}
`,
			wantLastCalls: 1,
		},
		{
			name:    "--last N with --json emits an array",
			lastN:   1,
			jsonOut: true,
			reader:  fakeConversationReader{conversation: exchange},
			wantStdout: `[
  {
    "Type": "user",
    "Content": "Hi",
    "Timestamp": "2025-01-01T00:00:00Z"
  },
  {
    "Type": "assistant",
    "Content": "Hello!",
    "Timestamp": "2025-01-01T00:00:01Z"
  }
]
`,
			wantConvCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			r := tc.reader

			err := runOutput(&out, &errOut, &r, workDir, sessionID, tc.lastN, tc.jsonOut)

			if tc.wantErrIs == nil && tc.wantErrSubstr == "" {
				if err != nil {
					t.Fatalf("runOutput returned error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("runOutput returned nil, want an error")
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("error = %v, want it to be %v", err, tc.wantErrIs)
				}
				if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErrSubstr)
				}
			}

			if out.String() != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", out.String(), tc.wantStdout)
			}
			if errOut.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", errOut.String(), tc.wantStderr)
			}

			if r.conversationCalls != tc.wantConvCalls {
				t.Errorf("GetConversation calls = %d, want %d", r.conversationCalls, tc.wantConvCalls)
			}
			if r.lastMessageCalls != tc.wantLastCalls {
				t.Errorf("GetLastMessage calls = %d, want %d", r.lastMessageCalls, tc.wantLastCalls)
			}
			if tc.wantConvCalls > 0 && r.gotLastN != tc.lastN {
				t.Errorf("GetConversation lastN = %d, want %d", r.gotLastN, tc.lastN)
			}
			if r.gotWorkDir != workDir || r.gotSessionID != sessionID {
				t.Errorf("reader called with (%q, %q), want (%q, %q)", r.gotWorkDir, r.gotSessionID, workDir, sessionID)
			}
		})
	}
}

func TestRenderConversation(t *testing.T) {
	msgs := []transcript.Message{
		{Type: "user", Content: "Hi", Timestamp: "2025-01-01T00:00:00Z"},
		{Type: "assistant", Content: "Hello!", Timestamp: "2025-01-01T00:00:01Z"},
	}

	tests := []struct {
		name        string
		msgs        []transcript.Message
		jsonOut     bool
		wantStdout  string
		wantWarning bool
	}{
		{
			name:       "text output lists every message",
			msgs:       msgs,
			wantStdout: "[user] Hi\n[assistant] Hello!\n",
		},
		{
			name:        "nothing readable yet writes the note to stderr, not stdout",
			msgs:        nil,
			wantStdout:  "",
			wantWarning: true,
		},
		{
			name: "json output stays an array",
			msgs: msgs,
			// asserted by decoding below
			jsonOut: true,
		},
		{
			name:        "json output of nothing is [] rather than null",
			msgs:        nil,
			jsonOut:     true,
			wantWarning: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if err := renderConversation(&out, &errOut, tc.msgs, tc.jsonOut); err != nil {
				t.Fatalf("renderConversation returned error: %v", err)
			}

			if tc.jsonOut {
				if strings.Contains(out.String(), "null") {
					t.Errorf("stdout = %q, want an array even when empty", out.String())
				}
				var parsed []transcript.Message
				if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
					t.Fatalf("stdout is not a JSON array: %v (%q)", err, out.String())
				}
				if len(parsed) != len(tc.msgs) {
					t.Errorf("got %d messages, want %d", len(parsed), len(tc.msgs))
				}
			} else if out.String() != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", out.String(), tc.wantStdout)
			}

			gotWarning := strings.Contains(errOut.String(), noReadableMessagesHint)
			if gotWarning != tc.wantWarning {
				t.Errorf("stderr = %q, want the no-readable-messages note: %v", errOut.String(), tc.wantWarning)
			}
		})
	}
}

func TestTranscriptWorkDir(t *testing.T) {
	t.Run("prefers CurrentWorkDir when set", func(t *testing.T) {
		info := &session.Info{
			WorkDir:        "/original/launch",
			CurrentWorkDir: "/actual/worktree",
		}
		got := transcriptWorkDir(info)
		if got != "/actual/worktree" {
			t.Errorf("got %q, want %q", got, "/actual/worktree")
		}
	})

	t.Run("falls back to WorkDir when CurrentWorkDir is empty", func(t *testing.T) {
		info := &session.Info{WorkDir: "/original/launch"}
		got := transcriptWorkDir(info)
		if got != "/original/launch" {
			t.Errorf("got %q, want %q", got, "/original/launch")
		}
	})
}

func TestRenderOutputJSON(t *testing.T) {
	t.Run("outputs single message as JSON", func(t *testing.T) {
		msg := &transcript.Message{
			Type:      "assistant",
			Content:   "Hello, world!",
			Timestamp: "2025-01-01T00:00:00Z",
		}
		var buf bytes.Buffer
		if err := renderOutputJSON(&buf, msg); err != nil {
			t.Fatalf("renderOutputJSON returned error: %v", err)
		}

		var parsed transcript.Message
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}
		if parsed.Type != "assistant" {
			t.Errorf("type = %q, want %q", parsed.Type, "assistant")
		}
		if parsed.Content != "Hello, world!" {
			t.Errorf("content = %q, want %q", parsed.Content, "Hello, world!")
		}
	})

	t.Run("outputs message list as JSON", func(t *testing.T) {
		msgs := []transcript.Message{
			{Type: "user", Content: "Hi", Timestamp: "2025-01-01T00:00:00Z"},
			{Type: "assistant", Content: "Hello!", Timestamp: "2025-01-01T00:00:01Z"},
		}
		var buf bytes.Buffer
		if err := renderOutputJSON(&buf, msgs); err != nil {
			t.Fatalf("renderOutputJSON returned error: %v", err)
		}

		var parsed []transcript.Message
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}
		if len(parsed) != 2 {
			t.Fatalf("got %d messages, want 2", len(parsed))
		}
		if parsed[0].Type != "user" {
			t.Errorf("first message type = %q, want %q", parsed[0].Type, "user")
		}
		if parsed[1].Type != "assistant" {
			t.Errorf("second message type = %q, want %q", parsed[1].Type, "assistant")
		}
	})

	t.Run("outputs empty list as JSON", func(t *testing.T) {
		msgs := []transcript.Message{}
		var buf bytes.Buffer
		if err := renderOutputJSON(&buf, msgs); err != nil {
			t.Fatalf("renderOutputJSON returned error: %v", err)
		}

		var parsed []transcript.Message
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}
		if len(parsed) != 0 {
			t.Fatalf("got %d messages, want 0", len(parsed))
		}
	})
}
