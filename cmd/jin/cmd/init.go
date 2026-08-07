package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// configTemplate is the default config.yaml written by `jin init`
const configTemplate = `# jin configuration
# See https://github.com/takaaki-s/jind-ai for details

# Customize keybindings (defaults are used when omitted)
keybindings:
  # Session list view
  up: ["up", "k"]
  down: ["down", "j"]
  attach: ["enter"]
  new: ["n"]
  kill: ["x"]
  delete: ["d"]
  refresh: ["r"]
  # search: keys that open the switch-session picker (fuzzy search).
  # Default is M-f (Alt+f); must be modifier-prefixed to avoid stealing
  # input from the display pane. Use ["/"] to restore the old bare-slash
  # binding (breaks agent slash-commands / vim-like search in the display pane).
  search: ["M-f"]
  vscode: ["v"]
  notifications: ["!"]
  quit: ["q", "ctrl+c"]
  help: ["?"]
  # Session creation form
  next_field: ["tab"]
  prev_field: ["shift+tab"]
  submit: ["enter"]
  cancel_form: ["esc"]
  # While attached
  # Supported keys: ctrl+^, ctrl+], ctrl+\, ctrl+g
  detach: ["ctrl+]"]

# Adapter used when 'jin session new' omits --agent.
# Leave commented to fall back to "claude". Uncomment and change to
# override (available kinds: "claude", "codex", "opencode").
# default_agent: claude

# Popup size overrides (percent-based, 1-100).
# Any omitted field falls back to the hardcoded default shown below.
# Out-of-range values log a warn and fall back silently.
# popups:
#   create:         { width: 80, height: 80 }
#   notify:         { width: 70, height: 60 }
#   session_filter: { width: 70, height: 70 }
#   help:           { width: 60, height: 60 }
#   action:         { width: 70, height: 70 }
#   # plugin_default applies to every plugin popup unless overridden per-plugin.
#   plugin_default: { width: 70, height: 70 }
#   # Per-plugin overrides beat the plugin's own manifest declaration.
#   plugins:
#     my-plugin:    { width: 40, height: 20 }
`

var (
	forceInit    bool
	initSkill    bool
	initNoSkill  bool
	initSkillDir string
	initDryRun   bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize jin configuration and optionally install the agent skill",
	Long: `Create the jin configuration directory and a default config.yaml, then
offer to install the jin skill for the agents found on your PATH.

If config.yaml already exists it is left alone unless --force is given; the
skill step still runs, so an existing install can pick it up.

The skill is a short document that teaches an agent to drive jin by reading
'jin docs'. It is shown in full before anything is written, and installing it
is opt-in: the prompt defaults to no, existing files are never replaced without
--force, and nothing is written at all when stdin is not a terminal.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		configDir := getConfigDir()
		configFile := filepath.Join(configDir, "config.yaml")

		switch {
		case !forceInit && fileExists(configFile):
			fmt.Fprintf(out, "Config already exists: %s\n", configFile)
			fmt.Fprintln(out, "Use --force to overwrite.")
		case initDryRun:
			// Checked before MkdirAll, not after: "would create" has to mean
			// the filesystem is left untouched, parent directories included.
			fmt.Fprintf(out, "--dry-run: would create %s\n", configFile)
		default:
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}
			if err := os.WriteFile(configFile, []byte(configTemplate), 0644); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}
			fmt.Fprintf(out, "Created: %s\n", configFile)
		}

		// An existing config used to end the command here. The skill step runs
		// regardless now, because the two are independent: someone who has run
		// jin for months has a config.yaml and no skill, and they are exactly
		// who this offer is for.
		return runSkillStep(cmd)
	},
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&forceInit, "force", false, "Overwrite an existing config.yaml, and allow replacing an existing skill")
	initCmd.Flags().BoolVar(&initSkill, "skill", false, "Install the agent skill without asking (works when stdin is not a terminal)")
	initCmd.Flags().BoolVar(&initNoSkill, "no-skill", false, "Skip the agent skill entirely")
	initCmd.Flags().StringVar(&initSkillDir, "skill-dir", "", "Install the skill into this directory instead of the per-agent defaults")
	initCmd.Flags().BoolVar(&initDryRun, "dry-run", false, "Show what would be written without writing anything")
	initCmd.MarkFlagsMutuallyExclusive("skill", "no-skill")
}
