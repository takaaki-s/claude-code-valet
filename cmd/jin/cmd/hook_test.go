package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agentdocs"
)

// runHookCmd feeds stdin to `jin hook` and returns whatever it wrote to
// stdout. The flag is reset on both sides because it is a package-level
// variable bound to a cobra flag, and a leaked "true" would make an unrelated
// test print JSON into a channel that must stay silent.
//
// This runs the real command, which forwards to whatever daemon JIN_SESSION_ID
// and JIN_SOCKET name. TestMain clears both for the whole package — see the
// comment there for what these fixtures did to a live session before it did.
func runHookCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	hookEmitContext = false
	t.Cleanup(func() {
		hookEmitContext = false
		_ = hookCmd.Flags().Set("emit-context", "false")
	})

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetIn(strings.NewReader(stdin))
	rootCmd.SetArgs(append([]string{"hook"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// TestHookWithoutFlagIsSilent is the compatibility guarantee for the five
// events that did not change: stdout must stay empty, because Claude Code and
// Codex both parse it and anything unexpected there is a protocol error.
func TestHookWithoutFlagIsSilent(t *testing.T) {
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "Notification"} {
		t.Run(event, func(t *testing.T) {
			in := `{"session_id":"abc","hook_event_name":"` + event + `"}`
			out, err := runHookCmd(t, in)
			if err != nil {
				t.Fatalf("hook returned an error: %v", err)
			}
			if out != "" {
				t.Errorf("stdout is not empty without --emit-context: %q", out)
			}
		})
	}
}

func TestHookEmitsContextOnSessionStart(t *testing.T) {
	in := `{"session_id":"abc","hook_event_name":"SessionStart"}`
	out, err := runHookCmd(t, in, "--emit-context")
	if err != nil {
		t.Fatalf("hook returned an error: %v", err)
	}

	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not valid JSON (%v): %q", err, out)
	}
	if payload.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", payload.HookSpecificOutput.HookEventName)
	}
	if payload.HookSpecificOutput.AdditionalContext != agentdocs.Context() {
		t.Error("additionalContext does not match agentdocs.Context()")
	}

	// Exactly one line, and nothing before it. Both agents parse stdout as a
	// single JSON document; a stray log line in front breaks the parse.
	if n := strings.Count(strings.TrimRight(out, "\n"), "\n"); n != 0 {
		t.Errorf("output spans %d extra lines; want a single line:\n%s", n, out)
	}
	if !strings.HasPrefix(out, "{") {
		t.Errorf("output does not start with the JSON object: %q", out)
	}
}

// TestHookEmitsContextOnlyOnSessionStart keeps the injection to once per
// session. Emitting on every event would repeat the same paragraph into the
// agent's context on every turn.
func TestHookEmitsContextOnlyOnSessionStart(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "Stop", "StopFailure", "PostToolUse", "Notification", "SessionEnd"} {
		t.Run(event, func(t *testing.T) {
			in := `{"session_id":"abc","hook_event_name":"` + event + `"}`
			out, err := runHookCmd(t, in, "--emit-context")
			if err != nil {
				t.Fatalf("hook returned an error: %v", err)
			}
			if out != "" {
				t.Errorf("%s emitted context: %q", event, out)
			}
		})
	}
}

// TestHookEmitsWithoutJinSessionID pins the fail-open ordering. The context is
// a static string in this binary, so it can be delivered even for a session
// jind-ai does not recognise — and an agent in a half-configured environment
// is exactly who most needs to be told `jin docs` exists.
func TestHookEmitsWithoutJinSessionID(t *testing.T) {
	t.Setenv("JIN_SESSION_ID", "")

	out, err := runHookCmd(t, `{"session_id":"abc","hook_event_name":"SessionStart"}`, "--emit-context")
	if err != nil {
		t.Fatalf("hook returned an error: %v", err)
	}
	if out == "" {
		t.Error("no context emitted when JIN_SESSION_ID is unset")
	}
}

// TestHookEmitsWithoutSessionID covers the same ordering for a payload that
// carries no session_id: the daemon notification is skipped, the context is
// not.
func TestHookEmitsWithoutSessionID(t *testing.T) {
	out, err := runHookCmd(t, `{"hook_event_name":"SessionStart"}`, "--emit-context")
	if err != nil {
		t.Fatalf("hook returned an error: %v", err)
	}
	if out == "" {
		t.Error("no context emitted when session_id is absent")
	}
}

// TestHookMalformedInputIsSilent covers the one case where staying quiet is
// right: with no parseable event name there is nothing to claim the output
// belongs to, and a guess could print JSON where the agent expects none.
func TestHookMalformedInputIsSilent(t *testing.T) {
	for _, in := range []string{"", "not json", "{", `{"hook_event_name":`} {
		t.Run(in, func(t *testing.T) {
			out, err := runHookCmd(t, in, "--emit-context")
			if err != nil {
				t.Fatalf("hook returned an error for malformed input: %v", err)
			}
			if out != "" {
				t.Errorf("emitted output for malformed input: %q", out)
			}
		})
	}
}

// TestHookAcceptsAdapterGeneratedFlag closes the loop that the flag name left
// open before agentdocs.HookCommand owned it.
//
// The adapters do not call this command, they write a string that a foreign
// process will later execute, so nothing here fails at compile time. Feeding
// the real cobra command exactly what HookCommand produces is what makes a
// rename impossible to ship green: cobra rejects an unknown flag, which would
// fail the hook outright and take SessionStart status detection down with it.
func TestHookAcceptsAdapterGeneratedFlag(t *testing.T) {
	fields := strings.Fields(agentdocs.HookCommand("/usr/local/bin/jin", true))
	if len(fields) < 3 || fields[1] != "hook" {
		t.Fatalf("unexpected command shape from HookCommand: %q", fields)
	}

	out, err := runHookCmd(t, `{"session_id":"abc","hook_event_name":"SessionStart"}`, fields[2:]...)
	if err != nil {
		t.Fatalf("cobra rejected the flag the adapters install (%v): %v", fields[2:], err)
	}
	if out == "" {
		t.Errorf("flag %v parsed but emitted nothing", fields[2:])
	}
}

// TestHookCommandWithoutContextHasNoFlags is the other half: the five events
// that do not inject must keep an argument-free command, or every one of them
// would start printing JSON into a channel that ignores it.
func TestHookCommandWithoutContextHasNoFlags(t *testing.T) {
	fields := strings.Fields(agentdocs.HookCommand("/usr/local/bin/jin", false))
	if len(fields) != 2 || fields[1] != "hook" {
		t.Errorf("HookCommand(_, false) = %q, want exactly \"<path> hook\"", fields)
	}
}
