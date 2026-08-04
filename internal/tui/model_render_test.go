//go:build render

// This file lets a human look at Model.View() without ever running `jin ui`.
//
// `jin ui` used to be the only way to eyeball a layout change, and that once
// connected to the user's real outer tmux server and resized a session pane
// that was actively in use (model.go's WindowSizeMsg handling calls
// ResizePaneWidth on the outer tmux client the moment the TUI starts). This
// harness renders Model.View() in-process instead. The Model it builds never
// gets a tmuxClient or innerTmuxClient — both are left at their zero value
// (nil) — so there is no outer tmux client for any code path to call
// ResizePaneWidth, or anything else, on. That is a structural guarantee, not
// a promise to be careful: grep this file for "tmuxClient" and there is
// nothing to find.
//
// # Usage
//
// Start a daemon against an isolated state/runtime dir, then point this test
// at its socket:
//
//	export XDG_STATE_HOME=/tmp/jin-render-state
//	export XDG_RUNTIME_DIR=/tmp/jin-render-run   // short path: AF_UNIX caps sun_path
//	export JIN_TMUX_SOCKET=jin-render
//	jin daemon start &
//	jin session new ...   # populate a session or two to look at
//
//	JIN_RENDER_SOCKET=$XDG_RUNTIME_DIR/jind-ai/daemon.sock \
//	  go test -tags render -run TestRenderSessions -v ./internal/tui/
//
// Do NOT export XDG_CONFIG_HOME — isolating it breaks mise's own config
// lookup for the shell running the test.
//
// If JIN_RENDER_SOCKET is unset, or the daemon behind it is unreachable, the
// test skips: this harness is for a human driving it on purpose, not
// something CI or `make test` should ever run (hence the "render" build tag,
// which keeps this file out of every untagged `go test ./...`).
package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/takaaki-s/jind-ai/internal/daemon"
)

// renderSocketEnv names the env var carrying the isolated daemon's socket
// path (see the usage note in the file doc comment above).
const renderSocketEnv = "JIN_RENDER_SOCKET"

// TestRenderSessions renders the current session list at the width/height
// combinations the spec's acceptance checklist cares about, and logs each one
// behind a header a human can scan. Every geometry also gets the same
// mechanical checks a screenshot cannot give you: an exact row count and a
// per-row width bound, so a layout regression fails the test even if nobody
// is reading the log at the time.
func TestRenderSessions(t *testing.T) {
	sock := os.Getenv(renderSocketEnv)
	if sock == "" {
		t.Skipf("%s not set; see the doc comment at the top of this file for how to point it at an isolated daemon", renderSocketEnv)
	}

	client := daemon.NewClient(sock)
	sessions, err := client.List()
	if err != nil {
		// No daemon behind the socket is not this test's problem to report as
		// a failure — it just means nobody set one up for this run.
		t.Skipf("daemon at %s: %v", sock, err)
	}

	geometries := []struct{ width, height int }{
		{minTUIWidth, 20},
		{minTUIWidth, 30},
		{40, 20},
		{40, 30},
		{maxTUIWidth, 20},
		{maxTUIWidth, 30},
		// One row below the documented detail-pane threshold (m.height >= 18):
		// the detail pane should drop out whole rather than shrink.
		{40, 17},
	}

	for _, g := range geometries {
		t.Run(fmt.Sprintf("%dx%d", g.width, g.height), func(t *testing.T) {
			// sessions is used as-is, in the order the daemon returned it —
			// no reordering or timestamp formatting here — so the same
			// daemon state renders the same bytes on every run and under
			// every locale.
			m := Model{
				sessions:    sessions,
				cursor:      0,
				deletingIDs: map[string]bool{},
				width:       g.width,
				height:      g.height,
			}
			// The viewed band (currentSessionID) is put on a DIFFERENT session
			// from the cursor, because the two are orthogonal and this is the
			// only place a human can see that: the cursor bar marks one row and
			// the band marks another, and the band has to run unbroken across
			// both lines of its row. Left unset, the band never renders and the
			// row it spans is the one thing this change cannot be eyeballed
			// for. With a single session the two land on the same row, which is
			// a real state of the list rather than a defect in the harness.
			if len(sessions) > 0 {
				m.currentSessionID = sessions[min(1, len(sessions)-1)].ID
			}
			m.adjustScrollForCursor()
			view := m.View()

			logRender(t, g.width, g.height, view)
			assertRenderGeometry(t, g.width, g.height, view)
		})
	}
}

// logRender writes view to the test log wrapped in a header/footer rule, so a
// human scrolling `go test -v` output can find where one geometry ends and
// the next begins without counting blank lines.
func logRender(t *testing.T, width, height int, view string) {
	t.Helper()
	rule := strings.Repeat("=", 70)
	t.Logf("\n%s\nwidth=%d height=%d\n%s\n%s\n%s", rule, width, height, rule, view, rule)
}

// assertRenderGeometry pins the two invariants a screenshot alone cannot
// check: View() always returns exactly max(height-helpChromeLines, 5) +
// helpChromeLines rows (the pane plus the rule and help line under it — see
// View() and helpChromeLines in model.go), and no row is wider than
// max(width, 20) columns.
func assertRenderGeometry(t *testing.T, width, height int, view string) {
	t.Helper()

	lines := strings.Split(view, "\n")
	wantLines := max(height-helpChromeLines, 5) + helpChromeLines
	if len(lines) != wantLines {
		t.Errorf("%dx%d: View() is %d rows, want %d", width, height, len(lines), wantLines)
	}

	wantWidth := max(width, 20)
	for i, line := range lines {
		if w := lipgloss.Width(line); w > wantWidth {
			t.Errorf("%dx%d: row %d is %d columns wide, want <= %d: %q", width, height, i, w, wantWidth, line)
		}
	}
}
