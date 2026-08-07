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
