package codex

import (
	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/session"
)

// HookStatusSource translates Codex hook payloads into StatusUpdates.
//
// Manager builds StatusSignal{Kind:"hook", Payload:{event, notification_type,
// stop_reason, cwd}} from the on-wire HookRequest. Codex's hook_event_name
// values line up with Claude Code's set for the events jind-ai cares about
// (SessionStart / UserPromptSubmit / Stop), so no cross-vocabulary translation
// happens here.
//
// A returned bool=false means "meaningful but no status change" — Manager still
// runs the agent-agnostic side effects.
type HookStatusSource struct{}

// NewHookStatusSource constructs the interpreter. Stateless, but held as a
// pointer so a future adapter can layer in memoisation or per-session state
// without changing callers.
func NewHookStatusSource() *HookStatusSource { return &HookStatusSource{} }

// Interpret implements session.StatusSource.
//
//	SessionStart       (zero, false)          side effects only (AgentSessionStarted, re-key, Layer C)
//	UserPromptSubmit   thinking + ClearError  canonical progression signal
//	PreToolUse         thinking + ClearError  doubles as PermissionRequest → thinking recovery
//	PostToolUse        thinking + ClearError  liveness signal during long turns
//	PermissionRequest  permission             ~= CC's Notification{permission_prompt}
//	Stop               idle + ClearError + task-complete
//
// Codex has no SessionEnd / StopFailure event surface today; StatusStopped is
// driven by the pane-death path in captureOutputTmux.
func (h *HookStatusSource) Interpret(sig agent.StatusSignal) (agent.StatusUpdate, bool) {
	if sig.Kind != "hook" {
		return agent.StatusUpdate{}, false
	}
	switch sig.Payload["event"] {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		return agent.StatusUpdate{
			Status:     session.StatusThinking,
			ClearError: true,
			Notify:     agent.NotifyNone,
		}, true
	case "PermissionRequest":
		return agent.StatusUpdate{
			Status: session.StatusPermission,
			Notify: agent.NotifyPermission,
		}, true
	case "Stop":
		return agent.StatusUpdate{
			Status:     session.StatusIdle,
			ClearError: true,
			Notify:     agent.NotifyTaskComplete,
		}, true
	}
	// SessionStart / unknown events — no status change, but Manager still
	// uses them (SessionStart marks AgentSessionStarted, re-keys
	// AgentSessionID with the real Codex UUID, and triggers Layer C).
	return agent.StatusUpdate{}, false
}
