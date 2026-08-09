package claude

import (
	"encoding/json"
	"os"
	"sync"
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

// Agent.Setup calls EnsureHooksSettingsFile on every session start, so a
// repeat call is the ordinary path rather than a hypothetical one, and this
// pins a stronger property than a guarded-write version would need: the second
// call must resolve to the same path and leave the same contents. Were that
// not so, a daemon would hand successive sessions different settings for the
// same state directory.
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

// TestEnsureHooksSettingsFile_NeverVisibleHalfWritten pins the property the
// per-start rewrite needs: a reader sees either the old file or the new one,
// never a fragment. See EnsureHooksSettingsFile for why writing in place would
// not give that, and for the size of the window it leaves — the reader here
// loops without sleeping because that window is measured in microseconds.
//
// The ReadDir at the end is the second half, and catches what the read loop
// cannot: a write that publishes correctly and then leaves its temp sibling
// behind.
func TestEnsureHooksSettingsFile_NeverVisibleHalfWritten(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureHooksSettingsFile(dir, "/usr/local/bin/jin"); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	path := hooksSettingsPath(dir)

	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 60; n++ {
				if _, err := EnsureHooksSettingsFile(dir, "/usr/local/bin/jin"); err != nil {
					t.Errorf("EnsureHooksSettingsFile: %v", err)
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()

	// Wait the writers out on every exit path, t.Fatalf included: a goroutine
	// that reports after its test has returned panics the run.
	defer func() { <-done }()

	for reads := 1; ; reads++ {
		select {
		case <-done:
			assertOnlyTheHooksFile(t, dir)
			return
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the published file disappeared mid-rewrite: %v", err)
		}
		if !usableHooksSettings(data) {
			t.Fatalf("read %d saw an empty or partial file", reads)
		}
	}
}

// assertOnlyTheHooksFile fails if anything besides the published file is left
// in dir — atomicfile.Write creates its temp sibling there. Measured
// load-bearing: a stray survives the read loop above and is caught only here.
// atomicfile's own tests catch the same mutation, so this is not the only
// guard; it is here because nothing sweeps a state directory, so a stray under
// one is permanent.
func assertOnlyTheHooksFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != hooksSettingsFileName {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("state dir = %v, want exactly [%s]", names, hooksSettingsFileName)
	}
}
