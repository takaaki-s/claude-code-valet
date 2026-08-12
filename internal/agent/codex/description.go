package codex

import (
	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/session"
)

// descriptionMaxBytes caps derived description text at 60 bytes, matching the
// Claude adapter's budget so `jin session list` renders identically across
// agent kinds.
const descriptionMaxBytes = 60

// DescriptionEnhancer implements session.DescriptionEnhancer by reading the
// first genuine user prompt from the Codex rollout JSONL and promoting it to
// Layer C-transcript.
//
// Codex is the first adapter that reaches for that layer. Codex 0.144.1 stores
// `title` in ~/.codex/state_5.sqlite `threads.title`, but empirically it is
// always identical to `first_user_message` — the CLI does not run the AI-summary
// job the Mac app appears to — so reading the rollout gives the same value with
// no new dependency. See "Session Description Model" in docs/architecture.md.
//
// It holds a Locator so it can be built once at Agent construction. Safe for
// concurrent use, and it shares that Locator with the TranscriptReader, so a
// path either one resolves is cached for both.
type DescriptionEnhancer struct {
	locator *Locator
}

// NewDescriptionEnhancer returns an enhancer that resolves rollouts through
// loc.
func NewDescriptionEnhancer(loc *Locator) *DescriptionEnhancer {
	return &DescriptionEnhancer{locator: loc}
}

// TryGenerate implements session.DescriptionEnhancer.
//
// Returns ("", 0, false) whenever the enhancer cannot yet produce a value:
//
//   - sess is nil
//   - sess.AgentSessionID is empty (pre-SessionStart write-back)
//   - the locator cannot find a rollout for the UUID (still queued, or the
//     UUID belongs to a session on another machine)
//   - the rollout has no genuine user turn yet
//
// On success, returns the truncated first user prompt and
// DescriptionLayerTranscript, the strong Layer C signal for this adapter.
func (e *DescriptionEnhancer) TryGenerate(sess *session.Session) (string, session.DescriptionLayer, bool) {
	if sess == nil || sess.AgentSessionID == "" {
		return "", 0, false
	}
	path, ok := e.locator.Find(sess.AgentSessionID)
	if !ok {
		return "", 0, false
	}
	prompt, ok := FirstUserPrompt(path)
	if !ok {
		return "", 0, false
	}
	return agent.SmartTruncate(prompt, descriptionMaxBytes), session.DescriptionLayerTranscript, true
}
