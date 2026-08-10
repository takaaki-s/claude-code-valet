//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/agentdocs"
	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/internal/daemon"
	"github.com/takaaki-s/jind-ai/internal/jinenv"
	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/testutil"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// --- helpers ---

// startE2EDaemon builds a daemon for socketPath, starts it, and returns a client
// once it is accepting connections — with the server, which a caller keeps only
// if it has to stop it before its test ends.
//
// Every daemon in this suite is built here, and that is the point. The
// goroutine, the channel Start's return value goes into, and the Cleanup that
// stops the server are one unit; a setup that assembles them itself can get any
// part of it wrong, and all three call sites had already drifted into three
// different spellings, two of which threw Start's error away. Only the bare
// `server.Start()` is a spelling errcheck can see, so listing `e2e` in
// .golangci.yml does not on its own keep `_ = server.Start()` from coming back.
// Owning the sequence does: there is no other way here to get a client.
func startE2EDaemon(t *testing.T, socketPath, sessionsDir, configDir, stateDir string) (*daemon.Client, *daemon.Server) {
	t.Helper()

	server, err := daemon.NewServer(socketPath, sessionsDir, configDir, stateDir)
	if err != nil {
		t.Fatalf("NewServer(sessions=%s, config=%s, state=%s): %v", sessionsDir, configDir, stateDir, err)
	}

	startErr := make(chan error, 1)
	go func() { startErr <- server.Start() }()

	waitForDaemonSocket(t, socketPath, startErr)

	t.Cleanup(func() {
		server.Stop()
	})

	return daemon.NewClient(socketPath), server
}

// setupE2EWithDataDir creates a daemon server using the provided sessions/config dirs.
// The same configDir is reused as stateDir (acceptable for ephemeral test scratch).
// Returns the client and server (server is needed for Stop in recovery tests).
func setupE2EWithDataDir(t *testing.T, sessionsDir, configDir string) (*daemon.Client, *daemon.Server) {
	t.Helper()

	isolateTmuxSocket(t)

	return startE2EDaemon(t, testutil.SocketPath(t, "e2e-tmux.sock"), sessionsDir, configDir, configDir)
}

// waitForDaemonSocket blocks until a daemon accepts a connection on socketPath,
// and fails the test if none does. Failing here rather than proceeding matters
// most where a test builds two daemons: a silent timeout surfaces later as the
// wrong daemon answering, which reads as a defect in what the test asserts
// rather than in its setup.
//
// startErr carries what Start returned, and watching it is what turns "no
// daemon bound within the deadline" into the reason there is none. Start fails
// fast when it cannot bind — the socket directory or the listen itself — so a
// caller that only polls the socket waits out the whole deadline and then
// reports the one fact it already had.
//
// Start otherwise blocks while serving, so anything arriving here means there
// is no daemon left to reach. A nil says Stop ran: it either beat Start to the
// stopping sentinel or closed the listener out from under the accept loop, the
// second being what the channel receives after a test stops its own server —
// by then this function has returned, and the buffer of one is why the sending
// goroutine does not block on a value nobody reads. Neither is a defect in
// Start, and neither leaves anything left to wait for.
func waitForDaemonSocket(t *testing.T, socketPath string, startErr <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-startErr:
			if err != nil {
				t.Fatalf("daemon Start(%s): %v", socketPath, err)
			} else {
				t.Fatalf("daemon Start(%s) returned before binding: the server was stopped", socketPath)
			}
		default:
		}
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no daemon bound %s within the deadline", socketPath)
}

// hasTmuxSession checks if a tmux session exists on the jin socket.
func hasTmuxSession(name string) bool {
	tc, err := tmux.NewClient()
	if err != nil {
		return false
	}
	return tc.HasSession(name)
}

// waitForStatus polls client.List until the session reaches the expected status or times out.
//
// This is the barrier for session *creation*, the mirror of waitForSessionGone
// for deletion. `new` returns a StatusCreating reservation and the daemon
// provisions and starts the session in a goroutine, so anything a test wants to
// observe about a Start:true session has to be waited for.
//
// Every client.NewWithOptions in this package must be followed by this call
// before any assertion about the session's status — including assertions made
// after some other operation, because the creation goroutine's final write
// races that operation rather than preceding it. Start:true settles on
// StatusRunning, Start:false on StatusStopped. Skipping it is how
// TestE2E_HookEventFlow came to fail CI on an unrelated PR: its hook set
// "thinking" and the goroutine then wrote "stopped" over it. StatusRunning is the
// useful barrier: startSessionTmux creates the inner tmux session first and only
// then flips the status, so "running" implies the tmux session exists — while
// still leaving a following hasTmuxSession check meaningful rather than
// tautological. The status holds for at least captureOutputTmux's 10s tick, so
// a poll cannot miss the window.
//
// Do not wait with time.Sleep instead. A fixed 500ms was what made the
// tmux-backed tests fail on any machine where provisioning takes longer.
func waitForStatus(t *testing.T, client *daemon.Client, sessionID string, want session.Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sessions, err := client.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, s := range sessions {
			if s.ID == sessionID && s.Status == want {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	// Show actual status on timeout
	sessions, _ := client.List()
	for _, s := range sessions {
		if s.ID == sessionID {
			t.Fatalf("timed out waiting for session %s to reach status %q (current: %q)", sessionID, want, s.Status)
		}
	}
	t.Fatalf("timed out: session %s not found in list", sessionID)
}

// waitForSessionGone polls client.List until the session no longer appears or times out.
// Needed because Delete now returns before the async DeleteFinalize goroutine finishes.
//
// Every client.Delete in this package must be followed by this call before any
// assertion about the deleted session's aftermath — an empty List, a removed
// JSON file, a killed tmux session, a removed worktree. DeleteFinalize does all
// of that and only then drops the record, so "gone from List" is the barrier
// that covers all of it. A bare time.Sleep is not: it made
// TestE2E_MultipleSessionsConcurrent fail CI intermittently with
// "expected 0 sessions, got 1". The async contract itself is deliberate and
// pinned by internal/daemon.TestHandleDelete_ReturnsBeforeFinalization — do not
// "fix" it on the production side.
func waitForSessionGone(t *testing.T, client *daemon.Client, sessionID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sessions, err := client.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		found := false
		for _, s := range sessions {
			if s.ID == sessionID {
				found = true
				break
			}
		}
		if !found {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for session %s to disappear from list", sessionID)
}

// --- tests ---

func TestE2E_TmuxSessionCreation(t *testing.T) {
	client := setupE2E(t)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "tmux-create",
		WorkDir:     t.TempDir(),
		Start:       true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	innerName := tmux.InnerSessionName(info.ID)

	// Creation is async; wait for the daemon to report the session up.
	waitForStatus(t, client, info.ID, session.StatusRunning, 10*time.Second)

	// tmux session should exist on the jin socket
	if !hasTmuxSession(innerName) {
		t.Fatalf("tmux session %q should exist after Start:true", innerName)
	}

	// Inner session name should follow the naming convention
	if innerName != "sess-"+info.ID {
		t.Errorf("InnerSessionName = %q, want %q", innerName, "sess-"+info.ID)
	}
}

func TestE2E_KillWithTmuxCleanup(t *testing.T) {
	client := setupE2E(t)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "tmux-kill",
		WorkDir:     t.TempDir(),
		Start:       true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	innerName := tmux.InnerSessionName(info.ID)
	waitForStatus(t, client, info.ID, session.StatusRunning, 10*time.Second)

	if !hasTmuxSession(innerName) {
		t.Fatal("tmux session should exist before Kill")
	}

	// Kill
	if err := client.Kill(info.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	// The inner session outlives the kill: killing stops the agent and leaves
	// the window standing, so whatever else the user had open in it survives
	// and a later start revives the agent in place. Releasing it is delete's
	// job (TestE2E_DeleteWithTmuxCleanup).
	//
	// Kill is synchronous, so this sleep is not waiting for it to finish —
	// there is nothing to poll for when the assertion is that nothing happens.
	// It is a grace window in which a stray teardown (the pane-died hook
	// reaping the window after the agent exits) would have a chance to show
	// itself instead of the check passing before it could run.
	time.Sleep(200 * time.Millisecond)
	if !hasTmuxSession(innerName) {
		t.Error("tmux session should outlive Kill")
	}

	// Status should be stopped
	sessions, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Status != session.StatusStopped {
		t.Errorf("Status after Kill: got %q, want %q", sessions[0].Status, session.StatusStopped)
	}
}

func TestE2E_DeleteWithTmuxCleanup(t *testing.T) {
	client := setupE2E(t)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "tmux-delete",
		WorkDir:     t.TempDir(),
		Start:       true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	innerName := tmux.InnerSessionName(info.ID)
	waitForStatus(t, client, info.ID, session.StatusRunning, 10*time.Second)

	if !hasTmuxSession(innerName) {
		t.Fatal("tmux session should exist before Delete")
	}

	// Delete returns before DeleteFinalize kills the tmux session; wait for
	// the record to disappear, which happens after the kill.
	if err := client.Delete(info.ID, false, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	waitForSessionGone(t, client, info.ID, 5*time.Second)

	// tmux session should be gone
	if hasTmuxSession(innerName) {
		t.Error("tmux session should not exist after Delete")
	}

	// Session should be removed from list
	sessions, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after Delete, got %d", len(sessions))
	}
}

func TestE2E_SessionDataPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "sessions")
	configDir := filepath.Join(tmpDir, "config")
	client, _ := setupE2EWithDataDir(t, dataDir, configDir)

	workDir := t.TempDir()
	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "persist-test",
		WorkDir:     workDir,
		Start:       true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	// JSON file should exist on disk
	jsonPath := filepath.Join(dataDir, info.ID+".json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("session JSON file not found: %v", err)
	}

	// Decode and verify fields
	var persisted map[string]interface{}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if persisted["id"] != info.ID {
		t.Errorf("persisted ID = %v, want %q", persisted["id"], info.ID)
	}
	if persisted["description"] != "persist-test" {
		t.Errorf("persisted description = %v, want %q", persisted["description"], "persist-test")
	}
	if persisted["description_locked"] != true {
		t.Errorf("persisted description_locked = %v, want true (--description sets Layer B lock)", persisted["description_locked"])
	}
	if _, hasName := persisted["name"]; hasName {
		t.Errorf("persisted JSON should not carry the retired \"name\" field (v1 schema)")
	}
	if persisted["work_dir"] != workDir {
		t.Errorf("persisted work_dir = %v, want %q", persisted["work_dir"], workDir)
	}

	// Delete returns before DeleteFinalize removes the file; wait for the
	// record to disappear from List before asserting the file is gone.
	if err := client.Delete(info.ID, false, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	waitForSessionGone(t, client, info.ID, 5*time.Second)
	if _, err := os.Stat(jsonPath); err == nil {
		t.Error("session JSON file should be removed after Delete")
	}
}

func TestE2E_SessionRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "sessions")
	configDir := filepath.Join(tmpDir, "config")

	// Phase 1: Start server and create a session
	client, server := setupE2EWithDataDir(t, dataDir, configDir)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "recovery-test",
		WorkDir:     t.TempDir(),
		Start:       true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	innerName := tmux.InnerSessionName(info.ID)
	waitForStatus(t, client, info.ID, session.StatusRunning, 10*time.Second)

	if !hasTmuxSession(innerName) {
		t.Fatal("tmux session should exist after Start")
	}

	// Phase 2: Stop the server (simulating daemon restart)
	server.Stop()

	// tmux session should still exist (daemon stop doesn't kill inner sessions)
	if !hasTmuxSession(innerName) {
		t.Fatal("tmux session should survive daemon stop")
	}

	// Phase 3: Create new Manager from same data directory (simulating daemon restart)
	configMgr, err := config.NewManager(configDir)
	if err != nil {
		t.Fatalf("NewConfigManager: %v", err)
	}

	mgr, err := session.NewManager(dataDir, configDir, jinenv.Identity{SocketPath: testutil.SocketPath(t, "recover.sock")}, configMgr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Set real tmux client and recover
	tc, err := tmux.NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	mgr.SetTmuxClient(tc)
	mgr.RecoverTmuxSessions()

	// Verify session is recovered
	recovered := mgr.List()
	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered session, got %d", len(recovered))
	}
	if recovered[0].ID != info.ID {
		t.Errorf("recovered ID = %q, want %q", recovered[0].ID, info.ID)
	}
	if recovered[0].Description != "recovery-test" {
		t.Errorf("recovered Description = %q, want %q", recovered[0].Description, "recovery-test")
	}
}

func TestE2E_MultipleSessionsTmux(t *testing.T) {
	client := setupE2E(t)

	// Create 3 sessions
	type sess struct {
		id        string
		innerName string
	}
	sessions := make([]sess, 3)
	for i := range 3 {
		info, _, err := client.NewWithOptions(daemon.NewOptions{
			Description: filepath.Base(t.TempDir()),
			WorkDir:     t.TempDir(),
			Start:       true,
		})
		if err != nil {
			t.Fatalf("NewWithOptions(%d): %v", i, err)
		}
		sessions[i] = sess{id: info.ID, innerName: tmux.InnerSessionName(info.ID)}
		// Wait per session rather than once after the loop: the daemon
		// serializes creation anyway, so this costs nothing and keeps each
		// check close to its own start.
		waitForStatus(t, client, info.ID, session.StatusRunning, 10*time.Second)
	}

	// All 3 tmux sessions should exist
	for i, s := range sessions {
		if !hasTmuxSession(s.innerName) {
			t.Fatalf("tmux session %d (%s) should exist", i, s.innerName)
		}
	}

	// Kill the middle one
	if err := client.Kill(sessions[1].id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// Kill is synchronous; this is the same deliberate grace window as in
	// TestE2E_KillWithTmuxCleanup, giving a wrong teardown room to happen
	// before the negative assertion below runs.
	time.Sleep(200 * time.Millisecond)

	// Every inner session is still there — the killed one because a kill
	// keeps its window (see TestE2E_KillWithTmuxCleanup), the others because
	// killing one session must not touch its neighbours.
	for i, s := range sessions {
		if !hasTmuxSession(s.innerName) {
			t.Errorf("tmux session %d (%s) should still exist after killing session 1", i, s.innerName)
		}
	}

	// Delete all three: the killed one is only released here. Each Delete
	// returns before its DeleteFinalize goroutine kills the tmux session, so
	// wait for every record to disappear before probing tmux.
	for _, i := range []int{0, 1, 2} {
		if err := client.Delete(sessions[i].id, false, false); err != nil {
			t.Fatalf("Delete(%d): %v", i, err)
		}
	}
	for _, s := range sessions {
		waitForSessionGone(t, client, s.id, 5*time.Second)
	}

	// All tmux sessions should be gone
	for i, s := range sessions {
		if hasTmuxSession(s.innerName) {
			t.Errorf("tmux session %d (%s) should not exist after cleanup", i, s.innerName)
		}
	}
}

func TestE2E_HookCWDUpdateOnStartedSession(t *testing.T) {
	client := setupE2E(t)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "cwd-update",
		WorkDir:     t.TempDir(),
		Start:       true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	// Same barrier as TestE2E_HookEventFlow, for the Start:true shape: the
	// creation goroutine ends in StartBackground, which drives the status to
	// running. Hooking before that lands lets the start overwrite the
	// "thinking" asserted below. Found by auditing every NewWithOptions in
	// this package rather than by a CI failure — it had not been hit yet.
	waitForStatus(t, client, info.ID, session.StatusRunning, 10*time.Second)

	// Send hook with CWD
	newCWD := t.TempDir()
	if err := client.SendHook(daemon.HookRequest{
		JinSessionID:  info.ID,
		HookEventName: "UserPromptSubmit",
		CWD:           newCWD,
	}); err != nil {
		t.Fatalf("SendHook: %v", err)
	}

	// Verify CWD is updated
	sessions, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].CurrentWorkDir != newCWD {
		t.Errorf("CurrentWorkDir = %q, want %q", sessions[0].CurrentWorkDir, newCWD)
	}
	if sessions[0].Status != session.StatusThinking {
		t.Errorf("Status = %q, want %q", sessions[0].Status, session.StatusThinking)
	}
}

// setupGitWorktree creates a temp git repo with a worktree and returns (repoDir, worktreeDir).
// Skips the test if git is not available.
func setupGitWorktree(t *testing.T) (string, string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoDir := t.TempDir()

	// Every git call below is fixture setup, so none of them has a failure
	// worth continuing past. CombinedOutput rather than Run because git puts
	// the reason on stderr, which an exit status on its own does not carry.
	// The %q is so an argument with a space in it does not read as two.
	runGit := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %s: %v", args, out, err)
		}
	}

	runGit("init")

	// Configure git user for commit
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "test")

	// Create initial commit (required for worktree)
	dummyFile := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("init"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "init")

	// Create worktree
	worktreeDir := filepath.Join(repoDir, "wt")
	runGit("worktree", "add", worktreeDir, "-b", "test-branch")

	return repoDir, worktreeDir
}

func TestE2E_DeleteWithWorktreeCleanup(t *testing.T) {
	client := setupE2E(t)
	_, worktreeDir := setupGitWorktree(t)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "wt-cleanup",
		WorkDir:     worktreeDir,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	// Delete returns before DeleteFinalize removes the worktree; wait for
	// the record to disappear before asserting the directory is gone.
	if err := client.Delete(info.ID, true, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	waitForSessionGone(t, client, info.ID, 5*time.Second)

	// Worktree directory should be removed
	if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
		t.Errorf("worktree directory should be removed, but still exists")
	}
}

func TestE2E_DeleteWithoutWorktreeCleanup(t *testing.T) {
	client := setupE2E(t)
	_, worktreeDir := setupGitWorktree(t)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "wt-no-cleanup",
		WorkDir:     worktreeDir,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	// Delete without worktree removal. Delete returns before DeleteFinalize
	// drops the record, so wait before asserting the list is empty.
	if err := client.Delete(info.ID, false, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	waitForSessionGone(t, client, info.ID, 5*time.Second)

	// Session should be gone
	sessions, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}

	// Worktree directory should still exist
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Errorf("worktree directory should still exist after session-only delete")
	}
}

func TestE2E_DeleteWorktreeDirty(t *testing.T) {
	client := setupE2E(t)
	_, worktreeDir := setupGitWorktree(t)

	// Create uncommitted file in worktree
	dirtyFile := filepath.Join(worktreeDir, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted"), 0644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "wt-dirty",
		WorkDir:     worktreeDir,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	// Delete with worktree removal — should fail with ErrWorktreeDirty
	err = client.Delete(info.ID, true, false)
	if err == nil {
		t.Fatal("expected ErrWorktreeDirty, got nil")
	}
	if !errors.Is(err, session.ErrWorktreeDirty) {
		t.Fatalf("expected ErrWorktreeDirty, got: %v", err)
	}

	// Session should still exist
	sessions, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session (not deleted), got %d", len(sessions))
	}

	// Worktree should still exist
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Error("worktree directory should still exist after dirty rejection")
	}

	// Force delete returns before DeleteFinalize completes; wait for the
	// record to disappear before asserting the worktree is gone.
	if err := client.Delete(info.ID, true, true); err != nil {
		t.Fatalf("force Delete: %v", err)
	}
	waitForSessionGone(t, client, info.ID, 5*time.Second)

	// Worktree should be removed
	if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
		t.Errorf("worktree directory should be removed after force delete")
	}
}

func TestE2E_DeleteWorktreeAlreadyRemoved(t *testing.T) {
	client := setupE2E(t)
	_, worktreeDir := setupGitWorktree(t)

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: "wt-removed",
		WorkDir:     worktreeDir,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	// Manually remove the worktree directory
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	// Delete with worktree removal — should succeed even though dir is gone
	if err := client.Delete(info.ID, true, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	waitForSessionGone(t, client, info.ID, 5*time.Second)
}

// setupE2EWithStateDir brings up a daemon rooted at stateDir and returns a
// client for it. Unlike setupE2E and setupE2EWithDataDir it names the state
// directory explicitly, because the state directory is what the caller is
// asserting about — setupE2EWithDataDir's habit of reusing configDir for it
// would leave the assertion talking about a directory the call site calls
// something else.
//
// It also does not isolate the tmux socket: a caller building two daemons
// isolates once for both, so they share one throwaway server the way two real
// daemons on one machine share the user's. A second isolateTmuxSocket mid-test
// would leave the daemons bound to different servers while any later
// tmux.NewClient() saw only the last one.
func setupE2EWithStateDir(t *testing.T, stateDir string) *daemon.Client {
	t.Helper()

	client, _ := startE2EDaemon(t, testutil.SocketPath(t, "e2e-hooks.sock"),
		filepath.Join(stateDir, "sessions"),
		filepath.Join(stateDir, "config"),
		stateDir)
	return client
}

// startRunningSession creates one Start:true session and waits for it to reach
// running. The wait is the barrier that says the adapter's Setup has run for
// this daemon's state directory: Setup happens inside the start, which the
// daemon does in a goroutine, so the file it writes need not be on disk when
// NewWithOptions returns.
func startRunningSession(t *testing.T, client *daemon.Client, description string) {
	t.Helper()

	info, _, err := client.NewWithOptions(daemon.NewOptions{
		Description: description,
		WorkDir:     t.TempDir(),
		Start:       true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions(%s): %v", description, err)
	}
	waitForStatus(t, client, info.ID, session.StatusRunning, 10*time.Second)
}

// assertOwnHooksSettings checks that the daemon rooted at stateDir wrote
// hooks-settings.json there, and that the hook command inside names that
// daemon's own copy of the jin binary rather than the one under otherStateDir.
//
// The binary path is spelled out rather than imported because
// session.hookBinaryPath is unexported; agentdocs.HookCommand is imported
// because the command's shape is that package's contract, and re-spelling it
// here would just be a second copy to keep in step.
func assertOwnHooksSettings(t *testing.T, stateDir, otherStateDir string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(stateDir, "hooks-settings.json"))
	if err != nil {
		t.Fatalf("hooks settings for the daemon at %s: %v", stateDir, err)
	}
	own := agentdocs.HookCommand(filepath.Join(stateDir, "bin", "jin"), false)
	if !strings.Contains(string(data), own) {
		t.Errorf("hooks settings at %s do not wire %q; file is:\n%s", stateDir, own, data)
	}
	foreign := filepath.Join(otherStateDir, "bin", "jin")
	if strings.Contains(string(data), foreign) {
		t.Errorf("hooks settings at %s name the other daemon's binary %q", stateDir, foreign)
	}
}

// TestE2E_SecondDaemonGetsItsOwnHooksSettings pins the one claim the Claude
// Code adapter's own unit tests cannot make: that a real daemon, resolving the
// adapter through the real registry, is handed the hooks file belonging to its
// own state directory — even when another daemon in the same process got there
// first.
//
// The registry holds one adapter instance per kind for the whole process, and
// Setup used to derive hooks-settings.json under a sync.Once, so it ran for the
// first SetupContext the process ever saw and never again. Measured on this
// suite before the fix: 9 Setup calls over 7 state directories, and 8 of the 9
// were handed a path under the first test's directory — one that test's own
// cleanup had already deleted, so those sessions were started against a file
// that did not exist. The suite stayed green throughout, because no test looked
// at the hook wiring. Production starts one daemon per process (a restart execs
// a new one), which is why it never showed there.
//
// It lives here because this is the only test surface that holds both halves
// at once: the real registry, from the blank import of internal/agent/register
// in e2e_test.go, and a real daemon.NewServer. The register package's own tests
// reach every adapter but build no daemon; internal/daemon's tests build one
// but do not blank-import the registry, so their Lookup finds only the stubs
// they register and never one of the three real adapters; and internal/session
// cannot import an adapter at all without a cycle. A unit test driving the adapter directly shows that Setup recomputes;
// nothing in it obliges the daemon to resolve the shared instance, which is the
// half that was broken.
//
// Both halves of the assertion do work. Existence alone kills the sync.Once,
// under which the second state directory got no file at all. It does not kill a
// variant that writes into the right directory with the first daemon's binary
// path baked in — hooks that exist and call a deleted binary — and that is what
// the content check is for.
func TestE2E_SecondDaemonGetsItsOwnHooksSettings(t *testing.T) {
	isolateTmuxSocket(t)

	root := t.TempDir()
	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")

	first := setupE2EWithStateDir(t, firstDir)
	second := setupE2EWithStateDir(t, secondDir)

	// Order matters: the first daemon must run Setup before the second, so
	// that a cached first context has something to be stale about.
	startRunningSession(t, first, "hooks-first")
	startRunningSession(t, second, "hooks-second")

	assertOwnHooksSettings(t, firstDir, secondDir)
	assertOwnHooksSettings(t, secondDir, firstDir)
}
