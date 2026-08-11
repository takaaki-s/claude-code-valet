package jinenv

import (
	"reflect"
	"strings"
	"testing"
)

// TestIdentity_Environ covers the three assignments and the rule that decides
// whether each appears. The rows with something missing are the load-bearing
// ones: a caller that could not resolve a value passes the zero string, and
// what it must not produce is an assignment claiming the value is empty.
func TestIdentity_Environ(t *testing.T) {
	for _, tt := range []struct {
		name string
		id   Identity
		want []string
	}{
		{
			name: "everything known",
			id:   Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin", Debug: true},
			want: []string{"JIN_SOCKET=/run/jin.sock", "JIN_BIN=/opt/jin", "JIN_DEBUG=1"},
		},
		{
			name: "debug off is an absence, not a zero",
			id:   Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin"},
			want: []string{"JIN_SOCKET=/run/jin.sock", "JIN_BIN=/opt/jin"},
		},
		{
			name: "no socket, e.g. a caller that was never told one",
			id:   Identity{BinPath: "/opt/jin"},
			want: []string{"JIN_BIN=/opt/jin"},
		},
		{
			name: "no binary, e.g. os.Executable failed",
			id:   Identity{SocketPath: "/run/jin.sock"},
			want: []string{"JIN_SOCKET=/run/jin.sock"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.Environ(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Environ() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIdentity_EnvironEmpty is separate from the table above because what it
// pins is a count, not a value: nothing known means nothing emitted. Asserting
// against a non-nil empty slice instead would fail a nil-vs-empty change that
// no consumer can observe — both callers only range over the result.
func TestIdentity_EnvironEmpty(t *testing.T) {
	if got := (Identity{}).Environ(); len(got) != 0 {
		t.Errorf("Environ() = %q, want nothing", got)
	}
}

// TestIdentity_EnvironOmitsDepth pins that Environ never emits EnvDepth, because
// a caller that relied on it not to would break silently rather than loudly:
// internal/plugin's buildEnv writes a plugin's own depth first and appends
// Identity.Environ() afterwards (exec.go's buildEnv), so if Environ ever
// started emitting EnvDepth, an empty assignment from here would land after
// buildEnv's and win, erasing the depth buildEnv had just set and disabling
// the chain guard for every plugin run — with no error, since the run simply
// looks like it started at depth 0.
func TestIdentity_EnvironOmitsDepth(t *testing.T) {
	got := Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin", Debug: true}.Environ()
	for _, kv := range got {
		if key, _, _ := strings.Cut(kv, "="); key == EnvDepth {
			t.Fatalf("Environ() emitted %q; buildEnv appends Environ() after writing its own depth, so this would silently overwrite it", kv)
		}
	}
}

// TestIdentity_TmuxEnviron is Environ's table with the opposite expectation on
// every row that is missing something: an assignment, empty, rather than no
// assignment. The rows exist to pin that inversion, because it is the only
// difference between the two and it is not one a reader can guess — leaving a
// key out of tmux's -e hands the pane the tmux server's value instead of
// nothing.
func TestIdentity_TmuxEnviron(t *testing.T) {
	for _, tt := range []struct {
		name      string
		id        Identity
		sessionID string
		want      []string
	}{
		{
			name:      "everything known",
			id:        Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin", Debug: true},
			sessionID: "sess-1",
			want:      []string{"JIN_SOCKET=/run/jin.sock", "JIN_BIN=/opt/jin", "JIN_DEBUG=1", "JIN_SESSION_ID=sess-1", "JIN_PLUGIN_DEPTH="},
		},
		{
			name:      "debug off is an empty assignment, not a 0 and not an absence",
			id:        Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin"},
			sessionID: "sess-1",
			want:      []string{"JIN_SOCKET=/run/jin.sock", "JIN_BIN=/opt/jin", "JIN_DEBUG=", "JIN_SESSION_ID=sess-1", "JIN_PLUGIN_DEPTH="},
		},
		{
			name:      "no binary, e.g. the stable copy could not be made",
			id:        Identity{SocketPath: "/run/jin.sock"},
			sessionID: "sess-1",
			want:      []string{"JIN_SOCKET=/run/jin.sock", "JIN_BIN=", "JIN_DEBUG=", "JIN_SESSION_ID=sess-1", "JIN_PLUGIN_DEPTH="},
		},
		{
			name:      "no session, e.g. a --here caller outside any session",
			id:        Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin"},
			sessionID: "",
			want:      []string{"JIN_SOCKET=/run/jin.sock", "JIN_BIN=/opt/jin", "JIN_DEBUG=", "JIN_SESSION_ID=", "JIN_PLUGIN_DEPTH="},
		},
		{
			name:      "nothing known still blocks every inherited value",
			id:        Identity{},
			sessionID: "",
			want:      []string{"JIN_SOCKET=", "JIN_BIN=", "JIN_DEBUG=", "JIN_SESSION_ID=", "JIN_PLUGIN_DEPTH="},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.TmuxEnviron(tt.sessionID); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TmuxEnviron() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIdentity_TmuxEnvironCoversEveryKeyEnvironCan is the guard against the two
// drifting: a key added to Environ and forgotten here would be one a pane keeps
// inheriting from the tmux server, silently. Comparing key sets rather than
// values is deliberate — the values differ by design, the keys must not.
func TestIdentity_TmuxEnvironCoversEveryKeyEnvironCan(t *testing.T) {
	full := Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin", Debug: true}
	tmuxKeys := make(map[string]bool)
	for _, kv := range full.TmuxEnviron("sess-1") {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("TmuxEnviron produced %q, which is not an assignment", kv)
		}
		tmuxKeys[key] = true
	}
	for _, kv := range full.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if !tmuxKeys[key] {
			t.Errorf("Environ emits %s but TmuxEnviron does not, so a pane inherits it from the tmux server", key)
		}
	}
}
