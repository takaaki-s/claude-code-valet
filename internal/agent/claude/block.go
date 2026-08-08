package claude

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/session"
)

// Claude Code draws a hint line under every blocking prompt, naming the keys
// that prompt accepts. Those lines are what DetectBlock matches, because they
// were measured to appear only while the prompt is live.
//
// Measured on Claude Code 2.1.226, sampling the visible pane at five points
// per round (idle before the prompt, mid-turn before the dialog, dialog live,
// immediately after answering, turn settled), three rounds of each dialog:
//
//	state                     tool-permission hint   question hint
//	idle                      0/3                    0/3
//	thinking, no dialog yet   0/3                    0/3
//	dialog live               3/3                    0/3   (and the mirror)
//	just answered             0/3                    0/3
//	turn settled              0/3                    0/3
//
// Every round ran on top of the previous rounds' output, so each capture had
// earlier menus sitting in scrollback — and they contributed nothing (6/6).
// That is the property the whole approach rests on: the OPTIONS of a finished
// menu stay on screen, so matching those would confuse a menu that is over
// with one that is waiting, but the hint line goes when the dialog goes.
const (
	// hintPermission marks a tool-approval dialog.
	hintPermission = "Esc to cancel · Tab to amend"
	// hintQuestion marks a single question. It is also a PREFIX of the
	// multi-question form's hint, which is why detectBlock checks the
	// multi-question anchors first — see there.
	hintQuestion = "Enter to select · ↑/↓ to navigate"
	// hintMultiTab marks a multi-question form that renders option previews
	// alongside the list. Its hint reads
	// "Enter to select · ↑/↓ to navigate · n to add notes · Tab to switch
	// questions · Esc to cancel".
	hintMultiTab = "Tab to switch questions"
	// hintMultiPlain marks a multi-question form without previews, whose
	// hint reads "Enter to select · Tab/Arrow keys to navigate · Esc to
	// cancel".
	hintMultiPlain = "Tab/Arrow keys to navigate"
	// anchorSubmit marks the confirmation screen a multi-question form shows
	// once every question is answered.
	//
	// This one is not a hint line, because that screen has none at all
	// (grepped for every hint above: 0 hits). It is body text, so unlike the
	// others it is not known to disappear when the screen does — that was
	// not measured. The consequence of it lingering is a refusal to answer a
	// prompt that could have been answered, which is the direction this file
	// errs in everywhere else too.
	anchorSubmit = "Ready to submit your answers?"
	// freeTextLabel is the option Claude Code appends to a question when it
	// will accept free text. Measured to stay in English while the question
	// and its own options were in Japanese, so matching the literal is safe
	// across locales (1/1).
	freeTextLabel = "Type something."
)

// blockAnchor pairs a screen literal with the kind it proves.
type blockAnchor struct {
	needle string
	kind   session.BlockKind
}

// blockAnchors is scanned in order, and the order is the safety property.
//
// Two facts force it. Claude Code's multi-question hint CONTAINS the
// single-question hint, so a scan that tested the single-question anchor
// first would call a multi-question form a single question. And the two
// forms take different keys: on a single question a digit selects and
// commits outright, while on the preview-bearing multi-question form the
// same digit only moves the cursor (2/2 each). Mistaking one for the other
// is therefore not a cosmetic error — it is jin typing into a form it cannot
// finish.
//
// So every kind that cannot be answered is ruled out before any kind that
// can. Uncertainty then costs a refusal instead of keys in the pane, which
// is the same direction session.BlockKind's doc comment describes.
var blockAnchors = []blockAnchor{
	{anchorSubmit, session.BlockQuestionSubmit},
	{hintMultiTab, session.BlockQuestionMulti},
	{hintMultiPlain, session.BlockQuestionMulti},
	{hintPermission, session.BlockPermission},
	{hintQuestion, session.BlockQuestion},
}

// DetectBlock reports which blocking prompt the captured pane shows.
//
// Both the capture and the anchors go through session.NormalizeForVerify, so
// a hint line the pane wrapped mid-phrase still matches: capture-pane emits a
// newline at the wrap position, and normalizing strips it from both sides.
func (a *Agent) DetectBlock(capture string) agent.BlockKind {
	norm := session.NormalizeForVerify(capture)
	for _, anc := range blockAnchors {
		if strings.Contains(norm, session.NormalizeForVerify(anc.needle)) {
			return anc.kind
		}
	}
	return session.BlockNone
}

// AnswerBlockKeys returns the keys that answer kind with ans.
//
// The two answerable kinds are driven the same way because Claude Code draws
// them the same way: a numbered list where the number is an absolute address.
// Measured on the tool-approval dialog, with the cursor deliberately moved
// away from the target first — pressing "1" while the cursor sat on option 3
// ran the tool, and pressing "3" while it sat on option 2 declined it (2/2).
// Out-of-range digits do nothing at all and leave the dialog standing (2/2,
// "7" and "0"), which is why no range check happens here: the miss surfaces
// as the block failing to clear, and that error already says the right thing.
func (a *Agent) AnswerBlockKeys(kind agent.BlockKind, capture string, ans agent.BlockAnswer) ([]agent.KeyStep, error) {
	switch kind {
	case session.BlockQuestionMulti:
		return nil, fmt.Errorf("this prompt asks several questions in one form, " +
			"and jin can only answer one-question prompts: answering a single question " +
			"leaves the form standing, so jin cannot tell a half-filled form from an " +
			"answer that never landed. Attach the session (jin attach <selector>) and " +
			"answer it there")
	case session.BlockQuestionSubmit:
		return nil, fmt.Errorf("this prompt is a multi-question form waiting for its " +
			"submit confirmation, which jin does not drive. Attach the session " +
			"(jin attach <selector>) and confirm it there")
	case session.BlockPermission, session.BlockQuestion:
		// handled below
	default:
		return nil, fmt.Errorf("nothing to answer: no blocking prompt was found in the pane")
	}

	if ans.Text != "" {
		return a.freeTextKeys(kind, capture, ans.Text)
	}
	// One keystroke, and deliberately no Enter after it. The digit commits by
	// itself, so an Enter here would arrive after the dialog is already gone
	// and land in whatever replaced it — the input box, or the next prompt.
	return []agent.KeyStep{{Literal: strconv.Itoa(ans.Option)}}, nil
}

// freeTextKeys drives Claude Code's free-text entry.
//
// It is the one path where the number does NOT commit. Measured: pressing the
// free-text option's number moves the cursor onto it and stops there, and
// characters typed afterwards are written into the option's own label
// ("❯ 4. ZZTOP"), with Enter submitting that text (1/1).
//
// That single observation is why the middle step carries Verify. Typing is
// drawn here and nowhere else in this file, so the sequence can check that
// the first step did what it is supposed to before pressing the Enter that
// commits. If the text is not on screen, Manager abandons the sequence and no
// Enter is sent — the same shape as DismissOverlayKeys re-checking that the
// prompt survived before committing it.
func (a *Agent) freeTextKeys(kind agent.BlockKind, capture, text string) ([]agent.KeyStep, error) {
	if kind != session.BlockQuestion {
		return nil, fmt.Errorf("this prompt takes a numbered choice, not free text: " +
			"pass --option <n> instead")
	}
	n, ok := freeTextOption(capture)
	if !ok {
		return nil, fmt.Errorf("this question offers no free-text entry (no %q option): "+
			"pass --option <n> to choose one of the numbered answers", freeTextLabel)
	}
	return []agent.KeyStep{
		{Literal: strconv.Itoa(n)},
		{Literal: text, Verify: true},
		{Key: "Enter"},
	}, nil
}

// freeTextOption returns the on-screen number of the free-text entry.
//
// It scans from the BOTTOM of the capture upwards and takes the first match,
// because a pane can hold the options of earlier, finished menus above the
// live one — those keep their own numbering, and answering with it would
// select something else entirely. Scanning up from the end reaches the live
// menu first, since it is the one nearest the hint line that got us here.
func freeTextOption(capture string) (int, bool) {
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if n, ok := numberedLabel(lines[i], freeTextLabel); ok {
			return n, true
		}
	}
	return 0, false
}

// numberedLabel parses a rendered option row of the form "❯ 4. Type
// something." and returns its number when the label matches.
//
// The comparison is normalized on both sides so the leading cursor glyph,
// the indentation Claude Code varies with selection state, and a row the pane
// wrapped all stop mattering.
func numberedLabel(line, label string) (int, bool) {
	norm := session.NormalizeForVerify(line)
	dot := strings.Index(norm, ".")
	if dot <= 0 {
		return 0, false
	}
	// Everything before the dot must be the number, once the cursor glyph is
	// dropped. Anything else means this is prose that happens to contain a
	// full stop.
	digits := strings.TrimPrefix(norm[:dot], "❯")
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, strings.HasPrefix(norm[dot+1:], session.NormalizeForVerify(label))
}
