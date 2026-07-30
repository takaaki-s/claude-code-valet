package tmux

import (
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestWindowName(t *testing.T) {
	tests := []struct {
		sessionID string
		want      string
	}{
		{"abc123", WindowPrefix + "abc123"},
		{"", WindowPrefix},
		{"some-long-session-id-with-dashes", WindowPrefix + "some-long-session-id-with-dashes"},
	}
	for _, tt := range tests {
		got := WindowName(tt.sessionID)
		if got != tt.want {
			t.Errorf("WindowName(%q) = %q, want %q", tt.sessionID, got, tt.want)
		}
	}
}

func TestInnerSessionName(t *testing.T) {
	tests := []struct {
		sessionID string
		want      string
	}{
		{"abc123", SessionPrefix + "abc123"},
		{"", SessionPrefix},
		{"uuid-1234-5678", SessionPrefix + "uuid-1234-5678"},
	}
	for _, tt := range tests {
		got := InnerSessionName(tt.sessionID)
		if got != tt.want {
			t.Errorf("InnerSessionName(%q) = %q, want %q", tt.sessionID, got, tt.want)
		}
	}
}

func TestWindowTarget(t *testing.T) {
	tests := []struct {
		windowName string
		pane       int
		want       string
	}{
		{"sess-abc123", 0, SessionName + ":sess-abc123.0"},
		{UIWindowName, 1, SessionName + ":ui.1"},
		{"mywindow", 2, SessionName + ":mywindow.2"},
	}
	for _, tt := range tests {
		got := WindowTarget(tt.windowName, tt.pane)
		if got != tt.want {
			t.Errorf("WindowTarget(%q, %d) = %q, want %q", tt.windowName, tt.pane, got, tt.want)
		}
	}
}

func TestUITarget(t *testing.T) {
	tests := []struct {
		pane int
		want string
	}{
		{0, SessionName + ":" + UIWindowName + ".0"},
		{1, SessionName + ":" + UIWindowName + ".1"},
		{5, SessionName + ":" + UIWindowName + ".5"},
	}
	for _, tt := range tests {
		got := UITarget(tt.pane)
		if got != tt.want {
			t.Errorf("UITarget(%d) = %q, want %q", tt.pane, got, tt.want)
		}
	}
}

func TestBaseArgs(t *testing.T) {
	t.Run("without config file", func(t *testing.T) {
		c := &Client{
			tmuxPath:   "/usr/bin/tmux",
			socketName: "test-socket",
		}
		got := c.baseArgs()
		want := []string{"-L", "test-socket"}
		if len(got) != len(want) {
			t.Fatalf("baseArgs() returned %d elements, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("baseArgs()[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("with config file", func(t *testing.T) {
		c := &Client{
			tmuxPath:   "/usr/bin/tmux",
			socketName: "mgr-socket",
			configFile: "/dev/null",
		}
		got := c.baseArgs()
		want := []string{"-L", "mgr-socket", "-f", "/dev/null"}
		if len(got) != len(want) {
			t.Fatalf("baseArgs() returned %d elements, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("baseArgs()[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("with default socket name", func(t *testing.T) {
		c := &Client{
			tmuxPath:   "/usr/bin/tmux",
			socketName: SocketName,
		}
		got := c.baseArgs()
		if got[0] != "-L" || got[1] != SocketName {
			t.Errorf("baseArgs() = %v, want [-L %s]", got, SocketName)
		}
	})

	t.Run("with socket path", func(t *testing.T) {
		c := &Client{
			tmuxPath:   "/usr/bin/tmux",
			socketPath: "/tmp/tmux-1000/default",
		}
		got := c.baseArgs()
		want := []string{"-S", "/tmp/tmux-1000/default"}
		if len(got) != len(want) {
			t.Fatalf("baseArgs() returned %d elements, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("baseArgs()[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("socket path takes precedence over socket name", func(t *testing.T) {
		c := &Client{
			tmuxPath:   "/usr/bin/tmux",
			socketName: "should-be-ignored",
			socketPath: "/tmp/tmux-1000/default",
		}
		got := c.baseArgs()
		want := []string{"-S", "/tmp/tmux-1000/default"}
		if len(got) != len(want) {
			t.Fatalf("baseArgs() returned %d elements, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("baseArgs()[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})
}

func TestDefaultSocketName(t *testing.T) {
	// An empty env var is treated as unset by DefaultSocketName, matching
	// what os.Getenv returns for an actually-unset variable.
	t.Run("empty falls back to SocketName", func(t *testing.T) {
		t.Setenv("JIN_TMUX_SOCKET", "")
		if got := DefaultSocketName(); got != SocketName {
			t.Errorf("DefaultSocketName() with env empty = %q, want %q", got, SocketName)
		}
	})
	t.Run("env value wins", func(t *testing.T) {
		t.Setenv("JIN_TMUX_SOCKET", "jin-test-abcd1234")
		if got := DefaultSocketName(); got != "jin-test-abcd1234" {
			t.Errorf("DefaultSocketName() with env set = %q, want %q", got, "jin-test-abcd1234")
		}
	})
}

func TestNewClientWithSocket_NoTmux(t *testing.T) {
	origPath := os.Getenv("PATH")
	t.Cleanup(func() {
		os.Setenv("PATH", origPath)
	})

	// Set PATH to an empty directory so tmux cannot be found
	os.Setenv("PATH", t.TempDir())

	_, err := NewClientWithSocket("test-socket")
	if err == nil {
		t.Fatal("NewClientWithSocket() should return error when tmux is not in PATH")
	}
}

func TestHasTmux(t *testing.T) {
	// Test with normal PATH -- tmux should be available in CI/dev environments.
	// We don't assert true because tmux might not be installed, but we can
	// at least verify the function doesn't panic.
	_ = HasTmux()

	// Test with empty PATH -- tmux should not be found
	origPath := os.Getenv("PATH")
	t.Cleanup(func() {
		os.Setenv("PATH", origPath)
	})

	os.Setenv("PATH", t.TempDir())
	if HasTmux() {
		t.Error("HasTmux() = true with empty PATH, want false")
	}
}

func TestParseEnvironmentOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"multiple lines", "FOO=bar\nBAZ=qux\nHELLO=world", map[string]string{"FOO": "bar", "BAZ": "qux", "HELLO": "world"}},
		{"unset lines skipped", "FOO=bar\n-UNSET_VAR\nBAZ=qux", map[string]string{"FOO": "bar", "BAZ": "qux"}},
		{"malformed lines skipped", "FOO=bar\nno_equals_here\nBAZ=qux\n", map[string]string{"FOO": "bar", "BAZ": "qux"}},
		{"empty string", "", map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEnvironmentOutput(tt.in)
			if got == nil {
				t.Fatal("parseEnvironmentOutput returned nil, want non-nil map")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseEnvironmentOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClientSessionForTTY(t *testing.T) {
	tests := []struct {
		name string
		out  string
		tty  string
		want string
	}{
		{"match", "/dev/pts/1 mysession", "/dev/pts/1", "mysession"},
		{"no match", "/dev/pts/1 mysession", "/dev/pts/2", ""},
		{"empty output", "", "/dev/pts/1", ""},
		{"multiple clients", "/dev/pts/1 sess-a\n/dev/pts/2 sess-b", "/dev/pts/2", "sess-b"},
		{"session name with space", "/dev/pts/1 my session", "/dev/pts/1", "my session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clientSessionForTTY(tt.out, tt.tty)
			if got != tt.want {
				t.Errorf("clientSessionForTTY(%q, %q) = %q, want %q", tt.out, tt.tty, got, tt.want)
			}
		})
	}
}

func TestSocketPathFromEnv(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"/tmp/tmux-1000/default,12345,0", "/tmp/tmux-1000/default"},
		{"/tmp/tmux-1000/default", "/tmp/tmux-1000/default"},
	}
	for _, tt := range tests {
		if got := SocketPathFromEnv(tt.in); got != tt.want {
			t.Errorf("SocketPathFromEnv(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildSplitArgs(t *testing.T) {
	tests := []struct {
		name string
		opts SplitOptions
		want []string
	}{
		{
			name: "defaults split down",
			opts: SplitOptions{},
			want: []string{"split-window", "-t", "%1", "-P", "-F", "#{pane_id}", "-v"},
		},
		{
			name: "up adds -b",
			opts: SplitOptions{Direction: "up"},
			want: []string{"split-window", "-t", "%1", "-P", "-F", "#{pane_id}", "-v", "-b"},
		},
		{
			name: "right is horizontal",
			opts: SplitOptions{Direction: "right"},
			want: []string{"split-window", "-t", "%1", "-P", "-F", "#{pane_id}", "-h"},
		},
		{
			name: "left is horizontal with -b",
			opts: SplitOptions{Direction: "left"},
			want: []string{"split-window", "-t", "%1", "-P", "-F", "#{pane_id}", "-h", "-b"},
		},
		{
			name: "all options",
			opts: SplitOptions{Direction: "right", Size: "30%", Full: true, NoFocus: true, Dir: "/work", Cmd: "htop"},
			want: []string{"split-window", "-t", "%1", "-P", "-F", "#{pane_id}", "-h", "-f", "-d", "-l", "30%", "-c", "/work", "htop"},
		},
		{
			name: "line size passes through",
			opts: SplitOptions{Size: "15"},
			want: []string{"split-window", "-t", "%1", "-P", "-F", "#{pane_id}", "-v", "-l", "15"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSplitArgs("%1", tt.opts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildSplitArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSplitOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    SplitOptions
		wantErr bool
	}{
		{"empty is valid", SplitOptions{}, false},
		{"down", SplitOptions{Direction: "down"}, false},
		{"up", SplitOptions{Direction: "up"}, false},
		{"left", SplitOptions{Direction: "left"}, false},
		{"right", SplitOptions{Direction: "right"}, false},
		{"invalid direction", SplitOptions{Direction: "sideways"}, true},
		{"percent size", SplitOptions{Size: "30%"}, false},
		{"line size", SplitOptions{Size: "15"}, false},
		{"zero percent", SplitOptions{Size: "0%"}, true},
		{"hundred percent", SplitOptions{Size: "100%"}, true},
		{"ninety-nine percent", SplitOptions{Size: "99%"}, false},
		{"zero lines", SplitOptions{Size: "0"}, true},
		{"negative", SplitOptions{Size: "-5"}, true},
		{"garbage", SplitOptions{Size: "abc"}, true},
		{"bare percent sign", SplitOptions{Size: "%"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidatePaneName(t *testing.T) {
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"simple", "demo", false},
		{"with separators", "ok-name.1_x", false},
		{"single char", "a", false},
		{"max length 64", string(long), false},
		{"too long 65", string(long) + "a", true},
		{"empty", "", true},
		{"space", "a b", true},
		{"leading dash", "-x", true},
		{"shell metachars", "a;rm", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePaneName(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePaneName(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestValidateIfExists(t *testing.T) {
	for _, v := range []string{"", "noop", "respawn", "error"} {
		if err := ValidateIfExists(v); err != nil {
			t.Errorf("ValidateIfExists(%q) = %v, want nil", v, err)
		}
	}
	if err := ValidateIfExists("maybe"); err == nil {
		t.Error("ValidateIfExists(\"maybe\") = nil, want error")
	}
}

func TestMatchPaneByName(t *testing.T) {
	out := "%1 \n%2 demo\n%3 other"
	tests := []struct {
		name string
		want string
	}{
		{"demo", "%2"},
		{"other", "%3"},
		{"missing", ""},
	}
	for _, tt := range tests {
		if got := matchPaneByName(out, tt.name); got != tt.want {
			t.Errorf("matchPaneByName(out, %q) = %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := matchPaneByName("", "demo"); got != "" {
		t.Errorf("matchPaneByName(empty) = %q, want empty", got)
	}
}

// fakeSlotOps is a minimal PaneSlotOps double for EnsureNamedPane tests.
type fakeSlotOps struct {
	named        map[string]string // name -> existing pane ID
	splitID      string            // pane ID SplitPane returns
	setOptionErr error             // injected SetPaneOption failure

	splitCalls   int
	respawnCalls []string // "target cmd"
	killCalls    []string
	setCalls     []string // "target option value"
}

func (f *fakeSlotOps) FindPaneByName(target, name string) (string, error) {
	return f.named[name], nil
}

func (f *fakeSlotOps) SplitPane(target string, opts SplitOptions) (string, error) {
	f.splitCalls++
	return f.splitID, nil
}

func (f *fakeSlotOps) SetPaneOption(target, option, value string) error {
	f.setCalls = append(f.setCalls, target+" "+option+" "+value)
	return f.setOptionErr
}

func (f *fakeSlotOps) RespawnPane(target, cmd string) error {
	f.respawnCalls = append(f.respawnCalls, target+" "+cmd)
	return nil
}

func (f *fakeSlotOps) KillPane(target string) error {
	f.killCalls = append(f.killCalls, target)
	return nil
}

func TestEnsureNamedPane(t *testing.T) {
	tests := []struct {
		name         string
		slotName     string
		ifExists     string
		existing     map[string]string
		wantPane     string
		wantErr      bool
		wantSplits   int
		wantRespawns int
	}{
		{
			name:       "empty name is a plain split",
			slotName:   "",
			wantPane:   "%99",
			wantSplits: 1,
		},
		{
			name:       "named pane not found splits and tags",
			slotName:   "demo",
			wantPane:   "%99",
			wantSplits: 1,
		},
		{
			name:     "existing pane noop by default",
			slotName: "demo",
			existing: map[string]string{"demo": "%50"},
			wantPane: "%50",
		},
		{
			name:     "existing pane explicit noop",
			slotName: "demo",
			ifExists: IfExistsNoop,
			existing: map[string]string{"demo": "%50"},
			wantPane: "%50",
		},
		{
			name:         "existing pane respawn",
			slotName:     "demo",
			ifExists:     IfExistsRespawn,
			existing:     map[string]string{"demo": "%50"},
			wantPane:     "%50",
			wantRespawns: 1,
		},
		{
			name:     "existing pane error policy",
			slotName: "demo",
			ifExists: IfExistsError,
			existing: map[string]string{"demo": "%50"},
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops := &fakeSlotOps{named: tt.existing, splitID: "%99"}
			got, err := EnsureNamedPane(ops, "%1", tt.slotName, tt.ifExists, SplitOptions{Cmd: "top"})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantPane {
				t.Errorf("pane ID = %q, want %q", got, tt.wantPane)
			}
			if ops.splitCalls != tt.wantSplits {
				t.Errorf("SplitPane calls = %d, want %d", ops.splitCalls, tt.wantSplits)
			}
			if len(ops.respawnCalls) != tt.wantRespawns {
				t.Errorf("RespawnPane calls = %d, want %d", len(ops.respawnCalls), tt.wantRespawns)
			}
		})
	}
}

func TestEnsureNamedPane_TagsNewPane(t *testing.T) {
	ops := &fakeSlotOps{splitID: "%99"}
	if _, err := EnsureNamedPane(ops, "%1", "demo", "", SplitOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops.setCalls) != 1 || ops.setCalls[0] != "%99 "+PaneNameOption+" demo" {
		t.Errorf("SetPaneOption calls = %v, want the new pane tagged with the slot name", ops.setCalls)
	}
}

func TestEnsureNamedPane_NamingFailureKillsPane(t *testing.T) {
	ops := &fakeSlotOps{splitID: "%99", setOptionErr: fmt.Errorf("boom")}
	_, err := EnsureNamedPane(ops, "%1", "demo", "", SplitOptions{})
	if err == nil {
		t.Fatal("expected error when naming fails")
	}
	if len(ops.killCalls) != 1 || ops.killCalls[0] != "%99" {
		t.Errorf("KillPane calls = %v, want the orphaned pane killed", ops.killCalls)
	}
}

func TestEnsureNamedPane_PlainSplitSkipsTagging(t *testing.T) {
	ops := &fakeSlotOps{splitID: "%99"}
	if _, err := EnsureNamedPane(ops, "%1", "", "", SplitOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops.setCalls) != 0 {
		t.Errorf("SetPaneOption calls = %v, want none for an unnamed split", ops.setCalls)
	}
}

func TestParsePanePID(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    int
		wantErr bool
	}{
		{name: "plain", out: "12345", want: 12345},
		{name: "trailing newline", out: "12345\n", want: 12345},
		{name: "surrounding space", out: "  12345  ", want: 12345},
		{name: "empty", out: "", wantErr: true},
		{name: "whitespace only", out: " \n", wantErr: true},
		// tmux answers an unknown target with an error, but a format that
		// resolved to nothing useful must not reach syscall.Kill either.
		{name: "not a number", out: "%42", wantErr: true},
		// A negative pid signals a whole process group and 0 signals the
		// caller's own group: both would reach far past the pane.
		{name: "negative", out: "-1", wantErr: true},
		{name: "zero", out: "0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePanePID(tt.out)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePanePID(%q) = %d, want error", tt.out, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePanePID(%q) returned error: %v", tt.out, err)
			}
			if got != tt.want {
				t.Errorf("parsePanePID(%q) = %d, want %d", tt.out, got, tt.want)
			}
		})
	}
}

// TestBuildSendKeysArgs_TerminatesOptions pins the `--` before the payload.
// Without it, tmux reads a leading dash as a flag, and the quiet failure is
// the dangerous one: `send-keys -l "-R"` exits 0 while sending nothing, so a
// caller that trusts the exit status carries on with a hole in its input.
// SendPrompt splits long prompts on a byte boundary, so a chunk can start
// with a dash even when the prompt does not.
func TestBuildSendKeysArgs_TerminatesOptions(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"plain", "hello"},
		{"leading-dash", "-abc"},
		{"valid-flag-letters", "-R"},
		{"double-dash", "--flag=1"},
		{"dash-only", "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			literal := buildSendKeysLiteralArgs("%1", tc.payload)
			wantLiteral := []string{"send-keys", "-t", "%1", "-l", "--", tc.payload}
			if !reflect.DeepEqual(literal, wantLiteral) {
				t.Errorf("buildSendKeysLiteralArgs = %q, want %q", literal, wantLiteral)
			}

			keys := buildSendKeysArgs("%1", tc.payload)
			wantKeys := []string{"send-keys", "-t", "%1", "--", tc.payload}
			if !reflect.DeepEqual(keys, wantKeys) {
				t.Errorf("buildSendKeysArgs = %q, want %q", keys, wantKeys)
			}

			// The payload must be the final element, with `--` immediately
			// before it — an argument appended after the payload would be
			// parsed as another key and injected into the pane.
			for _, got := range [][]string{literal, keys} {
				if got[len(got)-1] != tc.payload {
					t.Errorf("payload %q is not the last argument in %q", tc.payload, got)
				}
				if got[len(got)-2] != "--" {
					t.Errorf("%q does not have `--` immediately before the payload", got)
				}
			}
		})
	}
}

// TestBuildPasteBufferArgs_KeepsBracketedPaste pins `-p`.
//
// Dropping it is a silent, dangerous regression rather than a slow path:
// without paste markers tmux replays each newline as a Return, so a
// multi-line prompt is SUBMITTED ONE LINE AT A TIME. Measured against a real
// OpenCode pane, a three-line buffer became three separate messages. Nothing
// errors, so only this assertion stands between that and a release.
func TestBuildPasteBufferArgs_KeepsBracketedPaste(t *testing.T) {
	got := buildPasteBufferArgs("%7", "jin-prompt")
	want := []string{"paste-buffer", "-p", "-d", "-b", "jin-prompt", "-t", "%7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildPasteBufferArgs = %q, want %q", got, want)
	}

	var hasP, hasD bool
	for _, a := range got {
		switch a {
		case "-p":
			hasP = true
		case "-d":
			hasD = true
		}
	}
	if !hasP {
		t.Error("-p missing: newlines would be replayed as Return and the prompt " +
			"submitted line by line")
	}
	if !hasD {
		t.Error("-d missing: the prompt would stay readable in tmux's buffer stack")
	}
}

// TestBuildLoadBufferArgs_ReadsStdin pins the "-" that keeps the prompt out of
// argv — that is what removes the 16341-byte command-line limit and stops the
// prompt showing up in `ps`.
func TestBuildLoadBufferArgs_ReadsStdin(t *testing.T) {
	got := buildLoadBufferArgs("jin-prompt")
	want := []string{"load-buffer", "-b", "jin-prompt", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildLoadBufferArgs = %q, want %q", got, want)
	}
	if got[len(got)-1] != "-" {
		t.Error(`last arg must be "-" so the payload arrives on stdin, not in argv`)
	}
}
