package session

// maxAgentSessionIDLen bounds what may be stored in Session.AgentSessionID.
// The longest real id across the shipped adapters is 30 characters (opencode's
// "ses_" plus a 26-character body); a UUID is 36. 128 leaves every one of them
// several times over, and exists so a hostile payload cannot grow the session
// record — or a log line — without limit.
const maxAgentSessionIDLen = 128

// safeAgentSessionID reports whether id may be written to
// Session.AgentSessionID at all, independently of which agent reported it.
//
// This is the security half of the two gates HandleHookEvent applies, and the
// division of labour between them is the whole design. This one asks "is this
// value safe to store and to hand to a shell?"; Agent.RecognizesSessionID asks
// "is this the shape my agent produces?". Splitting them is what lets the
// per-adapter predicate be LOOSE: an adapter that also had to defend against
// injection would tighten its answer to be safe, and a tight predicate on this
// path is exactly the failure documented on opencode's isSessionID — refusing a
// real id starts a NEW agent session and the operator's conversation is simply
// gone, with nothing saying why.
//
// The character set is the reason this gate cannot be wrong about a real id.
// Every session id the shipped adapters have ever produced is drawn from
// [A-Za-z0-9_.-]; landing outside it would mean an agent had started putting
// whitespace, quotes, "$", backticks or ";" into its own identifiers. Those are
// precisely the characters that made a stored id executable: Manager splices
// SpawnPlan.Command into `SHELL -ic '...'`, so a value an adapter concatenates
// there is interpreted, and `ses_x$(...)` ran. Adapters no longer concatenate
// the id at all (it travels in ExtraEnv, which Manager quotes), so this is
// defence in depth rather than the only line — but it is the line that also
// covers whatever an adapter added since.
//
// A safe character set is not the whole of a safe value, and the two exclusions
// below are the difference. They are not the same kind of rule:
//
//   - A leading "-" is a live problem. The id becomes an argv entry — `--resume
//     <id>` here, and `opencode export --pure <id>` in that adapter's reader —
//     and a value starting with a hyphen is read there as an option to the
//     agent rather than as a session to reopen. Nothing needs a shell for that:
//     `--dangerously-skip-permissions` is spelled entirely in the character set
//     above. Refusing the shape is free, because no id any adapter produces
//     begins with a hyphen.
//   - "." and ".." are preventive, and it is worth being exact about why,
//     because "these traverse" is the obvious claim and it is FALSE here. Every
//     sink that builds a path from an id today appends a suffix
//     (internal/transcript joins `<id>.jsonl`, the Codex locator globs
//     `rollout-*-<id>.jsonl`), so ".." spells "...jsonl" — an ordinary
//     filename. What actually stops traversal today is "/" being outside the
//     character set. These two are refused so that a sink which one day joins a
//     bare id does not become a traversal, and refusing them costs nothing.
//     Do not delete them on the grounds that today's sinks are safe; that is
//     the premise they exist to outlive.
//
// Iterating bytes rather than runes is deliberate: every byte of a multi-byte
// rune is >= 0x80 and falls through to the default, so non-ASCII is rejected
// without a decode.
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
// length or arbitrary bytes. Swapping these two blocks would leave every
// verdict identical and quietly make that promise untrue, which is why
// TestRejectAgentSessionID_ConsultsTheAdapterOnlyAfterTheSafetyGate watches
// whether the adapter was asked rather than what came back.
func rejectAgentSessionID(ag Agent, id string) string {
	if !safeAgentSessionID(id) {
		return "unsafe characters or too long"
	}
	if !ag.RecognizesSessionID(id) {
		return "not the shape " + ag.Kind() + " produces"
	}
	return ""
}
