package claude

import (
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

func TestSpawnCommand_HooksPathAddsSettingsFlag(t *testing.T) {
	// Goes through Setup rather than assigning the field: hooksPath is guarded
	// by setupMu, so a test that poked it directly would be an unlocked write
	// and -race would be right to complain.
	isolateHome(t)
	stateDir := t.TempDir()
	a := New()
	if err := a.Setup(agent.SetupContext{StateDir: stateDir, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	want := "--settings " + hooksSettingsPath(stateDir)
	plan := a.SpawnCommand(agent.SpawnOptions{})
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
