// Package transcript provides reading functionality for Claude Code transcript files.
package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// maxTranscriptLineBytes bounds a single JSONL line. Claude Code writes a
// whole tool_result payload on one line, so the ceiling has to be generous.
// Every reader in this package shares it: the message readers used to carry
// their own 1 MiB limit, which stopped bufio.Scanner mid-file on real
// transcripts, and — because none of them checked scanner.Err() — the
// truncation was indistinguishable from the end of the conversation.
const maxTranscriptLineBytes = 16 * 1024 * 1024

// ErrNoTranscript reports that no JSONL file could be located for a session.
// It is deliberately distinct from a transcript that exists but holds no
// readable messages: the first means the caller is looking in the wrong
// place (or too early), while the second is a normal state every session
// passes through before it says anything. Collapsing the two into an empty
// result is what let `session output --last N` stay silent about a missing
// transcript.
var ErrNoTranscript = errors.New("no transcript file for session")

// Message represents a message from the transcript
type Message struct {
	Type      string // "user" or "assistant"
	Content   string // text content
	Timestamp string // ISO8601 timestamp
}

// LastMessages holds the last user and assistant messages
type LastMessages struct {
	User      *Message
	Assistant *Message
}

// Entry is a structured representation of a single transcript line.
// Unlike Message (display-oriented), Entry preserves all block kinds
// (text, thinking, tool_use, tool_result) and usage info, suitable for
// programmatic orchestration.
type Entry struct {
	// Type is "user", "assistant" or "system". A reader reports a turn that
	// died — an aborted message, a usage limit — as a "system" entry, because
	// from the conversation alone a turn that will never answer looks exactly
	// like one still being thought about. Orchestrators are told to look for
	// those, so a reader that has such a signal must not drop it.
	Type      string  `json:"type"`
	Timestamp string  `json:"timestamp,omitempty"` // ISO8601
	Blocks    []Block `json:"blocks,omitempty"`
	Usage     *Usage  `json:"usage,omitempty"` // assistant only

	// Injected reports that the agent produced this entry on the operator's
	// behalf rather than the operator writing it — an environment block, the body
	// of an invoked skill, an interruption notice filed in the user's voice.
	// Sidechain reports that the entry belongs to a subagent's own thread rather
	// than the main conversation.
	//
	// Both carry a reader's *conclusion*, not the evidence it reached that
	// conclusion from, and that distinction is the whole point. Claude Code marks
	// injections with isMeta and with the absence of a promptSource stamp; Codex
	// writes them under a `developer` role and behind recognisable prefixes. A
	// shared field holding "promptSource was missing" would be a Claude Code fact
	// wearing a neutral name, and applying it to Codex — which stamps nothing —
	// would silently discard every prompt the operator typed.
	//
	// A reader that filters injections while reading leaves both false.
	Injected  bool `json:"injected,omitempty"`
	Sidechain bool `json:"sidechain,omitempty"`
}

// Block is a single content block within a transcript entry.
type Block struct {
	Kind      string          `json:"kind"`                  // "text" | "thinking" | "tool_use" | "tool_result"
	Text      string          `json:"text,omitempty"`        // text/thinking
	ToolName  string          `json:"tool_name,omitempty"`   // tool_use only (tool_result carries only id)
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_use id, or tool_result's referenced id
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use input (preserved structure)
	Output    string          `json:"output,omitempty"`      // tool_result content (string-ified)
	// IsError flags a tool_result the reader concluded had failed. False means
	// "no failure was found", NOT "the call succeeded": what a reader can see
	// depends on the format, and Codex records no failure signal at all. Each
	// adapter documents its own recall with a measurement; do not read a false
	// here as evidence.
	IsError bool `json:"is_error,omitempty"`
}

// Usage captures Anthropic API usage info from an assistant message.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// Reader reads Claude Code transcript files
type Reader struct {
	claudeDir string
}

// NewReader creates a new transcript reader
func NewReader() *Reader {
	home, _ := os.UserHomeDir()
	return &Reader{
		claudeDir: filepath.Join(home, ".claude"),
	}
}

// GetLastMessage returns the last user or assistant message from the transcript
// workDir: the working directory of the session (may be empty; a glob fallback locates the JSONL by sessionID)
// sessionID: the Claude Code session ID (UUID format)
// Returns ErrNoTranscript when no transcript file exists (yet), and (nil, nil)
// when one exists but carries no plain-text message.
func (r *Reader) GetLastMessage(workDir, sessionID string) (*Message, error) {
	path, err := r.findTranscriptPath(workDir, sessionID)
	if err != nil {
		return nil, err
	}
	return r.readLastMessage(path)
}

// GetLastMessages returns the last user and assistant messages from the transcript
// workDir: the working directory of the session (may be empty; a glob fallback locates the JSONL by sessionID)
// sessionID: the Claude Code session ID (UUID format)
// Returns ErrNoTranscript when no transcript file exists (yet), and (nil, nil)
// when one exists but carries no plain-text message.
func (r *Reader) GetLastMessages(workDir, sessionID string) (*LastMessages, error) {
	path, err := r.findTranscriptPath(workDir, sessionID)
	if err != nil {
		return nil, err
	}
	return r.readLastMessages(path)
}

// GetConversation returns the last N exchanges from the transcript, where an
// exchange is a user prompt plus the assistant's reply to it (see
// lastExchanges). workDir may be empty: a glob fallback locates the JSONL by
// sessionID. lastN must be at least 1 — "no exchanges asked for" is rejected
// rather than answered with the whole conversation, which is the direction of
// failure that hurts, since callers pipe this into another agent's context.
// Returns ErrNoTranscript when no transcript file exists yet, and an empty
// slice when one exists but carries no plain-text message.
func (r *Reader) GetConversation(workDir, sessionID string, lastN int) ([]Message, error) {
	if lastN < 1 {
		return nil, errors.New("lastN must be >= 1")
	}
	path, err := r.findTranscriptPath(workDir, sessionID)
	if err != nil {
		return nil, err
	}
	return r.readConversation(path, lastN)
}

// readConversation reads the transcript and returns the user/assistant
// messages making up the last lastN exchanges.
func (r *Reader) readConversation(filePath string, lastN int) ([]Message, error) {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var allMessages []Message
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxTranscriptLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry transcriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		if !isConversationEntry(&entry) {
			continue
		}

		content := extractFullContent(&entry)
		if content == "" {
			continue
		}

		allMessages = append(allMessages, Message{
			Type:      entry.Type,
			Content:   content,
			Timestamp: entry.Timestamp,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lastExchanges(allMessages, lastN), nil
}

// lastExchanges returns the tail of msgs covering the last n exchanges.
//
// An exchange starts at a user message that either opens the transcript or
// follows an assistant reply, and runs until the next such message — so one
// exchange is a prompt plus everything the agent said in response, however many
// entries that took. A trailing run of user messages with no reply yet counts
// as an exchange of its own, which is what makes `--last 1` show the question
// the agent is still working on.
//
// Fewer than n boundaries — or none, as in a transcript that is all assistant
// text — returns everything rather than guessing at a cut point.
func lastExchanges(msgs []Message, n int) []Message {
	seen := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type != "user" {
			continue
		}
		if i > 0 && msgs[i-1].Type == "user" {
			continue // still inside one user run, not a new exchange
		}
		seen++
		if seen == n {
			return msgs[i:]
		}
	}
	return msgs
}

// readLastMessages reads the transcript file and returns the last user and
// assistant messages, as the shared view over Entry sees them.
//
// It delegates to LastMessagesFrom rather than walking the file itself, so
// "what counts as the operator's words" — the load-bearing decision in this
// package — has one implementation instead of two. Verified equal before the
// collapse: over 246 real Claude Code transcripts the two agreed on user
// content, assistant content, timestamps and nil-ness with no exceptions.
//
// The cost is that this materializes every entry where it used to stream, the
// same trade Manager.AttachLastMessages already makes on the list path.
func (r *Reader) readLastMessages(filePath string) (*LastMessages, error) {
	entries, err := readEntries(filePath, "")
	if err != nil {
		return nil, err
	}
	return LastMessagesFrom(entries), nil
}

// encodePathForClaude converts a path to Claude Code's directory name format
// Example: /Users/foo/bar → -Users-foo-bar
func encodePathForClaude(path string) string {
	// Replace / with -
	encoded := strings.ReplaceAll(path, "/", "-")
	// The path already starts with /, so after replacement it starts with -
	return encoded
}

// getTranscriptPath returns the full path to the transcript file
func (r *Reader) getTranscriptPath(workDir, sessionID string) string {
	encodedPath := encodePathForClaude(workDir)
	return filepath.Join(r.claudeDir, "projects", encodedPath, sessionID+".jsonl")
}

// transcriptEntry represents a single entry in the JSONL file
type transcriptEntry struct {
	Type      string    `json:"type"`
	Message   msgObject `json:"message"`
	Timestamp string    `json:"timestamp"`
	// PromptSource records how Claude Code received a user entry — "typed"
	// from the terminal, "queued" for one submitted while the agent was busy,
	// "suggestion_accepted", "system" for a notice Claude Code raises and
	// answers itself, "sdk" from a stream-json caller. Its absence on a user
	// entry means nobody supplied it — see conversationTextBlocks and
	// advancesTurn.
	PromptSource string `json:"promptSource"`
	IsSidechain  bool   `json:"isSidechain"`
	IsMeta       bool   `json:"isMeta"`
	// InterruptedMessageID names the assistant message the operator cut short.
	// Its presence is the whole signal: the turn that message belonged to is
	// over rather than in flight — see TurnState.
	//
	// Claude Code writes it on the marker entry it files in the user's voice,
	// never on the assistant message the field names (33/33 of the entries
	// carrying it in a 248-transcript survey were user entries), which is why
	// TurnState only reaches it for entries that are not turns of their own.
	InterruptedMessageID string `json:"interruptedMessageId"`
}

// msgObject represents the message field which can have different structures
type msgObject struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // can be string or []contentBlock
	Usage   *Usage `json:"usage,omitempty"`
}

// readLastMessage reads the transcript file and returns the last user/assistant message
func (r *Reader) readLastMessage(filePath string) (*Message, error) {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var lastMessage *Message
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxTranscriptLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry transcriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		// Only process user and assistant messages
		if !isConversationEntry(&entry) {
			continue
		}

		content := extractContent(&entry)
		if content == "" {
			continue
		}

		lastMessage = &Message{
			Type:      entry.Type,
			Content:   content,
			Timestamp: entry.Timestamp,
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lastMessage, nil
}

// isConversationEntry reports whether an entry belongs to the conversation
// between the user and the agent, as opposed to the machinery around it.
//
// Only user and assistant entries qualify. Sidechain entries are a subagent's
// own turns — treating them as the main thread makes a subagent's prompt look
// like the user's. Meta entries are context Claude Code injects on the user's
// behalf: skill bodies, environment reminders, command caveats. They are stored
// as user messages but nobody said them, and a single injected skill body runs
// to thousands of lines.
//
// The test is made of nothing but the flags the transcript itself sets, because
// TurnState calls this too: a session's live status is re-derived from which
// role spoke last, and that classification must not move because of what an
// entry happens to say. Exclusions that depend on the content live in
// conversationTextBlocks instead.
//
// The flags alone do not separate a prompt from the entries Claude Code writes
// in the user's voice; that second test is advancesTurn, which TurnState
// applies on top of this one.
func isConversationEntry(entry *transcriptEntry) bool {
	if entry.Type != "user" && entry.Type != "assistant" {
		return false
	}
	return !entry.IsSidechain && !entry.IsMeta
}

// collectTextBlocks pulls the text out of an entry's content. Content is a bare
// string on some entries and an array of blocks on others, and either shape can
// appear for either role, so this stays role-agnostic.
//
// Blocks other than text are skipped on purpose. A tool_result rides along
// inside a user entry but is what a tool wrote back, not something the user
// said, and letting them through buries every real message under tool output.
func collectTextBlocks(content any) []string {
	switch c := content.(type) {
	case string:
		if strings.TrimSpace(c) == "" {
			return nil
		}
		return []string{c}
	case []any:
		var texts []string
		for _, item := range c {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := block["type"].(string); kind != "text" {
				continue
			}
			if text, ok := block["text"].(string); ok && text != "" {
				texts = append(texts, text)
			}
		}
		return texts
	}
	return nil
}

// conversationTextBlocks returns the text of an entry that should be read as
// something its author actually said.
//
// Claude Code stamps a promptSource on every user entry it was handed as input:
// "typed" for a terminal prompt, "sdk" for one fed to `claude -p --input-format
// stream-json`. A user entry with no promptSource was supplied by nobody, and
// when its content is an array of nothing but text it is the agent writing in
// the user's voice — on real transcripts, always an interruption notice.
//
// The promptSource half of the test is what keeps a genuine prompt out of the
// rule: the shape alone does not separate them, because stream-json input
// arrives as a text-only array too. On a Claude Code old enough not to write
// promptSource the field is absent everywhere and this falls back to the
// shape-only behaviour it replaced.
//
// Assistant entries are the opposite case — a text-only array is their normal
// shape — so the rule is deliberately scoped to one role.
func conversationTextBlocks(entry *transcriptEntry) []string {
	if isUnstampedUser(entry) && isTextOnlyArray(entry.Message.Content) {
		return nil
	}
	return collectTextBlocks(entry.Message.Content)
}

// isUnstampedUser reports whether an entry is a user entry Claude Code was not
// handed as input: no promptSource stamp, so nobody supplied it.
//
// This is the Claude-Code-specific half of two separate rules —
// conversationTextBlocks and advancesTurn. It lives in one function so the
// stamp is written down once and a change to how Claude Code marks its input
// lands on both at the same time.
func isUnstampedUser(entry *transcriptEntry) bool {
	return entry.Type == "user" && entry.PromptSource == ""
}

// advancesTurn reports whether an entry moves the conversation forward.
//
// An assistant entry always does — that is the agent speaking. A user entry
// does only when it is input the agent was handed and owes a reply to, because
// Claude Code writes plenty into the transcript in the user's voice that nobody
// submitted: the stdout of a local slash command, the notice raised when a
// command is invoked, the echo of a `!` bash line. None of those is a request
// and none will be answered, so a transcript ending on one belongs to a session
// that is idle, not one whose reply is still coming.
//
// The user test is two positive signals rather than a list of those shapes,
// because the list is open-ended — a survey of real transcripts turned up six
// distinct ones and nothing stops Claude Code from adding a seventh:
//
//   - promptSource is the stamp Claude Code puts on everything it was handed
//     as input. Across ~21k user entries, every entry an operator actually
//     submitted carried one and no injected entry did. The few stamp-less
//     entries that were not one of the injected shapes were /compact
//     bookkeeping — also not a request.
//   - a tool_result block is the agent's own turn continuing: a tool wrote
//     back and the reply is still owed.
//
// Deliberately structural. Which words an entry happens to carry must not move
// a status verdict — see TurnState.
//
// Entry.Injected answers a neighbouring question and disagrees on these shapes.
// It derives provenance from conversationTextBlocks, whose rule only fires for
// a text-only array, so an injected entry that arrives as a bare string — a
// slash command's stdout, a `!` bash echo — is reported as operator-written.
// That is a gap in isInjected, not a decision.
//
// The rule leans entirely on Claude Code stamping what it was handed. A build
// that stopped doing so would make every session read as idle rather than
// thinking; no fallback is attempted, because "no stamps anywhere" cannot be
// told apart from a session driven only by slash commands and tool calls, which
// 39 of 235 surveyed transcripts turned out to be.
func advancesTurn(entry *transcriptEntry) bool {
	if !isUnstampedUser(entry) {
		return true
	}
	return hasBlockKind(entry.Message.Content, "tool_result")
}

// decidesTurn reports whether an entry settles what state the turn is in: input
// the agent owes a reply to, or an interruption that ended one. Everything else
// — the entries Claude Code writes in the user's voice — is passed over, so
// whatever decided last goes on deciding.
//
// Keeping the interruption marker rather than acting on it is what makes two
// rules fall out of the scan instead of needing to be enforced: a slash command
// typed after an interruption does not reopen the turn, and TurnState's closing
// test on advancesTurn settles which of the two a single entry carrying both
// signals counts as.
func decidesTurn(entry *transcriptEntry) bool {
	return advancesTurn(entry) || entry.InterruptedMessageID != ""
}

// hasBlockKind reports whether content is a block array carrying at least one
// block of the given kind. String content is not an array and never qualifies.
func hasBlockKind(content any, kind string) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if k, _ := block["type"].(string); k == kind {
			return true
		}
	}
	return false
}

// isTextOnlyArray reports whether content is a block array carrying no block
// kind other than "text". String content is not an array and never qualifies.
func isTextOnlyArray(content any) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if kind, _ := block["type"].(string); kind != "text" {
			return false
		}
	}
	return true
}

// extractContent extracts an entry's text collapsed onto a single line, for
// display in list rows and detail panes.
func extractContent(entry *transcriptEntry) string {
	texts := conversationTextBlocks(entry)
	if len(texts) == 0 {
		return ""
	}
	return cleanContent(strings.Join(texts, " "))
}

// extractFullContent extracts an entry's text with its line structure intact,
// for callers that render the message as the agent wrote it.
func extractFullContent(entry *transcriptEntry) string {
	texts := conversationTextBlocks(entry)
	if len(texts) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

// cleanContent cleans up the content string for display
func cleanContent(s string) string {
	// Remove newlines and extra whitespace
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", " ")

	// Collapse multiple spaces
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}

	return strings.TrimSpace(s)
}

// TruncateMessage truncates a message from the beginning to the specified length
func TruncateMessage(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// TruncateMessageFromEnd truncates a message from the end, keeping the last maxLen characters
// This is useful for assistant messages where the important content (like questions) is often at the end
func TruncateMessageFromEnd(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[len(s)-maxLen:]
	}
	return "..." + s[len(s)-maxLen+3:]
}

// findTranscriptPath locates the JSONL file for a given workDir/sessionID.
// It tries the canonical path first, then falls back to a glob over all
// project directories (sessionID is unique). Returns ErrNoTranscript if not found.
func (r *Reader) findTranscriptPath(workDir, sessionID string) (string, error) {
	if sessionID == "" {
		return "", ErrNoTranscript
	}
	if workDir != "" {
		p := r.getTranscriptPath(workDir, sessionID)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	matches, _ := filepath.Glob(filepath.Join(r.claudeDir, "projects", "*", sessionID+".jsonl"))
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", ErrNoTranscript
}

// Newer reports whether an entry stamped ts should be returned for a read
// bounded by since. It is the whole of the `--since` rule, in one place:
// exclusive, compared as a string, and permissive about a missing stamp — an
// entry with no timestamp would compare as "at or before" every bound and
// disappear the moment a caller read incrementally.
//
// Exported because it is protocol rather than implementation: every
// session.TranscriptSource promises these semantics, so a fix to the cursor has
// to land for all of them at once, or `--since` starts meaning different things
// per agent kind.
func Newer(ts, since string) bool {
	return since == "" || ts == "" || ts > since
}

// ReadEntries returns transcript entries with Timestamp strictly greater than `since`.
// An entry whose Timestamp equals `since` is excluded — pass the timestamp of the last
// entry already seen to receive only what came after it (no duplicates). String
// comparison is used (Claude Code emits lexicographically sortable RFC3339 timestamps).
// If `since` is empty, returns all entries. workDir may be empty: a glob fallback locates
// the JSONL by sessionID. Returns (nil, nil) if no transcript file exists yet.
func (r *Reader) ReadEntries(workDir, sessionID, since string) ([]Entry, error) {
	path, err := r.findTranscriptPath(workDir, sessionID)
	if err != nil {
		// `session result` treats "not started yet" as an empty result rather
		// than a failure; only genuine read errors propagate.
		if errors.Is(err, ErrNoTranscript) {
			return nil, nil
		}
		return nil, err
	}
	return readEntries(path, since)
}

// isInjected reports whether Claude Code wrote this entry in the user's voice
// rather than the operator supplying it. blocks is what parseBlocks already
// produced for raw, so the text is read back off it rather than collected twice.
//
// Two shapes qualify, and they are the same two the message readers already
// exclude — this is that decision named, so a view working from Entry can reach
// it without re-deriving it from fields Entry does not carry. isMeta is the
// explicit marker: skill bodies, environment reminders, command caveats. The
// second is conversationTextBlocks' rule restated: a user entry that carries
// text but that conversationTextBlocks refuses has no promptSource stamp and
// nothing but text in it, which on real transcripts is always the agent filing
// a notice on the user's behalf. The stamp test stays in isUnstampedUser.
//
// Why the conclusion is stored rather than promptSource itself: see the
// Injected field on Entry.
func isInjected(raw *transcriptEntry, blocks []Block) bool {
	if raw.IsMeta {
		return true
	}
	if len(conversationTextBlocks(raw)) > 0 {
		return false
	}
	// Reaching here means conversationTextBlocks refused the entry, which it
	// only does for a text-only content array — so parseBlocks turned that
	// same array into text blocks and asking it is asking the same question.
	for _, b := range blocks {
		if b.Kind == "text" && b.Text != "" {
			return true
		}
	}
	return false
}

// LastToolUse returns the last tool_use block. If toolName is non-empty,
// only blocks matching that tool name are considered. Returns (nil, nil)
// if no matching block is found.
func (r *Reader) LastToolUse(workDir, sessionID, toolName string) (*Block, error) {
	entries, err := r.ReadEntries(workDir, sessionID, "")
	if err != nil {
		return nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		for j := len(entries[i].Blocks) - 1; j >= 0; j-- {
			b := entries[i].Blocks[j]
			if b.Kind != "tool_use" {
				continue
			}
			if toolName != "" && b.ToolName != toolName {
				continue
			}
			return &b, nil
		}
	}
	return nil, nil
}

// LastToolResult returns the last tool_result block. If toolName is non-empty,
// only results corresponding to a tool_use of that name are considered (matched
// by tool_use_id within the same scan). If onlyErrors is true, only blocks with
// IsError=true are considered. Returns (nil, nil) if not found.
func (r *Reader) LastToolResult(workDir, sessionID, toolName string, onlyErrors bool) (*Block, error) {
	entries, err := r.ReadEntries(workDir, sessionID, "")
	if err != nil {
		return nil, err
	}
	// Build tool_use_id -> tool_name map from a single forward pass for name filtering.
	useNameByID := map[string]string{}
	if toolName != "" {
		for _, e := range entries {
			for _, b := range e.Blocks {
				if b.Kind == "tool_use" && b.ToolUseID != "" {
					useNameByID[b.ToolUseID] = b.ToolName
				}
			}
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		for j := len(entries[i].Blocks) - 1; j >= 0; j-- {
			b := entries[i].Blocks[j]
			if b.Kind != "tool_result" {
				continue
			}
			if onlyErrors && !b.IsError {
				continue
			}
			if toolName != "" {
				if useNameByID[b.ToolUseID] != toolName {
					continue
				}
			}
			return &b, nil
		}
	}
	return nil, nil
}

// TurnState classifies the most recent conversational turn in a transcript.
// It is used to re-derive a session's live status after a daemon restart, when
// the persisted (hook-driven) status may be stale.
type TurnState int

const (
	// TurnStateUnknown means the turn could not be classified: no transcript
	// file, an empty file, no user/assistant entries, or a read failure.
	TurnStateUnknown TurnState = iota
	// TurnStateComplete means the turn is over. Either the last entry is an
	// assistant message with no tool_use block — the API call ended without
	// requesting a tool — or the operator interrupted the turn, which ends it
	// just as decisively. Heuristic: stop_reason is not parsed from the
	// transcript, but "assistant ending without tool_use" is equivalent to a
	// stop/end_turn for status purposes.
	TurnStateComplete
	// TurnStatePendingTool means the last user/assistant entry is an assistant
	// message containing a tool_use block. A tool is executing or awaiting
	// permission; the two are indistinguishable from the transcript alone.
	TurnStatePendingTool
	// TurnStateUserPending means the assistant response is still being
	// generated: the last user/assistant entry is a user message (a freshly
	// submitted prompt, or a written tool_result), or an assistant entry
	// whose blocks are all "thinking" (a reply cut off mid-thought).
	TurnStateUserPending
)

// TurnState returns the classification of the last conversational turn.
//
// Two filters decide which entries count. isConversationEntry's flag test drops
// what is outside the main conversation: system/summary types, sidechain
// entries (a subagent's turns would otherwise read as the main thread
// finishing), and meta messages. On top of it, a user entry counts only if it
// is input the agent owes a reply to (advancesTurn) — the rest is Claude Code
// writing in the user's voice, and reading one of those as a prompt reports a
// finished session as still working. See docs/session-lifecycle.md for what
// that costs the caller.
//
// Both filters are structural: which words an entry happens to carry must never
// move a status verdict. The content-based exclusion the message readers apply
// (conversationTextBlocks) is deliberately not reused, and neither test
// contains the other — a tool_result-only user entry is nothing to show but is
// very much a turn in flight, while a slash command's stdout arrives as a bare
// string, so the readers keep it while this drops it. Only the stamp itself is
// shared, in isUnstampedUser.
//
// An interruption ends the turn rather than being passed over: whatever the
// operator cut short is not in flight, so the marker outranks the tool_result
// or half-written reply beneath it.
//
// Any failure (missing file, empty transcript, read error) folds into
// TurnStateUnknown, so callers can treat it as "cannot determine" without guard
// code.
func (r *Reader) TurnState(workDir, sessionID string) TurnState {
	path, err := r.findTranscriptPath(workDir, sessionID)
	if err != nil {
		return TurnStateUnknown
	}
	file, err := os.Open(path)
	if err != nil {
		return TurnStateUnknown
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxTranscriptLineBytes)

	var last *transcriptEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw transcriptEntry
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		if !isConversationEntry(&raw) || !decidesTurn(&raw) {
			continue
		}
		last = &raw
	}
	if scanner.Err() != nil || last == nil {
		return TurnStateUnknown
	}
	// The only kept entry that is not input is an interruption marker, so
	// reaching here means the operator cut a turn short and nothing followed:
	// what was interrupted is not in flight. Asking advancesTurn rather than
	// the field is what lets an entry carrying both count as input — reading a
	// prompt as an interruption would report a working agent as idle.
	if !advancesTurn(last) {
		return TurnStateComplete
	}

	if last.Type == "user" {
		return TurnStateUserPending
	}
	hasText := false
	if s, ok := last.Message.Content.(string); ok && s != "" {
		hasText = true
	}
	if blocks, ok := last.Message.Content.([]any); ok {
		for _, item := range blocks {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "tool_use":
				return TurnStatePendingTool
			case "text":
				hasText = true
			}
		}
	}
	if hasText {
		return TurnStateComplete
	}
	// Thinking-only (or empty) assistant entry: the turn is still in
	// flight — never report it as complete.
	return TurnStateUserPending
}

// ReadAITitle returns the AI-generated session title Claude Code writes to the
// transcript when it names the conversation from context (the same value CC
// surfaces as "Session name" in `/status`). Each line of the JSONL is checked
// for `{"type":"ai-title","aiTitle":"…"}` and the most recent non-empty value
// wins — CC may re-title the session later in the conversation.
//
// Returns ("", false) on any miss and never returns an error — every failure
// mode is a silent fallback by design, so the Layer C-name enhancer can call
// this on every hook without guard code.
func (r *Reader) ReadAITitle(workDir, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	path, err := r.findTranscriptPath(workDir, sessionID)
	if err != nil {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	var latest string
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxTranscriptLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type    string `json:"type"`
			AITitle string `json:"aiTitle"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		if probe.Type != "ai-title" || probe.AITitle == "" {
			continue
		}
		latest = probe.AITitle
	}
	if latest == "" {
		return "", false
	}
	return latest, true
}

// readEntries reads the JSONL file and returns parsed Entry values, optionally
// filtered by Timestamp > since.
func readEntries(filePath, since string) ([]Entry, error) {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var entries []Entry
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxTranscriptLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw transcriptEntry
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		if !Newer(raw.Timestamp, since) {
			continue
		}
		blocks := parseBlocks(&raw)
		entries = append(entries, Entry{
			Type:      raw.Type,
			Timestamp: raw.Timestamp,
			Blocks:    blocks,
			Usage:     raw.Message.Usage,
			// The flags are set here rather than the entry being dropped:
			// `session result` has always returned every line, and narrowing
			// that would change what every existing session reports. Marking
			// them lets a view exclude what it must without the raw stream
			// losing anything.
			Injected:  isInjected(&raw, blocks),
			Sidechain: raw.IsSidechain,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// parseBlocks normalizes a transcriptEntry's content into Blocks. Handles:
//   - user content as plain string -> single text block
//   - user content array containing text and tool_result blocks
//   - assistant content array containing text, thinking, and tool_use blocks
//
// Unknown block kinds are preserved with their declared Kind but otherwise empty.
func parseBlocks(entry *transcriptEntry) []Block {
	if entry.Message.Content == nil {
		return nil
	}
	if str, ok := entry.Message.Content.(string); ok {
		s := strings.TrimSpace(str)
		if s == "" {
			return nil
		}
		return []Block{{Kind: "text", Text: s}}
	}
	arr, ok := entry.Message.Content.([]any)
	if !ok {
		return nil
	}
	var blocks []Block
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["type"].(string)
		switch kind {
		case "text":
			text, _ := m["text"].(string)
			blocks = append(blocks, Block{Kind: "text", Text: text})
		case "thinking":
			text, _ := m["thinking"].(string)
			blocks = append(blocks, Block{Kind: "thinking", Text: text})
		case "tool_use":
			b := Block{Kind: "tool_use"}
			b.ToolName, _ = m["name"].(string)
			b.ToolUseID, _ = m["id"].(string)
			if input, ok := m["input"]; ok && input != nil {
				if raw, err := json.Marshal(input); err == nil {
					b.Input = json.RawMessage(raw)
				}
			}
			blocks = append(blocks, b)
		case "tool_result":
			b := Block{Kind: "tool_result"}
			b.ToolUseID, _ = m["tool_use_id"].(string)
			if v, ok := m["is_error"].(bool); ok {
				b.IsError = v
			}
			b.Output = stringifyToolResultContent(m["content"])
			blocks = append(blocks, b)
		default:
			if kind != "" {
				blocks = append(blocks, Block{Kind: kind})
			}
		}
	}
	return blocks
}

// stringifyToolResultContent converts a tool_result's content (string or array of blocks) into a single string.
func stringifyToolResultContent(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				parts = append(parts, t)
				continue
			}
			// Fall back to JSON for non-text blocks (e.g., image references).
			if raw, err := json.Marshal(item); err == nil {
				parts = append(parts, string(raw))
			}
		}
		return strings.Join(parts, "\n")
	default:
		if raw, err := json.Marshal(v); err == nil {
			return string(raw)
		}
		return ""
	}
}

// CheapEnoughToPoll implements session.PollableTranscriptSource.
//
// ReadEntries opens one file and walks it, so the preview path may call it on
// every list refresh — which is what it did before adapters owned their own
// readers, and what it must keep doing.
func (r *Reader) CheapEnoughToPoll() {}
