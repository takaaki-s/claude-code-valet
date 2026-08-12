package session

// maxAgentSessionIDLen bounds what may be stored in Session.AgentSessionID.
// The longest real id across the shipped adapters is 30 characters (opencode's
// "ses_" plus a 26-character body); a UUID is 36. 128 clears them all, and
// exists so a hostile payload cannot grow the session record without limit.
const maxAgentSessionIDLen = 128

// safeAgentSessionID reports whether id may be written to
// Session.AgentSessionID at all, independently of which agent reported it.
//
// This is the security half of the two gates HandleHookEvent applies:
// Agent.RecognizesSessionID asks "is this the shape my agent produces?", this
// one asks "is this value safe to store and to hand to a shell?". The split is
// what lets the per-adapter predicate stay LOOSE — an adapter that also had to
// defend against injection would tighten its answer, and a tight predicate on
// this path refuses a real id, starts a NEW agent session, and loses the
// operator's conversation with nothing saying why.
//
// Every session id the shipped adapters have produced is drawn from
// [A-Za-z0-9_.-], so this set cannot be wrong about a real one — and it
// excludes exactly the characters that made a stored id executable through
// SpawnPlan.Command.
//
// The two further exclusions are not the same kind of rule:
//
//   - A leading "-" is a live problem. The id becomes an argv entry (`--resume
//     <id>`, `opencode export --pure <id>`) and is read there as an option to
//     the agent. No shell is needed: `--dangerously-skip-permissions` is
//     spelled entirely in the character set above.
//   - "." and ".." are preventive, and "these traverse" is FALSE here: every
//     sink that builds a path from an id appends a suffix, so ".." spells
//     "...jsonl". What stops traversal today is "/" being outside the set.
//     These are refused so that a sink which one day joins a bare id does not
//     become a traversal. Do not delete them on the grounds that today's sinks
//     are safe; that is the premise they exist to outlive.
func safeAgentSessionID(id string) bool {
	if id == "" || len(id) > maxAgentSessionIDLen {
		return false
	}
	if id[0] == '-' || id == "." || id == ".." {
		return false
	}
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// rejectAgentSessionID reports why id must not become Session.AgentSessionID,
// or "" when it may. The reason is for the debug log — the two gates fail for
// different reasons and a rejection that does not say which is hard to act on.
//
// The order is part of the contract Agent.RecognizesSessionID states, not an
// implementation detail: an adapter is told it may answer loosely BECAUSE the
// safety gate has already run, so a predicate never sees a value of arbitrary
// length or arbitrary bytes.
func rejectAgentSessionID(ag Agent, id string) string {
	if !safeAgentSessionID(id) {
		return "unsafe characters or too long"
	}
	if !ag.RecognizesSessionID(id) {
		return "not the shape " + ag.Kind() + " produces"
	}
	return ""
}
