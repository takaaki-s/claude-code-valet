package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"github.com/takaaki-s/jind-ai/internal/atomicfile"
)

// rawConfig is the top level of ~/.claude.json, held as undecoded JSON so that
// every key jind-ai does not understand survives the round-trip. That file is
// not a settings file: it carries the user's OAuth session, their user- and
// local-scope MCP server configuration, and every cache Claude Code keeps —
// roughly 76 top-level keys on a working install. Decoding it into a typed
// struct and marshalling the struct back, which is what this file used to do
// against a different path, would delete all of them.
type rawConfig map[string]json.RawMessage

// rawProject is one entry of the "projects" map, kept undecoded for the same
// reason one level down: Claude Code writes ~30 keys per project (allowedTools,
// mcpServers, lastCost, lastSessionId, ...) and jind-ai only ever sets one.
type rawProject map[string]json.RawMessage

const (
	// trustKey is the flag Claude Code reads to decide whether to show the
	// trust dialog. It is not part of any documented settings schema — the
	// only place it is honoured is the projects map described above.
	trustKey = "hasTrustDialogAccepted"

	projectsKey = "projects"

	// configTmpPattern deliberately avoids starting with ".claude.json":
	// Claude Code carries a list of sensitive dotfile names that includes that
	// exact string, and a crash-orphaned sibling that looks like the real file
	// is worth avoiding even if the match is exact rather than by prefix. No
	// scan of the home directory belongs to jind-ai, so the only rule this
	// pattern has to satisfy is "recognisably ours".
	configTmpPattern = ".jin-claude-json-*"

	// trustWriteAttempts bounds the reload-and-retry loop that keeps a
	// concurrent Claude Code write from being rolled back. The last attempt
	// writes whatever it built, changed file or not: giving up would leave the
	// session facing the trust dialog this package exists to suppress, which is
	// a worse outcome than the narrow window three attempts cannot close. Three
	// is enough that only a process rewriting the file continuously reaches it.
	trustWriteAttempts = 3
)

// trustMu serialises the read-modify-write below against other goroutines in
// this process. Two callers that read the same snapshot both write it back, and
// the second rename discards the first one's entry — on a direct-call harness
// that costs 85 of 100 entries, which turns the bug this file exists to fix
// back on for whichever caller lost.
//
// Ordinary session starts do not race: Manager.StartBackground holds m.mu
// across startSessionTmux (internal/session/manager.go:2060), so they queue up.
// The window this closes is the quick-fail resume retry at manager.go:2471,
// which rebuilds the spawn command after releasing m.mu precisely so it can run
// while the manager is busy — concurrently with another session's start.
//
// NOTE this is an auxiliary lock taken *while* m.mu is held, which
// docs/conventions.md otherwise forbids. It is safe because trustMu is a leaf:
// nothing under it calls back into session.Manager. The cost is that a session
// start now waits out another goroutine's file write, tens of milliseconds at
// worst. The exception is recorded in conventions.md next to the others.
//
// What it does not cover is Claude Code writing the same file from its own
// process, which no lock jind-ai can take would help with. That window is real
// and is documented in docs/gotchas.md rather than papered over here.
var trustMu sync.Mutex

// EnsureTrustState sets hasTrustDialogAccepted=true for the absolute path of
// workDir in ~/.claude.json, which is where Claude Code actually looks; the
// documentation describes that file as holding "per-project state (allowed
// tools, trust settings)". Without the flag, `claude` opens with a trust
// dialog in a tmux pane nobody is watching and the session hangs forever.
//
// Note this is NOT any of the settings files. ~/.claude/settings.json,
// ~/.claude/settings.local.json and <project>/.claude/settings.local.json are a
// separate system with a validated schema that has no "projects" key at all;
// writing trust state there is silently ignored. jind-ai wrote to
// ~/.claude/settings.local.json until this was found, which is why some users
// have a large dead projects map in that file — see docs/gotchas.md.
//
// Idempotent, and deliberately more than key-exact: Claude Code checks the
// directory and then walks up to the filesystem root, so a trusted ancestor
// already covers everything beneath it. Mirroring that walk in isTrusted is
// what stops jind-ai from appending an entry per throwaway worktree.
//
// Degenerate workDirs are not rejected. A session started directly in $HOME or
// / writes an entry there and so trusts everything beneath it, which is a lot;
// refusing would be worse, because the only thing it could produce is the
// hung trust dialog this function exists to prevent, on a directory the user
// explicitly asked for a session in. The scope of the grant is the user's
// choice of workDir, and docs/gotchas.md says so where users will read it.
//
// A malformed or unreadable ~/.claude.json is reported rather than repaired.
// The worst outcome of returning an error here is a trust dialog; the worst
// outcome of rewriting that file from a partial read is logging the user out.
// Claude Code already handles the corrupt case better than jind-ai could:
// it names the file and the parse error on stderr, moves it aside into
// ~/.claude/backups/, and starts fresh. Repairing it here would race that
// recovery and skip the backup.
func EnsureTrustState(workDir string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	trustMu.Lock()
	defer trustMu.Unlock()

	configPath := resolveConfigPath(filepath.Join(homeDir, ".claude.json"))

	for attempt := 1; ; attempt++ {
		before, err := statConfig(configPath)
		if err != nil {
			return err
		}

		data, needed, err := buildTrustedConfig(configPath, absWorkDir)
		if err != nil || !needed {
			return err
		}

		// Re-stat before committing. Everything above read the file and built a
		// replacement for it; if Claude Code wrote in the meantime, renaming now
		// would roll its write back. Reloading is the whole fix — the merge has
		// to happen against what is on disk, not against a stale snapshot.
		after, err := statConfig(configPath)
		if err != nil {
			return err
		}
		if before != after && attempt < trustWriteAttempts {
			claudeLog("[TRUST] %s changed while merging, retrying (attempt %d)", configPath, attempt)
			continue
		}

		if err := atomicfile.Write(configPath, data, 0600, configTmpPattern); err != nil {
			return fmt.Errorf("failed to write %s: %w", configPath, err)
		}
		claudeLog("[TRUST] Set %s=true for %s in %s", trustKey, absWorkDir, configPath)
		return nil
	}
}

// buildTrustedConfig reads configPath and returns the bytes that would trust
// absWorkDir. needed is false when the directory is already covered, which is
// the common case once a worktree base directory has been trusted.
func buildTrustedConfig(configPath, absWorkDir string) (data []byte, needed bool, err error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, false, err
	}

	projects, err := decodeProjects(cfg, configPath)
	if err != nil {
		return nil, false, err
	}

	if isTrusted(projects, absWorkDir) {
		// Logged because this is the path that leaves no trace in the file:
		// "jind-ai said it set trust but there is no entry for my worktree" is
		// the expected outcome of a trusted ancestor, not a failure.
		claudeLog("[TRUST] %s already trusted (self or ancestor) in %s", absWorkDir, configPath)
		return nil, false, nil
	}

	// An existing false is overwritten rather than respected. It carries no
	// refusal: in the 2.1.223 binary the only place hasTrustDialogAccepted is
	// ever set to false is the template Claude Code stamps out for a brand new
	// project entry, there is no key recording a declined dialog, and cancelling
	// the dialog exits the process instead of persisting anything. A false here
	// means "this entry was created and never answered".
	entry := projects[absWorkDir]
	if entry == nil {
		entry = rawProject{}
	}
	entry[trustKey] = json.RawMessage("true")
	projects[absWorkDir] = entry

	// The inner encode goes through the same helper for its escaping rule only:
	// its indentation and trailing newline are discarded when the result is
	// re-indented as part of the whole file below. Skipping it would let the
	// default escaping back in for everything inside a project entry, which is
	// where a local-scope MCP server's URL actually lives.
	encodedProjects, err := marshalConfigJSON(projects)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal %q: %w", projectsKey, err)
	}
	cfg[projectsKey] = encodedProjects

	data, err = marshalConfigJSON(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal %s: %w", configPath, err)
	}
	return data, true, nil
}

// configStamp is the fingerprint used to notice another process writing
// ~/.claude.json between the read and the rename. Size and modification time
// are what a stat already returns; nothing stronger is worth the read.
type configStamp struct {
	modTimeNano int64
	size        int64
	exists      bool
}

// statConfig fingerprints the config file. A missing file is not an error — it
// is the machine that has never run Claude Code — and its zero stamp still
// compares unequal to a file that appeared in the meantime, which is exactly
// the change worth retrying on.
//
// It is a variable so that tests can make the file appear to change underneath
// the write. Production code never reassigns it.
var statConfig = func(path string) (configStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return configStamp{}, nil
		}
		return configStamp{}, fmt.Errorf("failed to stat %s: %w", path, err)
	}
	return configStamp{
		modTimeNano: info.ModTime().UnixNano(),
		size:        info.Size(),
		exists:      true,
	}, nil
}

// resolveConfigPath follows a symlinked ~/.claude.json to the file it points
// at. atomicfile.Write finishes with os.Rename, which replaces the link itself
// rather than the file behind it, so writing to the unresolved path would turn
// a user's dotfiles symlink into a plain file in $HOME and leave the repo copy
// orphaned at its old contents — a silent break of their dotfiles sync that no
// error would report. os.WriteFile, which this code used before, followed the
// link; keeping that property is not optional just because the write became
// atomic.
//
// A dangling link — target deleted, dotfiles repo not cloned yet, volume not
// mounted — is followed too, via os.Readlink, because EvalSymlinks refuses a
// path it cannot stat. Writing to the link path instead would replace the link
// with a regular file and never create the target, which is the same damage
// this function exists to prevent, only harder to notice because the target is
// already missing.
//
// A path that resolves to nothing at all is returned unchanged: that is the
// machine with no ~/.claude.json yet, where creating the file where we were
// told to is the whole point.
//
// The temp file atomicfile creates lands next to the resolved target, so on a
// symlinked config it exists inside the user's dotfiles directory for the
// duration of the write. That is forced — the rename has to stay on one
// filesystem — and it is removed on every path out of Write, but it is the
// reason configTmpPattern has to be recognisable rather than random.
func resolveConfigPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	target, err := os.Readlink(path)
	if err != nil {
		return path // not a symlink at all, or gone entirely
	}
	if !filepath.IsAbs(target) {
		// A relative link resolves against the directory holding the link,
		// not the working directory.
		target = filepath.Join(filepath.Dir(path), target)
	}
	return target
}

// marshalConfigJSON renders a value the way Claude Code writes ~/.claude.json:
// two-space indent, so a jind-ai write does not reformat every line, and no
// HTML escaping.
//
// The escaping matters more than it looks. encoding/json rewrites <, >, & and
// U+2028/U+2029 as \u escapes by default, and it does so inside json.RawMessage
// too — an MCP server URL carrying a query string ("?a=1&b=2") comes back with
// each ampersand replaced by its six-character backslash-u escape. The value
// parses to the same string, but the whole point of
// holding untouched keys as raw messages is that they survive byte for byte,
// and JavaScript's JSON.stringify — what actually produced the file — does not
// escape them either.
//
// Key order is still not preserved: Go sorts map keys on marshal. The file is
// not version-controlled and Claude Code rewrites it in its own order on the
// next run, so that difference costs nothing.
func marshalConfigJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode terminates the value with a newline that json.MarshalIndent does
	// not add and Claude Code does not write; trimming it keeps the two
	// producers from flipping the last byte back and forth.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// loadConfig reads ~/.claude.json. A missing file yields an empty config so a
// machine that has jind-ai but has never run Claude Code still gets its trust
// entry; anything else — unreadable, or present but not JSON — is an error,
// because the alternative is overwriting a file whose contents we could not
// read with one built from nothing.
func loadConfig(path string) (rawConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rawConfig{}, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	// Bytes that are not valid UTF-8 are refused before they reach Unmarshal.
	// Holding values as json.RawMessage protects them, but there is no raw
	// equivalent for object *keys*: those decode into Go strings, and the
	// decoder silently substitutes U+FFFD. Since the keys of the projects map
	// are filesystem paths, and a Linux path is bytes rather than text, one
	// stray byte in somebody else's project path would come back renamed —
	// carrying that project's allowedTools, mcpServers and trust flag into an
	// entry nothing will ever look up again. Refusing keeps the promise the
	// rest of this file makes; JSON is defined over UTF-8 anyway, so the input
	// was not valid JSON text to begin with.
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("refusing to rewrite malformed %s: not valid UTF-8", path)
	}

	cfg := rawConfig{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("refusing to rewrite malformed %s: %w", path, err)
	}
	return cfg, nil
}

// decodeProjects pulls the projects map out of cfg. An absent or JSON-null
// block is normal on a fresh install and becomes an empty map; a block that is
// present but not shaped like a map of objects is an error for the same reason
// loadConfig refuses malformed input — jind-ai would have to discard entries it
// cannot represent in order to write.
func decodeProjects(cfg rawConfig, path string) (map[string]rawProject, error) {
	raw, ok := cfg[projectsKey]
	if !ok {
		return map[string]rawProject{}, nil
	}

	projects := map[string]rawProject{}
	if err := json.Unmarshal(raw, &projects); err != nil {
		return nil, fmt.Errorf("refusing to rewrite malformed %q in %s: %w", projectsKey, path, err)
	}
	if projects == nil { // "projects": null unmarshals into a nil map
		projects = map[string]rawProject{}
	}
	return projects, nil
}

// isTrusted reports whether Claude Code would already consider dir trusted. It
// mirrors the CLI's own lookup: check the directory, then every ancestor up to
// the filesystem root, because a trusted parent trusts everything below it.
//
// Matching that walk is what keeps ~/.claude.json from growing an entry per
// session: once the user trusts a worktree base directory, every session under
// it is a no-op here.
//
// Note what the write itself grants. Because Claude Code inherits trust
// downwards, an entry for the session's workDir trusts that whole subtree, not
// just the one directory — the same thing accepting the dialog there would have
// done. jind-ai never writes an entry *above* workDir, so the tree it grants is
// always exactly the one the user pointed the session at.
func isTrusted(projects map[string]rawProject, dir string) bool {
	for {
		if entryTrusted(projects[dir]) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir { // filepath.Dir("/") == "/": the root terminates the walk
			return false
		}
		dir = parent
	}
}

// entryTrusted treats anything that is not the literal JSON true — absent,
// null, false, or some other type entirely — as untrusted, which is the same
// reading Claude Code applies (`?.hasTrustDialogAccepted === true`).
func entryTrusted(entry rawProject) bool {
	raw, ok := entry[trustKey]
	if !ok {
		return false
	}
	var trusted bool
	if err := json.Unmarshal(raw, &trusted); err != nil {
		return false
	}
	return trusted
}
