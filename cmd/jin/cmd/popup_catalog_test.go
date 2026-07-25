package cmd

import (
	"testing"

	"github.com/takaaki-s/jind-ai/internal/config"
)

// TestPopupCatalog_SubcmdsRegistered closes the catalog↔cobra gap: the TUI
// spawns `jin <spec.Subcmd>` inside a tmux popup and swallows the failure, so
// an entry naming a command nobody registered opens a popup that prints
// "unknown command" and disappears — with every unit test still green, since
// nothing else joins the two halves.
//
// Walking config.PopupNames() rather than a list here is the point: a popup
// added to the catalog is covered the moment it exists.
func TestPopupCatalog_SubcmdsRegistered(t *testing.T) {
	for _, name := range config.PopupNames() {
		subcmd := config.PopupSubcmd(name)
		if subcmd == "" {
			// plugin_default is a size-resolver tier, not a popup: it has no
			// command to spawn.
			continue
		}
		t.Run(name, func(t *testing.T) {
			// Find returns the root command for an unresolved name, so the name
			// check below is what actually asserts registration.
			cmd, _, err := rootCmd.Find([]string{subcmd})
			if err != nil {
				t.Fatalf("rootCmd.Find(%q): %v", subcmd, err)
			}
			if cmd == nil || cmd.Name() != subcmd {
				t.Fatalf("popup %q spawns %q, which is not registered on rootCmd (got %v)", name, subcmd, cmd)
			}
			if !cmd.Hidden {
				t.Errorf("%q should be Hidden: it is meaningless outside the popup the TUI opens", subcmd)
			}
			if cmd.RunE == nil {
				t.Errorf("%q has no RunE, so the popup would print help text instead of running", subcmd)
			}
		})
	}
}
