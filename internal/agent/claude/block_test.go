package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/session"
)

// The fixtures under testdata/ are real Claude Code 2.1.226 panes, trimmed to
// the rows these tests read: the dialog body, its options and its hint line.
// The surrounding conversation is dropped because nothing here looks at it,
// and a whole-screen fixture would pin layout this code makes no claim about.
func loadCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestDetectBlock(t *testing.T) {
	tests := []struct {
		fixture string
		want    session.BlockKind
	}{
		{"permission.txt", session.BlockPermission},
		{"question_single.txt", session.BlockQuestion},
		{"question_multi_plain.txt", session.BlockQuestionMulti},
		{"question_multi_preview.txt", session.BlockQuestionMulti},
		{"question_submit.txt", session.BlockQuestionSubmit},
		{"idle.txt", session.BlockNone},
	}
	a := New()
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			got := a.DetectBlock(loadCapture(t, tc.fixture))
			if got != tc.want {
				t.Errorf("DetectBlock(%s) = %q, want %q", tc.fixture, got, tc.want)
			}
		})
	}
}

// TestDetectBlockOrdersRefusalsFirst is the guardrail for the ordering in
// blockAnchors, and it is the single most load-bearing test in this file.
//
// The preview-bearing multi-question hint CONTAINS the single-question hint
// verbatim, so a scan that checked the answerable kinds first would classify
// that form as BlockQuestion. Nothing else would look wrong: DetectBlock
// would return a kind, AnswerBlockKeys would plan a digit, and jin would type
// it into a form where a digit only moves the cursor — leaving a half-driven
// form and reporting a timeout on it.
func TestDetectBlockOrdersRefusalsFirst(t *testing.T) {
	capture := loadCapture(t, "question_multi_preview.txt")

	// The premise: this fixture really does carry the single-question anchor.
	// If Claude Code ever stops overlapping the two hints this test still
	// passes, but it stops testing ordering — so assert the premise.
	norm := session.NormalizeForVerify(capture)
	if !strings.Contains(norm, session.NormalizeForVerify(hintQuestion)) {
		t.Fatalf("fixture no longer contains the single-question anchor %q; "+
			"this test no longer covers the ordering it was written for", hintQuestion)
	}

	if got := New().DetectBlock(capture); got != session.BlockQuestionMulti {
		t.Errorf("DetectBlock = %q, want %q — the multi-question anchors must be "+
			"checked before the single-question one", got, session.BlockQuestionMulti)
	}
}

// TestDetectBlockToleratesWrappedHint covers the reason both sides go through
// NormalizeForVerify: capture-pane inserts a newline wherever the pane wrapped
// a line, which lands inside the hint phrase on a narrow pane.
func TestDetectBlockToleratesWrappedHint(t *testing.T) {
	wrapped := " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to\namend · ctrl+e to explain\n"
	if got := New().DetectBlock(wrapped); got != session.BlockPermission {
		t.Errorf("DetectBlock(wrapped) = %q, want %q", got, session.BlockPermission)
	}
}

// TestDetectBlockIgnoresFinishedMenus pins the property the whole approach
// rests on. A pane keeps the OPTIONS of menus that are already over, so a
// detector keyed on those would report a block on a session that is idle.
// Only the hint line goes away when the dialog does.
func TestDetectBlockIgnoresFinishedMenus(t *testing.T) {
	stale := loadCapture(t, "question_single.txt")
	// Drop the hint line, keeping every option row — this is what a finished
	// menu leaves behind.
	lines := strings.Split(strings.TrimRight(stale, "\n"), "\n")
	scrollback := strings.Join(lines[:len(lines)-1], "\n") + "\n"

	if strings.Contains(scrollback, "Type something.") == false {
		t.Fatal("fixture trimmed too far: the option rows are what this test needs to survive")
	}
	if got := New().DetectBlock(scrollback); got != session.BlockNone {
		t.Errorf("DetectBlock(finished menu) = %q, want %q", got, session.BlockNone)
	}
}

func TestAnswerBlockKeysOption(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		kind    session.BlockKind
		option  int
	}{
		{"permission", "permission.txt", session.BlockPermission, 3},
		{"question", "question_single.txt", session.BlockQuestion, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			steps, err := New().AnswerBlockKeys(tc.kind, loadCapture(t, tc.fixture),
				session.BlockAnswer{Option: tc.option})
			if err != nil {
				t.Fatalf("AnswerBlockKeys returned err=%v, want nil", err)
			}
			want := []session.KeyStep{{Literal: "3"}}
			if tc.option == 2 {
				want = []session.KeyStep{{Literal: "2"}}
			}
			if len(steps) != len(want) || steps[0] != want[0] {
				t.Fatalf("steps = %+v, want %+v", steps, want)
			}
		})
	}
}

// TestAnswerBlockKeysOptionSendsNoEnter is separate from the shape assertion
// above because the absence is the contract, not an implementation detail.
// The digit commits by itself (measured), so an Enter after it would arrive
// once the dialog is already gone and land in whatever replaced it.
func TestAnswerBlockKeysOptionSendsNoEnter(t *testing.T) {
	steps, err := New().AnswerBlockKeys(session.BlockPermission,
		loadCapture(t, "permission.txt"), session.BlockAnswer{Option: 1})
	if err != nil {
		t.Fatalf("AnswerBlockKeys returned err=%v, want nil", err)
	}
	for _, s := range steps {
		if s.Key == "Enter" {
			t.Fatalf("steps contain Enter (%+v); a digit commits on its own", steps)
		}
	}
}

func TestAnswerBlockKeysText(t *testing.T) {
	steps, err := New().AnswerBlockKeys(session.BlockQuestion,
		loadCapture(t, "question_single.txt"), session.BlockAnswer{Text: "紫がいい"})
	if err != nil {
		t.Fatalf("AnswerBlockKeys returned err=%v, want nil", err)
	}
	want := []session.KeyStep{
		// "Type something." is option 4 in the fixture.
		{Literal: "4"},
		{Literal: "紫がいい", Verify: true},
		{Key: "Enter"},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps = %+v, want %+v", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
}

// TestAnswerBlockKeysTextVerifiesBeforeEnter states the invariant directly:
// the step carrying the text must ask to be verified, and the Enter must come
// after it. Manager abandons the sequence when a Verify step is not drawn, so
// this ordering is what keeps a committing Enter off a guess.
func TestAnswerBlockKeysTextVerifiesBeforeEnter(t *testing.T) {
	steps, err := New().AnswerBlockKeys(session.BlockQuestion,
		loadCapture(t, "question_single.txt"), session.BlockAnswer{Text: "x"})
	if err != nil {
		t.Fatalf("AnswerBlockKeys returned err=%v, want nil", err)
	}
	verifyAt, enterAt := -1, -1
	for i, s := range steps {
		if s.Verify {
			verifyAt = i
		}
		if s.Key == "Enter" {
			enterAt = i
		}
	}
	if verifyAt < 0 {
		t.Fatal("no step asks to be verified; the Enter would then commit on a guess")
	}
	if enterAt < 0 {
		t.Fatal("no Enter; free text is not submitted without one")
	}
	if verifyAt > enterAt {
		t.Errorf("verified step at %d comes after Enter at %d", verifyAt, enterAt)
	}
}

// TestAnswerBlockKeysRefusals covers every kind that must be turned away, and
// asserts the messages differ. They are the only thing the caller receives,
// and "attach and answer it" is not the same instruction as "attach and
// confirm the submission".
func TestAnswerBlockKeysRefusals(t *testing.T) {
	a := New()
	cases := []struct {
		name    string
		kind    session.BlockKind
		fixture string
		ans     session.BlockAnswer
	}{
		{"multi-plain", session.BlockQuestionMulti, "question_multi_plain.txt", session.BlockAnswer{Option: 1}},
		{"multi-preview", session.BlockQuestionMulti, "question_multi_preview.txt", session.BlockAnswer{Option: 1}},
		{"multi-with-text", session.BlockQuestionMulti, "question_multi_preview.txt", session.BlockAnswer{Text: "x"}},
		{"submit", session.BlockQuestionSubmit, "question_submit.txt", session.BlockAnswer{Option: 1}},
		{"none", session.BlockNone, "idle.txt", session.BlockAnswer{Option: 1}},
		{"text-on-permission", session.BlockPermission, "permission.txt", session.BlockAnswer{Text: "no thanks"}},
	}
	msgs := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, err := a.AnswerBlockKeys(tc.kind, loadCapture(t, tc.fixture), tc.ans)
			if err == nil {
				t.Fatalf("AnswerBlockKeys returned nil error and steps=%+v, want a refusal", steps)
			}
			if steps != nil {
				t.Errorf("steps = %+v on a refusal, want nil", steps)
			}
			msgs[tc.name] = err.Error()
		})
	}
	if msgs["multi-plain"] == msgs["submit"] {
		t.Errorf("multi-question and submit-confirmation refusals share a message (%q); "+
			"they call for different actions", msgs["submit"])
	}
}

// TestAnswerBlockKeysTextWithoutFreeTextOption covers a question whose options
// are all fixed. Refusing is the whole behaviour: guessing a number here would
// select one of the real answers.
func TestAnswerBlockKeysTextWithoutFreeTextOption(t *testing.T) {
	capture := " ☐ pick one\n❯ 1. yes\n  2. no\nEnter to select · ↑/↓ to navigate · Esc to cancel\n"
	if got := New().DetectBlock(capture); got != session.BlockQuestion {
		t.Fatalf("fixture does not read as a single question (%q)", got)
	}
	steps, err := New().AnswerBlockKeys(session.BlockQuestion, capture, session.BlockAnswer{Text: "maybe"})
	if err == nil {
		t.Fatalf("AnswerBlockKeys returned nil error and steps=%+v, want a refusal", steps)
	}
}

// TestFreeTextOptionPrefersTheLiveMenu pins the bottom-up scan. A pane can
// hold a finished menu above the live one, and those keep their own numbering
// — answering with the stale number would pick a different option entirely.
func TestFreeTextOptionPrefersTheLiveMenu(t *testing.T) {
	capture := strings.Join([]string{
		"❯ 1. old-a",
		"  2. old-b",
		"  3. Type something.", // stale menu: free text was 3
		"",
		" ☐ live question",
		"❯ 1. new-a",
		"  2. new-b",
		"  3. new-c",
		"  4. new-d",
		"  5. Type something.", // live menu: free text is 5
		"Enter to select · ↑/↓ to navigate · Esc to cancel",
	}, "\n")

	n, ok := freeTextOption(capture)
	if !ok {
		t.Fatal("freeTextOption found nothing")
	}
	if n != 5 {
		t.Errorf("freeTextOption = %d, want 5 (the live menu's number, not the stale 3)", n)
	}
}

func TestNumberedLabel(t *testing.T) {
	tests := []struct {
		line  string
		want  int
		found bool
	}{
		{"❯ 4. Type something.", 4, true},
		{"  4. Type something.", 4, true},
		{"  12. Type something.", 12, true},
		{"  4. Chat about this", 0, false},
		{"Ready to submit your answers?", 0, false},
		{"   Compute SHA-256 of /etc/hostname", 0, false},
		{"", 0, false},
		{"  0. Type something.", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			n, ok := numberedLabel(tc.line, freeTextLabel)
			if ok != tc.found || (ok && n != tc.want) {
				t.Errorf("numberedLabel(%q) = (%d, %v), want (%d, %v)", tc.line, n, ok, tc.want, tc.found)
			}
		})
	}
}
