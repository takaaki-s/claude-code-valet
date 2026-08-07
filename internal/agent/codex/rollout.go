// Package codex implements the Agent adapter for the OpenAI Codex CLI.
// See internal/agent/claude for the reference implementation the layout
// mirrors; the Codex-specific mapping is documented in
// .tasks/feat/additional-agent-adapters/02_design.md §3.
package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// scannerMaxLine caps a single rollout line at 4 MiB. Real sessions peak around
// 17 KiB (session_meta with git metadata + base_instructions), so this is pure
// defense-in-depth: bufio.Scanner's default 64 KiB buffer would silently error
// on outsized lines if some future Codex build embeds a larger system prompt,
// and losing the whole file to that would kill Layer C-transcript for the
// affected session.
const scannerMaxLine = 4 << 20

// Meta is the parsed form of the first line of a rollout JSONL (`type:
// "session_meta"`). Only the fields jind-ai actually consumes are kept.
type Meta struct {
	// ID is the Codex session UUID. Matches the `session_id` field the hook
	// stdin JSON carries, and is what `codex resume <UUID>` accepts.
	ID string
	// Cwd is the absolute working directory the session started in.
	Cwd string
}

// Locator resolves a Codex session UUID to its rollout JSONL path on disk.
// The Codex CLI shards rollouts by date (`<sessionsDir>/YYYY/MM/DD/rollout-*-<UUID>.jsonl`),
// so a UUID lookup requires a glob across every day shard.
type Locator struct {
	// SessionsDir is the absolute path to `~/.codex/sessions` (or the value
	// of the CODEX_HOME/sessions override — see NewLocator).
	SessionsDir string
}

// NewLocator returns a Locator whose SessionsDir honours the same precedence
// the Codex CLI itself uses:
//
//  1. `$CODEX_HOME/sessions` when CODEX_HOME is set (Codex's dev override)
//  2. `<home>/.codex/sessions` otherwise
//
// The caller passes the home dir explicitly so tests can substitute a
// t.TempDir() without touching real $HOME.
func NewLocator(home string) *Locator {
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		return &Locator{SessionsDir: filepath.Join(codexHome, "sessions")}
	}
	return &Locator{SessionsDir: filepath.Join(home, ".codex", "sessions")}
}

// Find returns the absolute path of the rollout file whose filename embeds
// uuid, together with ok=true. Returns ("", false) when uuid is empty, when
// the glob does not match, or when the glob fails.
//
// The glob spans every day shard because jind-ai does not know when the
// session was originally created — a resume may happen many days later. When
// several files match (theoretically impossible, but real filesystems have
// clocks that go backwards), the newest one by mtime wins.
func (l *Locator) Find(uuid string) (string, bool) {
	if uuid == "" || l == nil || l.SessionsDir == "" {
		return "", false
	}
	pattern := filepath.Join(l.SessionsDir, "*", "*", "*", "rollout-*-"+uuid+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", false
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	sort.SliceStable(matches, func(i, j int) bool {
		si, err1 := os.Stat(matches[i])
		sj, err2 := os.Stat(matches[j])
		if err1 != nil || err2 != nil {
			// A stat failure shouldn't ever happen for a glob hit, but if it
			// does, fall back to lexical order so behaviour stays defined.
			return matches[i] < matches[j]
		}
		return si.ModTime().After(sj.ModTime())
	})
	return matches[0], true
}

// rolloutRow is the union of every rollout line shape the parser inspects.
// Fields not decoded by json.Unmarshal (there are many — see 02_design.md
// §3.7 / f0.3-codex-runtime-notes.md) are silently ignored.
type rolloutRow struct {
	Type    string `json:"type"`
	Payload struct {
		// session_meta payload
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
		// response_item payload
		Type    string         `json:"type"`
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`
	} `json:"payload"`
}

// contentBlock is one element of a response_item message's content array.
// Codex splits a single user item across several of them.
type contentBlock struct {
	Text string `json:"text"`
}

// ReadMeta parses the first line of the rollout at path and returns the Meta
// fields jind-ai cares about. Returns an error when the file is empty, the
// first line cannot be parsed as JSON, or the first line is not a
// `session_meta` row (Codex always writes session_meta first, so any other
// shape is a corrupt or foreign file).
func ReadMeta(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxLine)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Meta{}, fmt.Errorf("read rollout meta: %w", err)
		}
		return Meta{}, errors.New("read rollout meta: empty file")
	}
	var row rolloutRow
	if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
		return Meta{}, fmt.Errorf("read rollout meta: parse first line: %w", err)
	}
	if row.Type != "session_meta" {
		return Meta{}, fmt.Errorf("read rollout meta: first line is %q, want %q", row.Type, "session_meta")
	}
	return Meta{ID: row.Payload.ID, Cwd: row.Payload.Cwd}, nil
}

// pseudoUserPrefixes lists the substrings Codex injects as the first
// `<message role="user">` bodies before any real user turn. They carry
// environment/context metadata rather than the operator's own words, so the
// Layer C-transcript enhancer must step past them to find the first prompt
// the user actually typed.
//
// See f0.3-codex-runtime-notes.md item 10 for what these look like in
// practice; the `<system` / `<instructions` prefixes are defensive against
// future Codex builds adding similar wrappers.
// Measured against 14 real rollouts (35 `role: "user"` items, ground-truthed
// against the 20 `event_msg/user_message` lines those files carry): with these
// prefixes the check rejects every injection and passes every human prompt.
// `<recommended_plugins>` and `<skill>` were added after that measurement
// found them leaking — `<recommended_plugins>` became the session description
// for 2 of the 14 sessions, both non-interactive `codex exec` runs, where the
// operator's actual first words were two lines further down.
var pseudoUserPrefixes = []string{
	"<environment_context>",
	"<recommended_plugins>",
	"<skill>",
	"<system",
	"<instructions",
}

// FirstUserPrompt streams the rollout at path and returns the text of the
// first genuine user turn — a `response_item` line whose payload is a
// `message` with `role: "user"` whose first content block is not one of the
// pseudo-user injections above.
//
// Returns ("", false) when the file has no such turn yet (common right after
// SessionStart, before the operator has said anything), when the file is
// empty, or when the file cannot be opened. Broken lines mid-stream are
// silently skipped — Codex may flush mid-write, so the tail can be truncated.
func FirstUserPrompt(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	return firstUserPromptFrom(f)
}

func firstUserPromptFrom(r io.Reader) (string, bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var row rolloutRow
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		if row.Type != "response_item" {
			continue
		}
		if row.Payload.Type != "message" || row.Payload.Role != "user" {
			continue
		}
		// Look past the injected blocks rather than judging the item by its
		// first one. Codex packs several blocks into one user item, and the
		// order is not fixed: the `<recommended_plugins>` items in the
		// measured corpus carry the injection at index 0 and
		// `<environment_context>` at index 1, so the reverse arrangement —
		// injection first, the operator's words after — is one Codex build
		// away, and a first-block-only test would answer "" for it.
		text, ok := firstGenuineBlock(row.Payload.Content)
		if !ok {
			continue
		}
		return text, true
	}
	return "", false
}

// firstGenuineBlock returns the text of the first content block that is not
// one of Codex's injections, and whether there was one. An item made entirely
// of injections has nothing the operator said in it.
func firstGenuineBlock(content []contentBlock) (string, bool) {
	for _, c := range content {
		if !isPseudoUser(c.Text) {
			return c.Text, true
		}
	}
	return "", false
}

func isPseudoUser(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\n\r")
	for _, p := range pseudoUserPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}
