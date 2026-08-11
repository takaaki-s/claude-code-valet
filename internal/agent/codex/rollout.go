// Package codex implements the Agent adapter for the OpenAI Codex CLI.
// See internal/agent/claude for the reference implementation the layout
// mirrors; the Codex-specific rollout mapping is documented under "Codex
// adapter" and "Session result" in docs/gotchas.md.
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
	"sync"
	"time"
)

// scannerMaxLine caps a single rollout line at 16 MiB, matching
// maxTranscriptLineBytes in internal/transcript.
//
// The ceiling was 4 MiB while the only readers stopped early. The transcript
// reader walks every line, and one line over the limit fails the whole read —
// permanently, since a rollout only grows: `jin session result` would report an
// error from then on and discard everything read before the oversized line. A
// tool output holding a build or test log reaches this range without being
// unusual. Real sessions still peak around 17 KiB.
const scannerMaxLine = 16 << 20

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
// so a UUID lookup requires a glob across every day shard — unless a prior
// Find already resolved it; see the cache field below.
type Locator struct {
	// SessionsDir is the absolute path to `~/.codex/sessions` (or the value
	// of the CODEX_HOME/sessions override — see NewLocator).
	SessionsDir string

	mu    sync.Mutex
	cache map[string]string // session UUID -> resolved rollout path
}

// NewLocator returns a Locator whose SessionsDir honours the same precedence
// the Codex CLI itself uses:
//
//  1. `$CODEX_HOME/sessions` when CODEX_HOME is set (Codex's dev override)
//  2. `<home>/.codex/sessions` otherwise
//
// The home dir is passed explicitly so tests can substitute a t.TempDir().
func NewLocator(home string) *Locator {
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		return &Locator{SessionsDir: filepath.Join(codexHome, "sessions")}
	}
	return &Locator{SessionsDir: filepath.Join(home, ".codex", "sessions")}
}

// Find returns the absolute path of the rollout file whose filename embeds
// uuid, together with ok=true. Returns ("", false) when uuid is empty, when the
// glob does not match, or when the glob fails.
//
// A resolved uuid is cached, since a caller that already knows the answer asks
// again routinely: `jin session result --since` polls the same session, and
// DescriptionEnhancer retries on every hook until it succeeds. A cache hit is
// re-verified with a single os.Stat, so a file deleted or moved out from under
// a stale entry falls back to a fresh glob. A miss is never cached — nothing
// here distinguishes "not written yet" (the rollout appears moments after
// SessionStart) from "never will be".
//
// The glob spans every day shard because jind-ai does not know when the session
// was created; a resume may happen many days later. When several files match,
// the newest by mtime wins.
//
// The cache assumes one uuid maps to one file for the life of the process.
// Whether `codex resume <UUID>` can retarget a UUID onto a different rollout
// file is unverified; SpawnCommand evicts the entry first so a Find made after
// the resume re-globs. That eviction happens before the resumed process starts,
// so a narrow window remains: a concurrent Find landing between the eviction
// and the resumed file actually existing would re-cache the pre-resume path,
// which then survives until the next resume. Accepted rather than closed, being
// a race around a Codex behaviour that is not even confirmed to happen.
func (l *Locator) Find(uuid string) (string, bool) {
	if uuid == "" || l == nil || l.SessionsDir == "" {
		return "", false
	}
	if path, ok := l.cached(uuid); ok {
		return path, true
	}
	path, ok := l.glob(uuid)
	if ok {
		l.remember(uuid, path)
	}
	return path, ok
}

// cached returns uuid's memoized path, verifying with os.Stat that it still
// exists before handing it back. A hit that fails the stat is evicted so the
// caller falls through to a fresh glob instead of a path that resolves to
// nothing.
func (l *Locator) cached(uuid string) (string, bool) {
	l.mu.Lock()
	path, ok := l.cache[uuid]
	l.mu.Unlock()
	if !ok {
		return "", false
	}
	if _, err := os.Stat(path); err != nil {
		l.invalidate(uuid)
		return "", false
	}
	return path, true
}

// remember stores uuid's resolved path for future Find calls.
func (l *Locator) remember(uuid, path string) {
	l.mu.Lock()
	if l.cache == nil {
		l.cache = make(map[string]string)
	}
	l.cache[uuid] = path
	l.mu.Unlock()
}

// invalidate evicts uuid's cached path, if any. A miss is a no-op. See
// Agent.SpawnCommand for the one caller jind-ai has today.
func (l *Locator) invalidate(uuid string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.cache, uuid)
	l.mu.Unlock()
}

// glob performs the day-shard search Find falls back to on a cache miss —
// unconditionally what Find used to do before caching was added.
func (l *Locator) glob(uuid string) (string, bool) {
	pattern := filepath.Join(l.SessionsDir, "*", "*", "*", "rollout-*-"+uuid+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", false
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return newestMatch(matches), true
}

// newestMatch returns the match with the most recent mtime. Each candidate is
// stat'd exactly once up front (decorate-sort-undecorate) rather than inside
// the sort comparator, which would stat it once per comparison — O(n log n)
// stats for n matches instead of O(n). This branch is rare (see Find's doc
// comment), but there is no reason to pay more than one stat per file when it
// does run.
func newestMatch(matches []string) string {
	type stamped struct {
		path string
		mod  time.Time
		ok   bool
	}
	decorated := make([]stamped, len(matches))
	for i, m := range matches {
		info, err := os.Stat(m)
		decorated[i] = stamped{path: m, ok: err == nil}
		if err == nil {
			decorated[i].mod = info.ModTime()
		}
	}
	sort.SliceStable(decorated, func(i, j int) bool {
		if !decorated[i].ok || !decorated[j].ok {
			// A stat failure shouldn't ever happen for a glob hit, but if it
			// does, fall back to lexical order so behaviour stays defined.
			return decorated[i].path < decorated[j].path
		}
		return decorated[i].mod.After(decorated[j].mod)
	})
	return decorated[0].path
}

// rolloutRow is the union of every rollout line shape the parser inspects.
// Fields not decoded by json.Unmarshal (there are many) are silently ignored.
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

// newRolloutScanner returns a line scanner sized for rollout JSONL. The buffer
// ceiling is the reason this is shared: three readers in this package walk the
// same file, and a limit raised in one of them but not the others would make
// the same rollout parse for one caller and truncate for another.
func newRolloutScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), scannerMaxLine)
	return s
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

	scanner := newRolloutScanner(f)
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
// Layer C-transcript enhancer must step past them to find the first prompt the
// user actually typed.
//
// `<system` / `<instructions` are defensive against future Codex builds adding
// similar wrappers. Measured against 14 real rollouts (35 `role: "user"` items,
// ground-truthed against the 20 `event_msg/user_message` lines those files
// carry): the check rejects every injection and passes every human prompt.
// `<recommended_plugins>` and `<skill>` were added after that measurement found
// them leaking — the former became the session description for 2 of the 14,
// both non-interactive `codex exec` runs.
var pseudoUserPrefixes = []string{
	"<environment_context>",
	"<recommended_plugins>",
	"<skill>",
	"<system",
	"<instructions",
}

// FirstUserPrompt streams the rollout at path and returns the text of the first
// genuine user turn — a `response_item` line whose payload is a `message` with
// `role: "user"` whose first content block is not one of the pseudo-user
// injections above.
//
// Returns ("", false) when the file has no such turn yet (common right after
// SessionStart), is empty, or cannot be opened. Broken lines mid-stream are
// silently skipped: Codex may flush mid-write, so the tail can be truncated.
func FirstUserPrompt(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	return firstUserPromptFrom(f)
}

func firstUserPromptFrom(r io.Reader) (string, bool) {
	scanner := newRolloutScanner(r)
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
		// Look past the injected blocks rather than judging the item by its first
		// one. Codex packs several blocks into one user item and the order is not
		// fixed: the `<recommended_plugins>` items in the measured corpus carry the
		// injection at index 0 and `<environment_context>` at index 1, so the
		// reverse arrangement is one Codex build away.
		text, ok := firstGenuineBlock(row.Payload.Content)
		if !ok {
			continue
		}
		return text, true
	}
	return "", false
}

// genuineBlocks returns the text of the content blocks the operator actually
// wrote — everything that is neither one of Codex's injections nor blank.
//
// One rule, two readers. The description enhancer wants the first of these and
// the transcript reader wants all of them joined, but "which blocks did the
// operator write" has to be the same question for both.
func genuineBlocks(content []contentBlock) []string {
	var out []string
	for _, c := range content {
		if isPseudoUser(c.Text) || strings.TrimSpace(c.Text) == "" {
			continue
		}
		out = append(out, c.Text)
	}
	return out
}

// firstGenuineBlock returns the text of the first content block that is not
// one of Codex's injections, and whether there was one. An item made entirely
// of injections has nothing the operator said in it.
func firstGenuineBlock(content []contentBlock) (string, bool) {
	blocks := genuineBlocks(content)
	if len(blocks) == 0 {
		return "", false
	}
	return blocks[0], true
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
