package session

import (
	"fmt"
	"sync"
	"time"

	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// mockCall records a method invocation on mockTmuxRunner.
type mockCall struct {
	method string
	args   []string
}

// mockTmuxRunner is a test double for tmux.Runner.
// Configure the maps before calling Manager methods, then inspect calls afterwards.
//
// All methods are safe for concurrent use (mu guards every map and the call
// log), so concurrency tests can drive recovery and session starts in
// parallel. Configure the maps and hooks BEFORE handing the mock to
// goroutines; direct map writes from the test body would still race.
type mockTmuxRunner struct {
	mu        sync.Mutex
	sessions  map[string]bool   // session existence (HasSession return value)
	deadPanes map[string]bool   // pane dead status (IsPaneDead return value)
	paneIDs   map[string]string // session name -> pane ID (GetPaneID return value)
	panePaths map[string]string // target -> current path (GetPaneCurrentPath return value)
	captured  map[string]string // target -> content (CapturePane return value)

	// splitPaneIDs overrides the pane ID SplitPane returns for a given
	// target; unset targets get "%99". namedPanes maps a slot name to the
	// pane ID FindPaneByName reports ("" / unset = not found).
	splitPaneIDs map[string]string
	namedPanes   map[string]string

	// capturedSequence overrides captured for tests that need CapturePane
	// to return different values on successive calls (send-verify retry
	// scenarios). If set, entries are consumed in order and the final
	// entry is repeated once exhausted. Empty/nil falls back to captured.
	capturedSequence map[string][]string
	capturedIdx      map[string]int

	// captureErr, if set for a target, makes CapturePane return that
	// error instead of any recorded content. Consumed on every call.
	captureErr map[string]error

	// captureErrAfter, if set for a target, is returned as the error on
	// every CapturePane call after the first (i.e. the "after" capture in
	// a SendPrompt attempt succeeds only on the initial "before" call,
	// then fails). Lets tests exercise the "after"-side error path in
	// isolation without failing the "before" capture first.
	captureErrAfter map[string]error

	// captureCallCount tracks how many times CapturePane was invoked per
	// target, so captureErrAfter can distinguish first vs. subsequent
	// calls without relying on capturedSequence consumption.
	captureCallCount map[string]int

	// sendKeysLiteralErr injects an error for SendKeysLiteral on a given
	// target. Used by SendPrompt tests to simulate a tmux write failure
	// during the prompt-injection phase.
	sendKeysLiteralErr map[string]error

	// sendKeysLiteralErrAfterN delays that injection until the Nth call
	// for the target (1-based): calls before N succeed. A long prompt goes
	// out as several chunks, so this is what lets a test fail partway
	// through the sequence rather than on the very first write.
	sendKeysLiteralErrAfterN map[string]int

	// sendKeysLiteralTimes records when each SendKeysLiteral landed, so a
	// test can assert the gap SendPrompt leaves between chunks without
	// timing the call from the outside.
	sendKeysLiteralTimes map[string][]time.Time

	// loadedBuffers records what the paste transport handed over, so a test
	// can assert the prompt went across whole. Call COUNTS come from the
	// shared call log via countCalls — no per-method counter needed.
	loadedBuffers  map[string]string
	loadBufferErr  error
	pasteBufferErr error

	// sendKeysErr injects an error for SendKeys keyed by the "keys" arg
	// (not the target), so tests can fail a specific key sequence
	// regardless of pane — for example error only on the adapter's
	// ClearInputKeys "C-u" while letting the final "Enter" succeed.
	// Absent/nil entries return nil, matching the default SendKeys
	// behaviour.
	sendKeysErr map[string]error

	// onHasSession, if set, fires ONCE: the next HasSession call consumes it
	// (under mu, so re-arming from the callback is race-free) and invokes it
	// with the queried name WITHOUT mu held, so it may call back into the
	// Manager (and thus into other mock methods) freely. Recovery probes run
	// without Manager.mu held, so tests use this to mutate manager state
	// mid-probe and exercise the apply-phase re-validation guards.
	onHasSession func(name string)

	// terminateErr injects an error for TerminatePaneProcess on a given
	// target, standing in for a pane whose pid tmux will not report.
	terminateErr map[string]error

	// terminateSurvivors marks targets whose process ignores the signal:
	// TerminatePaneProcess reports success but the pane never goes dead, so
	// the caller has to reach for its kill-pane fallback.
	terminateSurvivors map[string]bool

	// onTerminatePaneProcess, if set, fires ONCE on the next
	// TerminatePaneProcess with the same contract as onHasSession (consumed
	// under mu, invoked without it). Kill signals the pane outside
	// Manager.mu, so tests use this to mutate manager state inside that
	// window and exercise the apply-phase re-validation.
	onTerminatePaneProcess func(target string)

	// onIsPaneDead is onHasSession's counterpart for the pane probe, fired
	// once AFTER the return value is decided. Recovery probes HasSession then
	// IsPaneDead, so this is the hook for landing something (a Kill) in the
	// window between the last probe and the apply phase.
	onIsPaneDead func(target string)

	// onRespawnPane fires ONCE while a respawn is in flight, same contract as
	// the hooks above. The monitor's resume retry respawns without m.mu held,
	// so this is where a test lands a Kill in that window.
	onRespawnPane func(target string)

	calls []mockCall // recorded calls for assertion
}

func newMockTmuxRunner() *mockTmuxRunner {
	return &mockTmuxRunner{
		sessions:           make(map[string]bool),
		deadPanes:          make(map[string]bool),
		paneIDs:            make(map[string]string),
		panePaths:          make(map[string]string),
		captured:           make(map[string]string),
		splitPaneIDs:       make(map[string]string),
		namedPanes:         make(map[string]string),
		capturedSequence:   make(map[string][]string),
		capturedIdx:        make(map[string]int),
		captureErr:         make(map[string]error),
		captureErrAfter:    make(map[string]error),
		captureCallCount:   make(map[string]int),
		sendKeysLiteralErr: make(map[string]error),
		sendKeysErr:        make(map[string]error),
		terminateErr:       make(map[string]error),
		terminateSurvivors: make(map[string]bool),

		sendKeysLiteralErrAfterN: make(map[string]int),
		sendKeysLiteralTimes:     make(map[string][]time.Time),
		loadedBuffers:            make(map[string]string),
	}
}

// takeHook consumes a fire-once callback, clearing it so a later call does not
// fire it again. Caller must hold mu; the returned callback is invoked without
// it, so a hook is free to call back into the Manager (and thus into the mock).
func takeHook(hook *func(string)) func(string) {
	cb := *hook
	*hook = nil
	return cb
}

// record appends to the call log. Caller must hold mu.
func (m *mockTmuxRunner) record(method string, args ...string) {
	m.calls = append(m.calls, mockCall{method: method, args: args})
}

func (m *mockTmuxRunner) HasSession(name string) bool {
	m.mu.Lock()
	m.record("HasSession", name)
	cb := takeHook(&m.onHasSession)
	m.mu.Unlock()
	if cb != nil {
		cb(name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[name]
}

func (m *mockTmuxRunner) KillSession(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("KillSession", name)
	delete(m.sessions, name)
	return nil
}

func (m *mockTmuxRunner) NewSessionWithCmdInDir(name string, width, height int, dir, cmd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("NewSessionWithCmdInDir", name, dir, cmd)
	m.sessions[name] = true
	return nil
}

func (m *mockTmuxRunner) RespawnPane(target, cmd string) error {
	m.mu.Lock()
	m.record("RespawnPane", target, cmd)
	cb := takeHook(&m.onRespawnPane)
	m.mu.Unlock()
	if cb != nil {
		// Before the pane comes back, so a callback lands in the window where
		// the respawn has been issued but has not taken effect.
		cb(target)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.deadPanes, target) // a respawned pane runs again
	return nil
}

func (m *mockTmuxRunner) GetPaneID(sessionName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("GetPaneID", sessionName)
	if id, ok := m.paneIDs[sessionName]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no pane ID for session %s", sessionName)
}

func (m *mockTmuxRunner) IsPaneDead(target string) bool {
	m.mu.Lock()
	m.record("IsPaneDead", target)
	dead := m.deadPanes[target]
	cb := takeHook(&m.onIsPaneDead)
	m.mu.Unlock()
	if cb != nil {
		// After the answer is fixed, so a callback that changes deadPanes
		// cannot rewrite the reading this call already committed to.
		cb(target)
	}
	return dead
}

func (m *mockTmuxRunner) TagManagedPane(paneID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("TagManagedPane", paneID)
	return nil
}

func (m *mockTmuxRunner) SetupAutoCleanDeadPanes() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("SetupAutoCleanDeadPanes")
	return nil
}

func (m *mockTmuxRunner) KillPane(paneID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("KillPane", paneID)
	return nil
}

// TerminatePaneProcess models the real client's contract: on success the
// pane's process is gone (deadPanes flips) but the pane itself stays, so the
// window and every other pane in it survive. terminateErr /
// terminateSurvivors drive the two ways that can fail.
//
// Signalling a pane that already exited fails here the way it does in
// practice: tmux still reports the pid the pane started with, and the kill
// against that stale number comes back ESRCH. Callers are expected not to get
// this far — see stopAgentPane's dead-pane check.
func (m *mockTmuxRunner) TerminatePaneProcess(target string) error {
	m.mu.Lock()
	m.record("TerminatePaneProcess", target)
	cb := takeHook(&m.onTerminatePaneProcess)
	err := m.terminateErr[target]
	if err == nil && m.deadPanes[target] {
		err = fmt.Errorf("kill %s: no such process", target)
	}
	if err == nil && !m.terminateSurvivors[target] {
		m.deadPanes[target] = true
	}
	m.mu.Unlock()
	if cb != nil {
		cb(target)
	}
	return err
}

func (m *mockTmuxRunner) GetPaneCurrentPath(target string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("GetPaneCurrentPath", target)
	if p, ok := m.panePaths[target]; ok {
		return p, nil
	}
	return "", fmt.Errorf("no pane path for target %s", target)
}

func (m *mockTmuxRunner) SendKeys(target, keys string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("SendKeys", target, keys)
	if err, ok := m.sendKeysErr[keys]; ok && err != nil {
		return err
	}
	return nil
}

func (m *mockTmuxRunner) SendKeysLiteral(target, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("SendKeysLiteral", target, text)
	// sendKeysLiteralTimes doubles as the per-target call counter — it is
	// appended on every call, so a separate int map would be a second copy
	// of the same number.
	m.sendKeysLiteralTimes[target] = append(m.sendKeysLiteralTimes[target], time.Now())
	if err, ok := m.sendKeysLiteralErr[target]; ok && err != nil {
		if n, delayed := m.sendKeysLiteralErrAfterN[target]; delayed && len(m.sendKeysLiteralTimes[target]) < n {
			return nil
		}
		return err
	}
	return nil
}

func (m *mockTmuxRunner) LoadBuffer(name, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("LoadBuffer", name, content)
	m.loadedBuffers[name] = content
	return m.loadBufferErr
}

func (m *mockTmuxRunner) PasteBuffer(target, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("PasteBuffer", target, name)
	return m.pasteBufferErr
}

// pastedContent returns what the last LoadBuffer stored under name, so a test
// can assert the WHOLE prompt was handed over in one piece rather than
// reassembling chunks.
func (m *mockTmuxRunner) pastedContent(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.loadedBuffers[name]
	return v, ok
}

func (m *mockTmuxRunner) DisplayPopup(opts tmux.DisplayPopupOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("DisplayPopup", opts.Target, opts.Cmd, opts.Dir)
	return nil
}

func (m *mockTmuxRunner) SplitPane(target string, opts tmux.SplitOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("SplitPane", target, opts.Cmd, opts.Direction, opts.Size, opts.Dir)
	if id, ok := m.splitPaneIDs[target]; ok {
		return id, nil
	}
	return "%99", nil
}

func (m *mockTmuxRunner) FindPaneByName(target, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("FindPaneByName", target, name)
	return m.namedPanes[name], nil
}

func (m *mockTmuxRunner) SetPaneOption(target, option, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("SetPaneOption", target, option, value)
	return nil
}

func (m *mockTmuxRunner) CapturePane(target string, ansi bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("CapturePane", target)
	m.captureCallCount[target]++
	if err, ok := m.captureErr[target]; ok && err != nil {
		return "", err
	}
	if err, ok := m.captureErrAfter[target]; ok && err != nil && m.captureCallCount[target] > 1 {
		return "", err
	}
	if seq, ok := m.capturedSequence[target]; ok && len(seq) > 0 {
		idx := m.capturedIdx[target]
		if idx >= len(seq) {
			idx = len(seq) - 1
		}
		val := seq[idx]
		if idx+1 < len(seq) {
			m.capturedIdx[target] = idx + 1
		}
		return val, nil
	}
	return m.captured[target], nil
}

// hasCalledWith returns true if the mock recorded a call to the given method
// where the first argument matches arg.
func (m *mockTmuxRunner) hasCalledWith(method, arg string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c.method == method && len(c.args) > 0 && c.args[0] == arg {
			return true
		}
	}
	return false
}

// countCallsWithArgs returns how many calls the mock recorded to method
// whose arg list matches args exactly. Passing zero args matches only
// recorded calls with zero args (use hasCalledWith / countCalls for
// target-only matching). Used by SendPrompt tests to distinguish
// SendKeys(pane, "C-u") from SendKeys(pane, "Enter") — hasCalledWith /
// countCalls only look at the first arg (the target).
func (m *mockTmuxRunner) countCallsWithArgs(method string, args ...string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.method != method || len(c.args) != len(args) {
			continue
		}
		match := true
		for i := range args {
			if c.args[i] != args[i] {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

// sendKeysLiteralGaps returns the interval between each consecutive pair of
// SendKeysLiteral calls for target — the gap SendPrompt leaves between
// chunks. Returns nil for fewer than two calls. Caller must hold no locks
// on the mock.
func (m *mockTmuxRunner) sendKeysLiteralGaps(target string) []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	times := m.sendKeysLiteralTimes[target]
	if len(times) < 2 {
		return nil
	}
	gaps := make([]time.Duration, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		gaps = append(gaps, times[i].Sub(times[i-1]))
	}
	return gaps
}

// firstCallIndex returns the index of the first recorded call to method
// whose arg list matches args exactly, or -1 if not found. Used to assert
// ordering between two calls (e.g. clear-key SendKeys must precede
// SendKeysLiteral). Caller must hold no locks on the mock.
func (m *mockTmuxRunner) firstCallIndex(method string, args ...string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.calls {
		if c.method != method || len(c.args) != len(args) {
			continue
		}
		match := true
		for j := range args {
			if c.args[j] != args[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
