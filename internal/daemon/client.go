package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/takaaki-s/claude-code-valet/internal/session"
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

