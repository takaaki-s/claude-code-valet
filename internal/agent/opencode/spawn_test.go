package opencode

import (
	"reflect"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
)

const testConfigDir = "/home/u/.local/state/jind-ai/opencode"

// A pre-minted UUID is what Manager puts on Session.AgentSessionID before
// the agent has ever run. It must never reach `opencode --session`.
const preMintUUID = "01900000-0000-7000-8000-000000000abc"

// A real opencode session id, which always carries the ses_ prefix.
const realSessionID = "ses_084426f78ffeXBrPh5ABEu2dNX"

// resume is gated on one predicate — AgentSessionStarted AND a ses_ id —
// and it decides both the command line and whether a root is pinned, so the
// negative cases are checked together.
func TestSpawnCommand_DoesNotResume(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts agent.SpawnOptions
	}{
		{"never started", agent.SpawnOptions{}},
		// startSessionTmux sets AgentSessionStarted before the process
		// exists, so the flag alone must not be read as "resumable" — the
		// id is still the pre-minted UUID at that point.
		{"started with pre-mint uuid", agent.SpawnOptions{AgentSessionID: preMintUUID, AgentSessionStarted: true}},
		{"ses_ id but never started", agent.SpawnOptions{AgentSessionID: realSessionID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := SpawnCommand(tc.opts, testConfigDir)

			if plan.Command != "opencode" {
				t.Errorf("Command = %q, want bare %q", plan.Command, "opencode")
			}
			if strings.Contains(plan.Command, tc.opts.AgentSessionID) && tc.opts.AgentSessionID != "" {
				t.Errorf("session id leaked into command: %q", plan.Command)
			}
			// Pinning an id opencode never issued would make the plugin
			// ignore the real root once opencode creates it.
			if got, ok := plan.ExtraEnv[rootSessionEnv]; ok {
				t.Errorf("%s = %q, want unset", rootSessionEnv, got)
			}
		})
	}
}

func TestSpawnCommand_Resume(t *testing.T) {
	id := realSessionID
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      id,
		AgentSessionStarted: true,
	}, testConfigDir)

	want := "opencode --session " + id
	if plan.Command != want {
		t.Errorf("Command = %q, want %q", plan.Command, want)
	}
}

func TestSpawnCommand_ConfigDirEnv(t *testing.T) {
	plan := SpawnCommand(agent.SpawnOptions{}, testConfigDir)

	want := map[string]string{"OPENCODE_CONFIG_DIR": testConfigDir}
	if !reflect.DeepEqual(plan.ExtraEnv, want) {
		t.Errorf("ExtraEnv = %v, want %v", plan.ExtraEnv, want)
	}
}

// Setup failure must degrade to a working agent without status reporting,
// never to a failed spawn.
func TestSpawnCommand_NoConfigDir_FailsOpen(t *testing.T) {
	// The resume input is covered too: with no plugin to load there is
	// nothing to pin a root session for either.
	for _, opts := range []agent.SpawnOptions{
		{},
		{AgentSessionID: realSessionID, AgentSessionStarted: true},
	} {
		plan := SpawnCommand(opts, "")

		if plan.Command == "" {
			t.Error("Command is empty, want a runnable command")
		}
		if len(plan.ExtraEnv) != 0 {
			t.Errorf("ExtraEnv = %v, want empty when config dir is unknown", plan.ExtraEnv)
		}
	}
}

// Manager single-quotes ExtraEnv values, so the adapter must hand the path
// over raw. Pre-escaping here would double-escape at the shell.
func TestSpawnCommand_ConfigDirWithSpaces_NotPreEscaped(t *testing.T) {
	dir := "/home/some user/state/jin's dir/opencode"
	plan := SpawnCommand(agent.SpawnOptions{}, dir)

	if got := plan.ExtraEnv["OPENCODE_CONFIG_DIR"]; got != dir {
		t.Errorf("OPENCODE_CONFIG_DIR = %q, want verbatim %q", got, dir)
	}
}

// UnsetEnv is empty today; opencode ships no "already inside a sandbox"
// marker equivalent to CODEX_SANDBOX. Asserting it keeps a future addition
// deliberate rather than accidental.
func TestSpawnCommand_NoUnsetEnv(t *testing.T) {
	plan := SpawnCommand(agent.SpawnOptions{}, testConfigDir)

	if len(plan.UnsetEnv) != 0 {
		t.Errorf("UnsetEnv = %v, want empty", plan.UnsetEnv)
	}
}

// Resuming publishes no session.created, so jind-ai names the root it
// reopened. The plugin could also ask opencode, but only once the server
// answers — naming it keeps the first status correct before then.
func TestSpawnCommand_Resume_PinsRootSession(t *testing.T) {
	id := realSessionID
	plan := SpawnCommand(agent.SpawnOptions{
		AgentSessionID:      id,
		AgentSessionStarted: true,
	}, testConfigDir)

	if got := plan.ExtraEnv[rootSessionEnv]; got != id {
		t.Errorf("%s = %q, want %q", rootSessionEnv, got, id)
	}
}

// TestSpawnCommand_ResumesOnThePrefixAlone pins the looser of the two id
// predicates to the path that needs it.
//
// Getting this wrong is silent and unrecoverable: refusing to resume starts a
// NEW opencode session, so the operator's conversation is simply not there and
// nothing says why. The read path can afford to be strict because being wrong
// there costs an error message; this one cannot, so it asks only whether
// opencode has reported an id at all.
func TestSpawnCommand_ResumesOnThePrefixAlone(t *testing.T) {
	// Shapes the strict predicate rejects. If opencode ever widens its
	// alphabet, resume has to keep working.
	for _, id := range []string{
		"ses_0425f0107ffe2ruNWlf2QIqBEJ", // today's shape
		"ses_with-a-dash",
		"ses_with.dot",
		"ses_UPPER_and_under",
	} {
		plan := SpawnCommand(agent.SpawnOptions{AgentSessionID: id, AgentSessionStarted: true}, "")
		want := "opencode --session " + id
		if plan.Command != want {
			t.Errorf("SpawnCommand(%q) = %q, want %q — a resume was silently turned into a new session", id, plan.Command, want)
		}
	}

	// And the pre-minted UUID still means "not resumable".
	plan := SpawnCommand(agent.SpawnOptions{AgentSessionID: "0198f1b2-0000-7000-8000-000000000000", AgentSessionStarted: true}, "")
	if plan.Command != "opencode" {
		t.Errorf("SpawnCommand(uuid) = %q, want a fresh start", plan.Command)
	}
}
