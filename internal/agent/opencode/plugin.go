package opencode

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/takaaki-s/jind-ai/internal/agentdocs"
	"github.com/takaaki-s/jind-ai/internal/atomicfile"
)

// pluginSource is the TypeScript plugin opencode loads to report status
// back to jind-ai. It is embedded rather than shipped as an npm package so
// `jin` stays a single self-contained binary and the plugin can never drift
// out of sync with the canonical event names status.go expects.
//
//go:embed plugin/jin.ts
var pluginSource string

// execPathPlaceholder is replaced with the absolute path of the running jin
// binary, quoted, when the plugin is materialised. The plugin needs an
// absolute path because opencode's Bun runtime does not inherit the
// interactive shell's PATH resolution.
//
// It stands alone in the template rather than inside quotes because the
// substituted value brings its own — see quoteForJS.
const execPathPlaceholder = "__JIN_BIN__"

// pluginFileMode is the permission the materialised plugin gets. opencode only
// ever reads it.
const pluginFileMode os.FileMode = 0o644

// contextFileName is the markdown holding the agent-facing jin context, and
// configFileName is the config that points opencode at it.
//
// opencode has no hook-style channel for injecting context: the plugin runs
// `jin hook` with stdout set to "ignore", so the SessionStart output the
// Claude Code and Codex adapters rely on could never reach it. What it does
// have is `instructions`, a list of files merged into the session's prompt —
// and jind-ai already owns a directory on opencode's config search path.
const (
	contextFileName = "jin-agent.md"
	configFileName  = "opencode.json"
)

// Temp-name patterns for the two files above. Both must stay clear of
// everything opencode looks for in this directory: the config names
// (opencode.json / opencode.jsonc) it reads because the directory is
// OPENCODE_CONFIG_DIR, and the {plugin,plugins}/*.{ts,js} glob (which lives a
// level down and so cannot collide anyway). A leading dot and a .tmp suffix
// clear both.
const (
	contextTmpPattern = ".jin-context-*.tmp"
	configTmpPattern  = ".jin-config-*.tmp"
)

// openCodeConfig is the whole of what jind-ai contributes to opencode's
// configuration. It stays minimal on purpose: opencode merges every config on
// its search path, and `instructions` specifically is unioned rather than
// replaced (packages/opencode/src/config/config.ts), so the user's own
// instructions survive. Adding any field that merges by replacement would
// quietly override a setting the user made deliberately.
type openCodeConfig struct {
	Instructions []string `json:"instructions"`
}

// pluginTmpPattern names the in-flight temp file so it stays outside the glob
// opencode runs over this directory on every start ({plugin,plugins}/*.{ts,js}),
// which is what keeps it from importing a half-written module.
//
// A temp file stranded by a crash between create and rename is inert for the
// same reason, but nothing here reclaims it — there is no counterpart to the
// session Store's cleanupTempFiles, since WritePlugin has no construction point
// safe to sweep from.
const pluginTmpPattern = ".jin-plugin-*.tmp"

// WritePlugin materialises the embedded plugin under stateDir and returns
// the directory to hand to opencode as OPENCODE_CONFIG_DIR.
//
// Layout, matching opencode's ConfigPlugin.load glob ({plugin,plugins}/*.{ts,js}):
//
//	<stateDir>/opencode/            ← OPENCODE_CONFIG_DIR
//	<stateDir>/opencode/plugin/jin.ts
//
// opencode also treats this directory as one of its own: on start it writes
// a .gitignore there and installs @opencode-ai/plugin into a node_modules
// beside it. That is expected — it does the same to ~/.config/opencode —
// and is precisely why the directory belongs under jind-ai's state rather
// than anywhere the user owns.
//
// The file is rewritten on every call rather than only when missing, which
// makes it self-healing: a plugin the user deleted, truncated or edited by
// hand is restored on the next session start. (It does not exist to track a
// moved binary: execPath is a parameter here, not a value this package
// remembers, so a caller that resolves a different one simply passes it.) The
// write costs well under a millisecond next to spawning tmux and the agent
// itself.
func WritePlugin(stateDir, execPath string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("opencode: empty state dir")
	}
	if execPath == "" {
		return "", fmt.Errorf("opencode: empty exec path")
	}

	configDir := filepath.Join(stateDir, "opencode")
	pluginDir := filepath.Join(configDir, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return "", fmt.Errorf("opencode: create plugin dir: %w", err)
	}

	src := strings.ReplaceAll(pluginSource, execPathPlaceholder, quoteForJS(execPath))
	pluginPath := filepath.Join(pluginDir, "jin.ts")
	if err := atomicfile.Write(pluginPath, []byte(src), pluginFileMode, pluginTmpPattern); err != nil {
		return "", fmt.Errorf("opencode: write plugin: %w", err)
	}
	return configDir, nil
}

// WriteAgentContext materialises the agent-facing jin context into configDir
// and points opencode at it through an instructions entry.
//
// Rewritten on every call for the same reason as the plugin: the files live
// under jind-ai's state, are owned by jind-ai, and a copy the user deleted or
// truncated should heal on the next session start.
//
// The write order matters. The markdown lands first, so the config never names
// a file that does not exist yet — opencode reading the directory mid-write
// would otherwise see an instructions entry pointing at nothing. Each file is
// published by rename, so neither is ever observed half-written; a malformed
// opencode.json is not merely this feature failing but opencode's whole config
// load failing.
//
// The path recorded in instructions is absolute. opencode resolves these
// entries relative to a directory this package does not get to choose, and
// guessing wrong would fail silently — the session would simply start without
// the context.
func WriteAgentContext(configDir string) error {
	if configDir == "" {
		return fmt.Errorf("opencode: empty config dir")
	}

	contextPath := filepath.Join(configDir, contextFileName)
	if err := atomicfile.Write(contextPath, []byte(agentdocs.Context()), pluginFileMode, contextTmpPattern); err != nil {
		return fmt.Errorf("opencode: write agent context: %w", err)
	}

	data, err := json.MarshalIndent(openCodeConfig{Instructions: []string{contextPath}}, "", "  ")
	if err != nil {
		return fmt.Errorf("opencode: marshal config: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Write(filepath.Join(configDir, configFileName), data, pluginFileMode, configTmpPattern); err != nil {
		return fmt.Errorf("opencode: write config: %w", err)
	}
	return nil
}

// quoteForJS renders s as a complete JavaScript string literal, quotes
// included.
//
// JSON is the right tool rather than strconv.Quote: JSON's escape set is a
// strict subset of JavaScript's, whereas Go emits \a for U+0007, which
// JavaScript does not recognise as an escape at all — it would silently
// decode as a plain "a". Control characters become \uXXXX and printable
// UTF-8 passes through, so ordinary paths stay readable in the generated
// file.
//
// Marshalling a string cannot fail; the error is checked only so a future
// change of input type cannot make it silently wrong.
func quoteForJS(s string) string {
	quoted, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(quoted)
}
