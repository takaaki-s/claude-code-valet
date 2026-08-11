//go:build e2e

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/jinenv"
)

// The plugin chain depth is the one variable jind-ai writes into a pane in
// order to erase something rather than to say something: nothing on this path
// sets a depth, and the whole question is what the tmux server already holds.
// A mock cannot answer it — it is told what jind-ai writes and knows nothing of
// what tmux hands a pane it forks — so this test runs the real Manager against
// a real tmux server, forked from a process the test has polluted itself.

// envDumpAgent is e2eAgent with a spawn that writes the pane's own environment
// where the test can read it, then sits there the way e2eAgent's command does:
// startedSession waits for a live pane, and remain-on-exit leaves a dead one
// behind for a command that has already finished.
type envDumpAgent struct {
	e2eAgent
	dumpPath string
}

func (a envDumpAgent) SpawnCommand(SpawnOptions) SpawnPlan {
	return SpawnPlan{Command: "env > " + a.dumpPath + "; sleep 900"}
}

type envDumpResolver struct{ agent envDumpAgent }

func (r envDumpResolver) Resolve(string) (Agent, error) { return r.agent, nil }

// TestE2E_InnerServerDepthDoesNotReachTheAgentPane runs the whole chain the
// empty assignment closes, from a polluted process to the agent's environment.
//
// The chain: a `jin daemon start` issued from anything carrying a depth — a
// plugin entrypoint that starts the daemon it needs, or an agent in an
// already-polluted pane following the `jin daemon status || jin daemon start`
// line jind-ai ships in its own agent docs — leaves the daemon holding it, since
// the daemon command starts its background copy with os.Environ(). That daemon
// forks the inner tmux server here in startSessionTmux, through an exec.Cmd
// that sets no Env of its own, so tmux copies the depth into its global
// environment and hands it to every pane on that server. A `jin plugin run`
// from one of those panes reads it back as its caller's depth and is refused as
// a chain nobody started.
//
// t.Setenv stands in for the polluted daemon: this process is what forks the
// server, so its environment is the one tmux copies. (It is also why this test
// cannot call t.Parallel — the two panic together.)
//
// The control is not optional here. "The pane carries no depth" and "the server
// never had one" produce the same passing assertion, and only the first is the
// guarantee; reading the server's global environment first is the only thing
// that separates them.
func TestE2E_InnerServerDepthDoesNotReachTheAgentPane(t *testing.T) {
	// Before killFixture, which is where the first tmux command — and with it
	// the server fork — becomes possible.
	t.Setenv(jinenv.EnvDepth, "1")

	mgr, tc, _, _ := killFixture(t)
	dump := filepath.Join(t.TempDir(), "pane-env")
	mgr.SetAgentResolver(envDumpResolver{agent: envDumpAgent{dumpPath: dump}})

	startedSession(t, mgr, tc)

	if got, ok := serverGlobalEnv(t, tc.GetSocketName(), jinenv.EnvDepth); !ok || got != "1" {
		t.Fatalf("tmux server's global %s = %q (present=%v), want the depth this process was polluted with: "+
			"the server did not inherit it, so the pane assertion below would hold for the wrong reason",
			jinenv.EnvDepth, got, ok)
	}

	paneEnv := waitForEnvDump(t, dump)
	depth, assigned := paneEnv[jinenv.EnvDepth]
	if !assigned {
		// A different fault from a non-empty depth, and worth separating: the
		// key is written unconditionally, so its absence says the dump is not
		// the pane's environment rather than that the pane inherited anything.
		// The sibling key the same call emits tells the two apart.
		_, sibling := paneEnv["JIN_SESSION_ID"]
		t.Fatalf("the agent pane's environment carries no %s (JIN_SESSION_ID present: %v)", jinenv.EnvDepth, sibling)
	}
	if depth != "" {
		t.Errorf("agent pane %s = %q, want it empty: the pane took the server's depth, and every `jin plugin run` "+
			"the agent makes is refused as a chain", jinenv.EnvDepth, depth)
	}
}

// serverGlobalEnv reads one variable from the tmux server's global environment:
// the copy tmux takes of the environment of whatever process forked it, and
// what a pane on that server starts from. It shells out because the Client type
// reads the session environment instead (show-environment -t), which is a
// different table — measured, a variable inherited through the fork lands only
// in the global one, while the session table holds what update-environment
// carries and nothing else.
//
// The bool separates "set to something" from "not there at all": tmux fails the
// command outright for a name it does not hold, and a control that read that
// failure as an empty value would report the pollution as present.
func serverGlobalEnv(t *testing.T, socket, name string) (string, bool) {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "show-environment", "-g", name).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), false
	}
	return strings.CutPrefix(strings.TrimSpace(string(out)), name+"=")
}

// waitForEnvDump reads back the environment the agent pane wrote. tmux returns
// from new-session as soon as it has forked the command, and an interactive
// shell starts before it runs anything, so polling is the whole of the
// synchronisation.
func waitForEnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	var dumped []byte
	waitFor(t, "the agent pane to dump its environment", func() bool {
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			return false
		}
		dumped = b
		return true
	})
	env := map[string]string{}
	for _, line := range strings.Split(string(dumped), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			env[name] = value
		}
	}
	return env
}
