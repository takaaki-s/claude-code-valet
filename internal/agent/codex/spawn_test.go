package codex

import (
	"reflect"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

const testExecPath = "/usr/local/bin/jin"

func TestSpawnCommand_Fresh_IgnoresPreMintUUID(t *testing.T) {
	// AgentSessionStarted=false is the fresh-spawn signal. Manager will
	// have set AgentSessionID to a pre-mint UUID; §3.5 says we must NOT
	// pass it through — Codex will refuse a nonexistent resume target,
	// and the correct UUID only becomes known after SessionStart writes
	// back through the hook.
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      "pre-mint-01900000-0000-0000-0000-000000000000",
		AgentSessionStarted: false,
	}, testExecPath)

	if !strings.HasPrefix(plan.Command, "codex ") {
		t.Errorf("fresh spawn Command = %q, want prefix %q", plan.Command, "codex ")
	}
	if strings.HasPrefix(plan.Command, "codex resume") {
		t.Errorf("fresh spawn must not use `codex resume`; got %q", plan.Command)
	}
	if strings.Contains(plan.Command, "pre-mint") {
		t.Errorf("fresh spawn must not embed the pre-mint UUID; got %q", plan.Command)
	}
}

func TestSpawnCommand_Resume(t *testing.T) {
	uuid := "01900000-0000-7000-8000-000000000abc"
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      uuid,
		AgentSessionStarted: true,
	}, testExecPath)

	want := "codex resume " + uuid + " "
	if !strings.HasPrefix(plan.Command, want) {
		t.Errorf("resume Command = %q, want prefix %q", plan.Command, want)
	}
}

func TestSpawnCommand_NoResumeWithoutStarted(t *testing.T) {
	// Edge case: AgentSessionID is set (post-recovery re-key) but
	// AgentSessionStarted is somehow false. Falling back to fresh spawn
	// is safer than issuing a resume against a UUID whose provenance is
	// unclear — the SessionStart hook will re-key again if needed.
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      "01900000-0000-7000-8000-000000000abc",
		AgentSessionStarted: false,
	}, testExecPath)

	if strings.HasPrefix(plan.Command, "codex resume") {
		t.Errorf("Command = %q, want plain `codex` when not started", plan.Command)
	}
}

func TestSpawnCommand_HooksAppended(t *testing.T) {
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionStarted: false,
	}, testExecPath)

	if !strings.Contains(plan.Command, "--enable hooks") {
		t.Errorf("Command missing --enable hooks: %q", plan.Command)
	}
	for _, ev := range managedEvents {
		if !strings.Contains(plan.Command, "hooks."+ev+"=") {
			t.Errorf("Command missing event %q: %q", ev, plan.Command)
		}
	}
}

func TestSpawnCommand_NoHookArgsWhenExecPathEmpty(t *testing.T) {
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionStarted: false,
	}, "")

	if strings.Contains(plan.Command, "--enable hooks") {
		t.Errorf("Command = %q, want no hooks when execPath is empty", plan.Command)
	}
	if strings.Contains(plan.Command, "hooks.") {
		t.Errorf("Command = %q, want no hook events when execPath is empty", plan.Command)
	}
	// The config overrides are independent of hooks: they must survive
	// even when hook injection is skipped entirely.
	want := "codex " + strings.Join(configArgs(), " ")
	if plan.Command != want {
		t.Errorf("Command = %q, want %q", plan.Command, want)
	}
}

// TestSpawnCommand_ConfigArgs pins the two behaviour overrides. Both guard
// failures that are invisible at spawn time — Codex ignores unknown `-c`
// keys silently — so a rename upstream has to be caught here.
func TestSpawnCommand_ConfigArgs(t *testing.T) {
	want := []string{
		"-c 'disable_paste_burst=true'",
		"-c 'check_for_update_on_startup=false'",
	}
	cases := []struct {
		name string
		opts agent.SpawnOptions
	}{
		{"fresh", agent.SpawnOptions{AgentSessionStarted: false}},
		{"resume", agent.SpawnOptions{
			AgentSessionID:      "01900000-0000-7000-8000-000000000abc",
			AgentSessionStarted: true,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := SpawnCommand(tc.opts, testExecPath)
			for _, w := range want {
				if !strings.Contains(plan.Command, w) {
					t.Errorf("Command missing %q: %q", w, plan.Command)
				}
			}
		})
	}
}

// TestSpawnCommand_ConfigArgsFollowBase checks the assembly order: the
// base command still comes first, so `codex resume UUID` keeps its
// positional argument adjacent to the subcommand.
func TestSpawnCommand_ConfigArgsFollowBase(t *testing.T) {
	uuid := "01900000-0000-7000-8000-000000000abc"
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      uuid,
		AgentSessionStarted: true,
	}, testExecPath)

	resumeIdx := strings.Index(plan.Command, "codex resume "+uuid)
	cfgIdx := strings.Index(plan.Command, "-c 'disable_paste_burst=true'")
	if resumeIdx < 0 || cfgIdx < 0 {
		t.Fatalf("both segments must be present: %q", plan.Command)
	}
	if resumeIdx > cfgIdx {
		t.Errorf("`codex resume UUID` must precede the -c overrides: %q", plan.Command)
	}
}

func TestSpawnCommand_UnsetEnv(t *testing.T) {
	plan := SpawnCommand(agent.SpawnOptions{}, testExecPath)

	want := []string{
		"CODEX_SANDBOX",
		"CODEX_SANDBOX_NETWORK_DISABLED",
		"CODEX_CI",
	}
	if !reflect.DeepEqual(plan.UnsetEnv, want) {
		t.Errorf("UnsetEnv = %v, want %v", plan.UnsetEnv, want)
	}
}

func TestSpawnCommand_UnsetEnvOmitsAuthKeys(t *testing.T) {
	// Defensive: authentication env vars MUST NOT appear in UnsetEnv, or
	// the spawned Codex will fail to authenticate.
	plan := SpawnCommand(agent.SpawnOptions{}, testExecPath)
	forbidden := []string{
		"CODEX_API_KEY",
		"CODEX_ACCESS_TOKEN",
		"OPENAI_API_KEY",
		"CODEX_HOME",
	}
	for _, v := range plan.UnsetEnv {
		for _, f := range forbidden {
			if v == f {
				t.Errorf("UnsetEnv includes auth/config var %q; it must be left set", v)
			}
		}
	}
}

func TestSpawnCommand_NoExtraEnv(t *testing.T) {
	// JIN_SESSION_ID is injected by Manager's shared wrapper (see
	// manager.go:957), so the adapter must not double up. Any adapter-side
	// ExtraEnv here would be an error signal for future maintenance.
	plan := SpawnCommand(agent.SpawnOptions{}, testExecPath)
	if len(plan.ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v, want empty (JIN_SESSION_ID is Manager's job)", plan.ExtraEnv)
	}
}

func TestSpawnCommand_ResumePlusHooks(t *testing.T) {
	// resume + hooks together: verify both survive in the final command
	// string and appear in the expected order (base command first, hook
	// args after — Codex parses positional args left-to-right).
	uuid := "01900000-0000-7000-8000-000000000def"
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      uuid,
		AgentSessionStarted: true,
	}, testExecPath)

	resumeIdx := strings.Index(plan.Command, "codex resume "+uuid)
	enableIdx := strings.Index(plan.Command, "--enable hooks")
	if resumeIdx < 0 || enableIdx < 0 {
		t.Fatalf("both segments must be present: %q", plan.Command)
	}
	if resumeIdx > enableIdx {
		t.Errorf("`codex resume UUID` must precede `--enable hooks`: %q", plan.Command)
	}
}
