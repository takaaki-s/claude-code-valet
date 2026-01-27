package pty

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Manager handles PTY operations
type Manager struct{}

// NewManager creates a new PTY manager
func NewManager() *Manager {
	return &Manager{}
}

// Spawn creates a new PTY session with the given command
func (m *Manager) Spawn(cmd string, args []string, workDir string) (*Session, error) {
	// Create command
	c := exec.Command(cmd, args...)
	c.Dir = workDir
	// Inherit all environment variables and ensure proper terminal settings
	env := os.Environ()
	envMap := make(map[string]bool)
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				envMap[e[:i]] = true
				break
			}
		}
	}
	// Ensure TERM is set for proper terminal emulation
	if !envMap["TERM"] {
		env = append(env, "TERM=xterm-256color")
	}
	// Ensure COLORTERM for truecolor support
	if !envMap["COLORTERM"] {
		env = append(env, "COLORTERM=truecolor")
	}
	// Force interactive mode and color support
	if !envMap["FORCE_COLOR"] {
		env = append(env, "FORCE_COLOR=1")
	}
	// Remove problematic environment variables (like node-pty does)
	// This prevents terminal multiplexer interference and conflicting settings
	removeVars := map[string]bool{
		"TMUX":      true,
		"TMUX_PANE": true,
		"STY":       true, // screen
		"WINDOW":    true,
		"WINDOWID":  true,
		"TERMCAP":   true,
		"COLUMNS":   true, // Let PTY determine size
		"LINES":     true, // Let PTY determine size
		"CI":        true, // Non-interactive mode
	}
	newEnv := make([]string, 0, len(env))
	for _, e := range env {
		varName := ""
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				varName = e[:i]
				break
			}
		}
		if removeVars[varName] {
			continue
		}
		newEnv = append(newEnv, e)
	}
	c.Env = newEnv

	// Get current terminal size
	cols, rows := 80, 24
	if term.IsTerminal(int(os.Stdin.Fd())) {
		w, h, err := term.GetSize(int(os.Stdin.Fd()))
		if err == nil {
			cols, rows = w, h
		}
	}

	// Start command with PTY at initial size
	ptmx, err := pty.StartWithSize(c, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		return nil, err
	}

	return &Session{
		PTY: ptmx,
		Cmd: c,
	}, nil
}
