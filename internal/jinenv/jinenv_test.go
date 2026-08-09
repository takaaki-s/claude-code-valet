package jinenv

import (
	"reflect"
	"testing"
)

// TestIdentity_Vars covers the three assignments and the rule that decides
// whether each appears. The empty rows are the load-bearing ones: a caller that
// could not resolve a value passes the zero string, and what it must not
// produce is an assignment claiming the value is empty.
func TestIdentity_Vars(t *testing.T) {
	for _, tt := range []struct {
		name string
		id   Identity
		want []Var
	}{
		{
			name: "everything known",
			id:   Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin", Debug: true},
			want: []Var{
				{Key: "JIN_SOCKET", Value: "/run/jin.sock"},
				{Key: "JIN_BIN", Value: "/opt/jin"},
				{Key: "JIN_DEBUG", Value: "1"},
			},
		},
		{
			name: "debug off is an absence, not a zero",
			id:   Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin"},
			want: []Var{
				{Key: "JIN_SOCKET", Value: "/run/jin.sock"},
				{Key: "JIN_BIN", Value: "/opt/jin"},
			},
		},
		{
			name: "no socket, e.g. a caller that was never told one",
			id:   Identity{BinPath: "/opt/jin"},
			want: []Var{{Key: "JIN_BIN", Value: "/opt/jin"}},
		},
		{
			name: "no binary, e.g. os.Executable failed",
			id:   Identity{SocketPath: "/run/jin.sock"},
			want: []Var{{Key: "JIN_SOCKET", Value: "/run/jin.sock"}},
		},
		{
			name: "nothing known at all",
			id:   Identity{},
			want: []Var{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.Vars(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Vars() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestIdentity_Environ pins the rendering the exec-based caller relies on:
// the same set as Vars, joined the way os/exec reads an environment.
func TestIdentity_Environ(t *testing.T) {
	id := Identity{SocketPath: "/run/jin.sock", BinPath: "/opt/jin", Debug: true}

	want := []string{"JIN_SOCKET=/run/jin.sock", "JIN_BIN=/opt/jin", "JIN_DEBUG=1"}
	if got := id.Environ(); !reflect.DeepEqual(got, want) {
		t.Errorf("Environ() = %q, want %q", got, want)
	}
}

// TestIdentity_EnvironFollowsVars guards the pair against drifting: Environ is
// only a rendering, so a key or a skip rule added to one and not the other
// would hand two children different answers to the same question.
func TestIdentity_EnvironFollowsVars(t *testing.T) {
	for _, id := range []Identity{
		{SocketPath: "/run/jin.sock", BinPath: "/opt/jin", Debug: true},
		{SocketPath: "/run/jin.sock"},
		{Debug: true},
		{},
	} {
		vars := id.Vars()
		environ := id.Environ()
		if len(vars) != len(environ) {
			t.Fatalf("Vars() has %d entries but Environ() has %d, for %+v", len(vars), len(environ), id)
		}
		for i, v := range vars {
			if want := v.Key + "=" + v.Value; environ[i] != want {
				t.Errorf("Environ()[%d] = %q, want %q", i, environ[i], want)
			}
		}
	}
}
