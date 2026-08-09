package claude

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// isolateHome points os.UserHomeDir() at a throwaway directory. Setup calls
// EnsureTrustState, which edits ~/.claude.json; without this every test here
// would write into the developer's real one. The name avoids trust_test.go's
// fakeHome, which is a local variable there in some twenty tests and would
// shadow a helper of that name.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// setupIn runs Setup for one state dir and fails the test if it errors. It
// does not assert on the write succeeding — Setup is fail-open by contract, so
// the observable is always what it leaves behind, never its return value.
func setupIn(t *testing.T, a *Agent, stateDir, execPath string) {
	t.Helper()
	if err := a.Setup(agent.SetupContext{StateDir: stateDir, ExecPath: execPath, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Setup(%s): %v", stateDir, err)
	}
}

func TestAgent_Setup_LaterStateDirWins(t *testing.T) {
	// One adapter instance is shared by the whole process while StateDir
	// belongs to the Manager starting a session, so a second Manager must not
	// be handed the first one's path. This is the assertion a sync.Once here
	// fails; TestE2E_SecondDaemonGetsItsOwnHooksSettings carries what it was
	// measured to cost.
	isolateHome(t)
	first, second := t.TempDir(), t.TempDir()
	a := New()

	setupIn(t, a, first, "/first/jin")
	if got, want := a.SpawnCommand(agent.SpawnOptions{}).Command, "--settings "+hooksSettingsPath(first); !strings.Contains(got, want) {
		t.Fatalf("after first Setup: Command = %q, want %q", got, want)
	}

	setupIn(t, a, second, "/second/jin")
	got := a.SpawnCommand(agent.SpawnOptions{}).Command
	if want := "--settings " + hooksSettingsPath(second); !strings.Contains(got, want) {
		t.Errorf("after second Setup: Command = %q, want %q", got, want)
	}
	if strings.Contains(got, first) {
		t.Errorf("second Setup still names the first state dir: Command = %q", got)
	}
}

func TestAgent_Setup_WritesIntoEveryStateDir(t *testing.T) {
	// Distinct from LaterStateDirWins: that one pins which path is *named*,
	// this one pins that the file is *there* and describes the jin that asked
	// for it. A change that updated the field but skipped the write would
	// leave --settings pointing at nothing, and only this test would notice.
	isolateHome(t)
	dirs := []struct{ stateDir, execPath string }{
		{t.TempDir(), "/first/jin"},
		{t.TempDir(), "/second/jin"},
	}
	a := New()

	for _, d := range dirs {
		setupIn(t, a, d.stateDir, d.execPath)
	}

	for _, d := range dirs {
		data, err := os.ReadFile(hooksSettingsPath(d.stateDir))
		if err != nil {
			t.Fatalf("state dir %s got no hooks settings: %v", d.stateDir, err)
		}
		if !strings.Contains(string(data), d.execPath) {
			t.Errorf("hooks settings in %s do not name %s:\n%s", d.stateDir, d.execPath, data)
		}
	}
}

func TestAgent_Setup_RestoresAHandEditedFile(t *testing.T) {
	// The other direction from LaterStateDirWins: the same state dir, twice.
	// A Setup that cached per state directory passes every other test here,
	// and gives up the self-healing the changelog promises users. Nothing but
	// this notices.
	//
	// Corrupting rather than deleting is deliberate. A deleted file is
	// restored by any implementation that does not branch on the file's
	// existence, so a delete-and-recreate case killed nothing this does not —
	// measured across four mutations before it was dropped.
	isolateHome(t)
	dir := t.TempDir()
	a := New()

	setupIn(t, a, dir, "/some/jin")
	if err := os.WriteFile(hooksSettingsPath(dir), []byte("{}\n"), 0600); err != nil {
		t.Fatalf("corrupting the file: %v", err)
	}

	setupIn(t, a, dir, "/some/jin")

	data, err := os.ReadFile(hooksSettingsPath(dir))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !strings.Contains(string(data), "/some/jin") || !strings.Contains(string(data), "UserPromptSubmit") {
		t.Errorf("a hand-edited file was not restored:\n%s", data)
	}
}

func TestAgent_Setup_FailureDoesNotLeaveAnotherStateDirsPath(t *testing.T) {
	// The failure path is where falling back to the field would reintroduce
	// the defect: this session would spawn with --settings pointing into a
	// state directory it does not own. With nothing to fall back to inside
	// its own, empty is the documented answer — the session starts, without
	// status hooks.
	isolateHome(t)
	good := t.TempDir()
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	a := New()

	setupIn(t, a, good, "/good/jin")
	// Setup swallows the write failure by contract; the observable is the
	// command it leaves behind, not the return value.
	setupIn(t, a, missing, "/missing/jin")

	got := a.SpawnCommand(agent.SpawnOptions{}).Command
	if strings.Contains(got, "--settings") {
		t.Errorf("failed Setup still passes --settings: Command = %q", got)
	}
	if strings.Contains(got, good) {
		t.Errorf("failed Setup fell back to another state dir: Command = %q", got)
	}
}

func TestAgent_Setup_FailedRewriteKeepsThisStateDirsOwnFile(t *testing.T) {
	// The other half of the failure rule, and the reason it is a fallback
	// rather than a clear. One session's write failing must not strip
	// --settings from a concurrent session in the same state dir whose own
	// Setup succeeded — the file the earlier one published is still whole,
	// because the write publishes by rename.
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	isolateHome(t)
	dir := t.TempDir()
	a := New()

	setupIn(t, a, dir, "/some/jin")

	// Read+execute only: the published file stays readable, while creating the
	// temp sibling the write needs does not.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	// A different binary the second time, so the file's contents say whether
	// the write was refused. Asserting only on --settings would not: the
	// success path produces the same flag and the same path, so an environment
	// where the mode does not bite — root is not the only one — would leave
	// this test green while exercising nothing.
	setupIn(t, a, dir, "/second/jin")
	data, err := os.ReadFile(hooksSettingsPath(dir))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if strings.Contains(string(data), "/second/jin") {
		t.Fatalf("the rewrite succeeded, so nothing here exercised the failure path")
	}

	got := a.SpawnCommand(agent.SpawnOptions{}).Command
	if want := "--settings " + hooksSettingsPath(dir); !strings.Contains(got, want) {
		t.Errorf("a failed rewrite dropped this state dir's own file: Command = %q, want %q", got, want)
	}
}

func TestAgent_Setup_FailedRewriteIgnoresAnUnusableLeftoverFile(t *testing.T) {
	// The fallback asks what the state dir can still offer, and "a file is
	// there" is not the same answer as "a settings file is there" — see
	// existingHooksSettings for what that costs when it is answered wrong.
	//
	// Three inputs because usableHooksSettings is a conjunction and one input
	// decides nothing: a zero-length file fails both halves at once, so either
	// half could be dropped and this would stay green. The empty object
	// exercises the length alone and the type error the parse alone.
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"zero length", nil},
		{"parses, wires nothing", []byte("{}\n")},
		{"parses as JSON, not as settings", []byte(`{"hooks":{"Stop":"not-an-array"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateHome(t)
			dir := t.TempDir()
			if err := os.WriteFile(hooksSettingsPath(dir), tc.content, 0600); err != nil {
				t.Fatalf("planting the leftover: %v", err)
			}
			if err := os.Chmod(dir, 0500); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

			a := New()
			setupIn(t, a, dir, "/some/jin")

			// Same precondition as the sibling above: without it, a directory
			// mode that does not bite would make this fail while blaming the
			// fallback for a file the write had simply replaced.
			data, err := os.ReadFile(hooksSettingsPath(dir))
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if strings.Contains(string(data), "/some/jin") {
				t.Fatalf("the rewrite succeeded, so nothing here exercised the failure path")
			}

			if got := a.SpawnCommand(agent.SpawnOptions{}).Command; strings.Contains(got, "--settings") {
				t.Errorf("an unusable leftover was passed to the agent: Command = %q", got)
			}
		})
	}
}

func TestAgent_ConcurrentSetupAndSpawn(t *testing.T) {
	// The Agent contract allows Setup and SpawnCommand from parallel
	// goroutines (session.Agent's doc). hooksPath is now mutable, so the
	// mutex is what makes that legal — run under -race. Each goroutine brings
	// its own state dir so the writes genuinely differ; no assertion is made
	// about which one wins, because with one shared field there is no answer
	// to assert. The interleavings themselves are the test.
	isolateHome(t)
	const goroutines = 32
	dirs := make([]string, goroutines)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	a := New()

	var wg sync.WaitGroup
	// Released all at once so the goroutines contend rather than queueing up
	// behind their own start-up cost.
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_ = a.Setup(agent.SetupContext{StateDir: dirs[i], ExecPath: "/race/jin", WorkDir: dirs[i]})
			if plan := a.SpawnCommand(agent.SpawnOptions{}); !strings.HasPrefix(plan.Command, "claude") {
				t.Errorf("goroutine %d got Command = %q", i, plan.Command)
			}
		}(i)
	}
	close(start)
	wg.Wait()
}
