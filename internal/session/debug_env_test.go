package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/internal/debug"
)

// probeShellCmd returns the shell command Manager would run to start a session,
// for an adapter that contributes nothing of its own. What is left is the
// environment Manager assembles, which is what these tests are about.
func probeShellCmd(t *testing.T, mgr *Manager) string {
	t.Helper()
	mgr.SetAgentResolver(&fakeAgentResolver{agents: map[string]Agent{
		"probe": &fakeAgent{spawnFn: func(SpawnOptions) SpawnPlan {
			return SpawnPlan{Command: "true"}
		}},
	}})
	cmd, err := mgr.buildAgentShellCmd(spawnSnapshot{
		JinSessionID: "probe-session",
		AgentKind:    "probe",
		StartDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("buildAgentShellCmd: %v", err)
	}
	return cmd
}

// withDebugEnabled drives the flag Manager reads, which no test can arrange
// through the real environment: the process decides once at startup.
func withDebugEnabled(t *testing.T, on bool) {
	t.Helper()
	setForTest(t, &debugEnabled, func() bool { return on })
}

// TestBuildAgentShellCmd_PassesTheDebugFlagToTheAgent pins that the flag
// reaches the process that runs `jin hook`, and only when it is on.
// buildAgentShellCmd's comment has the why.
func TestBuildAgentShellCmd_PassesTheDebugFlagToTheAgent(t *testing.T) {
	for _, tt := range []struct {
		name string
		on   bool
	}{
		{"on, so the hook the agent runs records too", true},
		{"off, so nothing is injected", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withDebugEnabled(t, tt.on)

			mgr, _, _ := newTestManager(t)
			cmd := probeShellCmd(t, mgr)

			if got := strings.Contains(cmd, "JIN_DEBUG=1"); got != tt.on {
				t.Errorf("JIN_DEBUG=1 present = %v, want %v\ncommand: %s", got, tt.on, cmd)
			}
		})
	}
}

// TestDebugEnabled_IsWiredToTheRealFlag guards the one link the tests above
// cannot: they replace debugEnabled, so a version defined as a constant false
// would satisfy every one of them.
//
// Its reach is limited and saying so is the point — it can only fail in a
// process that has the flag on, because with it off a disconnected default
// agrees with the real answer by accident. So it bites under `JIN_DEBUG=1 go
// test` and is silent otherwise. That is still worth having: the mutation it
// exists for is the kind that produces a working binary, and the only signal
// would otherwise be someone noticing months later that a log they turned on
// was never written.
func TestDebugEnabled_IsWiredToTheRealFlag(t *testing.T) {
	if got, want := debugEnabled(), debug.Enabled(); got != want {
		t.Errorf("debugEnabled() = %v, but debug.Enabled() = %v — the seam has been disconnected from the flag it stands for", got, want)
	}
}

// TestBuildAgentShellCmd_ConfiguredDebugValueWinsOverThePropagatedOne pins the
// ordering buildAgentShellCmd's comment claims: an operator who names JIN_DEBUG
// in their own config is deciding something about the child, and jind-ai's
// propagation must not quietly override it.
func TestBuildAgentShellCmd_ConfiguredDebugValueWinsOverThePropagatedOne(t *testing.T) {
	withDebugEnabled(t, true)

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("env:\n  JIN_DEBUG: \"0\"\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configMgr, err := config.NewManager(configDir)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	mgr, _, _ := newTestManager(t)
	mgr.configMgr = configMgr

	cmd := probeShellCmd(t, mgr)

	// The configured value is shell-quoted by buildEnvString; the propagated
	// one is a literal jind-ai chose itself and needs no quoting.
	propagated := strings.Index(cmd, "JIN_DEBUG=1")
	configured := strings.Index(cmd, "JIN_DEBUG='0'")
	if propagated < 0 || configured < 0 {
		t.Fatalf("expected both assignments on the command line\ncommand: %s", cmd)
	}
	if configured < propagated {
		t.Errorf("the configured value is applied before the propagated one, so it loses\ncommand: %s", cmd)
	}
}
