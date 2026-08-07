package cmd

import (
	"errors"
	"reflect"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/tmux"
	"github.com/takaaki-s/jind-ai/internal/tui"
)

// fakeEnvWriter records SetEnvironment calls issued by the popup result
// writers so tests can assert what tmux env writes would have occurred,
// without spawning a real tmux server. err (when non-nil) is returned from
// every SetEnvironment call to exercise the "non-tmux environment" swallow
// path.
type fakeEnvWriter struct {
	sets [][3]string // [session, name, value]
	err  error
}

func (f *fakeEnvWriter) SetEnvironment(session, name, value string) error {
	f.sets = append(f.sets, [3]string{session, name, value})
	return f.err
}

// TestPushPopupResult covers both popup→parent writers in one table, since
// they differ only in the key they publish under. The empty-selection rows are
// the safety property and the reason the rule is not spelled per popup: the
// parent acts only on a key that is set, so a dismissal (Esc / Ctrl+C) must
// reach tmux as no write at all. For the confirm popup a write there would be
// an answer to a destructive prompt the user never answered — the parent would
// consume it and delete the target session.
//
// The value itself is never inspected by the writer, so one non-empty case per
// key is the whole non-empty behaviour; which values each popup can produce is
// pinned in internal/tui.
func TestPushPopupResult(t *testing.T) {
	cases := []struct {
		name     string
		push     func(selected string, tc envWriter)
		selected string
		writeErr error
		wantSets [][3]string
	}{
		{
			name:     "focus session",
			push:     pushFocusSession,
			selected: "sess-abc",
			wantSets: [][3]string{{tmux.SessionName, "JIN_FOCUS_SESSION", "sess-abc"}},
		},
		{
			// Stands in for the session-filter popup's three dismissal paths —
			// daemon.List() failing (which returns before the push), an empty
			// session list, and Esc/Ctrl+C — all of which funnel into an empty
			// selection or bypass the writer entirely.
			name: "focus session, dismissed",
			push: pushFocusSession,
		},
		{
			name:     "confirm answer",
			push:     pushConfirmResult,
			selected: tui.ConfirmResultWorktree,
			wantSets: [][3]string{{tmux.SessionName, tui.EnvConfirmResult, tui.ConfirmResultWorktree}},
		},
		{
			name: "confirm answer, dismissed",
			push: pushConfirmResult,
		},
		{
			name:     "tmux error is swallowed",
			push:     pushConfirmResult,
			selected: tui.ConfirmResultYes,
			writeErr: errors.New("tmux not running"),
			wantSets: [][3]string{{tmux.SessionName, tui.EnvConfirmResult, tui.ConfirmResultYes}},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fe := &fakeEnvWriter{err: tt.writeErr}

			tt.push(tt.selected, fe)

			if len(tt.wantSets) == 0 {
				if len(fe.sets) != 0 {
					t.Errorf("sets = %v, want none", fe.sets)
				}
				return
			}
			if !reflect.DeepEqual(fe.sets, tt.wantSets) {
				t.Errorf("sets = %v, want %v", fe.sets, tt.wantSets)
			}
		})
	}
}
