package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The re-key path in HandleHookEvent is the one place a value chosen outside
// jind-ai becomes a session's identity. It cannot be removed — Codex and
// opencode both mint their own id and report it through a hook, so a session
// that refused every re-key could never be resumed — which is why it is gated
// rather than closed.
//
// The tests below cover both halves of the gate and, just as importantly, the
// blast radius of a refusal: the id write is dropped and nothing else is.

const (
	rekeyValidID   = "0198f1b2-4c3d-7a1e-8b2f-000000000abc"
	rekeyForeignID = "ses_084426f78ffeXBrPh5ABEu2dNX"
)

// newRekeySession creates a session on the fake "claude" adapter and returns
// it. Callers that care about the adapter predicate set recognizesFn
// themselves; the fake accepts everything by default.
func newRekeySession(t *testing.T, mgr *Manager, desc string) *Session {
	t.Helper()
	sess, _, err := mgr.CreateWithOptions(CreateOptions{WorkDir: "/tmp/" + desc, Description: desc})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	return sess
}

// TestHandleHookEvent_ReKeysAValidID is the regression that matters most. Codex
// and opencode learn their own id only through this path, so a gate that closed
// it would leave every session of those kinds permanently unresumable — a
// failure the security tests below would not notice.
func TestHandleHookEvent_ReKeysAValidID(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	fakeClaudeAgent(t, mgr).recognizesFn = func(id string) bool { return id == rekeyValidID }
	sess := newRekeySession(t, mgr, "rekey-ok")

	mgr.HandleHookEvent(rekeyValidID, sess.ID, "SessionStart", "", "", "")

	got, _ := mgr.Get(sess.ID)
	if got.AgentSessionID != rekeyValidID {
		t.Errorf("AgentSessionID = %q, want %q — a valid re-key was refused", got.AgentSessionID, rekeyValidID)
	}
}

// TestHandleHookEvent_RefusesAnUnsafeID covers the kind-independent gate. Each
// id here was executable once recorded: an adapter used to concatenate the
// value into SpawnPlan.Command, which Manager splices into `SHELL -ic '...'`,
// so the substitution ran the next time that session resumed.
func TestHandleHookEvent_RefusesAnUnsafeID(t *testing.T) {
	// Two values, not a corpus: the alphabet belongs to
	// TestSafeAgentSessionID_*, and what this test adds is that Manager
	// consults the gate at all. One value per refusal reason keeps both
	// branches of rejectAgentSessionID on this path.
	for _, hostile := range []string{
		"ses_x$(touch /tmp/jin-should-not-exist)",
		strings.Repeat("a", maxAgentSessionIDLen+1),
	} {
		t.Run(hostile, func(t *testing.T) {
			mgr, _, _ := newTestManager(t)
			// Set explicitly rather than left to the fake's default: this
			// test's claim is that a refusal came from the SAFETY gate, and
			// that only follows if the adapter is known to accept. An adapter
			// predicate is allowed to be loose (opencode's answers on the
			// ses_ prefix alone), so this is the arrangement worth pinning.
			fakeClaudeAgent(t, mgr).recognizesFn = func(string) bool { return true }
			sess := newRekeySession(t, mgr, "rekey-unsafe")
			before := sess.AgentSessionID

			mgr.HandleHookEvent(hostile, sess.ID, "SessionStart", "", "", "")

			got, _ := mgr.Get(sess.ID)
			if got.AgentSessionID != before {
				t.Errorf("AgentSessionID = %q, want the pre-minted %q", got.AgentSessionID, before)
			}
		})
	}
}

// TestHandleHookEvent_RefusesAForeignShape covers the kind-specific gate, and
// with it the wiring: the value is safe by every character test, so only a
// Manager that actually consults the adapter can refuse it.
//
// The attack it closes is cross-session rather than cross-kind. JIN_SESSION_ID
// names the session a hook acts on and is read from the hook process's own
// environment, so anything running inside one session can report an id for
// another — pointing it at a transcript that is not its own, or at nothing.
func TestHandleHookEvent_RefusesAForeignShape(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	fakeClaudeAgent(t, mgr).recognizesFn = func(id string) bool { return id == rekeyValidID }
	sess := newRekeySession(t, mgr, "rekey-foreign")
	before := sess.AgentSessionID

	mgr.HandleHookEvent(rekeyForeignID, sess.ID, "SessionStart", "", "", "")

	got, _ := mgr.Get(sess.ID)
	if got.AgentSessionID != before {
		t.Errorf("AgentSessionID = %q, want the pre-minted %q", got.AgentSessionID, before)
	}
}

// TestHandleHookEvent_RefusalKeepsTheRecordedID pins that a refusal is not a
// clear. Emptying the field would cost the session its resume and its
// transcript on the strength of one bad payload — the same damage the gate
// exists to prevent, dealt by the gate itself.
func TestHandleHookEvent_RefusalKeepsTheRecordedID(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	fakeClaudeAgent(t, mgr).recognizesFn = func(id string) bool { return id == rekeyValidID }
	sess := newRekeySession(t, mgr, "rekey-keep")

	mgr.HandleHookEvent(rekeyValidID, sess.ID, "SessionStart", "", "", "")
	mgr.HandleHookEvent("x$(touch /tmp/jin-should-not-exist)", sess.ID, "Stop", "", "", "")

	got, _ := mgr.Get(sess.ID)
	if got.AgentSessionID != rekeyValidID {
		t.Errorf("AgentSessionID = %q, want the id recorded before the refusal (%q)", got.AgentSessionID, rekeyValidID)
	}
}

// TestHandleHookEvent_RefusalStillAppliesTheEvent is the scope of a refusal:
// the id write, and nothing else.
//
// Dropping the whole event would hand the same payload a second power — send
// one carrying a malformed id and status tracking stops for that session — so a
// defence written that way becomes the outage it was meant to prevent.
func TestHandleHookEvent_RefusalStillAppliesTheEvent(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess := newRekeySession(t, mgr, "rekey-side-effects")
	mgr.SetStatus(sess.ID, StatusThinking)

	gitRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(gitRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	mgr.HandleHookEvent("x$(touch /tmp/jin-should-not-exist)", sess.ID, "Stop", "", gitRoot, "")

	got, _ := mgr.Get(sess.ID)
	if got.Status != StatusIdle {
		t.Errorf("Status = %q, want %q — the status verdict was dropped with the id", got.Status, StatusIdle)
	}
	if got.WorkDir != gitRoot {
		t.Errorf("WorkDir = %q, want %q — CWD tracking was dropped with the id", got.WorkDir, gitRoot)
	}
}

// TestHandleHookEvent_AsksTheAdapterOutsideTheLock pins where the gate runs,
// not just what it answers.
//
// HandleHookEvent takes m.mu without a deferred unlock, so an adapter predicate
// consulted under that lock would not cost one event if it blocked — it would
// wedge every session in the daemon. Adapters are an extension surface, so
// "the three that ship are cheap pure functions" is not a property of the
// interface. The predicate here re-enters Manager through a method that takes
// the read lock, which deadlocks if and only if the gate moved back inside.
//
// Written as a race against a timeout because the failure IS a hang; the
// goroutine is abandoned in that case, and it holds only this test's Manager.
func TestHandleHookEvent_AsksTheAdapterOutsideTheLock(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	sess := newRekeySession(t, mgr, "rekey-lock")

	// Two separate facts, kept separate: that the predicate ran at all, and
	// that the re-entrant read worked. Folding them into one flag would report
	// "the predicate never ran" for a predicate that ran and simply missed the
	// session, which sends the next reader looking in the wrong place.
	ran, found := false, false
	fakeClaudeAgent(t, mgr).recognizesFn = func(string) bool {
		ran = true
		_, found = mgr.Get(sess.ID)
		return true
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.HandleHookEvent(rekeyValidID, sess.ID, "SessionStart", "", "", "")
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HandleHookEvent did not return: the adapter predicate is being asked while m.mu is held")
	}
	if !ran {
		t.Fatal("the predicate never ran, so this proved nothing about the lock")
	}
	if !found {
		t.Error("the re-entrant Get did not find the session (the lock claim still holds)")
	}
	if got, _ := mgr.Get(sess.ID); got.AgentSessionID != rekeyValidID {
		t.Errorf("AgentSessionID = %q, want %q", got.AgentSessionID, rekeyValidID)
	}
}

// TestHandleHookEvent_EmptyIDNeverReKeys pins the pre-existing contract that
// the gate sits behind: an agent that reports no id at all is saying nothing
// about identity, not asking for the field to be cleared.
func TestHandleHookEvent_EmptyIDNeverReKeys(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	calls := 0
	fakeClaudeAgent(t, mgr).recognizesFn = func(string) bool {
		calls++
		return true
	}
	sess := newRekeySession(t, mgr, "rekey-empty")
	before := sess.AgentSessionID

	mgr.HandleHookEvent("", sess.ID, "Stop", "", "", "")

	got, _ := mgr.Get(sess.ID)
	if got.AgentSessionID != before {
		t.Errorf("AgentSessionID = %q, want %q", got.AgentSessionID, before)
	}
	if calls != 0 {
		t.Errorf("RecognizesSessionID called %d times for an empty id, want 0", calls)
	}
}
