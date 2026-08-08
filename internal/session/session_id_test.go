package session

import (
	"strings"
	"testing"
)

func TestSafeAgentSessionID_AcceptsRealIDs(t *testing.T) {
	// The shapes the shipped adapters actually report. The safety gate exists
	// to keep hostile values out, and it earns that only if it never stands in
	// the way of a genuine one.
	for _, id := range []string{
		"0198f1b2-4c3d-7a1e-8b2f-000000000abc", // Claude Code / Codex
		"0198F1B2-4C3D-7A1E-8B2F-000000000ABC",
		"ses_084426f78ffeXBrPh5ABEu2dNX", // opencode
		"ses_0425f0107ffe2ruNWlf2QIqBEJ",
	} {
		if !safeAgentSessionID(id) {
			t.Errorf("safeAgentSessionID(%q) = false — a real id was refused", id)
		}
	}
	// Shapes no adapter ships today but that stay inside the character set.
	// The gate must not be the thing that breaks when an agent changes how it
	// spells an id; that judgement belongs to Agent.RecognizesSessionID.
	for _, id := range []string{"ses_with-a-dash", "ses_with.dot", "plain", "A.1_2-3"} {
		if !safeAgentSessionID(id) {
			t.Errorf("safeAgentSessionID(%q) = false — the gate is narrower than its charset", id)
		}
	}
}

// TestSafeAgentSessionID_RejectsShellMetacharacters is the security assertion.
// Each of these was executable once stored: Manager splices SpawnPlan.Command
// into `SHELL -ic '...'`, so a value an adapter concatenated there was
// interpreted at the unrelated later moment the session resumed.
func TestSafeAgentSessionID_RejectsShellMetacharacters(t *testing.T) {
	for _, id := range []string{
		"ses_x$(touch /tmp/jin-should-not-exist)",
		"ses_x;touch /tmp/jin-should-not-exist",
		"ses_x`touch /tmp/jin-should-not-exist`",
		"ses_x'; touch /tmp/jin-should-not-exist; '",
		"ses_x|id",
		"ses_x&&id",
		"ses_x>out",
		"ses_x with spaces",
		"ses_x\ttab",
		"ses_x\nnewline",
		"ses_x\\escape",
		"ses_x\"quote",
		"ses_x/slash", // a path separator has no business in an id either
		"ses_x*glob",
		"ses_x\x00nul",
	} {
		if safeAgentSessionID(id) {
			t.Errorf("safeAgentSessionID(%q) = true — an unsafe id was accepted", id)
		}
	}
}

// TestSafeAgentSessionID_RejectsFlagLookalikes covers the injection a safe
// character set does not stop. The id becomes an argv entry (`--resume <id>`),
// and a value beginning with "-" is read there as an option to the agent rather
// than as a session to reopen — no shell required, and every character below is
// inside the allowed set.
func TestSafeAgentSessionID_RejectsFlagLookalikes(t *testing.T) {
	for _, id := range []string{
		"--dangerously-skip-permissions",
		"-h",
		"--help",
		"-0198f1b2-4c3d-7a1e-8b2f-000000000abc",
	} {
		if safeAgentSessionID(id) {
			t.Errorf("safeAgentSessionID(%q) = true — a leading hyphen makes this argv, not an id", id)
		}
	}
	// A hyphen anywhere else is ordinary: every UUID has four of them.
	if !safeAgentSessionID("0198f1b2-4c3d-7a1e-8b2f-000000000abc") {
		t.Error("a UUID was refused; only a LEADING hyphen is the problem")
	}
}

// TestSafeAgentSessionID_RejectsTraversal pins a rule that is preventive rather
// than load-bearing today, and says so, because the tempting reading is that
// these two values traverse — and they do not. Every sink that builds a path
// from an id appends a suffix (`<id>.jsonl`), so ".." spells "...jsonl". What
// stops traversal now is "/" being outside the character set.
//
// The rule earns its place against the sink that joins a bare id one day. That
// is also why "only the exact . and .. are refused" is asserted in both
// directions: a broader rule (any id containing a dot, say) would start
// refusing real ids for a danger that is not there.
func TestSafeAgentSessionID_RejectsTraversal(t *testing.T) {
	for _, id := range []string{".", ".."} {
		if safeAgentSessionID(id) {
			t.Errorf("safeAgentSessionID(%q) = true, want false", id)
		}
	}
	for _, id := range []string{"a.b", "..a", "a..", "ses_1.2", "..."} {
		if !safeAgentSessionID(id) {
			t.Errorf("safeAgentSessionID(%q) = false — only the exact . and .. are refused", id)
		}
	}
}

// TestSafeAgentSessionID_RejectsNonASCII pins the byte-wise loop: every byte of
// a multi-byte rune is >= 0x80 and must fall through to the default arm.
func TestSafeAgentSessionID_RejectsNonASCII(t *testing.T) {
	// Fullwidth latin, kana, and a zero-width space — the last one is the
	// dangerous shape, because it makes a rejected id look identical to a
	// legitimate one in any log or terminal that prints it.
	for _, id := range []string{"ses_ｘ", "セッション", "ses_x\u200b"} {
		if safeAgentSessionID(id) {
			t.Errorf("safeAgentSessionID(%q) = true, want false", id)
		}
	}
}

func TestSafeAgentSessionID_Bounds(t *testing.T) {
	// A literal, because every other assertion here is written in terms of
	// maxAgentSessionIDLen and would follow the constant wherever it went. The
	// bound is a policy — a hostile payload must not be able to grow a session
	// record or a log line — and 200 is already far past any id an agent mints
	// (36 for a UUID, 30 for opencode's).
	if safeAgentSessionID(strings.Repeat("a", 200)) {
		t.Error("a 200-character id was accepted; the length bound has grown past what any real id needs")
	}
	if safeAgentSessionID("") {
		t.Error("safeAgentSessionID(\"\") = true, want false")
	}
	atLimit := strings.Repeat("a", maxAgentSessionIDLen)
	if !safeAgentSessionID(atLimit) {
		t.Errorf("safeAgentSessionID(%d chars) = false, want true at the limit", maxAgentSessionIDLen)
	}
	overLimit := strings.Repeat("a", maxAgentSessionIDLen+1)
	if safeAgentSessionID(overLimit) {
		t.Errorf("safeAgentSessionID(%d chars) = true, want false past the limit", maxAgentSessionIDLen+1)
	}
}

// TestRejectAgentSessionID_ReportsWhichGateFailed keeps the two gates
// distinguishable. They fail for different reasons and are fixed in different
// places — a rejection that does not say which is a log line nobody can act on.
func TestRejectAgentSessionID_ReportsWhichGateFailed(t *testing.T) {
	uuidOnly := &fakeAgent{recognizesFn: func(id string) bool {
		return strings.HasPrefix(id, "0198")
	}}

	if got := rejectAgentSessionID(uuidOnly, "0198f1b2-4c3d-7a1e-8b2f-000000000abc"); got != "" {
		t.Errorf("rejectAgentSessionID(valid) = %q, want \"\"", got)
	}
	unsafe := rejectAgentSessionID(uuidOnly, "0198$(id)")
	if unsafe == "" {
		t.Fatal("rejectAgentSessionID(unsafe) = \"\", want a reason")
	}
	unrecognized := rejectAgentSessionID(uuidOnly, "ses_084426f78ffeXBrPh5ABEu2dNX")
	if unrecognized == "" {
		t.Fatal("rejectAgentSessionID(foreign shape) = \"\", want a reason")
	}
	if unsafe == unrecognized {
		t.Errorf("both gates report %q; the reason must say which one failed", unsafe)
	}
}

// TestRejectAgentSessionID_ConsultsTheAdapterOnlyAfterTheSafetyGate pins the
// ORDER, which the outcome above cannot: swapping the two blocks leaves every
// verdict identical, because both still refuse.
//
// It also carries the outcome those gates exist for: an adapter predicate is
// allowed to be loose — opencode answers on the ses_ prefix alone, which
// `ses_x$(...)` satisfies — so a refusal has to come from the conjunction
// rather than from the adapter.
//
// The order is nonetheless a promise of its own. Agent.RecognizesSessionID tells adapter
// authors they may answer loosely because a kind-independent gate has already
// run — so an implementation is entitled to assume it never sees a megabyte of
// arbitrary bytes, and to index or slice accordingly. Reversing the gates would
// keep this file green while making that sentence false, so what is watched
// here is whether the adapter was asked at all.
func TestRejectAgentSessionID_ConsultsTheAdapterOnlyAfterTheSafetyGate(t *testing.T) {
	var consulted []string
	spy := &fakeAgent{recognizesFn: func(id string) bool {
		consulted = append(consulted, id)
		return true
	}}

	for _, unsafe := range []string{
		"ses_x$(touch /tmp/jin-should-not-exist)",
		"--dangerously-skip-permissions",
		"..",
		strings.Repeat("a", maxAgentSessionIDLen+1),
		"",
	} {
		if got := rejectAgentSessionID(spy, unsafe); got == "" {
			t.Errorf("rejectAgentSessionID(%q) = \"\", want a refusal", unsafe)
		}
	}
	if len(consulted) != 0 {
		t.Errorf("the adapter predicate was handed %q; safeAgentSessionID must run first", consulted)
	}

	// And it IS asked once the value is safe — otherwise the check above would
	// pass on an adapter that is never consulted at all.
	if got := rejectAgentSessionID(spy, "0198f1b2-4c3d-7a1e-8b2f-000000000abc"); got != "" {
		t.Errorf("rejectAgentSessionID(valid) = %q, want \"\"", got)
	}
	if len(consulted) != 1 {
		t.Errorf("adapter consulted %d times for one safe id, want 1", len(consulted))
	}
}
