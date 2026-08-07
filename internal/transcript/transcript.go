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
	Type      string  `json:"type"`                // "user" | "assistant" | "system" | ...
	Timestamp string  `json:"timestamp,omitempty"` // ISO8601
	Blocks    []Block `json:"blocks,omitempty"`
	Usage     *Usage  `json:"usage,omitempty"` // assistant only
}

// Block is a single content block within a transcript entry.
type Block struct {
	Kind      string          `json:"kind"`                  // "text" | "thinking" | "tool_use" | "tool_result"
	Text      string          `json:"text,omitempty"`        // text/thinking
	ToolName  string          `json:"tool_name,omitempty"`   // tool_use only (tool_result carries only id)
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_use id, or tool_result's referenced id
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use input (preserved structure)
	Output    string          `json:"output,omitempty"`      // tool_result content (string-ified)
	IsError   bool            `json:"is_error,omitempty"`    // tool_result error flag
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
// exchange is a user prompt plus the assistant's reply to it (see lastExchanges).
// workDir may be empty: a glob fallback locates the JSONL by sessionID.
// lastN is the number of exchanges to return and must be at least 1: "no
// exchanges asked for" is rejected rather than answered with the whole
// conversation, which is the direction of failure that hurts — callers pipe
// this into another agent's context.
// Returns ErrNoTranscript when no transcript file exists (yet), and an empty
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
// exchange is a prompt plus everything the agent said in response, however
// many entries that took. A trailing run of user messages with no reply yet
// counts as an exchange of its own, which is what makes `--last 1` show the
// question the agent is still working on.
//
// Slicing a fixed 2n messages instead, as this used to, only lines up with
// exchanges when every turn happens to be exactly one message; in practice it
// returned two consecutive assistant messages and no prompt at all. Fewer
// than n boundaries — or none, as in a transcript that is all assistant
// text — returns everything rather than guessing at a cut point. n comes from
// GetConversation, which refuses anything below 1.
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

// readLastMessages reads the transcript file and returns the last user and assistant messages
func (r *Reader) readLastMessages(filePath string) (*LastMessages, error) {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var lastUser *Message
	var lastAssistant *Message
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

		msg := &Message{
			Type:      entry.Type,
			Content:   content,
			Timestamp: entry.Timestamp,
		}

		if entry.Type == "user" {
			lastUser = msg
		} else {
			lastAssistant = msg
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if lastUser == nil && lastAssistant == nil {
		return nil, nil
	}

	return &LastMessages{
		User:      lastUser,
		Assistant: lastAssistant,
	}, nil
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
	// PromptSource records how Claude Code received a user entry ("typed" from
	// the terminal, "sdk" from a stream-json caller). Its absence on a user
	// entry means nobody supplied it — see conversationTextBlocks.
	PromptSource string `json:"promptSource"`
	IsSidechain  bool   `json:"isSidechain"`
	IsMeta       bool   `json:"isMeta"`
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
// behalf: the body of an invoked skill, environment reminders, command
// caveats. They are stored as user messages but nobody said them, and a
// single injected skill body runs to thousands of lines, so admitting them
// drowns the conversation it is supposed to show.
//
// TurnState calls this for the same reason, which is why the test is made of
// nothing but the flags the transcript itself sets: a session's live status is
// re-derived from which role spoke last, and that classification must not move
// because of what an entry happens to say. Exclusions that depend on the
// content live in conversationTextBlocks instead.
func isConversationEntry(entry *transcriptEntry) bool {
	if entry.Type != "user" && entry.Type != "assistant" {
		return false
	}
	return !entry.IsSidechain && !entry.IsMeta
}

// collectTextBlocks pulls the text out of an entry's content. Content is a
// bare string on some entries and an array of blocks on others, and either
// shape can appear for either role, so this stays role-agnostic — which of
// those texts count as conversation is conversationTextBlocks' decision.
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
// "typed" for a terminal prompt, "sdk" for one fed to `claude -p
// --input-format stream-json`. A user entry with no promptSource was supplied
// by nobody, and when its content is an array of nothing but text it is the
// agent writing in the user's voice — on real transcripts, always an
// interruption notice. That is the same class of entry as isMeta (see
// isConversationEntry), and admitting it puts the agent's own bookkeeping where
// the last real reply belongs on the default `session output` path.
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
	if entry.Type == "user" && entry.PromptSource == "" && isTextOnlyArray(entry.Message.Content) {
		return nil
	}
	return collectTextBlocks(entry.Message.Content)
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
	// TurnStateComplete means the last user/assistant entry is an assistant
	// message with no tool_use block. The API call ended without requesting a
	// tool, so the turn finished. Heuristic: stop_reason is not parsed from the
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
// Entries that are not part of the main conversation are ignored on the same
// terms as every reader in this package (isConversationEntry): system/summary
// types, sidechain entries (a subagent's turns would otherwise read as the
// main thread finishing), and meta messages. Any failure (missing file, empty
// transcript, read error) folds into TurnStateUnknown, so callers can treat it
// as "cannot determine" without guard code.
//
// The file is streamed keeping only the last main-conversation entry — a
// ReadEntries call would materialize every block (including re-marshalled
// tool payloads) just to look at the tail.
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
		if !isConversationEntry(&raw) {
			continue
		}
		last = &raw
	}
	if scanner.Err() != nil || last == nil {
		return TurnStateUnknown
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

// ReadAITitle returns the AI-generated session title Claude Code writes to
// the transcript when it names the conversation from context (the same value
// CC surfaces as "Session name" in `/status`). Each line of the JSONL is
// checked for `{"type":"ai-title","aiTitle":"…"}` and the most recent
// non-empty value wins — CC may re-title the session later in the
// conversation, and callers should see the latest title, not the first.
//
// Returns ("", false) on any miss: empty sessionID, no transcript file yet
// (silent), malformed lines (skipped), or no ai-title entry present. Never
// returns an error — all failure modes are silent fallbacks by design so the
// Layer C-name enhancer can call this on every hook without guard code.
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
		if since != "" && raw.Timestamp != "" && raw.Timestamp <= since {
			continue
		}
		entries = append(entries, Entry{
			Type:      raw.Type,
			Timestamp: raw.Timestamp,
			Blocks:    parseBlocks(&raw),
			Usage:     raw.Message.Usage,
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
