// Package agenttest exposes helpers for tests that need to drive the agent
// registry deterministically (register a stub, swap the current entry, wipe
// state between tests).
//
// It intentionally lives outside internal/agent so production code can never
// import Reset/Snapshot by mistake.
package agenttest

import (
	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/session"
)

// StubAgent is a minimal Agent implementation for tests. Zero-value works;
// the callbacks are optional and default to sensible no-ops. ClearKeys is
// nil by default (opt-out of the SendPrompt input-area clear, matching what
// most tests want — production adapters set their own keys).
type StubAgent struct {
	KindStr     string
	SpawnFn     func(session.SpawnOptions) session.SpawnPlan
	InterpretFn func(session.StatusSignal) (session.StatusUpdate, bool)
	SetupFn     func(session.SetupContext) error
	DescribeFn  session.DescriptionEnhancer
	ClearKeys   []string
	// PasteFn opts this stub into SendPrompt's paste transport; see
	// session.Agent.PastePlaceholder. Nil (the default) keeps the
	// keystroke path, which is what most tests want.
	PasteFn func(prompt string) string
	// DismissFn decides the overlay-dismiss keys per prompt; see
	// session.Agent.DismissOverlayKeys. Nil (the default) opts out, so a
	// stub sends no extra keys before Enter unless a test asks for them.
	DismissFn func(prompt string) []string
	// TranscriptSrc is what Transcript returns; see
	// session.Agent.Transcript. Nil (the default) means "this stub cannot
	// read a transcript", which is the answer `jin session result` turns
	// into an error — tests covering the readable path must set it.
	TranscriptSrc session.TranscriptSource
	// RecognizesFn decides which ids the hook re-key gate accepts; see
	// session.Agent.RecognizesSessionID. Nil (the default) accepts every
	// id, which is what tests unconcerned with the gate want.
	RecognizesFn func(id string) bool
}

func (s *StubAgent) Kind() string {
	if s.KindStr == "" {
		return "stub"
	}
	return s.KindStr
}

func (s *StubAgent) Setup(ctx session.SetupContext) error {
	if s.SetupFn != nil {
		return s.SetupFn(ctx)
	}
	return nil
}

func (s *StubAgent) SpawnCommand(opts session.SpawnOptions) session.SpawnPlan {
	if s.SpawnFn != nil {
		return s.SpawnFn(opts)
	}
	return session.SpawnPlan{Command: s.Kind()}
}

// RecognizesSessionID accepts every id by default, so a test that does not
// care about the hook re-key gate sees the pre-gate behaviour.
//
// Accept-all is the right default here and not in production: a stub exists to
// hold one variable still while a test moves another, and a stub that refused
// ids would make every unrelated test assert a refusal it never asked for.
// Tests that mean to exercise the gate set RecognizesFn.
func (s *StubAgent) RecognizesSessionID(id string) bool {
	if s.RecognizesFn == nil {
		return true
	}
	return s.RecognizesFn(id)
}

func (s *StubAgent) StatusSource() session.StatusSource { return statusSourceFn(s.InterpretFn) }

func (s *StubAgent) Description() session.DescriptionEnhancer { return s.DescribeFn }

// Transcript returns TranscriptSrc, nil by default. A nil source is a
// meaningful answer rather than a missing one, so it is returned verbatim.
func (s *StubAgent) Transcript() session.TranscriptSource { return s.TranscriptSrc }

func (s *StubAgent) ClearInputKeys() []string { return s.ClearKeys }

// PastePlaceholder returns "" by default, so stubs take SendPrompt's
// keystroke path unless a test opts into the paste transport via PasteFn.
func (s *StubAgent) PastePlaceholder(prompt string) string {
	if s.PasteFn == nil {
		return ""
	}
	return s.PasteFn(prompt)
}

// DismissOverlayKeys returns nil by default, so stubs press Enter straight
// after verify unless a test opts into the overlay-dismiss step via DismissFn.
//
// The default is load-bearing: every test that does not mention the dismissal
// is asserting the pre-fix key sequence, so a stub that quietly started
// returning keys would make those assertions describe something else.
// TestStubDefaultsOptOut pins it.
func (s *StubAgent) DismissOverlayKeys(prompt string) []string {
	if s.DismissFn == nil {
		return nil
	}
	return s.DismissFn(prompt)
}

type statusSourceFn func(session.StatusSignal) (session.StatusUpdate, bool)

func (f statusSourceFn) Interpret(sig session.StatusSignal) (session.StatusUpdate, bool) {
	if f == nil {
		return session.StatusUpdate{}, false
	}
	return f(sig)
}

// Reset wipes the registry. Call from t.Cleanup so tests never leak state
// into each other.
func Reset() {
	agent.ResetRegistryForTest()
}

// Snapshot captures every currently-registered adapter. Pair with Restore in
// a t.Cleanup to preserve adapters that root.go's blank import registered at
// program start, so tests that swap in stubs do not permanently empty the
// registry for later tests.
func Snapshot() []session.Agent {
	kinds := agent.Kinds()
	out := make([]session.Agent, 0, len(kinds))
	for _, k := range kinds {
		if a, err := agent.Lookup(k); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// Restore wipes the registry and re-registers every agent previously captured
// by Snapshot. Safe to call with a nil / empty slice — the registry ends up
// empty in that case.
func Restore(agents []session.Agent) {
	agent.ResetRegistryForTest()
	for _, a := range agents {
		agent.Register(a)
	}
}

// Register is a convenience wrapper around agent.Register that returns the
// argument to allow chaining in table tests.
func Register(a session.Agent) session.Agent {
	agent.Register(a)
	return a
}
