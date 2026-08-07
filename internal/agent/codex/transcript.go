package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/takaaki-s/jind-ai/internal/transcript"
)

// Reader turns a Codex rollout JSONL into jind-ai's shared transcript.Entry
// form, which is what `jin session result` serialises. It satisfies
// session.TranscriptSource.
//
// Everything here is derived from a full read of 14 real rollouts (501
// lines); the counts quoted in the comments below are from that corpus. The
// format is undocumented, so a rule with no measurement behind it is a guess,
// and the numbers are recorded so a later reader can tell which is which.
type Reader struct {
	locator *Locator
}

// NewTranscriptReader returns a Reader that locates rollouts the same way the
// description enhancer does — CODEX_HOME when set, else <home>/.codex/sessions.
func NewTranscriptReader(home string) *Reader {
	return &Reader{locator: NewLocator(home)}
}

// ReadEntries returns the conversation recorded for sessionID, keeping only
// entries whose Timestamp is strictly greater than since.
//
// workDir is ignored. Codex shards rollouts by date rather than by working
// directory, and the session UUID is already unique across the whole tree, so
// there is nothing for the hint to narrow. The parameter stays because
// session.TranscriptSource is shared with Claude Code, where it does help.
//
// A session with no rollout on disk yet returns (nil, nil): Codex writes the
// file when the agent starts, so an empty result here means "too early", not
// "broken". That matches what transcript.Reader does with ErrNoTranscript.
func (r *Reader) ReadEntries(_, sessionID, since string) ([]transcript.Entry, error) {
	path, ok := r.locator.Find(sessionID)
	if !ok {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return readEntriesFrom(f, since)
}

// entryRow is the conversation-shaped view of a rollout line.
//
// It is deliberately a second decoder alongside rolloutRow rather than an
// extension of it. The two read the same file for different things —
// rolloutRow answers "which session is this and what did the operator first
// say", this one answers "what happened in the conversation" — and the field
// names collide across those questions (`payload.id` is the session UUID on a
// session_meta line and a per-item identifier on a response_item). One struct
// covering both would need names that mean different things depending on the
// line, which is how a parser starts reading the wrong field.
type entryRow struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type string `json:"type"`

		// message
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`

		// reasoning
		Summary []contentBlock `json:"summary"`

		// custom_tool_call (input) / function_call (arguments)
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`

		// custom_tool_call_output / function_call_output
		Output json.RawMessage `json:"output"`

		// event_msg/task_complete
		Error *taskError `json:"error"`
	} `json:"payload"`
}

// taskError is the object a task_complete line carries when the turn ended in
// failure. Both fields are Codex's own: Message is the sentence shown to the
// operator, Info the machine-readable classifier ("usage_limit_exceeded").
type taskError struct {
	Message string `json:"message"`
	Info    string `json:"codex_error_info"`
}

// readEntriesFrom parses a rollout stream into entries.
//
// Lines that fail to parse are skipped rather than fatal: Codex flushes as it
// writes, so reading a live session routinely catches a half-written tail.
// A scanner error (a line past scannerMaxLine, or an I/O failure) is returned,
// matching transcript.Reader — a truncated read that looks like a short
// conversation is the failure mode worth being loud about.
func readEntriesFrom(r io.Reader, since string) ([]transcript.Entry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxLine)

	var b entryBuilder
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var row entryRow
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		// Top-level timestamps are present on 501/501 lines, fixed-width and
		// lexicographically ordered exactly like Claude Code's, so the same
		// string comparison works.
		if since != "" && row.Timestamp != "" && row.Timestamp <= since {
			continue
		}

		switch row.Type {
		case "response_item":
			role, blk, ok := conversationBlock(&row)
			if !ok {
				continue
			}
			b.add(role, row.Timestamp, blk)
		case "event_msg":
			// event_msg is the parallel stream Codex writes for its own UI.
			// Reading it for content double-counts: 12 of the 14 rollouts
			// carry each utterance on both streams. Only task_complete is
			// read, and only for the failure it alone records.
			if e, ok := turnFailureEntry(&row); ok {
				b.emit(e)
			}
		}
	}
	b.flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return b.out, nil
}

// entryBuilder groups consecutive blocks into entries at role boundaries.
//
// Codex writes one block per line, so copying lines to entries 1:1 would turn
// a 252-line corpus into 252 single-block entries and make `--last 5` mean
// "five blocks" for Codex while it means "five messages" for Claude Code —
// the same flag doing different things per agent kind. Grouping restores the
// shape Claude Code already has: an assistant entry holding its thinking,
// text and tool_use together, with tool results coming back as user entries.
type entryBuilder struct {
	out []transcript.Entry
	cur *transcript.Entry
}

// add appends blk to the open entry when the role still matches, and starts a
// new one when it does not.
//
// The entry's Timestamp tracks its *last* line, not its first. That is what
// makes incremental reads exact: a caller passing the timestamp of the last
// entry it saw as `since` needs every line already folded into that entry to
// compare as "at or before" it. Stamping the first line instead would leave
// the group's later lines above the bound, and they would come back a second
// time as a partial duplicate of an entry the caller already has.
func (b *entryBuilder) add(role, ts string, blk transcript.Block) {
	if b.cur != nil && b.cur.Type == role {
		b.cur.Blocks = append(b.cur.Blocks, blk)
		if ts != "" {
			b.cur.Timestamp = ts
		}
		return
	}
	b.flush()
	b.cur = &transcript.Entry{Type: role, Timestamp: ts, Blocks: []transcript.Block{blk}}
}

// emit closes any open entry and appends a standalone one after it.
func (b *entryBuilder) emit(e transcript.Entry) {
	b.flush()
	b.out = append(b.out, e)
}

func (b *entryBuilder) flush() {
	if b.cur == nil {
		return
	}
	b.out = append(b.out, *b.cur)
	b.cur = nil
}

// conversationBlock maps a response_item to the entry role it belongs to and
// the block it contributes. ok is false for items that are not part of the
// conversation at all.
func conversationBlock(row *entryRow) (string, transcript.Block, bool) {
	p := &row.Payload
	switch p.Type {
	case "message":
		switch p.Role {
		case "user":
			// Codex files context it injects on the operator's behalf under
			// role "user" as well as role "developer" — environment blocks,
			// plugin catalogues, skill bodies. isPseudoUser is the same test
			// the description enhancer uses, ground-truthed against the 20
			// event_msg/user_message lines the corpus carries. Filtering is
			// per block because one item can hold an injection and real words
			// side by side.
			text := joinBlockText(p.Content, true)
			if text == "" {
				return "", transcript.Block{}, false
			}
			return "user", transcript.Block{Kind: "text", Text: text}, true
		case "assistant":
			text := joinBlockText(p.Content, false)
			if text == "" {
				return "", transcript.Block{}, false
			}
			return "assistant", transcript.Block{Kind: "text", Text: text}, true
		}
		// role "developer" is Codex's system-prompt channel: all 43 items in
		// the corpus are injected instructions, one of them 17,885 characters
		// long. Nobody said them, and letting them through buries the
		// conversation they are supposed to frame.
		return "", transcript.Block{}, false

	case "reasoning":
		// The summary array is empty on 53/53 items — the reasoning itself
		// lives in encrypted_content and cannot be read. An empty thinking
		// block carries nothing and would still consume a slot in `--last N`,
		// so items with no readable summary contribute nothing.
		text := joinBlockText(p.Summary, false)
		if text == "" {
			return "", transcript.Block{}, false
		}
		return "assistant", transcript.Block{Kind: "thinking", Text: text}, true

	case "custom_tool_call":
		return "assistant", toolUseBlock(p.Name, p.CallID, p.Input), true
	case "function_call":
		return "assistant", toolUseBlock(p.Name, p.CallID, p.Arguments), true

	case "custom_tool_call_output", "function_call_output":
		out := stringifyOutput(p.Output)
		return "user", transcript.Block{
			Kind:      "tool_result",
			ToolUseID: p.CallID,
			Output:    out,
			IsError:   looksLikeToolError(out),
		}, true

	case "agent_message":
		// Not the assistant speaking: these carry author/recipient fields and
		// a routing envelope in the body. They are messages between agents,
		// and the assistant's own words for the same turn are already on a
		// message item, so admitting them duplicates.
		return "", transcript.Block{}, false
	}
	return "", transcript.Block{}, false
}

// toolUseBlock records a tool call. Codex's tool input is a bare string — the
// script source for custom_tool_call, a JSON document for function_call — but
// Block.Input is json.RawMessage, so the string is encoded as a JSON string.
//
// Wrapping it in a synthesised object instead (say {"source": "..."}) would
// read as structure the agent emitted, when in fact jind-ai made it up. A
// JSON string is the honest encoding of a string.
func toolUseBlock(name, callID, input string) transcript.Block {
	b := transcript.Block{Kind: "tool_use", ToolName: name, ToolUseID: callID}
	if input != "" {
		if raw, err := json.Marshal(input); err == nil {
			b.Input = raw
		}
	}
	return b
}

// stringifyOutput flattens a tool output, which the format writes three ways:
// an array of text blocks (40/41 custom, 1/5 function), a bare string (1/41
// custom, 4/5 function), and absent/null.
//
// Array elements are joined with no separator because they are consecutive
// slices of one stream — the harness header ends in its own newline and the
// command's output follows directly. Inserting a separator would add a blank
// line that was never in the output.
func stringifyOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, c := range blocks {
			sb.WriteString(c.Text)
		}
		return sb.String()
	}
	// Some shape nobody has seen. Handing back the raw JSON keeps the content
	// readable instead of dropping it.
	return string(raw)
}

// looksLikeToolError reports whether a tool output shows the call failed.
//
// It has low recall by construction, and callers must not read a false as
// proof of success. The format has no error flag: custom_tool_call.status is
// "completed" on 41/41 calls including the one whose patch failed to apply.
// What is left are two shapes the output itself happens to expose — the exec
// harness prefixing "Script failed" when it could not run the script (1/41),
// and tools that answer with a JSON object carrying timed_out (2 in the
// corpus).
//
// What this cannot see is the common case: a command that ran and exited
// non-zero is still reported as "Script completed", and the exit code appears
// nowhere in the rollout. A failing build or test suite is invisible here.
func looksLikeToolError(output string) bool {
	if strings.HasPrefix(output, "Script failed") {
		return true
	}
	var probe struct {
		TimedOut bool `json:"timed_out"`
	}
	if err := json.Unmarshal([]byte(output), &probe); err == nil && probe.TimedOut {
		return true
	}
	return false
}

// turnFailureEntry converts a task_complete line that reports an error into a
// standalone system entry.
//
// This is the only reason event_msg is read at all. Three of the 14 sessions
// end with the agent having said nothing, because the turn died on a usage
// limit — and from response_item lines alone that is indistinguishable from a
// turn still being thought about. Without this entry an orchestrator waits on
// a session that is never going to answer.
//
// Both fields Codex records are kept; neither is invented. Info is the
// classifier a caller can match on, Message the sentence a human reads.
func turnFailureEntry(row *entryRow) (transcript.Entry, bool) {
	if row.Payload.Type != "task_complete" || row.Payload.Error == nil {
		return transcript.Entry{}, false
	}
	e := row.Payload.Error
	text := e.Message
	switch {
	case e.Info != "" && text != "":
		text = e.Info + ": " + text
	case text == "":
		text = e.Info
	}
	if text == "" {
		return transcript.Entry{}, false
	}
	return transcript.Entry{
		Type:      "system",
		Timestamp: row.Timestamp,
		Blocks:    []transcript.Block{{Kind: "text", Text: text}},
	}, true
}

// joinBlockText concatenates a content array's text. Blocks are separate lines
// of one item, so they join with a newline. When skipPseudo is set, blocks
// Codex injected rather than the operator wrote are left out.
func joinBlockText(blocks []contentBlock, skipPseudo bool) string {
	var parts []string
	for _, c := range blocks {
		if skipPseudo && isPseudoUser(c.Text) {
			continue
		}
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}
