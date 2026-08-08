package session

import (
	"testing"
	"time"
)

// A hook that reports liveness is not a hook that reports a turn. These tests
// pin what Manager does with the difference.
//
// The sequence they replay is not invented. In a real hook-event log — 3587
// events across 26 sessions — 8 of 158 turns had a tool hook land after the
// Stop that ended them, with no prompt in between, 1.1s to 5.3s late. Those
// hooks belonged to subagents outliving the turn that spawned them (78 of 78
// matched a subagent's tool result within 0.01s), and the turns really were
// over: where the agent's own idle notification followed, it fired 60.04s
// after the Stop (n=5, spread 8ms) rather than after the straggler. All 158
// turns contained a UserPromptSubmit, and in 150 it was the first event that
// would have moved the session off idle — the 8 exceptions being these
// stragglers.

// idleSession returns a session in the state a finished turn leaves behind.
// The idle comes from the Stop hook rather than an assignment, so the tests
// below start where the production path actually puts a session. Shaped after
// stoppedSession in status_recovery_test.go, which does the same job for the
// status a restart leaves behind.
func idleSession(t *testing.T, mgr *Manager, workDir string) *Session {
	t.Helper()
	sess, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: workDir, Description: workDir})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "Stop", "", "", "")
	if got, _ := mgr.Get(sess.ID); got.Status != StatusIdle {
		t.Fatalf("setup: Status = %q after Stop, want %q", got.Status, StatusIdle)
	}
	return sess
}

// TestManager_HandleHookEvent_LivenessDoesNotLeaveIdle is the defect. A tool
// hook that arrives after the Stop used to write "thinking" over a session the
// agent had already finished, and nothing re-derived it — the agent's idle
// notification was what eventually corrected it, up to a minute later.
//
// Memory and disk are both checked here rather than in two tests: the daemon
// can restart, so a "thinking" that reached the session file would come back
// after it did, and the two answers are only worth anything together.
func TestManager_HandleHookEvent_LivenessDoesNotLeaveIdle(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess := idleSession(t, mgr, "/tmp/liveness-idle")

	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "PostToolUse", "", "", "")

	got, ok := mgr.Get(sess.ID)
	if !ok {
		t.Fatal("Get returned ok=false")
	}
	if got.Status != StatusIdle {
		t.Errorf("Status = %q, want %q — a tool that finished before the turn did "+
			"does not put the session back to work", got.Status, StatusIdle)
	}
	loaded, err := mgr.store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Status != StatusIdle {
		t.Errorf("persisted Status = %q, want %q", loaded.Status, StatusIdle)
	}
}

// TestSendPrompt_AcceptedAfterAStragglerToolHook is why any of this matters.
// The orchestration loop is send → wait → result: `session wait --status idle`
// returns on the Stop, and the send that follows it used to be refused by a
// status the straggler had already rewritten.
func TestSendPrompt_AcceptedAfterAStragglerToolHook(t *testing.T) {
	mgr, mock, _ := newTestManager(t)
	withShortSendVerify(t, 2*time.Second, time.Millisecond, time.Millisecond)
	const pane = "%straggler"
	sess := newIdleSessionWithPane(t, mgr, "/tmp/liveness-send", "straggler", pane)
	mock.capturedSequence[pane] = []string{"$ ", "$ carry on"}

	// Run a turn and end it, so the idle the send meets is the one a Stop
	// produced. newIdleSessionWithPane assigns idle directly, which would let
	// this pass without the Stop meaning anything.
	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "UserPromptSubmit", "", "", "")
	if got, _ := mgr.Get(sess.ID); got.Status != StatusThinking {
		t.Fatalf("setup: Status = %q, want %q", got.Status, StatusThinking)
	}
	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "Stop", "", "", "")
	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "PostToolUse", "", "", "")

	if err := mgr.SendPrompt(sess.ID, "carry on"); err != nil {
		t.Fatalf("SendPrompt returned err=%v, want nil — the agent was idle; "+
			"only a late hook said otherwise", err)
	}
}

// TestManager_HandleHookEvent_LivenessStillAppliesFromNonIdleStatuses is the guard
// that keeps the rule from becoming the same bug pointed the other way. Two
// rows carry weight beyond coverage:
//
//   - permission is how an approved tool gets a session working again. Claude
//     Code raises no event when the user answers the prompt; the next tool
//     hook is the whole signal.
//   - stopped is the stale-stop case a sibling fix already had to correct
//     once. A hook from a live agent contradicts "the process is gone", and
//     that has nothing to do with whether a turn began.
//
// StatusIdle is deliberately absent — it has its own test above. So is
// StatusDeleting, which is not "every other status" but a separate question:
// a hook verdict resurrects a session the user asked to remove, and it did so
// before this rule as well. Out of scope rather than covered.
func TestManager_HandleHookEvent_LivenessStillAppliesFromNonIdleStatuses(t *testing.T) {
	for _, status := range []Status{StatusPermission, StatusStopped, StatusRunning, StatusCreating, StatusThinking} {
		t.Run(string(status), func(t *testing.T) {
			mgr, _, _ := newTestManager(t)
			sess, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: "/tmp/liveness-" + string(status)})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			mgr.SetStatus(sess.ID, status)

			mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "PostToolUse", "", "", "")

			got, _ := mgr.Get(sess.ID)
			if got.Status != StatusThinking {
				t.Errorf("Status = %q, want %q — the rule withholds one transition, not the verdict",
					got.Status, StatusThinking)
			}
		})
	}
}

// TestManager_HandleHookEvent_APromptStillLeavesIdle is the other half of that guard.
// UserPromptSubmit is the event that opens a turn, and a rule that caught it
// too would leave every session reading idle while it worked — which is the
// failure `session wait` cannot distinguish from a finished turn.
func TestManager_HandleHookEvent_APromptStillLeavesIdle(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess := idleSession(t, mgr, "/tmp/liveness-prompt")

	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "UserPromptSubmit", "", "", "")

	got, _ := mgr.Get(sess.ID)
	if got.Status != StatusThinking {
		t.Errorf("Status = %q, want %q", got.Status, StatusThinking)
	}
}

// TestManager_HandleHookEvent_WithheldLivenessLeavesTheErrorMessage covers the
// rest of the withheld verdict. ClearError means "the agent took a new turn",
// which is the claim being rejected, so the message from the last turn has to
// survive until one really starts.
//
// It is also the half that would diverge silently: nothing saves a record
// whose status did not move, so clearing the field here would drop the message
// from memory while the session file still carried it — and a daemon restart
// would bring it back.
func TestManager_HandleHookEvent_WithheldLivenessLeavesTheErrorMessage(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: "/tmp/liveness-errmsg"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// StopFailure ends the turn idle and leaves the reason on the record.
	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "StopFailure", "", "", "rate_limit")
	if got, _ := mgr.Get(sess.ID); got.Status != StatusIdle || got.ErrorMessage != "rate_limit" {
		t.Fatalf("setup: Status=%q ErrorMessage=%q, want idle / rate_limit", got.Status, got.ErrorMessage)
	}

	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "PostToolUse", "", "", "")

	got, _ := mgr.Get(sess.ID)
	if got.ErrorMessage != "rate_limit" {
		t.Errorf("ErrorMessage = %q, want %q — the turn that failed is still the last one",
			got.ErrorMessage, "rate_limit")
	}
	loaded, err := mgr.store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ErrorMessage != got.ErrorMessage {
		t.Errorf("persisted ErrorMessage = %q but memory holds %q — the two must not diverge",
			loaded.ErrorMessage, got.ErrorMessage)
	}
}

// TestManager_HandleHookEvent_WithheldLivenessStillMarksTheAgentAlive keeps the rule
// scoped to the verdict. The hook did arrive, so the agent is demonstrably up, and
// LastOutputTime is what the monitor's "no hook for 30s" fallback reads.
func TestManager_HandleHookEvent_WithheldLivenessStillMarksTheAgentAlive(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess := idleSession(t, mgr, "/tmp/liveness-lastoutput")

	mgr.mu.Lock()
	sess.LastOutputTime = time.Now().Add(-time.Hour)
	mgr.mu.Unlock()

	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "PostToolUse", "", "", "")

	got, _ := mgr.Get(sess.ID)
	if time.Since(got.LastOutputTime) > time.Minute {
		t.Errorf("LastOutputTime = %v, want it moved to now — the hook is evidence "+
			"the agent is alive even when it may not change the status", got.LastOutputTime)
	}
}
