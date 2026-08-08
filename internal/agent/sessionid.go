package agent

import "github.com/google/uuid"

// canonicalUUIDLen is the length of a UUID written the one way jind-ai uses:
// five hyphen-separated groups, 8-4-4-4-12. It is what carries the weight in
// LooksLikeUUID — see there.
const canonicalUUIDLen = 36

// LooksLikeUUID reports whether s is a UUID written the canonical way. Both the
// Claude Code and the Codex adapter answer Agent.RecognizesSessionID with it, so
// the check lives here rather than being spelled twice.
//
// uuid.Validate does the parsing, because the same package mints these ids
// (uuid.New().String(), in Manager) and having generation and validation
// disagree is a class of bug worth not owning. What it does NOT do is restrict
// the spelling: it also accepts `urn:uuid:...`, `{...}` and 32 bare hex digits.
// Those are UUIDs, but they are not what any agent reports and not what any of
// them would accept back on a command line, so the length test excludes them —
// only the canonical form is 36 characters.
//
// Version and variant bits are not checked, by uuid.Validate or by this. That is
// deliberate: a v7 (or anything else an agent decides to mint) still passes.
// What this is asked is "could the agent have produced this?", and answering no
// to a real id is the expensive direction — it stops jind-ai from recording the
// id its agent just reported, and what that costs per adapter is spelled out on
// session.Agent.RecognizesSessionID.
//
// It is also not the safety check. Rejecting shell metacharacters, leading
// hyphens and path traversal is Manager's job and happens before this is
// consulted, which is what lets this one stay a question about identity.
func LooksLikeUUID(s string) bool {
	return len(s) == canonicalUUIDLen && uuid.Validate(s) == nil
}
