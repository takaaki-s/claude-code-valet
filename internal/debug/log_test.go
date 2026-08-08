package debug

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withStateHome forces internal/paths to resolve State() to a deterministic
// directory by setting XDG_STATE_HOME for the duration of the test.
func withStateHome(t *testing.T, dir string) string {
	t.Helper()
	orig, had := os.LookupEnv("XDG_STATE_HOME")
	os.Setenv("XDG_STATE_HOME", dir)
	t.Cleanup(func() {
		if had {
			os.Setenv("XDG_STATE_HOME", orig)
		} else {
			os.Unsetenv("XDG_STATE_HOME")
		}
	})
	stateDir := filepath.Join(dir, "jind-ai")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("failed to create state dir: %v", err)
	}
	return stateDir
}

func TestNewLogger_Disabled(t *testing.T) {
	// When JIN_DEBUG is not "1", the logger should be a no-op.
	origDebug := os.Getenv("JIN_DEBUG")
	os.Setenv("JIN_DEBUG", "0")
	defer os.Setenv("JIN_DEBUG", origDebug)

	// Reset enabled for this test
	origEnabled := enabled
	enabled = false
	defer func() { enabled = origEnabled }()

	stateDir := withStateHome(t, t.TempDir())
	filename := "test-disabled.log"

	log := NewLogger(filename)
	log("this message should not appear")

	logPath := filepath.Join(stateDir, filename)
	if _, err := os.Stat(logPath); err == nil {
		t.Error("logger created a file even though debug is disabled")
	}
}

func TestNewLogger_Enabled(t *testing.T) {
	origEnabled := enabled
	enabled = true
	defer func() { enabled = origEnabled }()

	stateDir := withStateHome(t, t.TempDir())

	log := NewLogger("test-enabled.log")
	log("hello %s %d", "world", 42)

	logPath := filepath.Join(stateDir, "test-enabled.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "hello world 42") {
		t.Errorf("log file content %q does not contain expected message", content)
	}
	// Verify timestamp format [HH:MM:SS.mmm] is present
	if !strings.Contains(content, "[") || !strings.Contains(content, "]") {
		t.Errorf("log file content %q does not contain timestamp brackets", content)
	}
}

func TestNewLogger_NoopWhenDisabled(t *testing.T) {
	origEnabled := enabled
	enabled = false
	defer func() { enabled = origEnabled }()

	log := NewLogger("noop.log")

	// Should not panic
	log("this should be a no-op")
}

// TestUntrusted covers both halves of the one verb: the bound and the quoting.
// They are one function because they only work together — a bound alone leaves
// newlines intact, and a value carrying them forges whole entries in a log read
// as jind-ai's own.
func TestUntrusted(t *testing.T) {
	short := "ses_084426f78ffeXBrPh5ABEu2dNX"
	if got, want := Untrusted(short, 128), `"`+short+`"`; got != want {
		t.Errorf("Untrusted(%q, 128) = %s, want %s", short, got, want)
	}
	if got, want := Untrusted(short, len(short)), `"`+short+`"`; got != want {
		t.Errorf("Untrusted at exactly len = %s, want %s", got, want)
	}
	// The quote is what a bound cannot do: without it a single value ends the
	// line and starts one that reads as jind-ai's own.
	if got := Untrusted("a\n[HOOK] forged", 128); strings.Contains(got, "\n") {
		t.Errorf("Untrusted left a raw newline in %s", got)
	}
	// Quoting adds two characters, so the bound applies to the value, not the
	// rendering: 512 in, 128 of value plus the quotes out.
	long := strings.Repeat("a", 512)
	if got := Untrusted(long, 128); len(got) != 130 {
		t.Errorf("Untrusted(512 chars, 128) rendered %d chars, want 130 (128 + two quotes)", len(got))
	}
	if got, want := Untrusted("", 128), `""`; got != want {
		t.Errorf("Untrusted(\"\", 128) = %s, want %s", got, want)
	}
	// A negative bound means "no bound" rather than a panic: a logger has no
	// business crashing the process it was recording for.
	if got, want := Untrusted(long, -1), `"`+long+`"`; got != want {
		t.Errorf("Untrusted with a negative bound truncated to %d chars", len(got))
	}
}

// TestUntrustedBytes exists because the string conversion is the expensive
// half: converting first copies the whole payload onto the heap before the
// bound throws it away, on precisely the input the bound exists to survive.
func TestUntrustedBytes(t *testing.T) {
	if got, want := UntrustedBytes([]byte("abc"), 128), `"abc"`; got != want {
		t.Errorf("UntrustedBytes = %s, want %s", got, want)
	}
	big := []byte(strings.Repeat("b", 1<<20))
	if got := UntrustedBytes(big, 64); len(got) != 66 {
		t.Errorf("UntrustedBytes(1MiB, 64) rendered %d chars, want 66", len(got))
	}
	if got := UntrustedBytes(nil, 64); got != `""` {
		t.Errorf("UntrustedBytes(nil, 64) = %s, want %s", got, `""`)
	}
}
