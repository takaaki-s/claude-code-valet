package claude

import (
	"os"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

// A real Claude Code session id, which is always a UUID.
const realSessionID = "0198f1b2-4c3d-7a1e-8b2f-000000000abc"

func TestSpawnCommand_FreshSessionUsesSessionIDFlag(t *testing.T) {
	a := New()
	plan := a.SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      realSessionID,
		AgentSessionStarted: false,
	})
	if !strings.Contains(plan.Command, `--session-id "$`+sessionArgEnv+`"`) {
		t.Errorf("Command = %q, want to name the id through %s", plan.Command, sessionArgEnv)
	}
	if strings.Contains(plan.Command, "--resume") {
		t.Errorf("Command = %q, must not contain --resume on a fresh spawn", plan.Command)
	}
	if got := plan.ExtraEnv[sessionArgEnv]; got != realSessionID {
		t.Errorf("%s = %q, want %q", sessionArgEnv, got, realSessionID)
	}
}

func TestSpawnCommand_StartedSessionUsesResumeFlag(t *testing.T) {
	a := New()
	plan := a.SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      realSessionID,
		AgentSessionStarted: true,
	})
	if !strings.Contains(plan.Command, `--resume "$`+sessionArgEnv+`"`) {
		t.Errorf("Command = %q, want to name the id through %s", plan.Command, sessionArgEnv)
	}
	if strings.Contains(plan.Command, "--session-id") {
		t.Errorf("Command = %q, must not carry --session-id when resuming", plan.Command)
	}
	if got := plan.ExtraEnv[sessionArgEnv]; got != realSessionID {
		t.Errorf("%s = %q, want %q", sessionArgEnv, got, realSessionID)
	}
}

// TestRecognizesSessionID checks the wiring, not the lexicon: this adapter
// answers with agent.LooksLikeUUID, whose own tests own the accept/reject
// corpus. Restating that corpus here would put the same judgement in three
// packages and let two of them drift.
//
// One accept and one reject are enough to catch both ways the delegation can be
// lost — a constant true, and a swap to another adapter's predicate.
func TestRecognizesSessionID(t *testing.T) {
	a := New()
	if !a.RecognizesSessionID(realSessionID) {
		t.Errorf("RecognizesSessionID(%q) = false; a real Claude Code id was refused", realSessionID)
	}
	if id := "ses_084426f78ffeXBrPh5ABEu2dNX"; a.RecognizesSessionID(id) {
		t.Errorf("RecognizesSessionID(%q) = true; that is opencode's shape, not this adapter's", id)
	}
}

func TestSpawnCommand_EmptyAgentSessionIDOmitsBothFlags(t *testing.T) {
	a := New()
	plan := a.SpawnCommand(agent.SpawnOptions{AgentSessionID: ""})
	if strings.Contains(plan.Command, "--session-id") || strings.Contains(plan.Command, "--resume") {
		t.Errorf("Command = %q, should be plain `claude` when no AgentSessionID is given", plan.Command)
	}
	// The variable and the flag that names it are one decision: exporting an
	// empty JIN_CLAUDE_SESSION for a command that never mentions it would be
	// harmless today and misleading to read.
	if len(plan.ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v, want empty when there is no id to carry", plan.ExtraEnv)
	}
}

// TestSpawnCommand_EmptyStateDirOmitsSettingsFlag pins the guard in
// existingHooksSettings, and the staging is what gives it force.
//
// hooksSettingsPath("") is the RELATIVE name "hooks-settings.json". The spawn
// this answers for runs in the session's own working directory, so without the
// guard a repository that happened to contain a file by that name would be
// handed to Claude Code as its hook configuration — a file of shell commands,
// from the checkout rather than from jind-ai's state.
//
// Run from a directory with no such file, the read fails whether or not the
// guard exists and the assertion holds for the wrong reason: measured, removing
// the guard left the whole suite green. Standing where the relative name WOULD
// resolve is what separates "guarded" from "got lucky".
func TestSpawnCommand_EmptyStateDirOmitsSettingsFlag(t *testing.T) {
	isolateHome(t)
	t.Chdir(t.TempDir())
	// Staged under the package's own spelling of the name: with a literal here,
	// renaming the file would leave the bait where nothing looks for it and
	// this assertion would pass for the wrong reason. Usable by
	// usableHooksSettings, so nothing but the guard can reject it.
	bait := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo bait","timeout":5}]}]}}`)
	if err := os.WriteFile(hooksSettingsFileName, bait, 0o644); err != nil {
		t.Fatalf("staging the relative file: %v", err)
	}

	plan := New().SpawnCommand(agent.SpawnOptions{})
	if strings.Contains(plan.Command, "--settings") {
		t.Errorf("Command = %q, want no --settings: an empty state dir names nothing", plan.Command)
	}
}

func TestSpawnCommand_HooksPathAddsSettingsFlag(t *testing.T) {
	// Goes through Setup rather than planting a file, so what is exercised is
	// the pair a real spawn runs: Setup writes into the state dir, and
	// SpawnCommand — handed the same directory — finds what it left.
	isolateHome(t)
	stateDir := t.TempDir()
	a := New()
	if err := a.Setup(agent.SetupContext{StateDir: stateDir, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	want := "--settings " + hooksSettingsPath(stateDir)
	plan := a.SpawnCommand(agent.SpawnOptions{StateDir: stateDir})
	if !strings.Contains(plan.Command, want) {
		t.Errorf("Command = %q, want %q", plan.Command, want)
	}
}

func TestSpawnCommand_UnsetsCCInheritanceEnv(t *testing.T) {
	a := New()
	plan := a.SpawnCommand(agent.SpawnOptions{})

	// Every Claude Code var that could leak in from a CC-parent env must
	// be unset — see spawn.go for the failure mode when
	// CLAUDE_CODE_CHILD_SESSION survives (CC 2.x refuses to persist a
	// transcript, silently breaking Layer C).
	required := map[string]bool{
		"CLAUDECODE":                false,
		"CLAUDE_CODE_CHILD_SESSION": false,
		"CLAUDE_CODE_SESSION_ID":    false,
		"CLAUDE_CODE_ENTRYPOINT":    false,
	}
	for _, k := range plan.UnsetEnv {
		if _, ok := required[k]; ok {
			required[k] = true
		}
	}
	for k, seen := range required {
		if !seen {
			t.Errorf("UnsetEnv = %v, want to contain %s", plan.UnsetEnv, k)
		}
	}
}
