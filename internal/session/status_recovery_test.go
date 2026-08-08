package session

import "testing"

// stoppedSession returns a session in the state a restart leaves behind: the
// dying instance's SessionEnd (or the monitor finding the pane dead) recorded
// the stop, and the replacement is about to announce itself.
//
// AgentSessionStarted is true because that is what a restart looks like — the
// agent has started before. It matters: leaving it false lets the first
// SessionStart's bookkeeping trigger the save on its own, which would hide
// whether the status correction reaches disk under its own power.
func stoppedSession(t *testing.T, mgr *Manager, workDir string) *Session {
	t.Helper()
	sess, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: workDir, Description: workDir})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.SetStatus(sess.ID, StatusStopped)
	mgr.mu.Lock()
	sess.AgentSessionStarted = true
	mgr.mu.Unlock()
	return sess
}

// TestHandleHookEvent_SessionStartClearsAStaleStop is the defect this exists
// for. A resumed agent announces itself with SessionStart; before this, the
// record stayed stopped until some later hook happened to map elsewhere, and an
// agent blocked inside one long tool call fires no such hook. Measured on a real
// session, that left a working agent reading as stopped for over half an hour —
// during which `session send` refuses it and `session wait` reports it finished.
func TestHandleHookEvent_SessionStartClearsAStaleStop(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess := stoppedSession(t, mgr, "/tmp/ss-clear")

	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "SessionStart", "", "", "")

	got, ok := mgr.Get(sess.ID)
	if !ok {
		t.Fatal("Get returned ok=false")
	}
	if got.Status != StatusIdle {
		t.Errorf("Status = %q, want %q — an agent that just started is not stopped", got.Status, StatusIdle)
	}
}

// TestHandleHookEvent_SessionStartLeavesEveryOtherStatusAlone is the guard that
// keeps the fix from becoming the same bug pointed the other way.
//
// SessionStart is not only fired at startup: Claude Code raises it for resume,
// /clear and /compact, and the generated hooks file carries no matcher, so all
// of them arrive. The thinking row is the one that matters — an auto-compaction
// mid-turn must not drop a working agent to idle, because that opens
// SendPrompt's idle gate and makes `session wait` report a turn that is still
// running as finished.
func TestHandleHookEvent_SessionStartLeavesEveryOtherStatusAlone(t *testing.T) {
	// StatusIdle is deliberately absent: an unconditional correction sets
	// idle, so an already-idle session would pass such a mutation and the row
	// would assert nothing.
	for _, status := range []Status{StatusThinking, StatusRunning, StatusPermission} {
		t.Run(string(status), func(t *testing.T) {
			mgr, _, _ := newTestManager(t)
			sess, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: "/tmp/ss-" + string(status)})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			mgr.SetStatus(sess.ID, status)

			mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "SessionStart", "", "", "")

			got, _ := mgr.Get(sess.ID)
			if got.Status != status {
				t.Errorf("Status = %q, want it left at %q", got.Status, status)
			}
		})
	}
}

// TestHandleHookEvent_SessionStartPersistsTheCorrection covers the half a
// memory-only fix would miss: the daemon can restart, and a correction that
// never reached disk would let the stale stop come back.
func TestHandleHookEvent_SessionStartPersistsTheCorrection(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess := stoppedSession(t, mgr, "/tmp/ss-persist")

	mgr.HandleHookEvent(sess.AgentSessionID, sess.ID, "SessionStart", "", "", "")

	loaded, err := mgr.store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Status != StatusIdle {
		t.Errorf("persisted Status = %q, want %q", loaded.Status, StatusIdle)
	}
}
