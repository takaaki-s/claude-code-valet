// Package transcript provides reading functionality for Claude Code transcript files.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

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
// workDir: the working directory of the session
// sessionID: the Claude Code session ID (UUID format)
func (r *Reader) GetLastMessage(workDir, sessionID string) (*Message, error) {
	if sessionID == "" {
		return nil, nil
	}

	transcriptPath := r.getTranscriptPath(workDir, sessionID)
	return r.readLastMessage(transcriptPath)
}

// GetLastMessages returns the last user and assistant messages from the transcript
// workDir: the working directory of the session
// sessionID: the Claude Code session ID (UUID format)
func (r *Reader) GetLastMessages(workDir, sessionID string) (*LastMessages, error) {
	if sessionID == "" {
		return nil, nil
	}

	transcriptPath := r.getTranscriptPath(workDir, sessionID)
	return r.readLastMessages(transcriptPath)
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
	// Increase buffer size for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

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
		if entry.Type != "user" && entry.Type != "assistant" {
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
}

// msgObject represents the message field which can have different structures
type msgObject struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // can be string or []contentBlock
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
	// Increase buffer size for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

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
		if entry.Type != "user" && entry.Type != "assistant" {
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

	return lastMessage, nil
}

// extractContent extracts the text content from a transcript entry
func extractContent(entry *transcriptEntry) string {
	if entry.Message.Content == nil {
		return ""
	}

	// User messages: content is a string
	if entry.Type == "user" {
		if str, ok := entry.Message.Content.(string); ok {
			return cleanContent(str)
		}
	}

	// Assistant messages: content is an array of content blocks
	if entry.Type == "assistant" {
		if arr, ok := entry.Message.Content.([]any); ok {
			var texts []string
			for _, item := range arr {
				if block, ok := item.(map[string]any); ok {
					if blockType, ok := block["type"].(string); ok && blockType == "text" {
						if text, ok := block["text"].(string); ok {
							texts = append(texts, text)
						}
					}
				}
			}
			if len(texts) > 0 {
				return cleanContent(strings.Join(texts, " "))
			}
		}
	}

	return ""
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
