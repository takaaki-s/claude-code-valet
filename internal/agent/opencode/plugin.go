package opencode

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
// moved binary — ExecPath is fixed for the daemon's lifetime, resolved once at
// startup; a new binary means a new daemon.) The write costs well under a
// millisecond next to spawning tmux and the agent itself.
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
