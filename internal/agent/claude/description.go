// Package claude houses Claude Code-specific glue that must live outside
// internal/session (which is agent-agnostic). The Layer C DescriptionEnhancer
// here tracks whatever name Claude Code has settled on for the conversation:
// the AI-generated title CC writes to the transcript (shown as "Session name"
// in `/status`), falling back to the name it writes to
// ~/.claude/sessions/<PID>.json when no AI title has been recorded yet.
package claude

import (
	"github.com/takaaki-s/jind-ai/internal/agent"
	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/transcript"
)

// descriptionMaxBytes caps the derived description length. 60 bytes keeps the
// value from wrapping in the tabwriter-based `jin session list` output and
// leaves headroom for the locked marker.
const descriptionMaxBytes = 60

// ccNameSourceDerived is the value CC 2.x writes to nameSource when the session
// name was derived from the tmux window name (or another externally supplied
// hint) rather than generated from the conversation. jind-ai hands CC that
// hint, so a "derived" name round-trips our own tmux name back into
// Description. It is still better than the Layer A baseline — it matches CC's
// own /resume picker — so it is accepted at the weak C-name sublayer and any
// later, stronger signal overwrites it.
const ccNameSourceDerived = "derived"

// CCDescriptionEnhancer implements session.DescriptionEnhancer using two Claude
// Code-specific signals, tried in order of informativeness:
//
//  1. The AI-generated title CC writes to the transcript as
//     `{"type":"ai-title","aiTitle":"…"}` — the same value CC shows next to
//     "Session name" in `/status`.
//  2. The name field in ~/.claude/sessions/<PID>.json. When nameSource is
//     "derived" this is the tmux hint jind-ai itself passed CC, so it is
//     downgraded to the weak sublayer.
//
// It never mines the raw first user prompt: Claude Code owns session naming and
// jind-ai lets it lead. An agent with no native naming path can plug in its own
// enhancer using DescriptionLayerTranscript.
type CCDescriptionEnhancer struct {
	reader     *transcript.Reader
	nameReader *CCSessionNameReader
}

// NewCCDescriptionEnhancer builds an enhancer bound to the local
// ~/.claude/{projects,sessions} stores. Safe to share across goroutines:
// both underlying readers only perform read-only file I/O.
func NewCCDescriptionEnhancer() *CCDescriptionEnhancer {
	return &CCDescriptionEnhancer{
		reader:     transcript.NewReader(),
		nameReader: NewCCSessionNameReader(),
	}
}

// TryGenerate returns the best available Layer C-name candidate for sess,
// tried in this order:
//
//   - Transcript aiTitle → DescriptionLayerAgentName (strong).
//   - sessions/<PID>.json name with nameSource != "derived" →
//     DescriptionLayerAgentName (strong). Same tier as aiTitle, so whichever
//     lands first is preserved by the strict-greater layer guard.
//   - sessions/<PID>.json name with nameSource == "derived" →
//     DescriptionLayerAgentNameDerived (weak). Escapes the Baseline but lets
//     any later stronger name overwrite it.
//
// Returns ("", 0, false) when no signal is available: nil sess, missing
// AgentSessionID, or no transcript file yet AND no session-name file. Never
// mutates sess. Never returns an error.
func (e *CCDescriptionEnhancer) TryGenerate(sess *session.Session) (string, session.DescriptionLayer, bool) {
	if sess == nil || sess.AgentSessionID == "" {
		return "", 0, false
	}

	if e.reader != nil {
		workDir := sess.CurrentWorkDir
		if workDir == "" {
			workDir = sess.WorkDir
		}
		if title, ok := e.reader.ReadAITitle(workDir, sess.AgentSessionID); ok {
			return agent.SmartTruncate(title, descriptionMaxBytes), session.DescriptionLayerAgentName, true
		}
	}

	if e.nameReader != nil {
		if name, src, ok := e.nameReader.LookupName(sess.AgentSessionID); ok {
			layer := session.DescriptionLayerAgentName
			if src == ccNameSourceDerived {
				layer = session.DescriptionLayerAgentNameDerived
			}
			return agent.SmartTruncate(name, descriptionMaxBytes), layer, true
		}
	}

	return "", 0, false
}
