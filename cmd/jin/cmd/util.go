package cmd

import (
	"os"

	"github.com/takaaki-s/jind-ai/internal/paths"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// getConfigDir returns the user configuration directory (XDG-compliant).
func getConfigDir() string {
	return paths.Config()
}

// getStateDir returns the persistent state directory (XDG-compliant).
func getStateDir() string {
	return paths.State()
}

// envWriter is the minimal tmux surface a popup needs to hand its result back
// to the parent TUI. *tmux.Client satisfies it directly; tests inject a fake.
type envWriter interface {
	SetEnvironment(session, name, value string) error
}

// pushPopupResult writes a popup's outcome to the outer-tmux env under key, so
// the parent TUI can consume it on the next envTick. An empty selection means
// the popup was dismissed (Esc / Ctrl+C) and must write nothing at all: the
// parent acts only on a key that is set, so a dismissal has to leave the env
// untouched rather than publish a value the parent would have to interpret —
// for the confirm popup that value would be an answer to a destructive prompt
// nobody answered. tmux errors are swallowed so non-tmux invocations stay
// non-fatal (V-014).
func pushPopupResult(tc envWriter, key, selected string) {
	if selected == "" {
		return
	}
	_ = tc.SetEnvironment(tmux.SessionName, key, selected)
}

// ensureSSHAuthSockFromTmux copies SSH_AUTH_SOCK from the outer tmux server
// environment into this process when the environment inherited from the
// parent shell did not carry one. tmux popup children can start under a
// stripped environment; this restores agent access for commands that rely
// on it. No-op when SSH_AUTH_SOCK is already set or when tmux has no value.
func ensureSSHAuthSockFromTmux(tc *tmux.Client) {
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		return
	}
	if sock := tc.GetEnvironment(tmux.SessionName, "SSH_AUTH_SOCK"); sock != "" {
		_ = os.Setenv("SSH_AUTH_SOCK", sock)
	}
}
