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

	// The id is named, not spliced. See sessionArgEnv for what splicing it cost;
	// the rule itself is enforced for every registered adapter by
	// TestSpawnCommand_NoAdapterPutsUntrustedValuesInTheCommand.
	want := `opencode --session "$JIN_OPENCODE_SESSION"`
	if plan.Command != want {
		t.Errorf("Command = %q, want %q", plan.Command, want)
	}
	if got := plan.ExtraEnv[sessionArgEnv]; got != id {
		t.Errorf("%s = %q, want the session id %q — the command names it, so an absent value resumes nothing",
			sessionArgEnv, got, id)
	}
	if strings.Contains(plan.Command, id) {
		t.Errorf("Command still carries the id verbatim: %q", plan.Command)
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
	plan := SpawnCommand(agent.SpawnOptions{}, "")
	if plan.Command == "" {
		t.Error("Command is empty, want a runnable command")
	}
	if len(plan.ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v, want empty when config dir is unknown", plan.ExtraEnv)
	}

	// A resume is the exception, and it has to be: the command names
	// sessionArgEnv whether or not a plugin was written, so dropping the value
	// with the rest of the environment would resume nothing and silently start
	// a fresh session instead.
	resume := SpawnCommand(agent.SpawnOptions{AgentSessionID: realSessionID, AgentSessionStarted: true}, "")
	if got := resume.ExtraEnv[sessionArgEnv]; got != realSessionID {
		t.Errorf("%s = %q, want the id even with no config dir", sessionArgEnv, got)
	}
	if _, ok := resume.ExtraEnv[configDirEnv]; ok {
		t.Errorf("ExtraEnv leaked a config dir: %v", resume.ExtraEnv)
	}
	if _, ok := resume.ExtraEnv[rootSessionEnv]; ok {
		t.Errorf("ExtraEnv pinned a root session with no plugin to read it: %v", resume.ExtraEnv)
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

// Every spawn clears the plugin's nested mark, including the one that failed
// open with no config dir — a pane inheriting it would run an opencode that
// reports no status for any session and says nothing about why (see nestedEnv).
//
// Compared exactly rather than by presence: what belongs here is one name, and
// an entry arriving by accident is the thing the previous version of this test
// existed to catch.
func TestSpawnCommand_ClearsTheNestedMark(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      agent.SpawnOptions
		configDir string
	}{
		{"fresh", agent.SpawnOptions{}, testConfigDir},
		{"resume", agent.SpawnOptions{AgentSessionID: realSessionID, AgentSessionStarted: true}, testConfigDir},
		{"no config dir", agent.SpawnOptions{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := SpawnCommand(tc.opts, tc.configDir)

			if !reflect.DeepEqual(plan.UnsetEnv, []string{nestedEnv}) {
				t.Errorf("UnsetEnv = %v, want exactly [%s]", plan.UnsetEnv, nestedEnv)
			}
		})
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
		if !strings.Contains(plan.Command, "--session") || plan.ExtraEnv[sessionArgEnv] != id {
			t.Errorf("SpawnCommand(%q) = %q env %v — a resume was silently turned into a new session",
				id, plan.Command, plan.ExtraEnv)
		}
	}

	// And the pre-minted UUID still means "not resumable".
	plan := SpawnCommand(agent.SpawnOptions{AgentSessionID: "0198f1b2-0000-7000-8000-000000000000", AgentSessionStarted: true}, "")
	if plan.Command != "opencode" {
		t.Errorf("SpawnCommand(uuid) = %q, want a fresh start", plan.Command)
	}
}

// TestRecognizesSessionID_MatchesTheResumeGate is the invariant that lets this
// adapter be gated at all.
//
// A refused id is never recorded, and for opencode that is silent and
// unrecoverable: no ses_ id means no resume, which starts a NEW session with
// the operator's conversation simply absent. Answering the write gate with the
// same predicate the resume path uses makes the two sets identical, so the gate
// cannot refuse an id that would have been resumed — it introduces no failure
// that was not already there.
//
// If someone tightens either side, this fails. That is the point: the pairing
// is the argument for the gate's safety, not an incidental duplication.
func TestRecognizesSessionID_MatchesTheResumeGate(t *testing.T) {
	a := New()
	for _, id := range []string{
		realSessionID,
		"ses_with-a-dash",
		"ses_with.dot",
		"ses_UPPER_and_under",
		"ses_x",
		"0198f1b2-4c3d-7a1e-8b2f-000000000abc", // a UUID is not an opencode id
		"ses_",
		"",
		"abc",
	} {
		plan := SpawnCommand(agent.SpawnOptions{AgentSessionID: id, AgentSessionStarted: true}, "")
		resumes := strings.Contains(plan.Command, "--session")
		if got := a.RecognizesSessionID(id); got != resumes {
			t.Errorf("RecognizesSessionID(%q) = %v but the resume gate says %v — the two have drifted",
				id, got, resumes)
		}
	}
}

// hostileModel — this is the adapter where the lesson was measured, so the
// model gets the posture sessionArgEnv's doc describes. opencode spells models
// `provider/model`; nothing here checks that, which is the point of the
// pass-through and the reason the value needs the same handling as an id.
const hostileModel = "anthropic/opus$(touch PWNED)`touch PWNED2`; touch PWNED3"

func TestSpawnCommand_ModelNamesEnvNeverText(t *testing.T) {
	cases := []struct {
		name      string
		opts      agent.SpawnOptions
		configDir string
	}{
		{"fresh", agent.SpawnOptions{Model: hostileModel}, testConfigDir},
		{"resume", agent.SpawnOptions{
			AgentSessionID:      realSessionID,
			AgentSessionStarted: true,
			Model:               hostileModel,
		}, testConfigDir},
		// Setup never succeeded: the adapter fails open to a bare `opencode`,
		// and the model must still survive that path.
		{"no config dir", agent.SpawnOptions{Model: hostileModel}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := SpawnCommand(tc.opts, tc.configDir)
			if !strings.Contains(plan.Command, `--model "$`+modelArgEnv+`"`) {
				t.Errorf("Command = %q, want it to name the model through %s", plan.Command, modelArgEnv)
			}
			if strings.Contains(plan.Command, "touch") {
				t.Errorf("Command = %q carries the model's own text; it must only name %s", plan.Command, modelArgEnv)
			}
			if got := plan.ExtraEnv[modelArgEnv]; got != hostileModel {
				t.Errorf("%s = %q, want %q", modelArgEnv, got, hostileModel)
			}
		})
	}
}

func TestSpawnCommand_NoModelOmitsTheFlag(t *testing.T) {
	plan := SpawnCommand(agent.SpawnOptions{}, testConfigDir)
	if strings.Contains(plan.Command, "--model") {
		t.Errorf("Command = %q, want no --model when the session names none", plan.Command)
	}
	if _, ok := plan.ExtraEnv[modelArgEnv]; ok {
		t.Errorf("ExtraEnv = %v, want no %s when the command never mentions it", plan.ExtraEnv, modelArgEnv)
	}
}
