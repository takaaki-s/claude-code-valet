package transcript

import "strings"

// LastMessagesFrom picks the last thing the operator said and the last thing
// the agent said out of a conversation, working from Entry alone so every
// agent kind reaches it through the same code.
//
// Entries the reader marked as injected or as a subagent's are skipped: an
// injected entry is stored as a user message but nobody typed it, and on real
// transcripts letting them through puts the body of an invoked skill where the
// operator's last words belong — measured on 231 Claude Code transcripts, that
// is what a naive scan returns for 55 of them. Non-text blocks are skipped for
// the same reason: a tool result rides along inside a user entry but is what a
// tool wrote back.
//
// Returns nil when the conversation holds neither, matching what the
// file-reading path returns for a transcript with nothing in it yet.
func LastMessagesFrom(entries []Entry) *LastMessages {
	// The loop only decides which two entries win; the text is joined and
	// cleaned afterwards, for those two alone. Building a Message per match
	// would throw most of them away — across 685 real transcripts, 8,055
	// entries qualify and 1,370 survive, so 83% of the joins and whitespace
	// rewrites were being discarded.
	userIdx, astIdx := -1, -1
	for i := range entries {
		e := &entries[i]
		if e.Type != "user" && e.Type != "assistant" {
			continue
		}
		if e.Injected || e.Sidechain {
			continue
		}
		if !spoke(e) {
			continue
		}
		if e.Type == "user" {
			userIdx = i
		} else {
			astIdx = i
		}
	}
	if userIdx < 0 && astIdx < 0 {
		return nil
	}
	var out LastMessages
	if userIdx >= 0 {
		out.User = messageFrom(&entries[userIdx])
	}
	if astIdx >= 0 {
		out.Assistant = messageFrom(&entries[astIdx])
	}
	return &out
}

// spoke reports whether an entry carries anything an operator would read as
// speech. Whitespace does not count: an entry holding only blank text must not
// displace the last real message, or the row goes empty for no reason.
func spoke(e *Entry) bool {
	for _, b := range e.Blocks {
		if b.Kind == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// messageFrom collapses one entry's text onto the single line the previews
// render as. Blank blocks are joined in rather than filtered: cleanContent
// collapses the runs of spaces they leave, so the result is the same and the
// filter would only restate spoke's test.
func messageFrom(e *Entry) *Message {
	var texts []string
	for _, b := range e.Blocks {
		if b.Kind == "text" {
			texts = append(texts, b.Text)
		}
	}
	return &Message{
		Type:      e.Type,
		Content:   cleanContent(strings.Join(texts, " ")),
		Timestamp: e.Timestamp,
	}
}
