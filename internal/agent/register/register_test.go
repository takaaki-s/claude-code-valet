package register_test

import (
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/transcript"
	// Blank import triggers init() in the register package, which is the
	// side-effect under test. Without this, agent.Registry stays empty in
	// tests because the daemon-side blank import from cmd/jin doesn't
	// reach here.
	_ "github.com/takaaki-s/jind-ai/internal/agent/register"
)

// TestRegisterInit_RegistersKnownKinds is the guardrail for `jin session
// new --agent codex`: if someone deletes the codex import or Register
// call in register.go, the daemon would silently reject Codex sessions
// with an "unknown agent kind" error at spawn time. Failing this test
// forces the mistake to be caught in CI.
func TestRegisterInit_RegistersKnownKinds(t *testing.T) {
	want := map[string]bool{
		"claude":   false,
		"codex":    false,
		"opencode": false,
	}
	for _, k := range agent.Kinds() {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("registry missing %q — check register.go", k)
		}
	}
}

func TestRegisterInit_LookupCodex(t *testing.T) {
	// Beyond presence in Kinds(): the actual Codex adapter object must
	// come back from Lookup and identify itself correctly.
	a, err := agent.Lookup("codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	if a.Kind() != "codex" {
		t.Errorf("Kind() = %q, want %q", a.Kind(), "codex")
	}
}

func TestRegisterInit_LookupOpencode(t *testing.T) {
	// Same guardrail as Codex above, for the opencode adapter.
	a, err := agent.Lookup("opencode")
	if err != nil {
		t.Fatalf("Lookup(opencode): %v", err)
	}
	if a.Kind() != "opencode" {
		t.Errorf("Kind() = %q, want %q", a.Kind(), "opencode")
	}
}

// TestRegisterInit_TranscriptWiring guards the three one-line methods that
// decide what `jin session result` answers for each kind.
//
// Every other part of that path is covered from both sides — the daemon test
// proves the handler asks the session's own adapter, and the codex reader's
// tests prove the parsing — but the methods joining them were not, and each
// one is a single return statement. Rewriting claude's to return nil makes
// `session result` fail for every Claude Code session, and before this test
// the whole suite stayed green.
func TestRegisterInit_TranscriptWiring(t *testing.T) {
	tests := []struct {
		kind     string
		readable bool
		why      string
	}{
		{"claude", true, "the reference reader; nil here breaks result for every Claude Code session"},
		{"codex", true, "reads the rollout JSONL; nil here silently removes the feature"},
		{"opencode", true, "shells out to `opencode export`; nil here silently removes the feature"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			a, err := agent.Lookup(tt.kind)
			if err != nil {
				t.Fatalf("Lookup(%s): %v", tt.kind, err)
			}
			src := a.Transcript()
			if (src != nil) != tt.readable {
				t.Fatalf("Transcript() != nil is %v, want %v — %s", src != nil, tt.readable, tt.why)
			}
		})
	}
}

// TestRegisterInit_ClaudeReturnsTheSharedReader pins claude to
// transcript.Reader specifically, not merely to something non-nil.
//
// The nil check above cannot see a swap to a different implementation, and a
// swap would change what every existing Claude Code session returns — the one
// outcome this change was required not to have.
func TestRegisterInit_ClaudeReturnsTheSharedReader(t *testing.T) {
	a, err := agent.Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup(claude): %v", err)
	}
	if _, ok := a.Transcript().(*transcript.Reader); !ok {
		t.Fatalf("Transcript() is %T, want *transcript.Reader (the pre-change reader, unchanged)", a.Transcript())
	}
}

// TestRegisterInit_PollableWiring pins which readers the preview path may call
// on a timer.
//
// Manager.AttachLastMessages decorates every row of `session list`, which the
// TUI refreshes on a timer, so it must skip a reader that spawns a process.
// The opencode reader does: it runs `opencode export`, which on a list of
// opencode rows would be one process per row per refresh, permanently. Neither `session result` nor the
// preview is wrong on its own, so nothing else would catch this.
func TestRegisterInit_PollableWiring(t *testing.T) {
	tests := []struct {
		kind     string
		pollable bool
		why      string
	}{
		{"claude", true, "opens one file; the preview path has always called it"},
		{"codex", true, "locates and walks a rollout, the same order of cost"},
		{"opencode", false, "spawns `opencode export`; a timer must never call it"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			a, err := agent.Lookup(tt.kind)
			if err != nil {
				t.Fatalf("Lookup(%s): %v", tt.kind, err)
			}
			_, got := a.Transcript().(session.PollableTranscriptSource)
			if got != tt.pollable {
				t.Fatalf("pollable = %v, want %v — %s", got, tt.pollable, tt.why)
			}
		})
	}
}

// hostileSessionIDs are values that were executable once recorded. Manager
// splices SpawnPlan.Command into `SHELL -ic '...'`, so a value an adapter
// concatenates there is interpreted — at the unrelated later moment the session
// resumes, in that session's working directory.
//
// The "ses_" variants matter as much as the bare ones: an adapter that gates its
// resume on a prefix only reaches the dangerous branch for an id that carries it,
// so an id without one would exercise the fresh-spawn path and prove nothing.
var hostileSessionIDs = []string{
	"x$(touch /tmp/jin-should-not-exist)",
	"x;touch /tmp/jin-should-not-exist",
	"x`touch /tmp/jin-should-not-exist`",
	"x'; touch /tmp/jin-should-not-exist; '",
	"ses_x$(touch /tmp/jin-should-not-exist)",
	"ses_x;touch /tmp/jin-should-not-exist",
	"0198f1b2-4c3d-7a1e-8b2f-000000000abc$(touch /tmp/jin-should-not-exist)",
}

// TestSpawnCommand_NoAdapterPutsTheSessionIDInTheCommand is the conformance
// check for the one rule that, if forgotten, restores arbitrary code execution.
//
// It is written over agent.Kinds() rather than per adapter, and that is the
// whole point. Manager validates an id before recording it, but validation and
// this rule defend different things: a record written by an older jind-ai, or
// edited by hand, reaches SpawnCommand having passed no gate. An adapter that
// writes `cmd += " --resume " + opts.AgentSessionID` compiles, and a per-package
// test cannot fail for a package that does not exist yet — so a fourth adapter
// would reintroduce the defect with nothing to catch it. Registering a kind is
// what enrols it here.
//
// This lives in register_test because package register_test imports both
// internal/agent and internal/session, so it can reach every adapter at once.
// An adapter's own package cannot: internal/agent imports internal/session, so
// the Manager-side half of the proof (that an ExtraEnv value is not interpreted
// — see TestBuildAgentShellCmd_ExtraEnvIsNotInterpreted) cannot be imported
// into it.
func TestSpawnCommand_NoAdapterPutsTheSessionIDInTheCommand(t *testing.T) {
	for _, kind := range agent.Kinds() {
		a, err := agent.Lookup(kind)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", kind, err)
		}
		for _, id := range hostileSessionIDs {
			for _, started := range []bool{false, true} {
				plan := a.SpawnCommand(session.SpawnOptions{
					AgentSessionID:      id,
					AgentSessionStarted: started,
					WorkDir:             t.TempDir(),
				})
				if strings.Contains(plan.Command, id) {
					t.Errorf("%s.SpawnCommand(started=%v) spliced the session id into Command: %q",
						kind, started, plan.Command)
				}
				// Catches a partial splice too — an adapter that escaped or
				// trimmed the value still hands the shell something to run.
				if strings.Contains(plan.Command, "touch") {
					t.Errorf("%s.SpawnCommand(started=%v) put a payload fragment into Command: %q",
						kind, started, plan.Command)
				}
				// Whatever an adapter chooses to carry, it carries verbatim:
				// Manager quotes every ExtraEnv value, so pre-escaping here
				// would double-escape at the shell.
				for k, v := range plan.ExtraEnv {
					if strings.Contains(v, "touch") && v != id {
						t.Errorf("%s.SpawnCommand(started=%v) pre-escaped the id in ExtraEnv[%s] = %q, want %q",
							kind, started, k, v, id)
					}
				}
			}
		}
	}
}

// TestRecognizesSessionID_NoAdapterAcceptsEverything is the companion for the
// second gate. An adapter that answers true unconditionally satisfies the
// interface and compiles, and Manager's own safety gate would still stand — so
// the failure is a quiet one: ids belonging to a different agent become
// recordable, and a session's identity can be pointed at a conversation that is
// not its own.
func TestRecognizesSessionID_NoAdapterAcceptsEverything(t *testing.T) {
	for _, kind := range agent.Kinds() {
		a, err := agent.Lookup(kind)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", kind, err)
		}
		// Safe by every character test and shaped like no agent's id.
		for _, id := range []string{"not-a-session-id", "x"} {
			if a.RecognizesSessionID(id) {
				t.Errorf("%s.RecognizesSessionID(%q) = true; the adapter recognises anything", kind, id)
			}
		}
	}
}
