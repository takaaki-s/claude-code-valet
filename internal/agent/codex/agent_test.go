package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/session"
)

func TestAgent_Kind(t *testing.T) {
	if got := New().Kind(); got != "codex" {
		t.Errorf("Kind() = %q, want %q", got, "codex")
	}
}

func TestAgent_Setup_NoFileWrites(t *testing.T) {
	// The Codex adapter must not touch the user's global config files —
	// hooks are injected per-invocation via -c. Setup with a fresh HOME +
	// CODEX_HOME and verify nothing lands under either.
	home := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	a := New()
	if err := a.Setup(agent.SetupContext{
		StateDir: t.TempDir(),
		ExecPath: "/usr/local/bin/jin",
		WorkDir:  t.TempDir(),
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	for _, root := range []string{home, codexHome} {
		count := 0
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				count++
				t.Errorf("unexpected file written under %s: %s", root, path)
			}
			return nil
		})
		if count > 0 {
			t.Errorf("Setup wrote %d files under %s; expected 0 (hooks are injected via -c)", count, root)
		}
	}
}

// TestAgent_SpawnCommand_ConcurrentSpawnsDoNotCross is the regression test for
// the hazard that outlived the mutex.
//
// A mutex made the shared execPath field safe to write and read, but Setup and
// SpawnCommand are two separate calls with no lock spanning them
// (session.Manager.buildAgentShellCmd), so two sessions could interleave as
// Setup(A) → Setup(B) → SpawnCommand(A), and A was handed B's binary. One
// Manager per process kept that out of production — both sessions then name the
// same path — but nothing made it impossible.
//
// Each spawn now carries its own ExecPath, so the interleave has nothing to
// corrupt. The assertion is per-goroutine on purpose: 32 starters each check
// they were given THEIR OWN path, which the field-and-mutex design cannot
// satisfy however correct its locking, because there is only one field.
func TestAgent_SpawnCommand_ConcurrentSpawnsDoNotCross(t *testing.T) {
	a := New()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			exec := fmt.Sprintf("/manager-%d/bin/jin", i)
			// The order a real spawn uses: Setup, then SpawnCommand.
			_ = a.Setup(agent.SetupContext{ExecPath: exec})
			plan := a.SpawnCommand(agent.SpawnOptions{ExecPath: exec})
			if !strings.Contains(plan.Command, exec+" hook") {
				t.Errorf("goroutine %d was handed %q, want its own %q", i, plan.Command, exec)
			}
		}(i)
	}
	wg.Wait()
}

func TestAgent_SpawnCommand_NoExecPath(t *testing.T) {
	// An empty ExecPath is reachable in production, not merely defensive: the
	// Manager passes what EstablishHookBinary resolved, and that is "" when
	// even os.Executable() failed. HookArgs then returns nil, so only the
	// config overrides remain — the session still starts, without hooks, and
	// the overrides (which depend on no binary path) are not lost with them.
	plan := New().SpawnCommand(agent.SpawnOptions{ExecPath: ""})
	want := "codex " + strings.Join(configArgs(), " ")
	if plan.Command != want {
		t.Errorf("Command = %q, want %q", plan.Command, want)
	}
	if strings.Contains(plan.Command, "hooks") {
		t.Errorf("Command = %q, want no hook args", plan.Command)
	}
}

func TestAgent_StatusSource_NonNil(t *testing.T) {
	if src := New().StatusSource(); src == nil {
		t.Error("StatusSource() = nil, want *HookStatusSource")
	}
}

func TestAgent_Description_NonNil(t *testing.T) {
	if enh := New().Description(); enh == nil {
		t.Error("Description() = nil, want *DescriptionEnhancer")
	}
}

func TestAgent_SharesLocatorAcrossDescriptionAndTranscript(t *testing.T) {
	a := New()
	tr, ok := a.Transcript().(*TranscriptReader)
	if !ok {
		t.Fatalf("Transcript() = %T, want *TranscriptReader", a.Transcript())
	}
	if tr.locator != a.enhancer.locator {
		t.Error("Transcript() and Description() do not share the same Locator — a path either one resolves would not be cached for the other")
	}
}

// TestAgent_SpawnCommand_CacheInvalidation covers both isResume outcomes with
// the same non-empty AgentSessionID in both cases — that is what lets the
// "not started" case notice a mutation that invalidates unconditionally
// instead of gating on isResume (see TestSpawnCommand_NoResumeWithoutStarted
// for the same edge case at the plain SpawnCommand level).
func TestAgent_SpawnCommand_CacheInvalidation(t *testing.T) {
	tests := []struct {
		name       string
		started    bool
		wantCached bool
	}{
		{"resume evicts", true, false},
		{"fresh spawn leaves cache intact", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			codexHome := filepath.Join(home, "codex")
			t.Setenv("CODEX_HOME", codexHome)
			stageRollout(t, filepath.Join(codexHome, "sessions"), "2026/07/11", basicUUID, fixtureBasic)

			a := New()
			if _, ok := a.locator.Find(basicUUID); !ok {
				t.Fatalf("warm-up Find = false, want true")
			}

			a.SpawnCommand(agent.SpawnOptions{AgentSessionID: basicUUID, AgentSessionStarted: tt.started})

			a.locator.mu.Lock()
			_, cached := a.locator.cache[basicUUID]
			a.locator.mu.Unlock()
			if cached != tt.wantCached {
				t.Errorf("cached = %v, want %v", cached, tt.wantCached)
			}
		})
	}
}

func TestAgent_ImplementsInterface(t *testing.T) {
	// Compile-time interface check: Agent must satisfy session.Agent so
	// register.go can hand it to agent.Register().
	var _ session.Agent = (*Agent)(nil)
	var _ session.Agent = New()
}
