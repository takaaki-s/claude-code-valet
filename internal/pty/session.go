package pty

import (
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Session represents a PTY session
type Session struct {
	PTY *os.File
	Cmd *exec.Cmd
}

// Run starts the interactive session
// This attaches stdin/stdout to the PTY and handles terminal resizing
func (s *Session) Run() error {
	// Make sure to close the pty at the end.
	defer func() { _ = s.PTY.Close() }()

	// Handle pty size.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, s.PTY); err != nil {
				log.Printf("error resizing pty: %s", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH                        // Initial resize.
	defer func() { signal.Stop(ch); close(ch) }() // Cleanup signals when done.

	// Set stdin in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Copy stdin to the pty and the pty to stdout.
	// Use a small buffer for stdin to ensure escape sequences are sent immediately
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				s.PTY.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	_, _ = io.Copy(os.Stdout, s.PTY)

	return nil
}

// Close closes the PTY session
func (s *Session) Close() error {
	return s.PTY.Close()
}

// Write sends data to the PTY
func (s *Session) Write(data []byte) (int, error) {
	return s.PTY.Write(data)
}

// Read reads data from the PTY
func (s *Session) Read(buf []byte) (int, error) {
	return s.PTY.Read(buf)
}
