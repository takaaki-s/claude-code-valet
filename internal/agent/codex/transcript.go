package codex

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/takaaki-s/jind-ai/internal/transcript"
)

// TranscriptReader turns a Codex rollout JSONL into jind-ai's shared transcript.Entry
// form, which is what `jin session result` serialises. It satisfies
// session.TranscriptSource.
//
// Everything here is derived from a full read of 14 real rollouts (501
// lines); the counts quoted in the comments below are from that corpus. The
// format is undocumented, so a rule with no measurement behind it is a guess,
// and the numbers are recorded so a later reader can tell which is which.
type TranscriptReader struct {
	locator *Locator
}

// NewTranscriptReader returns a reader that locates rollouts through loc.
func NewTranscriptReader(loc *Locator) *TranscriptReader {
	return &TranscriptReader{locator: loc}
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
func (r *TranscriptReader) ReadEntries(_, sessionID, since string) ([]transcript.Entry, error) {
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
// say", this one answers "what happened in the conversation" — and each
// declares exactly the fields its own question needs. Merging them would put
// ten conversation fields into the struct FirstUserPrompt decodes on every
// line of every rollout, and would leave neither parser's dependencies
// readable from its own type.
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
	scanner := newRolloutScanner(r)

	var b entryBuilder
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Top-level timestamps are present on 501/501 lines, fixed-width and
		// lexicographically ordered exactly like Claude Code's, so the shared
		// rule applies unchanged.
		//
		// On an incremental read the bound is checked before the payload is
		// decoded, because entryRow holds the tool output as json.RawMessage
		// and decoding it copies every byte. A poll that finds nothing new
		// would otherwise copy the whole file to throw it away — measured at
		// 52 MB allocated to return zero entries from a 49 MB rollout, against
		// 4 MB with the bound checked first. A full read skips the extra pass
		// entirely, so it pays nothing for this.
		if since != "" {
			var head struct {
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				continue
			}
			if !transcript.Newer(head.Timestamp, since) {
				continue
			}
		}
		var row entryRow
		if err := json.Unmarshal(line, &row); err != nil {
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
// the corpus's 252 response_item lines into 252 single-block entries and make
// `--last 5` mean
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
			return textBlock("user", "text", joinBlockText(p.Content, true))
		case "assistant":
			return textBlock("assistant", "text", joinBlockText(p.Content, false))
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
		return textBlock("assistant", "thinking", joinBlockText(p.Summary, false))

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
			// False here means "no failure signal was found", not "the call
			// succeeded" — the format has no flag to read. See
			// looksLikeToolError for what the two signals cover.
			IsError: looksLikeToolError(out),
		}, true

	case "agent_message":
		// Enumerated rather than left to the fallthrough: this is the payload
		// type a reader comes here to check, and finding no case for it reads
		// as an oversight.
		//
		// Not the assistant speaking: these carry author/recipient fields and
		// a routing envelope in the body. They are messages between agents,
		// and the assistant's own words for the same turn are already on a
		// message item, so admitting them duplicates.
		return "", transcript.Block{}, false
	}
	return "", transcript.Block{}, false
}

// textBlock is the shared exit for the payload types that contribute prose.
// All three drop the item when there is nothing left to say — an empty block
// carries no information and still takes a slot in `--last N` — and having one
// place to say so means a fourth prose type cannot quietly disagree.
func textBlock(role, kind, text string) (string, transcript.Block, bool) {
	if text == "" {
		return "", transcript.Block{}, false
	}
	return role, transcript.Block{Kind: kind, Text: text}, true
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
// slices of one stream — the opposite of joinBlockText above, and of the
// Claude Code reader's equivalent, both of which join with a newline because
// their blocks are separate lines of one message — the harness header ends in its own newline and the
// command's output follows directly. Inserting a separator would add a blank
// line that was never in the output.
func stringifyOutput(raw json.RawMessage) string {
	// An absent output stops here rather than falling through. The raw
	// fallback below would also return "", but it is documented as being for
	// shapes nobody has seen, and "the field was not there" is not one.
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return ""
	}
	// Dispatch on the opening byte rather than trying each decode in turn. The
	// array shape is the common one (40/41 custom outputs), and attempting a
	// string decode on it is not a cheap miss: the parser validates the whole
	// payload and then walks the array again to skip it, which measured at
	// roughly a fifth of all CPU spent reading a real rollout.
	switch trimmed[0] {
	case 'n': // null, i.e. recorded but empty
		return ""
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return string(raw)
	case '[':
		break
	default:
		return string(raw)
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(trimmed, &elems); err == nil {
		var sb strings.Builder
		for _, e := range elems {
			// An element is decoded for its text, but decoding into a struct
			// with one field succeeds for any object — so an element carrying
			// no text at all would come back as the empty string and vanish
			// without trace. Every element in the corpus is a text block; one
			// that is not (an image reference, say) is kept as its own JSON,
			// which is what the Claude Code reader does with the same shape.
			var c contentBlock
			if err := json.Unmarshal(e, &c); err == nil && c.Text != "" {
				sb.WriteString(c.Text)
				continue
			}
			sb.Write(e)
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
	// Only a JSON object can carry the flag, and the check is worth making
	// before the parse: json.Unmarshal takes []byte, so handing it a string
	// copies the whole output first. Tool output here is command output —
	// measured at 1.6ms per call on a 4 MiB build log, against 17ns once the
	// prefix rules it out, and 39 of the 41 outputs in the corpus begin with
	// the exec harness's own header rather than a brace.
	if !strings.HasPrefix(strings.TrimLeft(output, " \t\r\n"), "{") {
		return false
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
	var text string
	switch {
	case e.Info == "":
		text = e.Message
	case e.Message == "":
		text = e.Info
	default:
		text = e.Info + ": " + e.Message
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
// of one item, so they join with a newline — unlike a tool output's array,
// whose elements are consecutive slices of one stream and join with nothing.
//
// When skipPseudo is set the operator's own blocks are selected by
// genuineBlocks, the same rule the description enhancer reads, so a prefix
// added to the injection list reaches both.
func joinBlockText(blocks []contentBlock, skipPseudo bool) string {
	if skipPseudo {
		return strings.Join(genuineBlocks(blocks), "\n")
	}
	var parts []string
	for _, c := range blocks {
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}

// CheapEnoughToPoll implements session.PollableTranscriptSource.
//
// Locating the rollout and walking it is file I/O, the same order of cost as
// the Claude Code reader, so the preview path may call it on a timer.
func (r *TranscriptReader) CheapEnoughToPoll() {}
