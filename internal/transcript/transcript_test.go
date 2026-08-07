package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- TruncateMessage ---

func TestTruncateMessage_WithinLimit(t *testing.T) {
	got := TruncateMessage("hello", 10)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateMessage_ExactBoundary(t *testing.T) {
	got := TruncateMessage("hello", 5)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateMessage_Truncated(t *testing.T) {
	got := TruncateMessage("hello world", 8)
	// maxLen=8, so first 5 chars + "..." = "hello..."
	want := "hello..."
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestTruncateMessage_VeryShortMax(t *testing.T) {
	// maxLen <= 3 returns first maxLen chars without "..."
	got := TruncateMessage("hello", 3)
	want := "hel"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}

	got2 := TruncateMessage("hello", 1)
	want2 := "h"
	if got2 != want2 {
		t.Errorf("expected %q, got %q", want2, got2)
	}
}

// --- TruncateMessageFromEnd ---

func TestTruncateMessageFromEnd_WithinLimit(t *testing.T) {
	got := TruncateMessageFromEnd("hello", 10)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateMessageFromEnd_Truncated(t *testing.T) {
	got := TruncateMessageFromEnd("hello world", 8)
	// "..." + last 5 chars = "...world"
	want := "...world"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestTruncateMessageFromEnd_VeryShortMax(t *testing.T) {
	// maxLen <= 3 returns last maxLen chars without "..."
	got := TruncateMessageFromEnd("hello", 3)
	want := "llo"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}

	got2 := TruncateMessageFromEnd("hello", 1)
	want2 := "o"
	if got2 != want2 {
		t.Errorf("expected %q, got %q", want2, got2)
	}
}

// --- encodePathForClaude ---

func TestEncodePathForClaude(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/Users/foo/bar", "-Users-foo-bar"},
		{"/home/user/project", "-home-user-project"},
		{"relative/path", "relative-path"},
		{"/", "-"},
	}
	for _, tc := range cases {
		got := encodePathForClaude(tc.input)
		if got != tc.want {
			t.Errorf("encodePathForClaude(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- cleanContent ---

func TestCleanContent(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"newlines replaced", "hello\nworld", "hello world"},
		{"tabs replaced", "hello\tworld", "hello world"},
		{"carriage return removed", "hello\rworld", "helloworld"},
		{"multiple spaces collapsed", "hello    world", "hello world"},
		{"trimming", "  hello  ", "hello"},
		{"combined", " hello\n\tworld  foo  ", "hello world foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanContent(tc.input)
			if got != tc.want {
				t.Errorf("cleanContent(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// --- extractContent ---

func TestExtractContent_UserString(t *testing.T) {
	// The fixture carries a newline and a tab because collapsing to one line is
	// the whole point of extractContent, not an incidental effect of
	// cleanContent: its result lands in `jin session info`'s tabwriter row and
	// in the TUI detail pane, and neither of those splits on "\n".
	entry := &transcriptEntry{
		Type: "user",
		Message: msgObject{
			Role:    "user",
			Content: "hello\nworld\tand  more",
		},
	}
	got := extractContent(entry)
	want := "hello world and more"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractContent_AssistantBlocks(t *testing.T) {
	// Simulate what json.Unmarshal produces for []contentBlock.
	// This also holds the other side of the role split: a text-only array is
	// the assistant's ordinary shape and stays conversation, while the same
	// shape on a user entry does not (TestExtractContent_UserTextOnlyArrayIsNotConversation).
	blocks := []any{
		map[string]any{"type": "text", "text": "first"},
		map[string]any{"type": "text", "text": "second"},
	}
	entry := &transcriptEntry{
		Type: "assistant",
		Message: msgObject{
			Role:    "assistant",
			Content: blocks,
		},
	}
	got := extractContent(entry)
	want := "first second"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractContent_NilContent(t *testing.T) {
	entry := &transcriptEntry{
		Type: "user",
		Message: msgObject{
			Role:    "user",
			Content: nil,
		},
	}
	got := extractContent(entry)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// --- Reader ---

// writeJSONL writes JSONL entries to a file.
func writeJSONL(t *testing.T, path string, entries []transcriptEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReader_GetTranscriptPath(t *testing.T) {
	r := &Reader{claudeDir: "/home/user/.claude"}
	got := r.getTranscriptPath("/Users/foo/bar", "abc-123")
	want := filepath.Join("/home/user/.claude", "projects", "-Users-foo-bar", "abc-123.jsonl")
	if got != want {
		t.Errorf("getTranscriptPath = %q, want %q", got, want)
	}
}

func TestReader_ReadLastMessage(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	workDir := "/test/project"
	sessionID := "sess-001"

	transcriptPath := r.getTranscriptPath(workDir, sessionID)

	entries := []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "first question"},
			Timestamp: "2024-01-01T00:00:00Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "text", "text": "first answer"},
				},
			},
			Timestamp: "2024-01-01T00:00:01Z",
		},
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "second question"},
			Timestamp: "2024-01-01T00:00:02Z",
		},
	}
	writeJSONL(t, transcriptPath, entries)

	msg, err := r.readLastMessage(transcriptPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	// Last message overall is the second user message
	if msg.Type != "user" {
		t.Errorf("expected type %q, got %q", "user", msg.Type)
	}
	if msg.Content != "second question" {
		t.Errorf("expected content %q, got %q", "second question", msg.Content)
	}
	if msg.Timestamp != "2024-01-01T00:00:02Z" {
		t.Errorf("expected timestamp %q, got %q", "2024-01-01T00:00:02Z", msg.Timestamp)
	}
}

func TestReader_ReadLastMessages(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	workDir := "/test/project"
	sessionID := "sess-002"
	transcriptPath := r.getTranscriptPath(workDir, sessionID)

	entries := []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "hello"},
			Timestamp: "2024-01-01T00:00:00Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "text", "text": "world"},
				},
			},
			Timestamp: "2024-01-01T00:00:01Z",
		},
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "follow up"},
			Timestamp: "2024-01-01T00:00:02Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "text", "text": "final response"},
				},
			},
			Timestamp: "2024-01-01T00:00:03Z",
		},
	}
	writeJSONL(t, transcriptPath, entries)

	msgs, err := r.readLastMessages(transcriptPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs == nil {
		t.Fatal("expected non-nil LastMessages")
	}

	if msgs.User == nil {
		t.Fatal("expected non-nil User message")
	}
	if msgs.User.Content != "follow up" {
		t.Errorf("User.Content = %q, want %q", msgs.User.Content, "follow up")
	}

	if msgs.Assistant == nil {
		t.Fatal("expected non-nil Assistant message")
	}
	if msgs.Assistant.Content != "final response" {
		t.Errorf("Assistant.Content = %q, want %q", msgs.Assistant.Content, "final response")
	}
}

func TestReader_FileNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	msg, err := r.readLastMessage(filepath.Join(tmpDir, "nonexistent.jsonl"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if msg != nil {
		t.Errorf("expected nil message, got %+v", msg)
	}

	msgs, err := r.readLastMessages(filepath.Join(tmpDir, "nonexistent.jsonl"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil LastMessages, got %+v", msgs)
	}
}

func TestReader_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	emptyFile := filepath.Join(tmpDir, "empty.jsonl")
	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	msg, err := r.readLastMessage(emptyFile)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if msg != nil {
		t.Errorf("expected nil message for empty file, got %+v", msg)
	}

	msgs, err := r.readLastMessages(emptyFile)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil LastMessages for empty file, got %+v", msgs)
	}
}

func TestReader_EmptySessionID(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	// An empty sessionID cannot name a transcript, so it reports the same
	// ErrNoTranscript as a session whose JSONL is missing.
	msg, err := r.GetLastMessage("/some/dir", "")
	if !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("expected ErrNoTranscript, got %v", err)
	}
	if msg != nil {
		t.Errorf("expected nil message for empty sessionID, got %+v", msg)
	}

	msgs, err := r.GetLastMessages("/some/dir", "")
	if !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("expected ErrNoTranscript, got %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil LastMessages for empty sessionID, got %+v", msgs)
	}
}

// --- Additional edge cases ---

func TestExtractContent_AssistantNonTextBlock(t *testing.T) {
	// Blocks that are not type "text" should be ignored
	blocks := []any{
		map[string]any{"type": "tool_use", "name": "read_file"},
		map[string]any{"type": "text", "text": "only text"},
	}
	entry := &transcriptEntry{
		Type: "assistant",
		Message: msgObject{
			Role:    "assistant",
			Content: blocks,
		},
	}
	got := extractContent(entry)
	if got != "only text" {
		t.Errorf("expected %q, got %q", "only text", got)
	}
}

func TestReader_GetLastMessage_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	workDir := "/integration/test"
	sessionID := "sess-int"
	transcriptPath := r.getTranscriptPath(workDir, sessionID)

	entries := []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "the question"},
			Timestamp: "2024-06-01T12:00:00Z",
		},
	}
	writeJSONL(t, transcriptPath, entries)

	msg, err := r.GetLastMessage(workDir, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Content != "the question" {
		t.Errorf("Content = %q, want %q", msg.Content, "the question")
	}
}

func TestCleanContent_CarriageReturnNewline(t *testing.T) {
	got := cleanContent("line1\r\nline2")
	// \r is removed (replaced with ""), \n is replaced with space
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Errorf("expected both lines, got %q", got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("expected no CR/LF, got %q", got)
	}
}

func TestGetConversation(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	workDir := "/test/project"
	sessionID := "sess-conv"
	transcriptPath := r.getTranscriptPath(workDir, sessionID)

	entries := []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "first question"},
			Timestamp: "2024-01-01T00:00:00Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role:    "assistant",
				Content: []any{map[string]any{"type": "text", "text": "first answer"}},
			},
			Timestamp: "2024-01-01T00:00:01Z",
		},
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "second question"},
			Timestamp: "2024-01-01T00:00:02Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role:    "assistant",
				Content: []any{map[string]any{"type": "text", "text": "second answer"}},
			},
			Timestamp: "2024-01-01T00:00:03Z",
		},
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "third question"},
			Timestamp: "2024-01-01T00:00:04Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role:    "assistant",
				Content: []any{map[string]any{"type": "text", "text": "third answer"}},
			},
			Timestamp: "2024-01-01T00:00:05Z",
		},
		// The last turn deliberately runs to two assistant messages: with
		// every turn one message long, a fixed 2n tail slice and exchange
		// counting return the same thing and the test proves neither.
		{
			Type: "assistant",
			Message: msgObject{
				Role:    "assistant",
				Content: []any{map[string]any{"type": "text", "text": "third answer, continued"}},
			},
			Timestamp: "2024-01-01T00:00:06Z",
		},
	}
	writeJSONL(t, transcriptPath, entries)

	t.Run("last 1 exchange", func(t *testing.T) {
		msgs, err := r.GetConversation(workDir, sessionID, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContents(t, msgs, []string{"third question", "third answer", "third answer, continued"})
		if msgs[0].Type != "user" {
			t.Errorf("first message type = %q, want %q", msgs[0].Type, "user")
		}
	})

	t.Run("last 2 exchanges", func(t *testing.T) {
		msgs, err := r.GetConversation(workDir, sessionID, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContents(t, msgs, []string{
			"second question", "second answer",
			"third question", "third answer", "third answer, continued",
		})
	})

	t.Run("last N exceeds total", func(t *testing.T) {
		msgs, err := r.GetConversation(workDir, sessionID, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) != 7 {
			t.Fatalf("expected 7 messages, got %d", len(msgs))
		}
	})

	t.Run("lastN below 1 is refused", func(t *testing.T) {
		// Asking for no exchanges used to return the whole conversation —
		// hundreds of messages into whatever the caller was piping into.
		for _, n := range []int{0, -1} {
			msgs, err := r.GetConversation(workDir, sessionID, n)
			if err == nil {
				t.Errorf("lastN=%d: expected an error, got %d messages", n, len(msgs))
			}
			if msgs != nil {
				t.Errorf("lastN=%d: expected no messages, got %+v", n, msgs)
			}
		}
	})

	t.Run("empty session ID", func(t *testing.T) {
		msgs, err := r.GetConversation(workDir, "", 1)
		if !errors.Is(err, ErrNoTranscript) {
			t.Fatalf("expected ErrNoTranscript, got %v", err)
		}
		if msgs != nil {
			t.Errorf("expected nil, got %v", msgs)
		}
	})
}

func TestExtractFullContent_PreservesNewlines(t *testing.T) {
	entry := &transcriptEntry{
		Type:    "user",
		Message: msgObject{Role: "user", Content: "line1\nline2\nline3"},
	}
	got := extractFullContent(entry)
	if !strings.Contains(got, "\n") {
		t.Errorf("expected newlines preserved, got %q", got)
	}
}

// --- Structured API: parseBlocks ---

func TestParseBlocks_UserString(t *testing.T) {
	entry := &transcriptEntry{
		Type:    "user",
		Message: msgObject{Role: "user", Content: "  hello  "},
	}
	blocks := parseBlocks(entry)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Kind != "text" || blocks[0].Text != "hello" {
		t.Errorf("unexpected block: %+v", blocks[0])
	}
}

func TestParseBlocks_UserStringEmpty(t *testing.T) {
	entry := &transcriptEntry{
		Type:    "user",
		Message: msgObject{Role: "user", Content: "   "},
	}
	if blocks := parseBlocks(entry); blocks != nil {
		t.Errorf("expected nil for whitespace-only string, got %+v", blocks)
	}
}

func TestParseBlocks_AssistantMixed(t *testing.T) {
	content := []any{
		map[string]any{"type": "thinking", "thinking": "let me think"},
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "tool_use", "name": "Bash", "id": "tu_1", "input": map[string]any{"command": "echo hi"}},
	}
	entry := &transcriptEntry{
		Type:    "assistant",
		Message: msgObject{Role: "assistant", Content: content},
	}
	blocks := parseBlocks(entry)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0].Kind != "thinking" || blocks[0].Text != "let me think" {
		t.Errorf("thinking block wrong: %+v", blocks[0])
	}
	if blocks[1].Kind != "text" || blocks[1].Text != "hello" {
		t.Errorf("text block wrong: %+v", blocks[1])
	}
	if blocks[2].Kind != "tool_use" || blocks[2].ToolName != "Bash" || blocks[2].ToolUseID != "tu_1" {
		t.Errorf("tool_use block wrong: %+v", blocks[2])
	}
	// Input must be preserved as JSON
	if len(blocks[2].Input) == 0 {
		t.Fatal("expected non-empty Input")
	}
	var parsed map[string]any
	if err := json.Unmarshal(blocks[2].Input, &parsed); err != nil {
		t.Fatalf("Input not valid JSON: %v", err)
	}
	if parsed["command"] != "echo hi" {
		t.Errorf("Input.command = %v, want %q", parsed["command"], "echo hi")
	}
}

func TestParseBlocks_ToolUseEmptyInput(t *testing.T) {
	content := []any{
		map[string]any{"type": "tool_use", "name": "X", "id": "tu_e", "input": map[string]any{}},
	}
	entry := &transcriptEntry{
		Type:    "assistant",
		Message: msgObject{Role: "assistant", Content: content},
	}
	blocks := parseBlocks(entry)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if string(blocks[0].Input) != "{}" {
		t.Errorf("expected empty-object Input, got %q", string(blocks[0].Input))
	}
}

func TestParseBlocks_ToolResultStringContent(t *testing.T) {
	content := []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "tu_1",
			"content":     "command output",
			"is_error":    false,
		},
	}
	entry := &transcriptEntry{
		Type:    "user",
		Message: msgObject{Role: "user", Content: content},
	}
	blocks := parseBlocks(entry)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Kind != "tool_result" || blocks[0].Output != "command output" || blocks[0].ToolUseID != "tu_1" || blocks[0].IsError {
		t.Errorf("unexpected block: %+v", blocks[0])
	}
}

func TestParseBlocks_ToolResultArrayContent(t *testing.T) {
	content := []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "tu_2",
			"content": []any{
				map[string]any{"type": "text", "text": "line1"},
				map[string]any{"type": "text", "text": "line2"},
			},
			"is_error": true,
		},
	}
	entry := &transcriptEntry{
		Type:    "user",
		Message: msgObject{Role: "user", Content: content},
	}
	blocks := parseBlocks(entry)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	want := "line1\nline2"
	if blocks[0].Output != want || !blocks[0].IsError {
		t.Errorf("unexpected block: %+v (want Output=%q IsError=true)", blocks[0], want)
	}
}

// --- Structured API: ReadEntries ---

func TestReader_ReadEntries_AllAndSince(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	workDir := "/structured/test"
	sessionID := "sess-rd"
	transcriptPath := r.getTranscriptPath(workDir, sessionID)

	entries := []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "first"},
			Timestamp: "2024-01-01T00:00:00Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "tool_use", "name": "Bash", "id": "tu_a", "input": map[string]any{"command": "echo a"}},
				},
				Usage: &Usage{InputTokens: 10, OutputTokens: 5},
			},
			Timestamp: "2024-01-01T00:00:01Z",
		},
		{
			Type: "user",
			Message: msgObject{
				Role: "user",
				Content: []any{
					map[string]any{"type": "tool_result", "tool_use_id": "tu_a", "content": "a", "is_error": false},
				},
			},
			Timestamp: "2024-01-01T00:00:02Z",
		},
	}
	writeJSONL(t, transcriptPath, entries)

	all, err := r.ReadEntries(workDir, sessionID, "")
	if err != nil {
		t.Fatalf("ReadEntries(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
	if all[1].Usage == nil || all[1].Usage.InputTokens != 10 {
		t.Errorf("usage not parsed: %+v", all[1].Usage)
	}

	since, err := r.ReadEntries(workDir, sessionID, "2024-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("ReadEntries(since): %v", err)
	}
	if len(since) != 2 {
		t.Fatalf("expected 2 entries after since, got %d", len(since))
	}

	future, err := r.ReadEntries(workDir, sessionID, "2099-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("ReadEntries(future): %v", err)
	}
	if len(future) != 0 {
		t.Errorf("expected 0 entries for future since, got %d", len(future))
	}
}

func TestReader_ReadEntries_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	got, err := r.ReadEntries("/nope", "missing", "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil entries, got %+v", got)
	}
}

func TestReader_ReadEntries_GlobFallback(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	// Place file under one workDir but query with a wrong workDir.
	written := r.getTranscriptPath("/actual/dir", "sess-glob")
	writeJSONL(t, written, []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "x"},
			Timestamp: "2024-01-01T00:00:00Z",
		},
	})
	got, err := r.ReadEntries("/wrong/dir", "sess-glob", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 entry via glob fallback, got %d", len(got))
	}

	// Empty workDir should also work via glob.
	got2, err := r.ReadEntries("", "sess-glob", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got2) != 1 {
		t.Errorf("expected 1 entry via glob (empty workDir), got %d", len(got2))
	}
}

func TestReader_ReadEntries_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	p := r.getTranscriptPath("/empty/dir", "sess-empty")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadEntries("/empty/dir", "sess-empty", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil entries for empty file, got %+v", got)
	}
}

func TestReader_ReadEntries_LargeLine(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/large/test"
	sessionID := "sess-large"
	p := r.getTranscriptPath(workDir, sessionID)
	// Build a single-line JSONL with a tool_result of ~2 MiB.
	huge := strings.Repeat("A", 2*1024*1024)
	entry := transcriptEntry{
		Type: "user",
		Message: msgObject{
			Role: "user",
			Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_h", "content": huge},
			},
		},
		Timestamp: "2024-01-01T00:00:00Z",
	}
	writeJSONL(t, p, []transcriptEntry{entry})
	got, err := r.ReadEntries(workDir, sessionID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if len(got[0].Blocks) != 1 || len(got[0].Blocks[0].Output) != len(huge) {
		t.Errorf("large tool_result not preserved: blocks=%d outLen=%d", len(got[0].Blocks), len(got[0].Blocks[0].Output))
	}
}

// --- Array-shaped user content ---

func textBlock(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func TestExtractContent_UserTextOnlyArrayIsNotConversation(t *testing.T) {
	// No promptSource means Claude Code was handed this by nobody, so a
	// text-only array here is the agent writing in the user's voice. On real
	// transcripts every one of them was an interruption notice like the fixture
	// below — the rule keys on the missing field and the shape, not on this
	// wording.
	entry := &transcriptEntry{
		Type:    "user",
		Message: msgObject{Role: "user", Content: []any{textBlock("[Request interrupted by user]")}},
	}
	if got := extractContent(entry); got != "" {
		t.Errorf("extractContent = %q, want empty", got)
	}
	if got := extractFullContent(entry); got != "" {
		t.Errorf("extractFullContent = %q, want empty", got)
	}
}

func TestExtractContent_UserArrayWithAttachmentKeepsText(t *testing.T) {
	// Words plus a pasted image. The entry is admitted on shape alone — it is
	// not a text-only array — which is what keeps an attachment prompt readable
	// on a Claude Code too old to write promptSource.
	entry := &transcriptEntry{
		Type: "user",
		Message: msgObject{Role: "user", Content: []any{
			textBlock("this is what it looks like now"),
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "…"}},
		}},
	}
	if got := extractContent(entry); got != "this is what it looks like now" {
		t.Errorf("extractContent = %q, want the text block", got)
	}
	if got := extractFullContent(entry); got != "this is what it looks like now" {
		t.Errorf("extractFullContent = %q, want the text block", got)
	}
}

func TestExtractContent_UserToolResultOnlyIsNotConversation(t *testing.T) {
	// A tool_result is what a tool wrote back, not something the user said.
	// Admitting it would bury every real message under tool output. The block
	// carries a top-level "text" key it does not have in the wild, so that
	// dropping the block-kind check makes this fail instead of passing on the
	// technicality that the key was missing.
	entry := &transcriptEntry{
		Type: "user",
		Message: msgObject{Role: "user", Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "text": "exit status 0", "content": "exit status 0"},
		}},
	}
	if got := extractContent(entry); got != "" {
		t.Errorf("extractContent = %q, want empty", got)
	}
	if got := extractFullContent(entry); got != "" {
		t.Errorf("extractFullContent = %q, want empty", got)
	}
}

func TestExtractContent_UserMixedBlocksKeepsOnlyText(t *testing.T) {
	entry := &transcriptEntry{
		Type: "user",
		Message: msgObject{Role: "user", Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "text": "noise", "content": "noise"},
			textBlock("and here is the real question"),
		}},
	}
	if got := extractFullContent(entry); got != "and here is the real question" {
		t.Errorf("extractFullContent = %q, want the text block only", got)
	}
}

func TestGetConversation_SkipsInjectedAndSidechainEntries(t *testing.T) {
	// Once array-shaped user content became readable, two kinds of entry that
	// are stored as user messages but nobody said started showing up: the
	// body Claude Code injects when a skill is invoked (isMeta), and a
	// subagent's own turns (isSidechain). The injected skill body alone runs
	// to thousands of lines and buried the conversation it was meant to show.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/filtered/test"
	sessionID := "sess-filtered"

	// The two excluded user entries carry string content on purpose: a
	// text-only user array is dropped by conversationTextBlocks whatever its
	// flags say, so writing them as arrays would let this test pass with the
	// flag checks removed.
	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "user", Message: msgObject{Role: "user", Content: "real prompt"}, Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "user", IsMeta: true, Message: msgObject{Role: "user", Content: "Base directory for this skill: …"}, Timestamp: "2024-01-01T00:00:01Z"},
		{Type: "user", IsSidechain: true, Message: msgObject{Role: "user", Content: "subagent task brief"}, Timestamp: "2024-01-01T00:00:02Z"},
		{Type: "assistant", IsSidechain: true, Message: msgObject{Role: "assistant", Content: []any{textBlock("subagent reply")}}, Timestamp: "2024-01-01T00:00:03Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("real answer")}}, Timestamp: "2024-01-01T00:00:04Z"},
	})

	msgs, err := r.GetConversation(workDir, sessionID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContents(t, msgs, []string{"real prompt", "real answer"})

	last, err := r.GetLastMessage(workDir, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if last == nil || last.Content != "real answer" {
		t.Errorf("GetLastMessage = %+v, want the main-thread answer", last)
	}

	pair, err := r.GetLastMessages(workDir, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pair == nil || pair.User == nil || pair.User.Content != "real prompt" {
		t.Errorf("GetLastMessages.User = %+v, want the main-thread prompt", pair)
	}
}

// --- Exchange counting ---

func TestLastExchanges(t *testing.T) {
	// Turns are deliberately uneven: the second exchange has two assistant
	// messages, which is exactly the shape the old fixed 2n slice mishandled.
	msgs := []Message{
		{Type: "user", Content: "q1"},
		{Type: "assistant", Content: "a1"},
		{Type: "user", Content: "q2"},
		{Type: "assistant", Content: "a2a"},
		{Type: "assistant", Content: "a2b"},
	}

	t.Run("one exchange keeps the prompt with its full reply", func(t *testing.T) {
		got := lastExchanges(msgs, 1)
		want := []string{"q2", "a2a", "a2b"}
		assertContents(t, got, want)
	})

	t.Run("two exchanges reach further back", func(t *testing.T) {
		assertContents(t, lastExchanges(msgs, 2), []string{"q1", "a1", "q2", "a2a", "a2b"})
	})

	t.Run("more exchanges than exist returns everything", func(t *testing.T) {
		assertContents(t, lastExchanges(msgs, 100), []string{"q1", "a1", "q2", "a2a", "a2b"})
	})

	t.Run("consecutive user messages are one exchange", func(t *testing.T) {
		run := []Message{
			{Type: "user", Content: "q1"},
			{Type: "assistant", Content: "a1"},
			{Type: "user", Content: "q2a"},
			{Type: "user", Content: "q2b"},
			{Type: "assistant", Content: "a2"},
		}
		assertContents(t, lastExchanges(run, 1), []string{"q2a", "q2b", "a2"})
	})

	t.Run("trailing prompt with no reply yet is an exchange", func(t *testing.T) {
		pending := []Message{
			{Type: "user", Content: "q1"},
			{Type: "assistant", Content: "a1"},
			{Type: "user", Content: "q2"},
		}
		assertContents(t, lastExchanges(pending, 1), []string{"q2"})
	})

	t.Run("no user messages at all returns everything", func(t *testing.T) {
		assistantOnly := []Message{
			{Type: "assistant", Content: "a1"},
			{Type: "assistant", Content: "a2"},
		}
		assertContents(t, lastExchanges(assistantOnly, 1), []string{"a1", "a2"})
	})

	t.Run("empty input", func(t *testing.T) {
		if got := lastExchanges(nil, 1); len(got) != 0 {
			t.Errorf("expected no messages, got %+v", got)
		}
	})
}

func assertContents(t *testing.T, got []Message, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d messages %v, want %d %v", len(got), contents(got), len(want), want)
	}
	for i := range want {
		if got[i].Content != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i].Content, want[i])
		}
	}
}

func contents(msgs []Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func TestGetConversation_ExchangesSpanUnevenTurns(t *testing.T) {
	// End to end on the shape a real transcript has: a tool round trip in the
	// middle of a turn, and an agent that answers in two messages. The last
	// exchange is three messages on purpose — leave it that way. A fixture
	// that alternates user/assistant perfectly is returned identically by a
	// fixed 2n tail slice, so it cannot tell lastExchanges from the
	// implementation it replaced.
	//
	// One reply is deliberately multi-line: --last output is piped into another
	// agent's context, so this path reads through extractFullContent rather
	// than the single-line extractContent the display paths use. Keep the "\n"
	// in the expectations — it is what pins that choice.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/exchange/test"
	sessionID := "sess-exchange"

	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "user", Message: msgObject{Role: "user", Content: "first question"}, Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("first answer")}}, Timestamp: "2024-01-01T00:00:01Z"},
		{Type: "user", Message: msgObject{Role: "user", Content: "second question"}, Timestamp: "2024-01-01T00:00:02Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "name": "Bash", "id": "tu_1"}}}, Timestamp: "2024-01-01T00:00:03Z"},
		{Type: "user", Message: msgObject{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "ok"}}}, Timestamp: "2024-01-01T00:00:04Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("second answer:\n- one\n- two")}}, Timestamp: "2024-01-01T00:00:05Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("and one more thing")}}, Timestamp: "2024-01-01T00:00:06Z"},
	})

	msgs, err := r.GetConversation(workDir, sessionID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContents(t, msgs, []string{"second question", "second answer:\n- one\n- two", "and one more thing"})

	msgs, err = r.GetConversation(workDir, sessionID, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContents(t, msgs, []string{"first question", "first answer", "second question", "second answer:\n- one\n- two", "and one more thing"})
}

func TestReaders_UserTextOnlyArrayIsNobodySpeaking(t *testing.T) {
	// Every reader has to agree on this, so it is asserted through the API the
	// CLI and the TUI actually call. The agent-written user entry must not
	// become the last thing "the user said", and it must not open an exchange
	// either — doing so pushed the reply before it out of `--last 1`.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/interrupt/test"
	sessionID := "sess-interrupt"

	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "user", Message: msgObject{Role: "user", Content: "typed prompt"}, Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("first half of the answer")}}, Timestamp: "2024-01-01T00:00:01Z"},
		{Type: "user", Message: msgObject{Role: "user", Content: []any{textBlock("[Request interrupted by user]")}}, Timestamp: "2024-01-01T00:00:02Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("second half of the answer")}}, Timestamp: "2024-01-01T00:00:03Z"},
	})

	t.Run("GetLastMessage keeps returning the reply", func(t *testing.T) {
		msg, err := r.GetLastMessage(workDir, sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg == nil || msg.Content != "second half of the answer" {
			t.Fatalf("GetLastMessage = %+v, want the assistant reply", msg)
		}
	})

	t.Run("GetLastMessages attributes only the typed prompt to the user", func(t *testing.T) {
		msgs, err := r.GetLastMessages(workDir, sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msgs == nil || msgs.User == nil || msgs.User.Content != "typed prompt" {
			t.Fatalf("GetLastMessages.User = %+v, want the typed prompt", msgs)
		}
	})

	t.Run("no exchange boundary", func(t *testing.T) {
		msgs, err := r.GetConversation(workDir, sessionID, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContents(t, msgs, []string{"typed prompt", "first half of the answer", "second half of the answer"})
	})
}

func TestReaders_TextOnlyUserArrayWithPromptSourceIsAPrompt(t *testing.T) {
	// `claude -p --input-format stream-json` writes a real prompt as a
	// text-only array — the same shape an interruption marker has. promptSource
	// is what separates them: Claude Code stamps it on everything it received
	// as input. Keying on the shape alone dropped this prompt outright, leaving
	// `--last 1` showing a reply to a question nobody appeared to ask.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/sdk/test"
	sessionID := "sess-sdk"

	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "user", PromptSource: "sdk", Message: msgObject{Role: "user", Content: []any{textBlock("run the migration")}}, Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("migration applied")}}, Timestamp: "2024-01-01T00:00:01Z"},
	})

	t.Run("GetLastMessages attributes it to the user", func(t *testing.T) {
		msgs, err := r.GetLastMessages(workDir, sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msgs == nil || msgs.User == nil || msgs.User.Content != "run the migration" {
			t.Fatalf("GetLastMessages.User = %+v, want the SDK-supplied prompt", msgs)
		}
	})

	t.Run("GetConversation opens an exchange on it", func(t *testing.T) {
		msgs, err := r.GetConversation(workDir, sessionID, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertContents(t, msgs, []string{"run the migration", "migration applied"})
	})
}

func TestGetConversation_UserArrayWithAttachmentOpensAnExchange(t *testing.T) {
	// The other side of the rule: words plus a pasted image is a real prompt,
	// and it is the case the array-reading was added for.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/attachment/test"
	sessionID := "sess-attachment"

	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "user", Message: msgObject{Role: "user", Content: "earlier question"}, Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("earlier answer")}}, Timestamp: "2024-01-01T00:00:01Z"},
		{Type: "user", Message: msgObject{Role: "user", Content: []any{
			textBlock("this is what it looks like now"),
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "…"}},
		}}, Timestamp: "2024-01-01T00:00:02Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{textBlock("i see the problem")}}, Timestamp: "2024-01-01T00:00:03Z"},
	})

	msgs, err := r.GetConversation(workDir, sessionID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContents(t, msgs, []string{"this is what it looks like now", "i see the problem"})
}

// --- Oversized lines: the message readers must not stop mid-file ---

// hugeToolResultEntry builds a user entry whose tool_result payload is `size`
// bytes, i.e. one JSONL line far longer than the 1 MiB the message readers
// used to allow.
func hugeToolResultEntry(size int, ts string) transcriptEntry {
	return transcriptEntry{
		Type: "user",
		Message: msgObject{
			Role: "user",
			Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_h", "content": strings.Repeat("A", size)},
			},
		},
		Timestamp: ts,
	}
}

func TestReaders_SeePastOversizedLine(t *testing.T) {
	// A 2 MiB tool_result sits between the first and last exchange. The
	// readers carried a 1 MiB scanner limit and never checked scanner.Err(),
	// so everything after this line silently vanished — the transcript looked
	// like it ended early. Observed on a real 2913-line transcript where 8
	// trailing messages (including the newest reply) went missing.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/oversized/test"
	sessionID := "sess-oversized"

	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "user", Message: msgObject{Role: "user", Content: "early question"}, Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "early answer"}}}, Timestamp: "2024-01-01T00:00:01Z"},
		hugeToolResultEntry(2*1024*1024, "2024-01-01T00:00:02Z"),
		{Type: "user", Message: msgObject{Role: "user", Content: "late question"}, Timestamp: "2024-01-01T00:00:03Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "late answer"}}}, Timestamp: "2024-01-01T00:00:04Z"},
	})

	t.Run("GetLastMessage", func(t *testing.T) {
		msg, err := r.GetLastMessage(workDir, sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg == nil || msg.Content != "late answer" {
			t.Fatalf("expected the message after the oversized line, got %+v", msg)
		}
	})

	t.Run("GetLastMessages", func(t *testing.T) {
		msgs, err := r.GetLastMessages(workDir, sessionID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msgs == nil || msgs.Assistant == nil || msgs.Assistant.Content != "late answer" {
			t.Fatalf("expected the assistant message after the oversized line, got %+v", msgs)
		}
		if msgs.User == nil || msgs.User.Content != "late question" {
			t.Fatalf("expected the user message after the oversized line, got %+v", msgs)
		}
	})

	t.Run("GetConversation", func(t *testing.T) {
		msgs, err := r.GetConversation(workDir, sessionID, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatal("expected messages, got none")
		}
		if got := msgs[len(msgs)-1].Content; got != "late answer" {
			t.Errorf("last message = %q, want %q", got, "late answer")
		}
	})
}

func TestReaders_ReportLineOverLimitInsteadOfTruncating(t *testing.T) {
	// Past maxTranscriptLineBytes there is nothing to do but fail — and
	// failing is the point. Returning the messages read so far would hand the
	// caller a plausible-looking prefix of the conversation with no signal
	// that the rest was dropped.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/toolong/test"
	sessionID := "sess-toolong"

	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "user", Message: msgObject{Role: "user", Content: "question"}, Timestamp: "2024-01-01T00:00:00Z"},
		hugeToolResultEntry(maxTranscriptLineBytes+1, "2024-01-01T00:00:01Z"),
	})

	if _, err := r.GetConversation(workDir, sessionID, 1); !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("GetConversation: expected bufio.ErrTooLong, got %v", err)
	}
	if _, err := r.GetLastMessage(workDir, sessionID); !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("GetLastMessage: expected bufio.ErrTooLong, got %v", err)
	}
	if _, err := r.GetLastMessages(workDir, sessionID); !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("GetLastMessages: expected bufio.ErrTooLong, got %v", err)
	}
}

// --- Structured API: LastToolUse / LastToolResult ---

func TestReader_LastToolUseAndResult(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/last/test"
	sessionID := "sess-last"
	p := r.getTranscriptPath(workDir, sessionID)

	entries := []transcriptEntry{
		{
			Type: "assistant",
			Message: msgObject{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "name": "Bash", "id": "tu_1", "input": map[string]any{"command": "echo first"}},
			}},
			Timestamp: "2024-01-01T00:00:00Z",
		},
		{
			Type: "user",
			Message: msgObject{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "first", "is_error": false},
			}},
			Timestamp: "2024-01-01T00:00:01Z",
		},
		{
			Type: "assistant",
			Message: msgObject{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "name": "Read", "id": "tu_2", "input": map[string]any{"path": "/x"}},
				map[string]any{"type": "tool_use", "name": "Bash", "id": "tu_3", "input": map[string]any{"command": "echo last"}},
			}},
			Timestamp: "2024-01-01T00:00:02Z",
		},
		{
			Type: "user",
			Message: msgObject{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_2", "content": "ok"},
				map[string]any{"type": "tool_result", "tool_use_id": "tu_3", "content": "boom", "is_error": true},
			}},
			Timestamp: "2024-01-01T00:00:03Z",
		},
	}
	writeJSONL(t, p, entries)

	t.Run("LastToolUse any", func(t *testing.T) {
		b, err := r.LastToolUse(workDir, sessionID, "")
		if err != nil || b == nil {
			t.Fatalf("err=%v block=%+v", err, b)
		}
		if b.ToolUseID != "tu_3" || b.ToolName != "Bash" {
			t.Errorf("unexpected: %+v", b)
		}
	})

	t.Run("LastToolUse by name", func(t *testing.T) {
		b, err := r.LastToolUse(workDir, sessionID, "Read")
		if err != nil || b == nil {
			t.Fatalf("err=%v block=%+v", err, b)
		}
		if b.ToolUseID != "tu_2" {
			t.Errorf("unexpected: %+v", b)
		}
	})

	t.Run("LastToolUse missing", func(t *testing.T) {
		b, err := r.LastToolUse(workDir, sessionID, "NoSuch")
		if err != nil || b != nil {
			t.Errorf("expected nil/nil, got %+v %v", b, err)
		}
	})

	t.Run("LastToolResult any", func(t *testing.T) {
		b, err := r.LastToolResult(workDir, sessionID, "", false)
		if err != nil || b == nil {
			t.Fatalf("err=%v block=%+v", err, b)
		}
		if b.ToolUseID != "tu_3" || !b.IsError {
			t.Errorf("unexpected: %+v", b)
		}
	})

	t.Run("LastToolResult by tool name", func(t *testing.T) {
		b, err := r.LastToolResult(workDir, sessionID, "Bash", false)
		if err != nil || b == nil {
			t.Fatalf("err=%v block=%+v", err, b)
		}
		if b.ToolUseID != "tu_3" {
			t.Errorf("unexpected: %+v", b)
		}
	})

	t.Run("LastToolResult errors only", func(t *testing.T) {
		b, err := r.LastToolResult(workDir, sessionID, "", true)
		if err != nil || b == nil {
			t.Fatalf("err=%v block=%+v", err, b)
		}
		if !b.IsError || b.ToolUseID != "tu_3" {
			t.Errorf("unexpected: %+v", b)
		}
	})

	t.Run("LastToolResult by name + errors only", func(t *testing.T) {
		b, err := r.LastToolResult(workDir, sessionID, "Read", true)
		if err != nil || b != nil {
			t.Errorf("expected nil (Read result was not error), got %+v %v", b, err)
		}
	})
}

// --- findTranscriptPath ---

func TestReader_FindTranscriptPath(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	// 1. Direct hit.
	direct := r.getTranscriptPath("/direct", "sess-d")
	if err := os.MkdirAll(filepath.Dir(direct), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(direct, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := r.findTranscriptPath("/direct", "sess-d")
	if err != nil || got != direct {
		t.Errorf("direct hit failed: got=%q err=%v", got, err)
	}

	// 2. Glob fallback.
	other := r.getTranscriptPath("/other/dir", "sess-g")
	if err := os.MkdirAll(filepath.Dir(other), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = r.findTranscriptPath("/wrong", "sess-g")
	if err != nil || got != other {
		t.Errorf("glob fallback failed: got=%q err=%v", got, err)
	}

	// 3. Not found.
	_, err = r.findTranscriptPath("/wrong", "no-such")
	if !errors.Is(err, ErrNoTranscript) {
		t.Errorf("expected ErrNoTranscript, got %v", err)
	}

	// 4. Empty sessionID.
	_, err = r.findTranscriptPath("/wrong", "")
	if !errors.Is(err, ErrNoTranscript) {
		t.Errorf("expected ErrNoTranscript for empty sessionID, got %v", err)
	}
}

// writeRawJSONL writes each line verbatim to path (newline-terminated). Used
// for ai-title tests because ai-title entries have a shape that transcriptEntry
// cannot round-trip (no `message` field), so we can't encode via writeJSONL.
func writeRawJSONL(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReader_ReadAITitle(t *testing.T) {
	t.Run("no transcript file returns miss", func(t *testing.T) {
		tmpDir := t.TempDir()
		r := &Reader{claudeDir: tmpDir}
		if title, ok := r.ReadAITitle("/nowhere", "sess-missing"); ok || title != "" {
			t.Fatalf("ReadAITitle = (%q, %v), want (\"\", false)", title, ok)
		}
	})

	t.Run("empty sessionID returns miss", func(t *testing.T) {
		tmpDir := t.TempDir()
		r := &Reader{claudeDir: tmpDir}
		if title, ok := r.ReadAITitle("/tmp/foo", ""); ok || title != "" {
			t.Fatalf("ReadAITitle = (%q, %v), want (\"\", false)", title, ok)
		}
	})

	t.Run("transcript with no ai-title entry returns miss", func(t *testing.T) {
		tmpDir := t.TempDir()
		r := &Reader{claudeDir: tmpDir}
		workDir := "/test/project"
		sessionID := "sess-no-title"
		writeRawJSONL(t, r.getTranscriptPath(workDir, sessionID), []string{
			`{"type":"mode","mode":"normal","sessionId":"sess-no-title"}`,
			`{"type":"user","message":{"role":"user","content":"hi"},"timestamp":"2024-01-01T00:00:00Z"}`,
		})
		if title, ok := r.ReadAITitle(workDir, sessionID); ok || title != "" {
			t.Fatalf("ReadAITitle = (%q, %v), want (\"\", false)", title, ok)
		}
	})

	t.Run("returns aiTitle when present", func(t *testing.T) {
		tmpDir := t.TempDir()
		r := &Reader{claudeDir: tmpDir}
		workDir := "/test/project"
		sessionID := "sess-with-title"
		writeRawJSONL(t, r.getTranscriptPath(workDir, sessionID), []string{
			`{"type":"mode","mode":"normal","sessionId":"sess-with-title"}`,
			`{"type":"ai-title","aiTitle":"リポジトリの目的を確認","sessionId":"sess-with-title"}`,
			`{"type":"user","message":{"role":"user","content":"hi"},"timestamp":"2024-01-01T00:00:00Z"}`,
		})
		title, ok := r.ReadAITitle(workDir, sessionID)
		if !ok || title != "リポジトリの目的を確認" {
			t.Fatalf("ReadAITitle = (%q, %v), want (%q, true)", title, ok, "リポジトリの目的を確認")
		}
	})

	t.Run("multiple ai-title entries return the last one", func(t *testing.T) {
		// CC may re-title the session as the conversation evolves. Callers
		// should see the latest title, not the first.
		tmpDir := t.TempDir()
		r := &Reader{claudeDir: tmpDir}
		workDir := "/test/project"
		sessionID := "sess-relabel"
		writeRawJSONL(t, r.getTranscriptPath(workDir, sessionID), []string{
			`{"type":"ai-title","aiTitle":"first title","sessionId":"sess-relabel"}`,
			`{"type":"user","message":{"role":"user","content":"more"},"timestamp":"2024-01-01T00:00:00Z"}`,
			`{"type":"ai-title","aiTitle":"second title","sessionId":"sess-relabel"}`,
		})
		title, ok := r.ReadAITitle(workDir, sessionID)
		if !ok || title != "second title" {
			t.Fatalf("ReadAITitle = (%q, %v), want (%q, true)", title, ok, "second title")
		}
	})

	t.Run("malformed lines are skipped", func(t *testing.T) {
		tmpDir := t.TempDir()
		r := &Reader{claudeDir: tmpDir}
		workDir := "/test/project"
		sessionID := "sess-broken"
		writeRawJSONL(t, r.getTranscriptPath(workDir, sessionID), []string{
			`{not json`,
			`{"type":"ai-title","aiTitle":"survives","sessionId":"sess-broken"}`,
			`}also not json`,
		})
		title, ok := r.ReadAITitle(workDir, sessionID)
		if !ok || title != "survives" {
			t.Fatalf("ReadAITitle = (%q, %v), want (%q, true)", title, ok, "survives")
		}
	})

	t.Run("empty aiTitle value is treated as absent", func(t *testing.T) {
		tmpDir := t.TempDir()
		r := &Reader{claudeDir: tmpDir}
		workDir := "/test/project"
		sessionID := "sess-empty"
		writeRawJSONL(t, r.getTranscriptPath(workDir, sessionID), []string{
			`{"type":"ai-title","aiTitle":"","sessionId":"sess-empty"}`,
		})
		if title, ok := r.ReadAITitle(workDir, sessionID); ok || title != "" {
			t.Fatalf("ReadAITitle = (%q, %v), want (\"\", false)", title, ok)
		}
	})
}

// --- Structured API: TurnState ---

func TestTurnState(t *testing.T) {
	userText := transcriptEntry{
		Type:      "user",
		Message:   msgObject{Role: "user", Content: "please do the thing"},
		Timestamp: "2024-01-01T00:00:00Z",
	}
	assistantText := transcriptEntry{
		Type: "assistant",
		Message: msgObject{Role: "assistant", Content: []any{
			map[string]any{"type": "text", "text": "done"},
		}},
		Timestamp: "2024-01-01T00:00:01Z",
	}
	assistantToolUse := transcriptEntry{
		Type: "assistant",
		Message: msgObject{Role: "assistant", Content: []any{
			map[string]any{"type": "text", "text": "running a command"},
			map[string]any{"type": "tool_use", "name": "Bash", "id": "tu_1", "input": map[string]any{"command": "echo hi"}},
		}},
		Timestamp: "2024-01-01T00:00:01Z",
	}
	userToolResult := transcriptEntry{
		Type: "user",
		Message: msgObject{Role: "user", Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "hi", "is_error": false},
		}},
		Timestamp: "2024-01-01T00:00:02Z",
	}
	systemTail := transcriptEntry{
		Type:      "system",
		Timestamp: "2024-01-01T00:00:03Z",
	}
	assistantThinkingOnly := transcriptEntry{
		Type: "assistant",
		Message: msgObject{Role: "assistant", Content: []any{
			map[string]any{"type": "thinking", "thinking": "let me see"},
		}},
		Timestamp: "2024-01-01T00:00:01Z",
	}
	sidechainAssistantText := transcriptEntry{
		Type: "assistant",
		Message: msgObject{Role: "assistant", Content: []any{
			map[string]any{"type": "text", "text": "subagent finished"},
		}},
		Timestamp:   "2024-01-01T00:00:02Z",
		IsSidechain: true,
	}
	metaUser := transcriptEntry{
		Type:      "user",
		Message:   msgObject{Role: "user", Content: "injected meta note"},
		Timestamp: "2024-01-01T00:00:02Z",
		IsMeta:    true,
	}
	interruptMarkerUser := transcriptEntry{
		Type: "user",
		Message: msgObject{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "[Request interrupted by user]"},
		}},
		Timestamp: "2024-01-01T00:00:02Z",
	}

	cases := []struct {
		name    string
		entries []transcriptEntry
		want    TurnState
	}{
		{
			name:    "completed turn: assistant with text only",
			entries: []transcriptEntry{userText, assistantText},
			want:    TurnStateComplete,
		},
		{
			name:    "pending tool: assistant with tool_use block",
			entries: []transcriptEntry{userText, assistantToolUse},
			want:    TurnStatePendingTool,
		},
		{
			name:    "user pending: last entry is a tool_result",
			entries: []transcriptEntry{assistantToolUse, userToolResult},
			want:    TurnStateUserPending,
		},
		{
			name:    "user pending: last entry is a text prompt",
			entries: []transcriptEntry{assistantText, userText},
			want:    TurnStateUserPending,
		},
		{
			name:    "system tail is skipped, prior assistant decides",
			entries: []transcriptEntry{userText, assistantText, systemTail},
			want:    TurnStateComplete,
		},
		{
			// A subagent's closing message must not read as the main thread
			// finishing while the main turn still has a pending tool_use.
			name:    "sidechain tail is skipped, main thread pending tool decides",
			entries: []transcriptEntry{userText, assistantToolUse, sidechainAssistantText},
			want:    TurnStatePendingTool,
		},
		{
			name:    "meta user tail is skipped, prior assistant decides",
			entries: []transcriptEntry{userText, assistantText, metaUser},
			want:    TurnStateComplete,
		},
		{
			// Deliberate split, not an oversight: every message reader drops
			// this entry (conversationTextBlocks — a text-only user array with
			// no promptSource is nobody speaking), and TurnState must not.
			// Status is re-derived from which role spoke last, so it stays on
			// the entry's type and block kinds and never asks whether the words
			// count as conversation. The price is visible and accepted: a
			// transcript ending on an interruption marker reports UserPending,
			// which the Claude adapter renders as "thinking" on daemon
			// recovery.
			name:    "text-only user array still counts as the user speaking",
			entries: []transcriptEntry{userText, assistantText, interruptMarkerUser},
			want:    TurnStateUserPending,
		},
		{
			name:    "thinking-only assistant tail is still in flight",
			entries: []transcriptEntry{userText, assistantThinkingOnly},
			want:    TurnStateUserPending,
		},
		{
			name:    "sidechain-only transcript is unknown",
			entries: []transcriptEntry{sidechainAssistantText},
			want:    TurnStateUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			r := &Reader{claudeDir: tmpDir}
			workDir := "/turnstate/test"
			sessionID := "sess-ts"
			writeJSONL(t, r.getTranscriptPath(workDir, sessionID), tc.entries)

			if got := r.TurnState(workDir, sessionID); got != tc.want {
				t.Errorf("TurnState = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTurnState_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	p := r.getTranscriptPath("/turnstate/empty", "sess-ts-empty")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if got := r.TurnState("/turnstate/empty", "sess-ts-empty"); got != TurnStateUnknown {
		t.Errorf("TurnState(empty file) = %d, want %d", got, TurnStateUnknown)
	}
}

func TestTurnState_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	if got := r.TurnState("/nowhere", "no-such-session"); got != TurnStateUnknown {
		t.Errorf("TurnState(no file) = %d, want %d", got, TurnStateUnknown)
	}
}

func TestTurnState_MalformedLinesSkipped(t *testing.T) {
	// A broken JSONL line must not abort classification: readEntries skips it
	// and the last valid assistant entry still decides the state.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/turnstate/broken"
	sessionID := "sess-ts-broken"
	writeRawJSONL(t, r.getTranscriptPath(workDir, sessionID), []string{
		`{not json`,
		`{"type":"user","message":{"role":"user","content":"hi"},"timestamp":"2024-01-01T00:00:00Z"}`,
		`}also not json`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"timestamp":"2024-01-01T00:00:01Z"}`,
	})
	if got := r.TurnState(workDir, sessionID); got != TurnStateComplete {
		t.Errorf("TurnState(with malformed lines) = %d, want %d", got, TurnStateComplete)
	}
}

// --- glob fallback for GetLastMessage / GetLastMessages / GetConversation ---
//
// The bug motivating these tests: sessions frequently cd into a subdir or
// worktree, so info.WorkDir (the launch dir) points at one projects/<dir>/
// while the JSONL Claude Code actually writes lives under another. The old
// implementations built an exact path from workDir and returned nil when it
// missed, surfacing as "no messages found in transcript" from `jin session
// output`. findTranscriptPath (already used by ReadEntries) globs by
// sessionID across all projects dirs, so wiring these helpers through it
// fixes the miss.

func TestGetLastMessage_GlobFallback_WhenWorkDirMismatches(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	// Write the transcript under the ACTUAL dir Claude landed in (say, a
	// worktree the agent cd'd into after start).
	actualDir := "/actual/worktree"
	sessionID := "sess-mismatch"
	writeJSONL(t, r.getTranscriptPath(actualDir, sessionID), []transcriptEntry{
		{
			Type: "assistant",
			Message: msgObject{
				Role:    "assistant",
				Content: []any{map[string]any{"type": "text", "text": "hello from the real dir"}},
			},
			Timestamp: "2024-01-01T00:00:00Z",
		},
	})

	// Query with the WRONG dir (matches the old bug: info.WorkDir before cd).
	msg, err := r.GetLastMessage("/original/launch/dir", sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected fallback to find the transcript by sessionID glob, got nil")
	}
	if msg.Content != "hello from the real dir" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello from the real dir")
	}
}

func TestGetLastMessages_GlobFallback_WhenWorkDirMismatches(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	actualDir := "/actual/worktree"
	sessionID := "sess-mismatch-lm"
	writeJSONL(t, r.getTranscriptPath(actualDir, sessionID), []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "u1"},
			Timestamp: "2024-01-01T00:00:00Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role:    "assistant",
				Content: []any{map[string]any{"type": "text", "text": "a1"}},
			},
			Timestamp: "2024-01-01T00:00:01Z",
		},
	})

	msgs, err := r.GetLastMessages("/original/launch/dir", sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs == nil {
		t.Fatal("expected fallback to find the transcript by sessionID glob, got nil")
	}
	if msgs.User == nil || msgs.User.Content != "u1" {
		t.Errorf("User = %+v, want content=%q", msgs.User, "u1")
	}
	if msgs.Assistant == nil || msgs.Assistant.Content != "a1" {
		t.Errorf("Assistant = %+v, want content=%q", msgs.Assistant, "a1")
	}
}

func TestGetConversation_GlobFallback_WhenWorkDirMismatches(t *testing.T) {
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}

	actualDir := "/actual/worktree"
	sessionID := "sess-mismatch-conv"
	writeJSONL(t, r.getTranscriptPath(actualDir, sessionID), []transcriptEntry{
		{
			Type:      "user",
			Message:   msgObject{Role: "user", Content: "q"},
			Timestamp: "2024-01-01T00:00:00Z",
		},
		{
			Type: "assistant",
			Message: msgObject{
				Role:    "assistant",
				Content: []any{map[string]any{"type": "text", "text": "a"}},
			},
			Timestamp: "2024-01-01T00:00:01Z",
		},
	})

	msgs, err := r.GetConversation("/original/launch/dir", sessionID, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages via glob fallback, got %d", len(msgs))
	}
}

func TestGetLastMessage_ReturnsErrNoTranscriptWhenTrulyMissing(t *testing.T) {
	// When no transcript file exists anywhere, GetLastMessage reports
	// ErrNoTranscript. output_cmd relies on that to tell "wrong/too-early
	// session" apart from "transcript exists but says nothing yet"; the two
	// used to collapse into one silent empty result.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	msg, err := r.GetLastMessage("/nowhere", "no-such-session")
	if !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("expected ErrNoTranscript, got err=%v", err)
	}
	if msg != nil {
		t.Fatalf("expected nil message, got %+v", msg)
	}
}

func TestGetLastMessage_ToolOnlyTranscriptHasNothingToSay(t *testing.T) {
	// A session that has so far only called tools has a transcript, but no
	// message to print. GetLastMessage reports that as (nil, nil), which
	// output_cmd turns into the "no plain-text messages" hint. Returning the
	// empty text of a tool_use entry instead would make `jin session output`
	// print a blank line and exit 0 — indistinguishable from the missing
	// transcript this pair of states was split apart to expose.
	tmpDir := t.TempDir()
	r := &Reader{claudeDir: tmpDir}
	workDir := "/toolonly/test"
	sessionID := "sess-toolonly"

	writeJSONL(t, r.getTranscriptPath(workDir, sessionID), []transcriptEntry{
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{
			map[string]any{"type": "tool_use", "name": "Bash", "id": "tu_1", "input": map[string]any{"command": "echo hi"}},
		}}, Timestamp: "2024-01-01T00:00:00Z"},
		{Type: "user", Message: msgObject{Role: "user", Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "hi"},
		}}, Timestamp: "2024-01-01T00:00:01Z"},
		{Type: "assistant", Message: msgObject{Role: "assistant", Content: []any{
			map[string]any{"type": "thinking", "thinking": "let me see"},
		}}, Timestamp: "2024-01-01T00:00:02Z"},
	})

	msg, err := r.GetLastMessage(workDir, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatalf("GetLastMessage = %+v, want nil: none of these entries carries plain text", msg)
	}
}
