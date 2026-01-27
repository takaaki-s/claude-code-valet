package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/takaaki-s/claude-code-valet/internal/session"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Client is the daemon client
type Client struct {
	socketPath string
}

// NewClient creates a new daemon client
func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath}
}

// IsRunning checks if the daemon is running
func (c *Client) IsRunning() bool {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (c *Client) send(req Request) (*Response, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("daemon not running. Start with: ccvalet daemon")
	}
	defer conn.Close()

	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(req); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(conn)
	var resp Response
	if err := decoder.Decode(&resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// NewOptions contains options for creating a new session
type NewOptions struct {
	Name          string
	WorkDir       string
	Start         bool
	Async         bool // If true, returns immediately with creating status
	PromptName    string
	PromptArgs    string
	Repository    string
	Branch        string
	BaseBranch    string
	NewBranch     bool   // If true, creates a new branch
	IsNewWorktree bool   // If true, creates a new worktree
	WorktreeName  string // Worktree name (directory name)
}

// New creates a new session
func (c *Client) New(name, workDir string, start bool) (*session.Info, error) {
	return c.NewWithOptions(NewOptions{
		Name:    name,
		WorkDir: workDir,
		Start:   start,
	})
}

// NewWithOptions creates a new session with full options
func (c *Client) NewWithOptions(opts NewOptions) (*session.Info, error) {
	data, _ := json.Marshal(NewRequest{
		Name:          opts.Name,
		WorkDir:       opts.WorkDir,
		Start:         opts.Start,
		Async:         opts.Async,
		PromptName:    opts.PromptName,
		PromptArgs:    opts.PromptArgs,
		Repository:    opts.Repository,
		Branch:        opts.Branch,
		BaseBranch:    opts.BaseBranch,
		NewBranch:     opts.NewBranch,
		IsNewWorktree: opts.IsNewWorktree,
		WorktreeName:  opts.WorktreeName,
	})

	resp, err := c.send(Request{Action: "new", Data: data})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var info session.Info
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// List lists all sessions
func (c *Client) List() ([]session.Info, error) {
	resp, err := c.send(Request{Action: "list"})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var sessions []session.Info
	if err := json.Unmarshal(resp.Data, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// Start starts a session
func (c *Client) Start(id string) error {
	data, _ := json.Marshal(IDRequest{ID: id})
	resp, err := c.send(Request{Action: "start", Data: data})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// Kill kills a session
func (c *Client) Kill(id string) error {
	data, _ := json.Marshal(IDRequest{ID: id})
	resp, err := c.send(Request{Action: "kill", Data: data})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// Delete deletes a session
func (c *Client) Delete(id string) error {
	data, _ := json.Marshal(IDRequest{ID: id})
	resp, err := c.send(Request{Action: "delete", Data: data})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// Stop stops the daemon
func (c *Client) Stop() error {
	resp, err := c.send(Request{Action: "stop"})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// Attach attaches to a session interactively
// detachKey is the byte value of the key to detach (e.g., 0x1d for Ctrl+])
// detachKeyHint is the human-readable hint (e.g., "Ctrl+]")
// detachKeyCSIu is the CSI u sequence for the key (e.g., "\x1b[93;5u" for Ctrl+])
func (c *Client) Attach(id string, detachKey byte, detachKeyHint string, detachKeyCSIu []byte) error {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("daemon not running. Start with: ccvalet daemon")
	}
	defer conn.Close()

	// Send attach request
	data, _ := json.Marshal(IDRequest{ID: id})
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(Request{Action: "attach", Data: data}); err != nil {
		return err
	}

	// Read response
	decoder := json.NewDecoder(conn)
	var resp Response
	if err := decoder.Decode(&resp); err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}

	// Get any buffered data that the JSON decoder read ahead
	bufferedReader := decoder.Buffered()

	// Set up terminal
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() {
		// Disable CSI u (progressive keyboard enhancement) before restoring terminal
		// This tells the terminal to stop sending CSI u sequences
		os.Stdout.WriteString("\x1b[?u")    // Pop keyboard mode (if supported)
		os.Stdout.WriteString("\x1b[<u")    // Disable CSI u mode
		os.Stdout.WriteString("\x1b[>4;0m") // Reset modifyOtherKeys

		// Small delay to let terminal process the mode changes
		time.Sleep(50 * time.Millisecond)

		// Drain any pending input
		drainStdin()

		term.Restore(int(os.Stdin.Fd()), oldState)
		// Reset terminal to sane state to fix any leftover mode issues
		resetTerminal()
	}()

	// Switch to alternate screen buffer
	os.Stdout.WriteString("\x1b[?1049h") // Enter alternate screen
	os.Stdout.WriteString("\x1b[H")      // Move cursor to home
	defer os.Stdout.WriteString("\x1b[?1049l") // Exit alternate screen on return

	// Set terminal title with detach hint
	os.Stdout.WriteString(fmt.Sprintf("\x1b]0;ccvalet | %s to detach\x07", detachKeyHint))

	// Track current size for change detection
	var lastCols, lastRows int

	// sendResize sends a resize command to the daemon if size changed
	sendResize := func() {
		if cols, rows, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
			if cols != lastCols || rows != lastRows {
				lastCols, lastRows = cols, rows
				// Send resize command: \x00\x00RESIZE:cols:rows\x00\x00
				resizeCmd := fmt.Sprintf("\x00\x00RESIZE:%d:%d\x00\x00", cols, rows)
				conn.Write([]byte(resizeCmd))
			}
		}
	}

	// Handle window size changes via SIGWINCH
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	go func() {
		for range winchCh {
			sendResize()
		}
	}()
	defer func() { signal.Stop(winchCh); close(winchCh) }()

	// Poll for size changes (for tmux and other cases where SIGWINCH isn't delivered)
	resizeStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sendResize()
			case <-resizeStop:
				return
			}
		}
	}()
	defer close(resizeStop)

	// Initial resize
	sendResize()

	// Stream I/O
	stdinDone := make(chan struct{})
	connDone := make(chan struct{})
	detached := make(chan struct{})

	// Read from stdin with detach detection
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 1024)

		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			data := buf[:n]

			// Check for CSI u sequence (iTerm2 and other CSI u enabled terminals)
			if len(detachKeyCSIu) > 0 && bytes.Contains(data, detachKeyCSIu) {
				close(detached)
				return
			}

			// Check for traditional detach key (raw byte)
			for i := range n {
				if buf[i] == detachKey {
					close(detached)
					return
				}
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}()

	// Read from connection
	go func() {
		defer close(connDone)
		// First, write any buffered data from the JSON decoder
		io.Copy(os.Stdout, bufferedReader)
		// Then continue reading from connection
		io.Copy(os.Stdout, conn)
	}()

	select {
	case <-connDone:
		// Session ended or connection closed
		conn.Close()
		// Don't wait for stdinDone - it will exit on next input attempt
		return nil
	case <-detached:
		// User requested detach - close connection to stop goroutines
		conn.Close()
		<-connDone // Wait for conn reader to finish
	}

	return nil
}

// drainStdin reads and discards all pending data from stdin
func drainStdin() {
	fd := int(os.Stdin.Fd())

	// Drain any pending data using non-blocking read
	unix.SetNonblock(fd, true)
	defer unix.SetNonblock(fd, false)

	buf := make([]byte, 256)
	for {
		n, err := syscall.Read(fd, buf)
		if n <= 0 || err != nil {
			break
		}
	}
}

// resetTerminal resets the terminal state using stty sane
func resetTerminal() {
	// Reset terminal modes that TUI apps may have enabled
	os.Stdout.WriteString("\x1b[?2004l") // Disable bracketed paste mode
	os.Stdout.WriteString("\x1b[?1l")    // Disable application cursor keys
	os.Stdout.WriteString("\x1b[?1000l") // Disable mouse tracking
	os.Stdout.WriteString("\x1b[?1002l") // Disable mouse button tracking
	os.Stdout.WriteString("\x1b[?1003l") // Disable all mouse tracking
	os.Stdout.WriteString("\x1b[?1004l") // Disable focus events
	os.Stdout.WriteString("\x1b[?25h")   // Show cursor
	os.Stdout.WriteString("\x1b[0m")     // Reset text attributes

	// Reset keyboard modes
	os.Stdout.WriteString("\x1b[?66l")   // Disable numeric keypad mode
	os.Stdout.WriteString("\x1b>")       // Normal keypad mode
	os.Stdout.WriteString("\x1b[?1036l") // Disable metaSendsEscape

	// Use stty sane for additional cleanup
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}
