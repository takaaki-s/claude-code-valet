package tmux

// Runner defines the interface for tmux operations used by session.Manager.
// The concrete *Client satisfies this interface.
type Runner interface {
	HasSession(name string) bool
	KillSession(name string) error
	NewSessionWithCmdInDir(name string, width, height int, dir, cmd string) error
	// RespawnPane replaces the process running in a pane. env is KEY=VALUE
	// assignments the new process gets, one -e each; a nil env says this pane
	// is not a process that calls back into jin, so it may keep whatever the
	// tmux server gave it.
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
