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

// newManagerWithSocket builds a Manager told that its daemon listens on
// socketPath, so an assertion on the command line is about what travelled from
// NewManager's argument rather than about whatever default a helper chose.
func newManagerWithSocket(t *testing.T, socketPath string) *Manager {
	t.Helper()
	configMgr, err := config.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	mgr, err := NewManager(t.TempDir(), t.TempDir(), socketPath, configMgr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetTmuxClient(newMockTmuxRunner())
	mgr.SetHookRunner(newMockHookRunner())
	return mgr
}

// withDebugEnabled drives the flag Manager reads, which no test can arrange
// through the real environment: the process decides once at startup.
func withDebugEnabled(t *testing.T, on bool) {
	t.Helper()
	setForTest(t, &debugEnabled, func() bool { return on })
}

// TestBuildAgentShellCmd_NamesTheDaemonTheAgentCallsBackTo pins the wiring from
// NewManager's socketPath argument through to the agent's environment.
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
func TestBuildAgentShellCmd_NamesTheDaemonTheAgentCallsBackTo(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "this-run-only.sock")
	mgr := newManagerWithSocket(t, socket)

	cmd := probeShellCmd(t, mgr)

	if want := "JIN_SOCKET='" + socket + "'"; !strings.Contains(cmd, want) {
		t.Errorf("command does not carry %s\ncommand: %s", want, cmd)
	}
}

// TestBuildAgentShellCmd_NamesTheBinaryTheAgentReEnters pins that the agent is
// pointed at the stable copy of the jin binary, not at the daemon's live
// executable.
//
// The distinction is the whole reason the copy exists: this environment is read
// once, when the agent starts, and is never revisited, so a path into whatever
// directory the daemon launched from can stop existing while the session is
// still running. EstablishHookBinary has the full account; what is checked here
// is only that the field it upgrades is the field this builder reads.
func TestBuildAgentShellCmd_NamesTheBinaryTheAgentReEnters(t *testing.T) {
	mgr := newManagerWithSocket(t, testSocketPath)
	stable := filepath.Join(t.TempDir(), "bin", "jin")
	mgr.hookExecPath = stable

	cmd := probeShellCmd(t, mgr)

	if want := "JIN_BIN='" + stable + "'"; !strings.Contains(cmd, want) {
		t.Errorf("command does not carry %s\ncommand: %s", want, cmd)
	}
	live, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if strings.Contains(cmd, "JIN_BIN='"+live+"'") {
		t.Errorf("command names the live executable rather than the stable copy\ncommand: %s", cmd)
	}
}

// TestBuildAgentShellCmd_QuotesTheValuesItPropagates guards the one thing that
// separates these assignments from the literal "1" that used to be the only
// propagated value: they are now paths, and a path may contain a space. Left
// bare, `env` would read the remainder as the command to run.
func TestBuildAgentShellCmd_QuotesTheValuesItPropagates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a dir with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	socket := filepath.Join(dir, "daemon.sock")
	mgr := newManagerWithSocket(t, socket)

	cmd := probeShellCmd(t, mgr)

	if want := "JIN_SOCKET='" + socket + "'"; !strings.Contains(cmd, want) {
		t.Errorf("a socket path with a space is not quoted as one word\nwant substring: %s\ncommand: %s", want, cmd)
	}
}

// TestBuildAgentShellCmd_OmitsWhatItDoesNotKnow pins that an unknown value is
// left out rather than assigned empty. An empty JIN_SOCKET reads to every
// consumer as no socket at all, so writing one adds nothing except a claim to
// know something.
func TestBuildAgentShellCmd_OmitsWhatItDoesNotKnow(t *testing.T) {
	mgr := newManagerWithSocket(t, "")
	mgr.hookExecPath = ""

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
			withDebugEnabled(t, tt.on)

			mgr, _, _ := newTestManager(t)
			cmd := probeShellCmd(t, mgr)

			if got := strings.Contains(cmd, "JIN_DEBUG='1'"); got != tt.on {
				t.Errorf("JIN_DEBUG present = %v, want %v\ncommand: %s", got, tt.on, cmd)
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

	propagated := strings.Index(cmd, "JIN_DEBUG='1'")
	configured := strings.Index(cmd, "JIN_DEBUG='0'")
	if propagated < 0 || configured < 0 {
		t.Fatalf("expected both assignments on the command line\ncommand: %s", cmd)
	}
	if configured < propagated {
		t.Errorf("the configured value is applied before the propagated one, so it loses\ncommand: %s", cmd)
	}
}
