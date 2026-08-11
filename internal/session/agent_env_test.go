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
// NewManager's identity argument through to the agent's environment. The value
// is unique per run so that an implementation handing out a constant — the
// default socket path, or the state dir it sits next to — cannot pass by
// coincidence.
//
// Leaving it to inheritance is what this replaces: a tmux pane is handed the
// tmux server's environment, and that server outlives any one daemon, so the
// socket an agent saw was whichever daemon happened to start the server. When
// none had, the agent's hooks reached no daemon at all and said nothing about
// it — exit 0, status unchanged.
//
// The quoting is part of the assertion. These values are paths, and left bare
// `env` would read the remainder as the command to run.
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
// This environment is read once, when the agent starts, and never revisited, so
// a path into whatever directory the daemon launched from can stop describing
// the running daemon — or stop existing — while the session is still going.
// os.Executable() is checked for explicitly because it is also a plausible
// non-empty path, so an assertion on presence alone would accept it.
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

// TestBuildAgentShellCmd_TellsTheAdapterOneStory pins the obligation that lets
// adapters carry nothing between calls: this builder, and nothing else, resolves
// the paths a spawn is built from, and it must put them on BOTH structs it hands
// the adapter.
//
// It belongs here rather than beside an adapter because this is where the values
// are decided. An adapter test can only ask "given these options, what comes
// back?" — and each adapter answers an absent path by quietly dropping what
// depended on it: claude its --settings and opencode its OPENCODE_CONFIG_DIR
// when StateDir is empty, codex its whole hook injection when ExecPath is.
// (Measured, and not symmetric: an empty StateDir alone leaves codex injecting
// hooks as usual, because its payload is built from ExecPath.) All three still
// return a runnable command, so a builder that quietly stopped passing either
// value would leave three adapters silently unconfigured — measured with every
// test in ./internal/agent/... still green.
//
// The agreement half is what earns the type duplication: SetupContext is
// deliberately narrower than SpawnOptions, so its three fields are written twice
// from one call site, and that is only safe while the two cannot disagree.
func TestBuildAgentShellCmd_TellsTheAdapterOneStory(t *testing.T) {
	stable := filepath.Join(t.TempDir(), "bin", "jin")
	workDir := t.TempDir()
	mgr, _, _ := newTestManagerOn(t, jinenv.Identity{BinPath: stable})

	var setup SetupContext
	var spawn SpawnOptions
	mgr.SetAgentResolver(&fakeAgentResolver{agents: map[string]Agent{
		"probe": &fakeAgent{
			setupFn: func(ctx SetupContext) error { setup = ctx; return nil },
			spawnFn: func(opts SpawnOptions) SpawnPlan {
				spawn = opts
				return SpawnPlan{Command: "true"}
			},
		},
	}})
	if _, err := mgr.buildAgentShellCmd(spawnSnapshot{
		JinSessionID:    "probe-session",
		AgentKind:       "probe",
		StartDir:        t.TempDir(),
		ExpandedWorkDir: workDir,
	}); err != nil {
		t.Fatalf("buildAgentShellCmd: %v", err)
	}

	// Compared against the Manager's own field, not against a literal: the
	// point is that the adapter is told where THIS Manager keeps its state.
	//
	// Every field is compared against the value it should carry, never merely
	// against its counterpart. Two fields agreeing says nothing about either
	// being right, and this test has been wrong that way twice: the WorkDir pair
	// was "" == "" until the snapshot above started carrying one, and once it
	// did, a mutant handing BOTH structs snap.StartDir instead still passed.
	if mgr.stateDir == "" {
		t.Fatal("test manager has no state dir, so the assertions below would hold vacuously")
	}
	if spawn.StateDir != mgr.stateDir {
		t.Errorf("SpawnOptions.StateDir = %q, want this Manager's own %q", spawn.StateDir, mgr.stateDir)
	}
	if spawn.ExecPath != stable {
		t.Errorf("SpawnOptions.ExecPath = %q, want the identity's binary %q", spawn.ExecPath, stable)
	}
	if setup.StateDir != spawn.StateDir {
		t.Errorf("Setup was pointed at %q while the spawn was built for %q; whatever Setup wrote, "+
			"SpawnCommand is looking somewhere else", setup.StateDir, spawn.StateDir)
	}
	if setup.ExecPath != spawn.ExecPath {
		t.Errorf("Setup wired callbacks through %q while the spawn names %q", setup.ExecPath, spawn.ExecPath)
	}
	// Both WorkDirs must be the TILDE-EXPANDED directory, which is a stronger
	// requirement than agreeing with each other. snapshotForSpawn carries two work
	// dirs — StartDir as the session recorded it, which may still begin with "~",
	// and ExpandedWorkDir, which is what the pane is actually created in. The
	// Claude Code adapter records trust for the one Setup is handed, and
	// EnsureTrustState only calls filepath.Abs, so handing it the unexpanded form
	// registers a literal "~" directory under the daemon's own cwd — a trust dialog
	// nobody is watching, for a path that does not exist. Measured: a mutant
	// putting StartDir on both structs satisfies an equality-only check.
	//
	// SpawnOptions.WorkDir has no adapter reading it today — zero non-test hits
	// under internal/agent/ — but is pinned anyway, because it is part of the
	// interface an adapter may start using at any time.
	if setup.WorkDir != workDir {
		t.Errorf("SetupContext.WorkDir = %q, want the expanded work dir %q", setup.WorkDir, workDir)
	}
	if spawn.WorkDir != workDir {
		t.Errorf("SpawnOptions.WorkDir = %q, want the expanded work dir %q", spawn.WorkDir, workDir)
	}
}

// TestBuildAgentShellCmd_AssignsEvenWhatItDoesNotKnow pins the opposite of what
// this test used to. Leaving an unknown value out looked harmless — every
// consumer reads an empty JIN_SOCKET as no socket, so the assignment seemed to
// add nothing — but the destination is not a blank environment. `env` without
// -i adds to what it is handed, and what tmux hands a pane is the tmux server's
// environment, so an omitted key is not absent, it is the server's. Measured:
// with the daemon's flag off and a server forked from an environment carrying
// JIN_DEBUG=1, the agent pane ran with JIN_DEBUG=1, 3/3.
func TestBuildAgentShellCmd_AssignsEvenWhatItDoesNotKnow(t *testing.T) {
	mgr, _, _ := newTestManagerOn(t, jinenv.Identity{})

	cmd := probeShellCmd(t, mgr)

	for _, key := range []string{"JIN_SOCKET", "JIN_BIN", "JIN_DEBUG"} {
		if !strings.Contains(cmd, "'"+key+"='") {
			t.Errorf("%s is left out, so the agent inherits the tmux server's\ncommand: %s", key, cmd)
		}
	}
}

// TestBuildAgentShellCmd_NamesTheSessionItStarts pins the id the agent's hooks
// will report under. Nothing covered it before: the assignment was built inline
// from the snapshot, and every test that read the command was reading it for
// some other key. Replacing the snapshot's id with a constant passed the suite.
func TestBuildAgentShellCmd_NamesTheSessionItStarts(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	cmd := probeShellCmd(t, mgr)

	if !strings.Contains(cmd, "'JIN_SESSION_ID=probe-session'") {
		t.Errorf("the agent is not told which session it is\ncommand: %s", cmd)
	}
}

// TestBuildAgentShellCmd_TellsTheAgentItIsNotInAPluginChain pins the other key
// TmuxEnviron appends past the identity, and the one no value here could be
// right for: a depth belongs to the plugin process that is running, and the
// agent this line starts is not continuing one.
//
// What it prevents is inheritance rather than a wrong value written here.
// startSessionTmux forks the inner tmux server from the daemon's own process, so
// a daemon started from something that carried a depth hands one to that server,
// and the server hands it to every pane it opens. `jin plugin run` from such a
// pane reads it back as its caller's depth and the dispatcher refuses the run as
// a chain — see maxDepth.
//
// The closing quote is part of the assertion: shellEscape quotes the whole
// assignment, so 'JIN_PLUGIN_DEPTH=' matches only the empty one.
func TestBuildAgentShellCmd_TellsTheAgentItIsNotInAPluginChain(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	cmd := probeShellCmd(t, mgr)

	if want := "'" + jinenv.EnvDepth + "='"; !strings.Contains(cmd, want) {
		t.Errorf("command does not carry %s, so the agent inherits whatever depth the tmux server holds\ncommand: %s", want, cmd)
	}
}

// TestBuildAgentShellCmd_PassesTheDebugFlagToTheAgent pins that the flag
// reaches the process that runs `jin hook`, and reaches it as what the daemon
// decided rather than as silence. Off is an assignment too: buildAgentShellCmd's
// comment has why an omitted key does not mean off here.
func TestBuildAgentShellCmd_PassesTheDebugFlagToTheAgent(t *testing.T) {
	for _, tt := range []struct {
		name string
		on   bool
		want string
	}{
		{"on, so the hook the agent runs records too", true, "'JIN_DEBUG=1'"},
		{"off, so the agent is told off rather than left to inherit it", false, "'JIN_DEBUG='"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mgr, _, _ := newTestManagerOn(t, jinenv.Identity{Debug: tt.on})
			cmd := probeShellCmd(t, mgr)

			if !strings.Contains(cmd, tt.want) {
				t.Errorf("want %s in the command\ncommand: %s", tt.want, cmd)
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
