package cmd

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/takaaki-s/jind-ai/internal/debug"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// TestPanePopupCmd_HereFlagRegistered verifies the --here flag exists on the
// popup subcommand.
func TestPanePopupCmd_HereFlagRegistered(t *testing.T) {
	if panePopupCmd.Flags().Lookup("here") == nil {
		t.Error("panePopupCmd is missing the --here flag")
	}
}

// TestRunPopupHere_NoTmuxClient verifies that --here fails with a clear error
// when neither $TMUX nor JIN_CALLER_TMUX_SOCKET can resolve a server socket.
func TestRunPopupHere_NoTmuxClient(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("JIN_CALLER_TMUX_SOCKET", "")
	t.Setenv("JIN_CALLER_TMUX_PANE", "")

	err := runPopupHere("echo hi", "", "", "")
	if err == nil {
		t.Fatal("expected error when no tmux client is resolvable, got nil")
	}
	if !strings.Contains(err.Error(), "requires a tmux client") {
		t.Errorf("error = %q, want to mention %q", err.Error(), "requires a tmux client")
	}
}

// TestPopupSizeWithEnvFallback_FlagOverridesEnv verifies an explicit flag
// value wins over the JIN_PLUGIN_POPUP_* env vars.
func TestPopupSizeWithEnvFallback_FlagOverridesEnv(t *testing.T) {
	t.Setenv("JIN_PLUGIN_POPUP_WIDTH", "80%")
	t.Setenv("JIN_PLUGIN_POPUP_HEIGHT", "80%")

	width, height := popupSizeWithEnvFallback("30%", "40%")
	if width != "30%" {
		t.Errorf("width = %q, want %q", width, "30%")
	}
	if height != "40%" {
		t.Errorf("height = %q, want %q", height, "40%")
	}
}

// TestPopupSizeWithEnvFallback_UsesEnvWhenFlagEmpty verifies the env var is
// used when the corresponding flag was left unset.
func TestPopupSizeWithEnvFallback_UsesEnvWhenFlagEmpty(t *testing.T) {
	t.Setenv("JIN_PLUGIN_POPUP_WIDTH", "80%")
	t.Setenv("JIN_PLUGIN_POPUP_HEIGHT", "60%")

	width, height := popupSizeWithEnvFallback("", "")
	if width != "80%" {
		t.Errorf("width = %q, want %q", width, "80%")
	}
	if height != "60%" {
		t.Errorf("height = %q, want %q", height, "60%")
	}
}

// TestPopupSizeWithEnvFallback_EmptyWhenBothMissing verifies both values
// stay empty when neither the flag nor the env var is set, so tmux falls
// back to its own default.
func TestPopupSizeWithEnvFallback_EmptyWhenBothMissing(t *testing.T) {
	t.Setenv("JIN_PLUGIN_POPUP_WIDTH", "")
	t.Setenv("JIN_PLUGIN_POPUP_HEIGHT", "")

	width, height := popupSizeWithEnvFallback("", "")
	if width != "" {
		t.Errorf("width = %q, want empty", width)
	}
	if height != "" {
		t.Errorf("height = %q, want empty", height)
	}
}

// TestPopupSizeWithEnvFallback_Table exercises width/height fallback
// precedence independently via a table of flag/env combinations.
func TestPopupSizeWithEnvFallback_Table(t *testing.T) {
	tests := []struct {
		name       string
		flagWidth  string
		flagHeight string
		envWidth   string
		envHeight  string
		wantWidth  string
		wantHeight string
	}{
		{
			name:       "both flags set, env ignored",
			flagWidth:  "50%",
			flagHeight: "50%",
			envWidth:   "90%",
			envHeight:  "90%",
			wantWidth:  "50%",
			wantHeight: "50%",
		},
		{
			name:       "only width flag set",
			flagWidth:  "50%",
			flagHeight: "",
			envWidth:   "90%",
			envHeight:  "90%",
			wantWidth:  "50%",
			wantHeight: "90%",
		},
		{
			name:       "only height flag set",
			flagWidth:  "",
			flagHeight: "50%",
			envWidth:   "90%",
			envHeight:  "90%",
			wantWidth:  "90%",
			wantHeight: "50%",
		},
		{
			name:       "no flags, no env",
			flagWidth:  "",
			flagHeight: "",
			envWidth:   "",
			envHeight:  "",
			wantWidth:  "",
			wantHeight: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("JIN_PLUGIN_POPUP_WIDTH", tt.envWidth)
			t.Setenv("JIN_PLUGIN_POPUP_HEIGHT", tt.envHeight)

			width, height := popupSizeWithEnvFallback(tt.flagWidth, tt.flagHeight)
			if width != tt.wantWidth {
				t.Errorf("width = %q, want %q", width, tt.wantWidth)
			}
			if height != tt.wantHeight {
				t.Errorf("height = %q, want %q", height, tt.wantHeight)
			}
		})
	}
}

// TestPaneSplitCmd_FlagsRegistered verifies the redesigned split flag set,
// including the deprecated aliases.
func TestPaneSplitCmd_FlagsRegistered(t *testing.T) {
	for _, name := range []string{"here", "direction", "size", "full", "no-focus", "name", "if-exists", "horizontal", "percent"} {
		if paneSplitCmd.Flags().Lookup(name) == nil {
			t.Errorf("paneSplitCmd is missing the --%s flag", name)
		}
	}
	for _, name := range []string{"horizontal", "percent"} {
		if f := paneSplitCmd.Flags().Lookup(name); f != nil && f.Deprecated == "" {
			t.Errorf("--%s should be marked deprecated", name)
		}
	}
}

// TestPaneCloseCmd_FlagsRegistered verifies the close subcommand and its flags.
func TestPaneCloseCmd_FlagsRegistered(t *testing.T) {
	for _, name := range []string{"here", "name"} {
		if paneCloseCmd.Flags().Lookup(name) == nil {
			t.Errorf("paneCloseCmd is missing the --%s flag", name)
		}
	}
}

// newSplitFlagsCmd returns a throwaway command carrying the split flag set, so
// splitGeometryFromFlags can be exercised without mutating paneSplitCmd.
func newSplitFlagsCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "split"}
	registerSplitFlags(cmd)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v) failed: %v", args, err)
	}
	return cmd
}

func TestSplitGeometryFromFlags(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantDirection string
		wantSize      string
		wantErr       string
	}{
		{
			name:          "defaults",
			args:          nil,
			wantDirection: "down",
			wantSize:      "",
		},
		{
			name:          "new flags pass through",
			args:          []string{"--direction", "left", "--size", "25%"},
			wantDirection: "left",
			wantSize:      "25%",
		},
		{
			name:          "deprecated horizontal maps to right",
			args:          []string{"--horizontal"},
			wantDirection: "right",
		},
		{
			name:          "deprecated percent maps to size",
			args:          []string{"--percent", "40"},
			wantDirection: "down",
			wantSize:      "40%",
		},
		{
			name:    "horizontal conflicts with direction",
			args:    []string{"--horizontal", "--direction", "up"},
			wantErr: "--horizontal conflicts with --direction",
		},
		{
			name:    "percent conflicts with size",
			args:    []string{"--percent", "40", "--size", "30%"},
			wantErr: "--percent conflicts with --size",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newSplitFlagsCmd(t, tt.args...)
			direction, size, err := splitGeometryFromFlags(cmd)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if direction != tt.wantDirection {
				t.Errorf("direction = %q, want %q", direction, tt.wantDirection)
			}
			if size != tt.wantSize {
				t.Errorf("size = %q, want %q", size, tt.wantSize)
			}
		})
	}
}

func TestValidateSlotFlags(t *testing.T) {
	tests := []struct {
		name     string
		slotName string
		ifExists string
		wantErr  string
	}{
		{"no slot flags", "", "", ""},
		{"name alone", "demo", "", ""},
		{"name with respawn", "demo", "respawn", ""},
		{"if-exists without name", "", "respawn", "--if-exists requires --name"},
		{"invalid if-exists", "demo", "maybe", "invalid if-exists"},
		{"invalid name", "has space", "", "invalid pane name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tmux.ValidateSlotOptions(tt.slotName, tt.ifExists, tmux.SplitOptions{})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestRunSplitHere_NoTmuxClient mirrors TestRunPopupHere_NoTmuxClient for the
// split --here path.
func TestRunSplitHere_NoTmuxClient(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("JIN_CALLER_TMUX_SOCKET", "")
	t.Setenv("JIN_CALLER_TMUX_PANE", "")

	_, err := runSplitHere(tmux.SplitOptions{Cmd: "echo hi"}, "", "")
	if err == nil {
		t.Fatal("expected error when no tmux client is resolvable, got nil")
	}
	if !strings.Contains(err.Error(), "requires a tmux client") {
		t.Errorf("error = %q, want to mention %q", err.Error(), "requires a tmux client")
	}
}

// TestRunCloseHere_NoTmuxClient verifies close --here fails cleanly outside tmux.
func TestRunCloseHere_NoTmuxClient(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("JIN_CALLER_TMUX_SOCKET", "")
	t.Setenv("JIN_CALLER_TMUX_PANE", "")

	if err := runCloseHere("demo"); err == nil {
		t.Fatal("expected error when no tmux client is resolvable, got nil")
	}
}

// wantPaneEnv is the assignments callerPaneEnv must produce for a caller told
// these three things. It reads debug.Enabled rather than being fixed at "off"
// because that flag is a package variable set at init: a suite run by someone
// with JIN_DEBUG exported would otherwise fail on the environment rather than
// on the code.
//
// debug.Enabled and not paneDebugEnabled, deliberately. Asking the seam would
// make both sides of every comparison move together, so unbinding the seam from
// the real flag would satisfy them all — which is what an earlier version of
// this helper did, and what let that mutation live.
func wantPaneEnv(socket, bin, session string) []string {
	debugAssign := "JIN_DEBUG="
	if debug.Enabled() {
		debugAssign = "JIN_DEBUG=1"
	}
	return []string{"JIN_SOCKET=" + socket, "JIN_BIN=" + bin, debugAssign, "JIN_SESSION_ID=" + session}
}

// TestPaneDebugEnabled_IsWiredToTheRealFlag guards the link the test that
// replaces paneDebugEnabled cannot: a version defined as a constant would
// satisfy that one while leaving the pane told something the daemon never said.
//
// Its reach depends on the flag, and saying so is the point. With JIN_DEBUG
// unset both sides read false and a constant false passes — nothing running in
// this process can tell those apart, because internal/debug fixes its answer at
// init. Under JIN_DEBUG=1 the comparison is real, and that is the run where a
// disconnection matters: the pane would be told to stay quiet while the daemon
// was recording.
func TestPaneDebugEnabled_IsWiredToTheRealFlag(t *testing.T) {
	if paneDebugEnabled() != debug.Enabled() {
		t.Errorf("paneDebugEnabled() = %v, debug.Enabled() = %v; the seam has come loose from the flag it stands for",
			paneDebugEnabled(), debug.Enabled())
	}
}

// TestCallerPaneEnv_ForwardsWhatItWasTold is the --here counterpart of the
// daemon path's hand-off test. What it pins is that nothing here is worked out
// locally: --here never reaches the daemon, so the only correct source for
// which jin and which session is the environment this process was started with.
func TestCallerPaneEnv_ForwardsWhatItWasTold(t *testing.T) {
	t.Setenv("JIN_SOCKET", "/nonexistent/told.sock")
	t.Setenv("JIN_BIN", "/nonexistent/told-bin/jin")
	t.Setenv("JIN_SESSION_ID", "told-session")

	want := wantPaneEnv("/nonexistent/told.sock", "/nonexistent/told-bin/jin", "told-session")
	if got := callerPaneEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("callerPaneEnv() = %q, want %q", got, want)
	}
}

// TestCallerPaneEnv_CarriesTheDebugFlag is separate because the flag cannot be
// driven through the environment: internal/debug reads it into a package
// variable at init. Without this, hardcoding the field to false passes.
func TestCallerPaneEnv_CarriesTheDebugFlag(t *testing.T) {
	t.Setenv("JIN_SOCKET", "/nonexistent/told.sock")
	t.Setenv("JIN_BIN", "")
	t.Setenv("JIN_SESSION_ID", "")

	for _, on := range []bool{true, false} {
		prev := paneDebugEnabled
		paneDebugEnabled = func() bool { return on }
		got := callerPaneEnv()
		paneDebugEnabled = prev

		want := "JIN_DEBUG="
		if on {
			want = "JIN_DEBUG=1"
		}
		if !slices.Contains(got, want) {
			t.Errorf("with the flag %v, callerPaneEnv() = %q, want it to contain %q", on, got, want)
		}
	}
}

// TestCallerPaneEnv_NeverGuessesTheBinary is the one that has to fail if
// someone reaches for os.Executable() here. A caller with no JIN_BIN is a
// caller jind-ai did not start, and the honest answer is that we do not know
// which binary it should re-enter — an empty assignment, which
// "${JIN_BIN:-jin}" resolves to the PATH copy. The path of *this* process is
// not an answer to that question, and it is what a derivation would produce.
func TestCallerPaneEnv_NeverGuessesTheBinary(t *testing.T) {
	t.Setenv("JIN_SOCKET", "/nonexistent/told.sock")
	t.Setenv("JIN_BIN", "")
	t.Setenv("JIN_SESSION_ID", "")

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	got := callerPaneEnv()
	if slices.Contains(got, "JIN_BIN="+self) {
		t.Fatalf("callerPaneEnv() derived JIN_BIN from this process (%s)", self)
	}
	if !slices.Contains(got, "JIN_BIN=") {
		t.Errorf("callerPaneEnv() = %q, want an empty JIN_BIN assignment: leaving the key out lets the pane inherit the tmux server's", got)
	}
}

// TestCallerPaneEnv_ResolvesTheSocketItselfRatherThanForwardingIt pins the one
// field --here does not simply pass through. A caller with no JIN_SOCKET still
// has to name one, because leaving the pane to work it out means leaving it to
// a $XDG_RUNTIME_DIR that came from the tmux server — the value this whole
// change stops trusting.
//
// The --socket flag is not exercised: it is registered on the daemon
// subcommand only, so no `jin pane` invocation can set it. getSocketPath() is
// still the right call because it is the one place that decides, and it is
// where the flag would start applying if it were ever promoted.
func TestCallerPaneEnv_ResolvesTheSocketItselfRatherThanForwardingIt(t *testing.T) {
	t.Setenv("JIN_BIN", "")
	t.Setenv("JIN_SESSION_ID", "")

	t.Setenv("JIN_SOCKET", "/nonexistent/from-env.sock")
	if got := callerPaneEnv(); !slices.Contains(got, "JIN_SOCKET=/nonexistent/from-env.sock") {
		t.Errorf("callerPaneEnv() = %q, want the caller's own socket", got)
	}

	t.Setenv("JIN_SOCKET", "")
	got := callerPaneEnv()
	if slices.Contains(got, "JIN_SOCKET=") {
		t.Errorf("callerPaneEnv() = %q, want a resolved socket rather than an empty one when the caller was told none", got)
	}
	if !slices.Contains(got, "JIN_SOCKET="+getSocketPath()) {
		t.Errorf("callerPaneEnv() = %q, want the path getSocketPath() resolves", got)
	}
}

// fakeCallerTmux stands in for the caller's tmux server so the --here runners
// can be driven end to end. Reaching this far up matters: a double wired in
// below the runner leaves the runner free to stop using it, and that is exactly
// the regression measured to pass the whole suite.
type fakeCallerTmux struct {
	popups []tmux.DisplayPopupOptions
	splits []tmux.SplitOptions
	named  map[string]string // slot name -> existing pane ID
}

func (f *fakeCallerTmux) DisplayPopup(o tmux.DisplayPopupOptions) error {
	f.popups = append(f.popups, o)
	return nil
}
func (f *fakeCallerTmux) FindPaneByName(_, name string) (string, error) { return f.named[name], nil }
func (f *fakeCallerTmux) SplitPane(_ string, o tmux.SplitOptions) (string, error) {
	f.splits = append(f.splits, o)
	return "%9", nil
}
func (f *fakeCallerTmux) SetPaneOption(string, string, string) error { return nil }
func (f *fakeCallerTmux) RespawnPane(string, string, []string) error { return nil }
func (f *fakeCallerTmux) KillPane(string) error                      { return nil }

// useFakeCallerTmux swaps the resolution for the duration of a test.
func useFakeCallerTmux(t *testing.T, anchorPane string) *fakeCallerTmux {
	t.Helper()
	f := &fakeCallerTmux{}
	prev := resolveCallerTmux
	resolveCallerTmux = func() (callerPaneOps, string, error) { return f, anchorPane, nil }
	t.Cleanup(func() { resolveCallerTmux = prev })
	return f
}

// TestRunPopupHere_TellsThePaneWhichJin and its split twin cover the call, not
// the callee: what regressed before was a runner that assembled the options
// itself, leaving every helper and its test intact and green.
func TestRunPopupHere_TellsThePaneWhichJin(t *testing.T) {
	t.Setenv("JIN_SOCKET", "/nonexistent/told.sock")
	t.Setenv("JIN_BIN", "/nonexistent/told-bin/jin")
	t.Setenv("JIN_SESSION_ID", "told-session")
	f := useFakeCallerTmux(t, "%3")

	if err := runPopupHere("less /tmp/x", "T", "80%", "50%"); err != nil {
		t.Fatalf("runPopupHere failed: %v", err)
	}
	if len(f.popups) != 1 {
		t.Fatalf("DisplayPopup called %d times, want 1", len(f.popups))
	}
	want := wantPaneEnv("/nonexistent/told.sock", "/nonexistent/told-bin/jin", "told-session")
	if !reflect.DeepEqual(f.popups[0].Env, want) {
		t.Errorf("popup Env = %q, want %q", f.popups[0].Env, want)
	}
	o := f.popups[0]
	if o.Target != "%3" || o.Cmd != "less /tmp/x" || o.Title != "T" || o.Width != "80%" || o.Height != "50%" {
		t.Errorf("runPopupHere mangled a field: %+v", o)
	}
}

func TestRunSplitHere_TellsThePaneWhichJin(t *testing.T) {
	t.Setenv("JIN_SOCKET", "/nonexistent/told.sock")
	t.Setenv("JIN_BIN", "/nonexistent/told-bin/jin")
	t.Setenv("JIN_SESSION_ID", "told-session")
	f := useFakeCallerTmux(t, "%3")

	caller := tmux.SplitOptions{Cmd: "htop", Env: []string{"JIN_SOCKET=/tmp/attacker.sock"}}
	if _, err := runSplitHere(caller, "", ""); err != nil {
		t.Fatalf("runSplitHere failed: %v", err)
	}
	if len(f.splits) != 1 {
		t.Fatalf("SplitPane called %d times, want 1", len(f.splits))
	}
	want := wantPaneEnv("/nonexistent/told.sock", "/nonexistent/told-bin/jin", "told-session")
	if !reflect.DeepEqual(f.splits[0].Env, want) {
		t.Errorf("split Env = %q, want %q (the caller's own Env must not stand)", f.splits[0].Env, want)
	}
	if f.splits[0].Cmd != "htop" {
		t.Errorf("runSplitHere mangled Cmd = %q", f.splits[0].Cmd)
	}
}

// TestRunSplitHere_RequiresAnAnchorPane and the named-slot test below cover the
// --here split's own behaviour, which the seam made reachable and nothing was
// yet using it for. Both were measured to survive without them: the anchor
// guard could be dropped (taking `jin pane close --here`'s refusal to kill the
// caller's own pane with it) and the named-slot call could be replaced by a
// plain split, with the suite staying green either way.
func TestRunSplitHere_RequiresAnAnchorPane(t *testing.T) {
	f := &fakeCallerTmux{}
	prev := resolveCallerTmux
	resolveCallerTmux = func() (callerPaneOps, string, error) { return f, "", nil }
	t.Cleanup(func() { resolveCallerTmux = prev })

	if _, err := runSplitHere(tmux.SplitOptions{Cmd: "htop"}, "", ""); err == nil {
		t.Error("runSplitHere succeeded with no anchor pane; the split lands wherever tmux feels like")
	}
	if len(f.splits) != 0 {
		t.Errorf("SplitPane called %d times despite no anchor pane", len(f.splits))
	}
}

// TestRunSplitHere_ReusesANamedSlot pins that --here goes through the same
// named-slot procedure the daemon path does. A plain split instead would stack
// a new pane on every invocation, which is the whole thing --name prevents.
func TestRunSplitHere_ReusesANamedSlot(t *testing.T) {
	f := useFakeCallerTmux(t, "%3")
	f.named = map[string]string{"monitor": "%50"}

	got, err := runSplitHere(tmux.SplitOptions{Cmd: "htop"}, "monitor", tmux.IfExistsNoop)
	if err != nil {
		t.Fatalf("runSplitHere failed: %v", err)
	}
	if got != "%50" {
		t.Errorf("runSplitHere returned %q, want the existing slot %q", got, "%50")
	}
	if len(f.splits) != 0 {
		t.Errorf("SplitPane called %d times for a slot that already exists, want 0", len(f.splits))
	}
}
