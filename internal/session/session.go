package session

import (
	"sort"
	"time"
)

// DefaultFleet is the fleet name used when no fleet is specified.
const DefaultFleet = "default"

// Status represents the session status
type Status string

const (
	// StatusCreating covers both worktree/session provisioning (before the
	// agent has been launched) and the agent's own startup window (before the
	// first hook arrives). The union is intentional: both are "not yet usable"
	// and the UI treats them identically, so one status keeps the state
	// machine small.
	StatusCreating   Status = "creating"
	StatusStopped    Status = "stopped"    // Process stopped
	StatusRunning    Status = "running"    // Running (details unknown)
	StatusIdle       Status = "idle"       // Waiting for input (Stop hook)
	StatusThinking   Status = "thinking"   // Processing (UserPromptSubmit hook)
	StatusPermission Status = "permission" // Waiting for permission (Notification hook)
	// StatusDeleting marks a session whose deletion has been accepted but not
	// yet finalized. The daemon handles delete asynchronously so `git worktree
	// remove` (an rm -rf of a checkout) does not hold up the client; the
	// session sits in this state from the moment the request is accepted
	// until either the record is dropped (success) or the status flips back
	// to Stopped with ErrorMessage set (failure).
	StatusDeleting Status = "deleting"
)

// Session represents an agent session managed by jind-ai. The concrete agent
// (Claude Code, Codex CLI, ...) is identified by AgentKind and driven through
// the interfaces in agent_types.go.
type Session struct {
	ID                string    `json:"id"`
	Description       string    `json:"description"`
	DescriptionLocked bool      `json:"description_locked,omitempty"`
	WorkDir           string    `json:"work_dir"`
	CreatedAt         time.Time `json:"created_at"`
	Status            Status    `json:"status"`

	// Last active time (persisted)
	LastActiveAt time.Time `json:"last_active_at,omitzero"`

	// Error info
	ErrorMessage string `json:"error_message,omitempty"` // Error message

	// CreationWarning carries a non-fatal message produced during async
	// provisioning (e.g. "post-create hook detected but not allowed"). It is
	// distinct from ErrorMessage — a warning does not fail the creation, and
	// the session becomes usable regardless — but the client still needs a
	// way to see it, so it lives on the record and is surfaced through Info
	// on every `get`. It is intentionally not cleared automatically: the
	// warning was true at creation time, and clearing it would erase the
	// audit trail; the record is dropped when the session is deleted.
	CreationWarning string `json:"creation_warning,omitempty"`

	// AgentKind identifies the adapter (registry key) that owns this session.
	// Always non-empty in persisted form; the store migration backfills legacy
	// records with "claude".
	AgentKind string `json:"agent_kind"`
	// AgentSessionID is the adapter-side persistent identifier (Claude Code's
	// --session-id / --resume UUID, for example). Kept alongside AgentKind so
	// the same field can serve every adapter.
	AgentSessionID string `json:"agent_session_id,omitempty"`
	// AgentSessionStarted is true once the agent has been launched at least
	// once with AgentSessionID; adapters use it to switch between "start" and
	// "resume" command lines.
	AgentSessionStarted bool `json:"agent_session_started,omitempty"`

	// Fleet grouping
	Fleet string `json:"fleet"` // Fleet name for session grouping

	// tmux integration
	TmuxWindowName string `json:"tmux_window_name,omitempty"` // tmux window name for this session
	TmuxPaneID     string `json:"tmux_pane_id,omitempty"`     // CC pane ID (e.g., "%42") for capture-pane

	// Runtime fields (not persisted)
	LastOutputTime   time.Time        `json:"-"` // Last PTY output received (for idle stability detection)
	StartedAt        time.Time        `json:"-"` // Process start time (prevents false error detection right after startup)
	SSHAuthSock      string           `json:"-"` // SSH_AUTH_SOCK (for git operations, not persisted)
	DescriptionLayer DescriptionLayer `json:"-"` // Runtime-only enhancer layer; see DescriptionLayer docs + TryUpgradeDescription's restart guard
	PersistedStatus  Status           `json:"-"` // Status read from disk at load time, before the in-memory normalization to Stopped; consumed once by recovery
	// killSeq counts the stops Kill has recorded for this session. Unexported
	// and runtime-only: its whole job is to let code that dropped m.mu tell
	// whether a Kill landed while it was away. TmuxWindowName used to answer
	// that (a kill cleared it), but a kill now keeps the window standing so
	// the session can be revived in place, and a session reloaded from disk
	// looks Stopped whether or not anyone killed it — neither can be read as
	// evidence any more.
	//
	// Kill is the only writer. Stops from other sources (a hook reporting the
	// agent exited, the monitor finding the pane dead) do not bump it: those
	// are observations of a session that stopped by itself, and the next
	// monitor tick reconverges anything a stale decision got wrong.
	killSeq uint64

	// Tracked runtime fields (CurrentWorkDir is persisted so worktree/subdir
	// context survives daemon restarts and enables resume in the last known dir).
	CurrentWorkDir string `json:"current_work_dir,omitempty"` // Current working directory (tmux pane_current_path)
	CurrentBranch  string `json:"-"`                          // Current git branch
	IsGitRepo      bool   `json:"-"`                          // Whether CurrentWorkDir is inside a git repository
	IsWorktree     bool   `json:"-"`                          // Whether CurrentWorkDir is a git worktree (not the main repo)
	// RepoName is the human-facing repository name (ResolveRepoName): for a
	// worktree it is the main repo's name, not the worktree directory's.
	// Runtime-only for the same reason as CurrentBranch — it describes where
	// the agent currently is, which the next poll re-derives — but unlike the
	// branch it is also seeded at create, provision and load time so stopped
	// and freshly-restored sessions still have one.
	RepoName string `json:"-"`
}

// Info returns session information for display
type Info struct {
	ID                string    `json:"id"`
	Description       string    `json:"description"`
	DescriptionLocked bool      `json:"description_locked,omitempty"`
	WorkDir           string    `json:"work_dir"`
	Status            Status    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	LastActiveAt      time.Time `json:"last_active_at,omitzero"`
	ErrorMessage      string    `json:"error_message,omitempty"`
	CreationWarning   string    `json:"creation_warning,omitempty"` // Non-fatal warning from async provisioning (see Session.CreationWarning)
	AgentKind         string    `json:"agent_kind,omitempty"`       // Adapter identifier ("claude" etc.)
	AgentSessionID    string    `json:"agent_session_id,omitempty"` // Adapter-side persistent session id (transcript lookup, resume)
	TmuxWindowName    string    `json:"tmux_window_name,omitempty"` // tmux window name
	Fleet             string    `json:"fleet"`                      // Fleet name for session grouping

	// Tracked fields (dynamic, from daemon polling)
	CurrentWorkDir string `json:"current_work_dir,omitempty"` // Current working directory
	CurrentBranch  string `json:"current_branch,omitempty"`   // Current git branch
	RepoName       string `json:"repo_name,omitempty"`        // Repository name; the main repo's for a worktree (see Session.RepoName)
	IsWorktree     bool   `json:"is_worktree,omitempty"`      // Whether WorkDir is a git worktree

	// Last messages from transcript
	LastUserMessage      string `json:"last_user_message,omitempty"`      // Last user message content (truncated)
	LastAssistantMessage string `json:"last_assistant_message,omitempty"` // Last assistant message content (truncated)
}

// SortInfos sorts a slice of Info by Fleet (lexicographically, DefaultFleet last),
// then by CreatedAt (oldest first). This is the canonical sort order used
// throughout the application. This function sorts the slice in-place.
func SortInfos(infos []Info) {
	sort.SliceStable(infos, func(i, j int) bool {
		fi, fj := infos[i].Fleet, infos[j].Fleet
		if fi != fj {
			// DefaultFleet always sorts last
			if fi == DefaultFleet {
				return false
			}
			if fj == DefaultFleet {
				return true
			}
			return fi < fj
		}
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})
}

// ToInfo converts Session to Info
func (s *Session) ToInfo() Info {
	return Info{
		ID:                s.ID,
		Description:       s.Description,
		DescriptionLocked: s.DescriptionLocked,
		WorkDir:           s.WorkDir,
		Status:            s.Status,
		CreatedAt:         s.CreatedAt,
		LastActiveAt:      s.LastActiveAt,
		ErrorMessage:      s.ErrorMessage,
		CreationWarning:   s.CreationWarning,
		AgentKind:         s.AgentKind,
		AgentSessionID:    s.AgentSessionID,
		TmuxWindowName:    s.TmuxWindowName,
		Fleet:             s.Fleet,
		CurrentWorkDir:    s.CurrentWorkDir,
		CurrentBranch:     s.CurrentBranch,
		RepoName:          s.RepoName,
		IsWorktree:        s.IsWorktree,
	}
}
