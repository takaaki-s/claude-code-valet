package jinenv

import (
	"reflect"
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
