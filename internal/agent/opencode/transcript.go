package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/takaaki-s/jind-ai/internal/procgroup"
	"github.com/takaaki-s/jind-ai/internal/transcript"
)

// exportBinary is the command that prints a session as JSON. It is resolved on
// PATH at read time rather than captured at start-up: the daemon outlives any
// single install, and a version manager can move the binary under it.
const exportBinary = "opencode"

// exportTimeout bounds one export. Measured cost is 1.45–1.77s across sessions
// from 3 to 117 parts — the work is opencode's start-up, not the conversation,
// so the ceiling is set against that constant rather than against session size.
// It is deliberately far above it: the point is to stop a wedged process
// holding the daemon's handler, not to police a slow one. It has an upper
// bound too — daemon.defaultRequestTimeout, the 60s the client waits — because
// past that the client reports a timeout while the daemon is still exporting.
// A wedged export can take procgroup.GracePeriod longer than this to unblock
// Wait, since a process that ignores SIGTERM is only killed after it; 30s plus
// that grace still leaves room under the client's 60s.
const exportTimeout = 30 * time.Second

// exportStderrLimit caps how much of an export's stderr jind-ai keeps. The
// tail is what is kept, because opencode writes a progress line on every run
// and the reason a failed one gives comes after it.
//
// It bounds memory as well as the message. A process given 30 seconds can
// write as much as it likes, and a Buffer would hold all of it to produce
// these 512 bytes.
const exportStderrLimit = 512

// stderrTail keeps only the last exportStderrLimit bytes written to it, so a
// verbose or looping process cannot grow the daemon's memory through a channel
// nobody reads in full.
type stderrTail struct {
	buf [exportStderrLimit]byte
	n   int // bytes written into buf, capped at len(buf)
	at  int // next write position
}

func (t *stderrTail) Write(p []byte) (int, error) {
	written := len(p)
	if len(p) > len(t.buf) {
		p = p[len(p)-len(t.buf):]
	}
	for _, c := range p {
		t.buf[t.at] = c
		t.at = (t.at + 1) % len(t.buf)
		if t.n < len(t.buf) {
			t.n++
		}
	}
	return written, nil
}

// String returns what was kept, oldest byte first.
func (t *stderrTail) String() string {
	if t.n < len(t.buf) {
		return string(t.buf[:t.n])
	}
	return string(t.buf[t.at:]) + string(t.buf[:t.at])
}

// TranscriptReader turns `opencode export` output into jind-ai's shared
// transcript.Entry form. It satisfies session.TranscriptSource.
//
// opencode keeps its conversation in a SQLite database, and jind-ai reads
// neither the database nor a copy of its own. Reading the database directly
// would mean a pure-Go SQLite driver (+25 modules, +6.2MB) and a dependency on
// opencode's schema — which already carries an unused `session_message` table
// waiting to become the live one. Keeping a copy, which an earlier revision of
// this adapter did, meant jind-ai owning a growing set of files with nothing to
// reclaim them. Asking opencode to read its own database costs a process.
//
// `--pure` is not optional. It stops opencode loading plugins, including
// jind-ai's own, which keeps this read off the status-reporting path entirely.
//
// Entry.Injected and Entry.Sidechain are left false throughout, which is the
// "drop them while reading" half of the TranscriptSource contract rather than
// an omission. Injected text is dropped by partBlocks. A subagent's turns
// cannot arrive at all: the task tool gives a child its own session, and an
// export names one session — measured on a parent with 4 children and 39
// messages between them, 0 reached the parent's document, while its 4 task
// calls are there as ordinary tool blocks, which is the right level of detail
// for reading what the parent did.
type TranscriptReader struct {
	// export runs the command and returns the document. Replaced in tests so
	// the parsing can be exercised without a real opencode install.
	export func(sessionID string) ([]byte, error)
}

// NewTranscriptReader returns a reader that shells out to opencode.
func NewTranscriptReader() *TranscriptReader {
	return &TranscriptReader{export: runExport}
}

// ReadEntries returns the conversation opencode holds for sessionID, keeping
// only entries whose Timestamp is strictly greater than since.
//
// workDir is ignored: opencode finds the session by id in its own database.
// The parameter stays because session.TranscriptSource is shared with Claude
// Code, where it does help.
//
// An id that is not an opencode session id returns (nil, nil) rather than an
// error. That is the window every session passes through: jind-ai pre-mints
// Session.AgentSessionID as a UUID and only learns opencode's own `ses_` id
// when the plugin reports session.created. Asking opencode about a UUID would
// fail, and "the agent has not started yet" is not a failure. The Codex
// adapter has the same window; see docs/gotchas.md.
//
// Everything past that point is loud. Once the id is opencode's own, a session
// opencode cannot produce is a genuine read failure, and this returns the
// error rather than an empty conversation.
func (r *TranscriptReader) ReadEntries(_, sessionID, since string) ([]transcript.Entry, error) {
	if !isSessionID(sessionID) {
		return nil, nil
	}
	doc, err := r.export(sessionID)
	if err != nil {
		return nil, err
	}
	return entriesFromExport(doc, since)
}

// isSessionID reports whether s is shaped like an opencode session id: the
// ses_ prefix followed by base62 characters.
//
// It is the test that separates the pre-minted UUID from opencode's own id, and
// it is the only one: SpawnCommand calls it too, to tell "resumable" from
// "spawned once". Two predicates for one question is how the resume path and
// the read path come to disagree about what an id is.
//
// It also keeps an unexpected value out of a subprocess's argv. Nothing here
// builds a shell string, so this is not guarding against quoting; it is
// guarding against spending a second and an error message on a value that
// cannot name a session.
//
// An allow-list rather than a deny-list, because the alphabet is known: across
// 877 real ids (sessions, messages and parts) every character after the prefix
// is base62 and every body is exactly 26 characters. The length is evidence
// that the alphabet is settled, not a rule this applies — pinning a width would
// reject a longer real id, which fails the quiet way. Rejecting a real id would
// be its own quiet failure — the read would answer empty and successful — so
// if opencode ever widens the alphabet this is the line to change, and the
// count is what makes that a measurement rather than a matter of taste.
func isSessionID(s string) bool {
	rest, ok := strings.CutPrefix(s, sessionIDPrefix)
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// runExport executes `opencode export --pure <id>` and returns its stdout.
//
// The output goes to a temp file rather than a pipe, and that is measured, not
// stylistic: reading the same 133KB session through a pipe came back truncated
// at exactly 65536 bytes on 8 of 10 runs, against 0 of 10 into a file. Every
// truncation produced invalid JSON, so it would have been caught rather than
// returned as a short conversation — but a read that fails 80% of the time is
// not a read.
func runExport(sessionID string) ([]byte, error) {
	bin, err := exec.LookPath(exportBinary)
	if err != nil {
		// Worth spelling out, because the session itself works: jind-ai
		// launches an agent through the user's login shell, which resolves a
		// version manager's shims, while the daemon's own PATH may not.
		return nil, fmt.Errorf("opencode: %q is not on the daemon's PATH, so its conversation cannot be read "+
			"(sessions still start, because they are launched through your login shell): %w", exportBinary, err)
	}

	f, err := os.CreateTemp("", "jin-opencode-export-*.json")
	if err != nil {
		return nil, fmt.Errorf("opencode: create export temp file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()

	var stderr stderrTail
	cmd := newExportCmd(ctx, bin, sessionID, f, &stderr)
	if err := cmd.Run(); err != nil {
		// The exit status is the verdict, not stderr: opencode writes a
		// progress line there on every run, successful ones included.
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			return nil, fmt.Errorf("opencode: export of %s did not finish within %s: %s", sessionID, exportTimeout, msg)
		}
		return nil, fmt.Errorf("opencode: export of %s failed: %w: %s", sessionID, err, msg)
	}
	return os.ReadFile(f.Name())
}

// newExportCmd builds the command, separately from running it, so a test can
// see how it was wired without an opencode to run.
//
// procgroup.KillOnCancel is the part worth naming: opencode is a runtime that
// starts more processes than the one named here, so cancelling the context has
// to reach the whole group. The standard library would signal only the leader
// and leave the rest running past the timeout that exists to stop them.
func newExportCmd(ctx context.Context, bin, sessionID string, stdout, stderr io.Writer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, exportArgs(sessionID)...)
	procgroup.KillOnCancel(cmd)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd
}

// exportArgs is the command line, split out so a test can pin it.
//
// `--pure` is the load-bearing one. Without it opencode loads plugins,
// including jind-ai's own status reporter, which would put a `jin hook` call
// and an event handler on a path whose only job is to print a session — and
// would do it once per read. Dropping the flag breaks nothing a parser test
// would notice.
func exportArgs(sessionID string) []string {
	return []string{"export", "--pure", sessionID}
}

// exportDoc is the conversation-shaped view of an export document. opencode
// prints {"info": {...}, "messages": [{"info": ..., "parts": [...]}]}; only the
// messages are read, and only the fields below are declared, so a field added
// upstream is ignored rather than a parse failure.
//
// The nesting is worth noticing: a part arrives inside its own message rather
// than carrying a messageID to be joined on. There is no id to key that join
// wrongly, and no orphan part to drop.
type exportDoc struct {
	Messages []exportMessage `json:"messages"`
}

type exportMessage struct {
	Info  messageRow `json:"info"`
	Parts []partRow  `json:"parts"`
}

// messageRow is the conversation-shaped view of an opencode Message.
type messageRow struct {
	Role string `json:"role"`
	Time struct {
		Created *int64 `json:"created"`
	} `json:"time"`
	// Tokens is present on assistant messages only (122/124 in the corpus —
	// the two without it are turns that were aborted).
	Tokens *struct {
		Input  int `json:"input"`
		Output int `json:"output"`
		Cache  struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	// Error is set when the turn ended in failure ("MessageAbortedError" in
	// both of the corpus's two cases).
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// partRow is the conversation-shaped view of an opencode Part.
type partRow struct {
	Type string `json:"type"`

	// text / reasoning
	Text string `json:"text"`
	// Synthetic and Ignored mark text opencode inserted on the operator's
	// behalf rather than words anyone typed. Neither flag is set on any of
	// the 132 text parts in the corpus, so this rule comes from opencode's
	// schema rather than from observation — which is exactly why it is
	// written down here.
	Synthetic bool `json:"synthetic"`
	Ignored   bool `json:"ignored"`
	Time      struct {
		Start *int64 `json:"start"`
	} `json:"time"`

	// tool
	Tool   string `json:"tool"`
	CallID string `json:"callID"`
	State  struct {
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input"`
		Output   string          `json:"output"`
		Error    string          `json:"error"`
		Metadata struct {
			// Exit is the process exit status for tools that run one.
			// json.Number is a string underneath, so an absent field and a
			// null both arrive as "" while a real zero arrives as "0" —
			// which is the distinction nonZeroExit needs, with no pointer.
			Exit json.Number `json:"exit"`
		} `json:"metadata"`
		Time struct {
			Start *int64 `json:"start"`
			End   *int64 `json:"end"`
		} `json:"time"`
	} `json:"state"`
}

// entriesFromExport parses an export document into entries.
//
// A document that does not parse is an error, not an empty conversation. That
// matters more here than it looks: a truncated export is still a prefix of
// valid JSON right up to the point it stops, so the only thing standing between
// a cut-off read and a plausibly short answer is refusing to accept one.
func entriesFromExport(doc []byte, since string) ([]transcript.Entry, error) {
	var parsed exportDoc
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, fmt.Errorf("opencode: export output is not a session document (%d bytes): %w", len(doc), err)
	}

	var b entryBuilder
	for i := range parsed.Messages {
		m := &parsed.Messages[i]
		for j := range m.Parts {
			b.addPart(&m.Info, i, &m.Parts[j], since)
		}
		// The clock moves to the message's own time whether or not the turn
		// failed, so a later message cannot inherit a stamp from before it.
		ts := b.stamp(m.Info.Time.Created)
		// A turn that ended in failure is reported after the content it
		// interrupted, which is where opencode itself records it.
		if e, ok := turnFailureEntry(&m.Info, ts); ok && transcript.Newer(e.Timestamp, since) {
			b.emit(e)
		}
	}
	b.flush()
	return b.out, nil
}

// partBlocks maps one part to the blocks it contributes, in order. An empty
// result means the part is not conversation.
func partBlocks(p *partRow) []transcript.Block {
	switch p.Type {
	case "text":
		// Context opencode inserted on the operator's behalf is not something
		// anyone said. See partRow.Synthetic for why this rule is not
		// ground-truthed.
		if p.Synthetic || p.Ignored {
			return nil
		}
		return textBlock("text", p.Text)

	case "reasoning":
		// Readable here, unlike Codex, whose reasoning lives in
		// encrypted_content and arrives as an empty summary on 53/53 items.
		return textBlock("thinking", p.Text)

	case "tool":
		return toolBlocks(p)

	case "step-start", "step-finish":
		// Bookkeeping, not conversation: opencode emits exactly one of each
		// per assistant message (124 and 122 against 124 messages), so
		// admitting them would double the entry count with rows nobody said.
		// Enumerated rather than left to the default so a reader can see they
		// were considered.
		return nil
	}
	// file, agent, patch and snapshot exist in opencode's schema and appear
	// nowhere in the corpus (0/672). Mapping a shape nobody has seen is how a
	// reader invents content; they are dropped until there is something real
	// to map.
	return nil
}

// textBlock is the shared exit for the part types that contribute prose. Both
// drop the part when there is nothing left to say — an empty block carries no
// information and still takes a slot in `--last N`.
func textBlock(kind, text string) []transcript.Block {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []transcript.Block{{Kind: kind, Text: text}}
}

// toolBlocks renders a tool part as the call and, once it has one, its result.
//
// pending and running produce no result block. Both are states a live read can
// catch — an export taken mid-turn returns the conversation as far as it has
// committed, measured 5/5 against a running session — and a result block with
// no output would read as a tool that returned nothing.
func toolBlocks(p *partRow) []transcript.Block {
	use := transcript.Block{Kind: "tool_use", ToolName: p.Tool, ToolUseID: p.CallID}
	if len(p.State.Input) > 0 {
		use.Input = p.State.Input
	}
	res := transcript.Block{Kind: "tool_result", ToolUseID: p.CallID}
	switch p.State.Status {
	case "completed":
		res.Output = p.State.Output
		res.IsError = nonZeroExit(p)
	case "error":
		// The error message, not state.output. Whether opencode ever fills
		// both cannot be told from one failed call in a 194-call corpus, so
		// this takes the field the error shape declares rather than
		// concatenating two things that may be the same text.
		res.Output = p.State.Error
		res.IsError = true
	default:
		// pending, running, or no state at all: nothing has been returned yet.
		return []transcript.Block{use}
	}
	return []transcript.Block{use, res}
}

// nonZeroExit reports whether a completed tool call actually failed.
//
// state.status is not the answer, and believing it was is what made
// `--errors-only` wrong here for a while. In the 194-call corpus every one of
// the 5 bash calls that exited non-zero is recorded `completed`; the single
// `error` status belongs to a `read` that could not open a file. Reading the
// status alone, `--errors-only` returned none of the five — the same trap this
// project documents for Codex, reintroduced while the docs claimed the
// opposite.
//
// metadata.exit is the real exit status, and Codex records nothing like it.
// Only bash carries it (32 of 33 as a number, 1 as null); read, grep, glob,
// task, skill, websearch and write have no exit field at all — 0 of 161,
// which is every tool call in the corpus that is not bash.
//
// So false here means one of two different things, and callers must not read
// it as the second: either the tool reported an exit status and it was zero, or
// the tool reports no exit status and jind-ai cannot tell. `--errors-only` is
// therefore trustworthy for bash and blind for everything else, which is
// stated in docs/gotchas.md rather than papered over.
func nonZeroExit(p *partRow) bool {
	// An absent or null exit parses as an error, which lands on "cannot tell".
	n, err := p.State.Metadata.Exit.Int64()
	return err == nil && n != 0
}

// turnFailureEntry converts a message that ended in an error into a standalone
// system entry.
//
// This exists for the same reason as the Codex reader's task_complete entry: a
// turn that died leaves the assistant having said nothing, which from the parts
// alone is indistinguishable from a turn still being thought about. Without it
// an orchestrator waits on a session that is never going to answer.
//
// Both fields opencode records are kept and neither is invented — Name is the
// classifier a caller can match on, Data.Message the sentence a human reads.
func turnFailureEntry(m *messageRow, ts string) (transcript.Entry, bool) {
	if m.Error == nil {
		return transcript.Entry{}, false
	}
	text := m.Error.Name
	if msg := m.Error.Data.Message; msg != "" {
		if text != "" {
			text += ": "
		}
		text += msg
	}
	if text == "" {
		return transcript.Entry{}, false
	}
	return transcript.Entry{
		Type:      "system",
		Timestamp: ts,
		Blocks:    []transcript.Block{{Kind: "text", Text: text}},
	}, true
}

// entryBuilder groups consecutive blocks into entries at role boundaries and
// assigns each one a timestamp.
//
// opencode splits one assistant turn across several messages — one per step,
// up to 14 in a row in the corpus — so copying messages to entries 1:1 would
// make `--last 5` mean "five steps" here and "five messages" on Claude Code.
// Grouping restores the shape the other readers already have.
type entryBuilder struct {
	out []transcript.Entry
	cur *transcript.Entry
	// credited names the message indices already counted into some entry's
	// Usage. Indices rather than ids because one message's blocks can land in
	// more than one entry — a message issuing two tool calls produces
	// assistant, user, assistant, user — and billing it once per assistant run
	// reported 200 tokens where 100 were spent. An index cannot collide the
	// way a missing id can.
	credited map[int]bool
	// lastMS is the highest timestamp handed out so far, in epoch
	// milliseconds. See stamp.
	lastMS int64
}

// stamp renders an opencode timestamp, never going backwards.
//
// The value is opencode's own, except where opencode's own clock disagrees with
// the order of the conversation, in which case the previous entry's value is
// carried forward. That is not tidying: parallel tool calls mean a call issued
// first can finish last, so a truthful sequence of real times is genuinely
// out of order — measured across 34 real sessions, 13 of 620 blocks need the
// correction and the largest is 204s.
//
// Carrying forward rather than nudging by a millisecond keeps every value a
// time opencode actually recorded. The cost is that two entries can then share
// a timestamp — 12 of 478 in the corpus — and `--since` is an exclusive bound,
// so a caller polling across one of those boundaries can lose the second. That
// is the same hazard Claude Code and Codex carry, and it is written down in
// docs/gotchas.md rather than hidden behind an invented number.
func (b *entryBuilder) stamp(ms *int64) string {
	return renderStamp(b.advance(ms))
}

// advance moves the clock to ms when that is later than where it already is,
// and returns where it now stands.
func (b *entryBuilder) advance(ms *int64) int64 {
	if ms != nil && *ms > b.lastMS {
		b.lastMS = *ms
	}
	return b.lastMS
}

// renderStamp writes an epoch-millisecond value in the fixed-width ISO 8601
// form every reader in jind-ai produces, so `since` can compare it as a string.
// Zero means the document carried no time at all, which is not a moment.
func renderStamp(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// addPart appends the blocks a part contributes, under the role of the message
// it belongs to, dropping whatever falls at or below the since bound.
func (b *entryBuilder) addPart(msg *messageRow, msgIndex int, p *partRow, since string) {
	// opencode's schema always carries a role; a message without one means the
	// document is not what it claims to be, and filing its parts under Type ""
	// would put them in the result as a speaker that does not exist.
	if msg.Role == "" {
		return
	}
	blocks := partBlocks(p)
	// A part that contributes no block does not move the clock. It cannot need
	// to: the clock only ever moves forward, so the blocks that do get stamped
	// are non-decreasing whether or not it saw this one. Advancing anyway would
	// be worse than pointless — an injected part carries a time, and letting it
	// through would stamp the next real entry with a moment from content the
	// reader just decided nobody said.
	if len(blocks) == 0 {
		return
	}
	for _, blk := range blocks {
		// A tool result is the operator's side of the exchange, the same way
		// Claude Code files it and the same way the Codex reader maps
		// function_call_output. opencode keeps the call and its output in one
		// part, so this split is jind-ai's doing — but without it a whole
		// assistant turn collapses into a single entry (measured: 478 entries
		// become 92 across 34 sessions), and `--last N` stops meaning
		// anything comparable to what it means on the other two kinds.
		role := msg.Role
		at := partStart(p, msg)
		if blk.Kind == "tool_result" {
			role = "user"
			at = partEnd(p, msg)
		}
		ts := b.stamp(at)
		if !transcript.Newer(ts, since) {
			continue
		}
		b.add(role, ts, blk)
		if role == "assistant" {
			b.credit(msgIndex, msg)
		}
	}
}

// partStart is the moment a part began, falling back to its message's creation
// time for the part types that carry no clock of their own — a user's text
// part, and the step rows.
func partStart(p *partRow, msg *messageRow) *int64 {
	if p.Time.Start != nil {
		return p.Time.Start
	}
	if p.State.Time.Start != nil {
		return p.State.Time.Start
	}
	return msg.Time.Created
}

// partEnd is the moment a tool call returned, falling back to its start.
func partEnd(p *partRow, msg *messageRow) *int64 {
	if p.State.Time.End != nil {
		return p.State.Time.End
	}
	return partStart(p, msg)
}

// add appends blk to the open entry when the role still matches, and starts a
// new one when it does not.
//
// The entry's Timestamp tracks its LAST block. That is what makes incremental
// reads exact: a caller passing the timestamp of the last entry it saw as
// `since` needs every block already folded into that entry to compare as "at
// or before" it. Stamping the first block instead would leave the group's
// later blocks above the bound, and they would come back as a partial
// duplicate of an entry the caller already has.
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

// credit folds a message's token counts into the open entry, once per message
// across the whole result.
//
// Consecutive assistant messages are the steps of one turn, so the entry that
// holds them reports their sum — the cost of the turn, which is the question a
// caller reading usage is asking. A message split across several entries is
// billed to the first of them. opencode's reasoning token count has no field in
// transcript.Usage and is dropped rather than folded into another number.
func (b *entryBuilder) credit(msgIndex int, msg *messageRow) {
	if b.cur == nil || msg.Tokens == nil {
		return
	}
	if b.credited[msgIndex] { // reading a nil map is legal and returns false
		return
	}
	if b.credited == nil {
		b.credited = make(map[int]bool)
	}
	b.credited[msgIndex] = true
	if b.cur.Usage == nil {
		b.cur.Usage = &transcript.Usage{}
	}
	b.cur.Usage.InputTokens += msg.Tokens.Input
	b.cur.Usage.OutputTokens += msg.Tokens.Output
	b.cur.Usage.CacheReadTokens += msg.Tokens.Cache.Read
	b.cur.Usage.CacheCreationTokens += msg.Tokens.Cache.Write
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
