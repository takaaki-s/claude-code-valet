package daemon

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// The tests in this file come in two shapes.
//
// The validation cases exercise the rejection branches of the pane-* handlers
// without spinning a full Server, in the style of handle_new_test.go: each one
// returns before touching s.manager, so the zero-value Server{} is safe.
//
// The popup cases under "popup success path" run on a real Manager instead,
// because a handler that never gets past its own guards cannot show what it
// does with a request it accepts — which left the arguments it forwards, all
// five of them strings, observed by nobody. Most of them reach tmux through a
// recorder — to watch a popup open, or to watch one be refused; the rest are
// turned away by the manager before tmux is consulted at all, which is why the
// fixture supplies both a recorder and a session.

func TestHandlePanePopup_MissingID(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PanePopupRequest{Cmd: "echo hi"})
	resp := s.handlePanePopup(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "id is required") {
		t.Errorf("Error = %q, want to contain 'id is required'", resp.Error)
	}
}

func TestHandlePanePopup_MissingCmd(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PanePopupRequest{ID: "sess-1"})
	resp := s.handlePanePopup(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "cmd is required") {
		t.Errorf("Error = %q, want to contain 'cmd is required'", resp.Error)
	}
}

func TestHandlePanePopup_InvalidJSON(t *testing.T) {
	s := &Server{}
	resp := s.handlePanePopup(json.RawMessage(`{`))

	if resp.Success {
		t.Fatal("expected Success=false")
	}
}

func TestHandlePaneSplit_MissingID(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSplitRequest{Cmd: "top"})
	resp := s.handlePaneSplit(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "id is required") {
		t.Errorf("Error = %q, want to contain 'id is required'", resp.Error)
	}
}

func TestHandlePaneSplit_InvalidJSON(t *testing.T) {
	s := &Server{}
	resp := s.handlePaneSplit(json.RawMessage(`{`))

	if resp.Success {
		t.Fatal("expected Success=false")
	}
}

func TestHandlePaneSplit_InvalidDirection(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSplitRequest{ID: "sess-1", Direction: "sideways"})
	resp := s.handlePaneSplit(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "invalid direction") {
		t.Errorf("Error = %q, want to contain 'invalid direction'", resp.Error)
	}
}

func TestHandlePaneSplit_InvalidSize(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSplitRequest{ID: "sess-1", Size: "abc"})
	resp := s.handlePaneSplit(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "invalid size") {
		t.Errorf("Error = %q, want to contain 'invalid size'", resp.Error)
	}
}

func TestHandlePaneSplit_InvalidIfExists(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSplitRequest{ID: "sess-1", Name: "demo", IfExists: "maybe"})
	resp := s.handlePaneSplit(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "invalid if-exists") {
		t.Errorf("Error = %q, want to contain 'invalid if-exists'", resp.Error)
	}
}

func TestHandlePaneSplit_IfExistsWithoutName(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSplitRequest{ID: "sess-1", IfExists: "respawn"})
	resp := s.handlePaneSplit(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "--if-exists requires --name") {
		t.Errorf("Error = %q, want to contain '--if-exists requires --name'", resp.Error)
	}
}

func TestHandlePaneSplit_InvalidName(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSplitRequest{ID: "sess-1", Name: "has space"})
	resp := s.handlePaneSplit(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "invalid pane name") {
		t.Errorf("Error = %q, want to contain 'invalid pane name'", resp.Error)
	}
}

func TestHandlePaneClose_MissingID(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneCloseRequest{Name: "demo"})
	resp := s.handlePaneClose(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "id is required") {
		t.Errorf("Error = %q, want to contain 'id is required'", resp.Error)
	}
}

func TestHandlePaneClose_MissingName(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneCloseRequest{ID: "sess-1"})
	resp := s.handlePaneClose(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "name is required") {
		t.Errorf("Error = %q, want to contain 'name is required'", resp.Error)
	}
}

func TestHandlePaneClose_InvalidJSON(t *testing.T) {
	s := &Server{}
	resp := s.handlePaneClose(json.RawMessage(`{`))

	if resp.Success {
		t.Fatal("expected Success=false")
	}
}

func TestHandlePaneCapture_MissingID(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneCaptureRequest{})
	resp := s.handlePaneCapture(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "id is required") {
		t.Errorf("Error = %q, want to contain 'id is required'", resp.Error)
	}
}

func TestHandlePaneCapture_InvalidJSON(t *testing.T) {
	s := &Server{}
	resp := s.handlePaneCapture(json.RawMessage(`{`))

	if resp.Success {
		t.Fatal("expected Success=false")
	}
}

func TestHandlePaneSendKeys_MissingID(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSendKeysRequest{Keys: "hello"})
	resp := s.handlePaneSendKeys(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "id is required") {
		t.Errorf("Error = %q, want to contain 'id is required'", resp.Error)
	}
}

func TestHandlePaneSendKeys_MissingKeys(t *testing.T) {
	s := &Server{}
	data, _ := json.Marshal(PaneSendKeysRequest{ID: "sess-1"})
	resp := s.handlePaneSendKeys(data)

	if resp.Success {
		t.Fatal("expected Success=false")
	}
	if !strings.Contains(resp.Error, "keys is required") {
		t.Errorf("Error = %q, want to contain 'keys is required'", resp.Error)
	}
}

func TestHandlePaneSendKeys_InvalidJSON(t *testing.T) {
	s := &Server{}
	resp := s.handlePaneSendKeys(json.RawMessage(`{`))

	if resp.Success {
		t.Fatal("expected Success=false")
	}
}

// --- popup success path ---

// popupTestPaneID is the pane the fixture below anchors its session to, named
// once so the fixture and the assertions cannot drift apart.
const popupTestPaneID = "%7"

// recordingTmux is a tmux.Runner that keeps the popups it is asked to open and
// answers each one with err.
//
// The embedded Runner is left nil on purpose. DisplayPopup is the only verb
// the popup path has any business using, so a caller that reaches tmux some
// other way panics here rather than being absorbed by a stub that returns nil
// and records nothing. The cost is that such a panic takes the whole package's
// test binary down with it, so the run reports one failure and stops; the stack
// names recordingTmux and the offending call site, which makes that affordable.
//
// err exists because a double that can only succeed leaves "tmux refused the
// popup but the caller was told it opened" unobservable, and that is the one
// failure a user of `jin pane popup` actually sees.
//
// The mutex matches the recorder in internal/session: nothing here reads from
// a second goroutine today, and a later test that does should not have to
// remember to add one. Both fields go through it, which is why tests arm the
// error with setErr: a bare `rec.err = ...` would be the unsynchronized write
// the lock is here to rule out.
type recordingTmux struct {
	tmux.Runner
	mu     sync.Mutex
	err    error
	popups []tmux.DisplayPopupOptions
}

func (r *recordingTmux) DisplayPopup(opts tmux.DisplayPopupOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.popups = append(r.popups, opts)
	return r.err
}

// setErr arms the recorder to refuse every popup from here on.
func (r *recordingTmux) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// popupCalls is the locked reader, as its counterpart in internal/session is.
// Only the outer slice is cloned: copying a DisplayPopupOptions duplicates the
// Env slice header while leaving the backing array shared, so value semantics
// alone would isolate nothing. What makes the shallow clone enough is on the
// producing side — every Env reaching here is a fresh
// jinenv.Identity.TmuxEnviron result, allocated per call and retained by
// nobody, so there is no second writer to clone away from.
func (r *recordingTmux) popupCalls() []tmux.DisplayPopupOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.popups)
}

// newPopupTestServer builds a Server whose handlers reach a real
// session.Manager with tmux recorded, plus one session to open a popup over.
//
// The Server itself comes from newAsyncTestServer rather than a second fixture
// assembling the same three managers — that copy is the thing that drifts.
// What it leaves out is tmux, which is the whole point here: SetTmuxClient
// takes a tmux.Runner, so the double goes in at the seam production uses and
// the handler runs the entire way down to the options struct.
func newPopupTestServer(t *testing.T) (*Server, *recordingTmux, *session.Session) {
	t.Helper()
	s := newAsyncTestServer(t)
	rec := &recordingTmux{}
	s.manager.SetTmuxClient(rec)

	sess, _, err := s.manager.CreateWithOptions(session.CreateOptions{
		WorkDir:     t.TempDir(),
		Description: "pane-popup",
	})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	// Written without the manager's lock, which this package cannot take.
	// Safe because nothing else is looking: NewManager and CreateWithOptions
	// start no goroutines of their own (captureOutputTmux is reached only from
	// the recovery and start paths, neither of which runs here), and the
	// handler under test runs on this goroutine.
	sess.TmuxPaneID = popupTestPaneID
	return s, rec, sess
}

// TestHandlePanePopup_ForwardsGeometryToTmux is the success path this file did
// without. Title, Width and Height cross three hops to reach tmux — request
// struct, five positional string arguments, options struct — and none of the
// three was observed, so a popup could lose its size or wear another field's
// value with the whole suite still green.
//
// Every value below differs from every other, which is what makes a
// transposition fail rather than pass unnoticed.
func TestHandlePanePopup_ForwardsGeometryToTmux(t *testing.T) {
	s, rec, sess := newPopupTestServer(t)

	data, _ := json.Marshal(PanePopupRequest{
		ID:     sess.ID,
		Cmd:    "less /tmp/x",
		Title:  " Diff ",
		Width:  "81%",
		Height: "43%",
	})
	resp := s.handlePanePopup(data)
	if !resp.Success {
		t.Fatalf("Success = false: %s", resp.Error)
	}

	popups := rec.popupCalls()
	if len(popups) != 1 {
		t.Fatalf("DisplayPopup called %d times, want 1", len(popups))
	}
	opts := popups[0]
	// Env is not asserted here: which jin the popup calls back into is the
	// Manager's hand-off, pinned in internal/session against the identity it
	// was built with. Restating it would tie this test to that fixture's
	// literals without adding an observation.
	for _, c := range []struct{ field, got, want string }{
		{"Cmd", opts.Cmd, "less /tmp/x"},
		{"Title", opts.Title, " Diff "},
		{"Width", opts.Width, "81%"},
		{"Height", opts.Height, "43%"},
		{"Target", opts.Target, popupTestPaneID},
		{"Dir", opts.Dir, sess.WorkDir},
	} {
		if c.got != c.want {
			t.Errorf("popup %s = %q, want %q", c.field, c.got, c.want)
		}
	}
}

// TestHandlePanePopup_OmittedGeometryStaysEmpty pins the other half: a request
// that names no size or title must arrive with none, not with something this
// layer invented. buildPopupArgs omits -w/-h/-T for an empty string, which is
// how a caller asks for the tmux default; a default substituted here would be
// indistinguishable from the caller having asked for it.
func TestHandlePanePopup_OmittedGeometryStaysEmpty(t *testing.T) {
	s, rec, sess := newPopupTestServer(t)

	data, _ := json.Marshal(PanePopupRequest{ID: sess.ID, Cmd: "echo hi"})
	resp := s.handlePanePopup(data)
	if !resp.Success {
		t.Fatalf("Success = false: %s", resp.Error)
	}

	popups := rec.popupCalls()
	if len(popups) != 1 {
		t.Fatalf("DisplayPopup called %d times, want 1", len(popups))
	}
	if opts := popups[0]; opts.Title != "" || opts.Width != "" || opts.Height != "" {
		t.Errorf("handler invented a geometry the request did not ask for: %+v", opts)
	}
}

// TestHandlePanePopup_TmuxRefusal_ReportsFailure pins the outcome a user of
// `jin pane popup` actually meets when something goes wrong: tmux declines to
// open the popup. The handler learns that only as PanePopup's return value, so
// a Manager that dropped it — or a handler that ignored it — would answer
// Success for a popup that never appeared, and no test would disagree. That
// gap was measured, not imagined: swallowing the DisplayPopup error survived
// the whole suite before this test existed.
func TestHandlePanePopup_TmuxRefusal_ReportsFailure(t *testing.T) {
	const tmuxReason = "height too large"
	// A height tmux would plausibly refuse, so the request and the refusal read
	// as one story. It is asserted below rather than left as scenery.
	const refusedHeight = "99"

	s, rec, sess := newPopupTestServer(t)
	rec.setErr(errors.New(tmuxReason))

	data, _ := json.Marshal(PanePopupRequest{ID: sess.ID, Cmd: "echo hi", Height: refusedHeight})
	resp := s.handlePanePopup(data)

	if resp.Success {
		t.Fatal("expected Success=false when tmux refuses the popup")
	}
	if !strings.Contains(resp.Error, tmuxReason) {
		t.Errorf("Error = %q, want to carry tmux's reason %q", resp.Error, tmuxReason)
	}
	// The refusal must be reported, not worked around. Its siblings pin the
	// call count for popups that reach tmux and open, and for a request that
	// never reaches tmux at all; between those two sits "reaches tmux and is
	// refused", and that is the gap a retry hides in — measured: retrying once
	// on failure survived the whole suite until this line existed.
	popups := rec.popupCalls()
	if len(popups) != 1 {
		t.Fatalf("DisplayPopup called %d times, want 1", len(popups))
	}
	if got := popups[0].Height; got != refusedHeight {
		t.Errorf("popup Height = %q, want %q", got, refusedHeight)
	}
}

// TestHandlePanePopup_UnknownSession_PropagatesError covers the refusal that
// arrives before tmux is reached at all: the request is well-formed, so the
// error has to come back from the manager's own session lookup rather than
// from the handler's guards or from tmux. It also pins that nothing opens — an
// error that arrives after a popup is already on screen is a different bug
// from one that arrives instead of it.
//
// The fixture's session is discarded on purpose. A server with no sessions at
// all would report "not found" for reasons this test is not about; the point
// is that a session exists and this is not its id.
func TestHandlePanePopup_UnknownSession_PropagatesError(t *testing.T) {
	s, rec, _ := newPopupTestServer(t)

	data, _ := json.Marshal(PanePopupRequest{ID: "does-not-exist", Cmd: "echo hi"})
	resp := s.handlePanePopup(data)

	if resp.Success {
		t.Fatal("expected Success=false for an unknown session")
	}
	if !strings.Contains(resp.Error, "session not found") {
		t.Errorf("Error = %q, want to contain 'session not found'", resp.Error)
	}
	if n := len(rec.popupCalls()); n != 0 {
		t.Errorf("DisplayPopup called %d times, want 0", n)
	}
}
