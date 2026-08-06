package codex

import (
	"strings"
	"testing"
)

func TestHookArgs_Empty(t *testing.T) {
	if got := HookArgs(""); got != nil {
		t.Errorf("HookArgs(\"\") = %#v, want nil", got)
	}
}

func TestHookArgs_EnableFlagFirst(t *testing.T) {
	args := HookArgs("/usr/local/bin/jin")
	if len(args) < 2 || args[0] != "--enable" || args[1] != "hooks" {
		t.Errorf("first two args = %v, want [--enable hooks]", args[:min(2, len(args))])
	}
}

func TestHookArgs_AllEventsPresent(t *testing.T) {
	args := HookArgs("/usr/local/bin/jin")
	joined := strings.Join(args, " ")
	for _, ev := range managedEvents {
		if !strings.Contains(joined, "hooks."+ev+"=") {
			t.Errorf("event %q missing from output:\n%s", ev, joined)
		}
	}
}

func TestHookArgs_LengthMatchesManagedEvents(t *testing.T) {
	args := HookArgs("/usr/local/bin/jin")
	// 2 for --enable hooks + 2 per event (-c and the TOML value) = 2 + 2*N
	want := 2 + 2*len(managedEvents)
	if len(args) != want {
		t.Errorf("len(args) = %d, want %d — args=%v", len(args), want, args)
	}
}

func TestHookArgs_Golden(t *testing.T) {
	// The exact shape spawn.go will splice into SpawnPlan.Command. If this
	// ever changes, review the shell-escape trace in the TOML doc comment
	// on tomlEscapeForShell — the outer manager wrapping depends on
	// balanced ' quotes here.
	args := HookArgs("/usr/local/bin/jin")
	got := strings.Join(args, " ")
	want := "--enable hooks" +
		` -c 'hooks.SessionStart=[{hooks=[{type="command",command="/usr/local/bin/jin hook --emit-context",timeout=10000}]}]'` +
		` -c 'hooks.UserPromptSubmit=[{hooks=[{type="command",command="/usr/local/bin/jin hook",timeout=10000}]}]'` +
		` -c 'hooks.PreToolUse=[{hooks=[{type="command",command="/usr/local/bin/jin hook",timeout=10000}]}]'` +
		` -c 'hooks.PostToolUse=[{hooks=[{type="command",command="/usr/local/bin/jin hook",timeout=10000}]}]'` +
		` -c 'hooks.PermissionRequest=[{hooks=[{type="command",command="/usr/local/bin/jin hook",timeout=10000}]}]'` +
		` -c 'hooks.Stop=[{hooks=[{type="command",command="/usr/local/bin/jin hook",timeout=10000}]}]'`
	if got != want {
		t.Errorf("HookArgs joined mismatch:\nwant: %s\n got: %s", want, got)
	}
}

// TestHookArgs_ContextFlagOnSessionStartOnly guards the blast radius of the
// --emit-context addition. Only SessionStart's stdout is read by Codex; the
// flag appearing on any other event would print JSON into a channel nothing
// parses, and its absence from SessionStart would silently stop injecting the
// context. The golden test above pins the exact strings, but it would also
// pass if a future edit moved the flag and updated the golden to match — this
// states the rule itself.
func TestHookArgs_ContextFlagOnSessionStartOnly(t *testing.T) {
	args := HookArgs("/usr/local/bin/jin")

	seen := 0
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-c" {
			continue
		}
		val := args[i+1]
		seen++
		isSessionStart := strings.Contains(val, "hooks."+sessionStartEvent+"=")
		hasFlag := strings.Contains(val, "--emit-context")
		if isSessionStart && !hasFlag {
			t.Errorf("%s carries no --emit-context; children would never learn about `jin docs`:\n%s", sessionStartEvent, val)
		}
		if !isSessionStart && hasFlag {
			t.Errorf("--emit-context leaked onto a non-SessionStart event:\n%s", val)
		}
	}
	if seen != len(managedEvents) {
		t.Fatalf("inspected %d -c values, expected %d", seen, len(managedEvents))
	}
}

// TestHookArgs_NonSessionStartEventsUnchanged states the other half of the
// same rule as a shape assertion: every event but SessionStart must still
// produce exactly the pre-flag command string. A quoting mistake in the
// SessionStart branch that spilled into the shared format string would show
// up here.
func TestHookArgs_NonSessionStartEventsUnchanged(t *testing.T) {
	const path = "/usr/local/bin/jin"
	args := HookArgs(path)

	for _, ev := range managedEvents {
		if ev == sessionStartEvent {
			continue
		}
		want := `-c 'hooks.` + ev + `=[{hooks=[{type="command",command="` + path + ` hook",timeout=10000}]}]'`
		if joined := strings.Join(args, " "); !strings.Contains(joined, want) {
			t.Errorf("event %q is not in its expected pre-flag form; want substring:\n%s", ev, want)
		}
	}
}

func TestHookArgs_PathWithSpace(t *testing.T) {
	// Space inside the path is fine because the outer shell single quotes
	// group the whole -c value; Codex's TOML parser then reads
	// command="..." verbatim.
	args := HookArgs("/tmp/dir with space/jin")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `command="/tmp/dir with space/jin hook"`) {
		t.Errorf("path with space not embedded verbatim:\n%s", joined)
	}
	if strings.Contains(joined, `'`) && !strings.Contains(joined, `-c '`) {
		t.Errorf("expected outer -c '...' grouping to survive space:\n%s", joined)
	}
}

func TestHookArgs_PathWithSingleQuote(t *testing.T) {
	// A ' in the path would break the outer shell -c '...' grouping unless
	// we replace it with a TOML unicode escape. Manager's outer wrap does
	// its own ' → '\'' escape; feeding it a raw ' here would produce
	// unbalanced quoting. The parser inside Codex sees the escape as ' and
	// invokes the correct path.
	args := HookArgs("/tmp/foo'bar/jin")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "command=\"/tmp/foo\\u0027bar/jin hook\"") {
		t.Errorf("single quote not encoded as \\u0027:\n%s", joined)
	}
	// A raw ' must never leak into the emitted TOML value (aside from the
	// outer shell -c '...' grouping quotes). Count both.
	rawSingleQuotes := strings.Count(joined, "'")
	// Expected: 2 outer quotes per -c pair (12 total for 6 events). No extra.
	wantRaw := 2 * len(managedEvents)
	if rawSingleQuotes != wantRaw {
		t.Errorf("raw single quotes in output = %d, want %d (2 per -c grouping):\n%s",
			rawSingleQuotes, wantRaw, joined)
	}
}

func TestHookArgs_PathWithDoubleQuote(t *testing.T) {
	args := HookArgs(`/tmp/quote"path/jin`)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `command="/tmp/quote\"path/jin hook"`) {
		t.Errorf("double quote not TOML-escaped:\n%s", joined)
	}
}

func TestHookArgs_PathWithBackslash(t *testing.T) {
	args := HookArgs(`C:\Program Files\jin.exe`)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `command="C:\\Program Files\\jin.exe hook"`) {
		t.Errorf("backslash not TOML-escaped:\n%s", joined)
	}
}

func TestTomlEscapeForShell_NoOpForCleanPath(t *testing.T) {
	// Fast-path optimisation: paths without ' " \ should be returned
	// verbatim (same string identity is not required, but content must
	// match byte-for-byte). Confirms the ContainsAny bail-out works.
	in := "/usr/local/bin/jin"
	if got := tomlEscapeForShell(in); got != in {
		t.Errorf("tomlEscapeForShell(%q) = %q, want %q", in, got, in)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
