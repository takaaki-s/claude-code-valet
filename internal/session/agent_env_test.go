package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/internal/jinenv"
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

// TestBuildAgentShellCmd_NamesTheDaemonTheAgentCallsBackTo pins the wiring from
// NewManager's identity argument through to the agent's environment.
//
// The value is unique per run so that an implementation which hands out a
// constant — the default socket path, or the state dir it sits next to —
// cannot pass by coincidence. Nothing but this argument produces it.
//
// Leaving it to inheritance is what this replaces: a tmux pane is handed the
// tmux server's environment, and that server outlives any one daemon, so the
// socket an agent saw was whichever daemon happened to start the server. When
// none had, the agent's hooks reached no daemon at all and said nothing about
// it — exit 0, status unchanged.
//
// The quoting is part of the assertion. These values are paths, and a path may
// contain a space; left bare, `env` would read the remainder as the command to
// run.
func TestBuildAgentShellCmd_NamesTheDaemonTheAgentCallsBackTo(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "this-run-only.sock")
	mgr, _, _ := newTestManagerOn(t, jinenv.Identity{SocketPath: socket})

	cmd := probeShellCmd(t, mgr)

	if want := "'JIN_SOCKET=" + socket + "'"; !strings.Contains(cmd, want) {
		t.Errorf("command does not carry %s\ncommand: %s", want, cmd)
	}
}

// TestBuildAgentShellCmd_NamesTheBinaryTheAgentReEnters pins that the agent is
// pointed at the binary its identity names, not at a path this process resolves
// for itself.
//
// The distinction is the whole reason a stable copy exists: this environment is
// read once, when the agent starts, and is never revisited, so a path into
// whatever directory the daemon launched from can stop describing the running
// daemon — or stop existing — while the session is still going.
// EstablishHookBinary has the full account. os.Executable() is checked for
// explicitly because it is also a plausible non-empty path, so an assertion on
// presence alone would accept it.
func TestBuildAgentShellCmd_NamesTheBinaryTheAgentReEnters(t *testing.T) {
	stable := filepath.Join(t.TempDir(), "bin", "jin")
	mgr, _, _ := newTestManagerOn(t, jinenv.Identity{BinPath: stable})

	cmd := probeShellCmd(t, mgr)

	if want := "'JIN_BIN=" + stable + "'"; !strings.Contains(cmd, want) {
		t.Errorf("command does not carry %s\ncommand: %s", want, cmd)
	}
	live, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if strings.Contains(cmd, "'JIN_BIN="+live+"'") {
		t.Errorf("command names the live executable rather than the identity's binary\ncommand: %s", cmd)
	}
}

// TestBuildAgentShellCmd_TellsTheAdapterTheSameBinary pins the other half of
// the agent's hook wiring. The environment this builder writes is only what the
// agent process sees; an adapter separately bakes ExecPath into the settings
// file or CLI injection the agent's hooks are launched from, and a session
// whose hooks name a different binary than its environment does is exactly the
// split this identity exists to prevent.
func TestBuildAgentShellCmd_TellsTheAdapterTheSameBinary(t *testing.T) {
	stable := filepath.Join(t.TempDir(), "bin", "jin")
	mgr, _, _ := newTestManagerOn(t, jinenv.Identity{BinPath: stable})

	var seen SetupContext
	mgr.SetAgentResolver(&fakeAgentResolver{agents: map[string]Agent{
		"probe": &fakeAgent{
			spawnFn: func(SpawnOptions) SpawnPlan { return SpawnPlan{Command: "true"} },
			setupFn: func(ctx SetupContext) error { seen = ctx; return nil },
		},
	}})
	if _, err := mgr.buildAgentShellCmd(spawnSnapshot{
		JinSessionID: "probe-session",
		AgentKind:    "probe",
		StartDir:     t.TempDir(),
	}); err != nil {
		t.Fatalf("buildAgentShellCmd: %v", err)
	}

	if seen.ExecPath != stable {
		t.Errorf("adapter is told to wire hooks through %q, want %q", seen.ExecPath, stable)
	}
}

// TestBuildAgentShellCmd_OmitsWhatItDoesNotKnow pins that an unknown value is
// left out rather than assigned empty. An empty JIN_SOCKET reads to every
// consumer as no socket at all, so writing one adds nothing except a claim to
// know something.
func TestBuildAgentShellCmd_OmitsWhatItDoesNotKnow(t *testing.T) {
	mgr, _, _ := newTestManagerOn(t, jinenv.Identity{})

	cmd := probeShellCmd(t, mgr)

	for _, key := range []string{"JIN_SOCKET", "JIN_BIN"} {
		if strings.Contains(cmd, key) {
			t.Errorf("%s is assigned despite having no value\ncommand: %s", key, cmd)
		}
	}
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
			mgr, _, _ := newTestManagerOn(t, jinenv.Identity{Debug: tt.on})
			cmd := probeShellCmd(t, mgr)

			if got := strings.Contains(cmd, "'JIN_DEBUG=1'"); got != tt.on {
				t.Errorf("JIN_DEBUG present = %v, want %v\ncommand: %s", got, tt.on, cmd)
			}
		})
	}
}

// TestBuildAgentShellCmd_ConfiguredDebugValueWinsOverThePropagatedOne pins the
// ordering buildAgentShellCmd's comment claims: an operator who names JIN_DEBUG
// in their own config is deciding something about the child, and jind-ai's
// propagation must not quietly override it.
func TestBuildAgentShellCmd_ConfiguredDebugValueWinsOverThePropagatedOne(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("env:\n  JIN_DEBUG: \"0\"\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configMgr, err := config.NewManager(configDir)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	mgr, _, _ := newTestManagerOn(t, jinenv.Identity{Debug: true})
	mgr.configMgr = configMgr

	cmd := probeShellCmd(t, mgr)

	propagated := strings.Index(cmd, "'JIN_DEBUG=1'")
	configured := strings.Index(cmd, "JIN_DEBUG='0'")
	if propagated < 0 || configured < 0 {
		t.Fatalf("expected both assignments on the command line\ncommand: %s", cmd)
	}
	if configured < propagated {
		t.Errorf("the configured value is applied before the propagated one, so it loses\ncommand: %s", cmd)
	}
}
