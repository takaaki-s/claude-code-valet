package claude

import (
	"encoding/json"
	"os"
	"testing"
)

func TestEnsureHooksSettingsFile_NewHooks(t *testing.T) {
	dir := t.TempDir()
	path, err := EnsureHooksSettingsFile(dir, "/usr/local/bin/jin")
	if err != nil {
		t.Fatalf("EnsureHooksSettingsFile failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	var settings hooksSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	requiredHooks := []string{"UserPromptSubmit", "Stop", "StopFailure", "PostToolUse", "CwdChanged", "SessionStart", "SessionEnd", "Notification"}
	for _, hook := range requiredHooks {
		if _, ok := settings.Hooks[hook]; !ok {
			t.Errorf("hooks-settings.json missing hook: %s", hook)
		}
	}
}

// EnsureHooksSettingsFile is called from Agent.Setup under sync.Once, but the
// helper itself must be safe to invoke repeatedly (Setup runs per-session-start
// even though the write itself is guarded). Verify the file content is stable
// across a second call.
func TestEnsureHooksSettingsFile_Idempotent(t *testing.T) {
	dir := t.TempDir()

	path1, err := EnsureHooksSettingsFile(dir, "/usr/local/bin/jin")
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	first, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("first read failed: %v", err)
	}

	path2, err := EnsureHooksSettingsFile(dir, "/usr/local/bin/jin")
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	second, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("second read failed: %v", err)
	}

	if path1 != path2 {
		t.Errorf("path differed across calls: %q vs %q", path1, path2)
	}
	if string(first) != string(second) {
		t.Errorf("content differed across calls:\nfirst=%s\nsecond=%s", first, second)
	}
}

// TestEnsureHooksSettingsFile_ContextFlagOnSessionStartOnly pins which hook
// command asks `jin hook` to print the agent-facing context.
//
// Claude Code adds a SessionStart hook's stdout to the session context and
// ignores it for the other events, so the flag has to sit on exactly that one
// entry: missing there, no child ever learns `jin docs` exists; present
// anywhere else, jin writes JSON into a channel nothing reads.
func TestEnsureHooksSettingsFile_ContextFlagOnSessionStartOnly(t *testing.T) {
	const execPath = "/usr/local/bin/jin"
	dir := t.TempDir()

	path, err := EnsureHooksSettingsFile(dir, execPath)
	if err != nil {
		t.Fatalf("EnsureHooksSettingsFile failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var settings hooksSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	const plain = execPath + " hook"
	const withContext = plain + " --emit-context"

	for event, matchers := range settings.Hooks {
		want := plain
		if event == "SessionStart" {
			want = withContext
		}
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if h.Command != want {
					t.Errorf("%s command = %q, want %q", event, h.Command, want)
				}
			}
		}
	}
}

// TestEnsureHooksSettingsFile_TimeoutUnchangedBySessionStart guards the copy
// that produces the SessionStart entry: it is derived from the shared one, so
// a future edit that rebuilt it from scratch could silently drop the timeout
// that bounds what a wedged daemon costs a Claude session.
func TestEnsureHooksSettingsFile_TimeoutUnchangedBySessionStart(t *testing.T) {
	dir := t.TempDir()
	path, err := EnsureHooksSettingsFile(dir, "/usr/local/bin/jin")
	if err != nil {
		t.Fatalf("EnsureHooksSettingsFile failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var settings hooksSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	for event, matchers := range settings.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if h.Timeout != 10 {
					t.Errorf("%s timeout = %d, want 10", event, h.Timeout)
				}
				if h.Type != "command" {
					t.Errorf("%s type = %q, want \"command\"", event, h.Type)
				}
			}
		}
	}
}
