package tmux

// Runner defines the interface for tmux operations used by session.Manager.
// The concrete *Client satisfies this interface.
//
// PaneSlotOps is embedded rather than restated: EnsureNamedPane takes the
// smaller interface and session.Manager hands it a Runner, so the superset
// relation is load-bearing and belongs to the compiler.
type Runner interface {
	PaneSlotOps

	HasSession(name string) bool
	KillSession(name string) error
	NewSessionWithCmdInDir(name string, width, height int, dir, cmd string) error
	GetPaneID(sessionName string) (string, error)
	IsPaneDead(target string) bool
	TagManagedPane(paneID string) error
	SetupAutoCleanDeadPanes() error
	TerminatePaneProcess(target string) error
	GetPaneCurrentPath(target string) (string, error)
	SendKeys(target, keys string) error
	SendKeysLiteral(target, text string) error
	LoadBuffer(name, content string) error
	PasteBuffer(target, name string) error
	DisplayPopup(opts DisplayPopupOptions) error
	CapturePane(target string, ansi bool) (string, error)
}
