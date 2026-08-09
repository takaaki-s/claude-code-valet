package tmux

// Runner defines the interface for tmux operations used by session.Manager.
// The concrete *Client satisfies this interface.
type Runner interface {
	HasSession(name string) bool
	KillSession(name string) error
	NewSessionWithCmdInDir(name string, width, height int, dir, cmd string) error
	// RespawnPane replaces the process running in a pane. env is KEY=VALUE
	// assignments the new process gets, one -e each.
	//
	// A nil env says only that this call is not where the pane is told which
	// jin to call back into — either because it does not (a placeholder, a
	// nested attach), or because the command string carries the assignments
	// itself, which is what an agent pane's does. It does not say the pane is
	// harmless: omitting a key here leaves the pane whatever the tmux server
	// holds, so nil is a claim that something else has already answered.
	RespawnPane(target, cmd string, env []string) error
	GetPaneID(sessionName string) (string, error)
	IsPaneDead(target string) bool
	TagManagedPane(paneID string) error
	SetupAutoCleanDeadPanes() error
	KillPane(paneID string) error
	TerminatePaneProcess(target string) error
	GetPaneCurrentPath(target string) (string, error)
	SendKeys(target, keys string) error
	SendKeysLiteral(target, text string) error
	LoadBuffer(name, content string) error
	PasteBuffer(target, name string) error
	DisplayPopup(opts DisplayPopupOptions) error
	SplitPane(target string, opts SplitOptions) (string, error)
	FindPaneByName(target, name string) (string, error)
	SetPaneOption(target, option, value string) error
	CapturePane(target string, ansi bool) (string, error)
}
