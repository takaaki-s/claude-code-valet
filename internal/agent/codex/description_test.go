package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/session"
)

// newEnhancer builds a DescriptionEnhancer honouring whatever CODEX_HOME the
// caller has already set via t.Setenv.
func newEnhancer() *DescriptionEnhancer {
	return NewDescriptionEnhancer(NewLocator(""))
}

// newEnhancerWithFixture builds a DescriptionEnhancer whose Locator points at
// a fake $CODEX_HOME populated by copying `fixture` into a real day-shard
// path under sessionsDir. Returns the enhancer and the session UUID a caller
// can plug into sess.AgentSessionID.
func newEnhancerWithFixture(t *testing.T, fixture string) (*DescriptionEnhancer, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	stageRollout(t, filepath.Join(root, "sessions"), "2026/07/11", basicUUID, fixture)
	return newEnhancer(), basicUUID
}

func TestDescriptionEnhancer_HitReturnsTranscriptLayer(t *testing.T) {
	e, uuid := newEnhancerWithFixture(t, fixtureBasic)
	sess := &session.Session{AgentSessionID: uuid}

	got, layer, ok := e.TryGenerate(sess)
	if !ok {
		t.Fatalf("TryGenerate = _, _, false; want ok=true")
	}
	if got != "Hello, echo the current date" {
		t.Errorf("got = %q, want %q", got, "Hello, echo the current date")
	}
	if layer != session.DescriptionLayerTranscript {
		t.Errorf("layer = %d, want DescriptionLayerTranscript (%d)", layer, session.DescriptionLayerTranscript)
	}
}

func TestDescriptionEnhancer_NilSession(t *testing.T) {
	e, _ := newEnhancerWithFixture(t, fixtureBasic)
	if got, _, ok := e.TryGenerate(nil); ok {
		t.Errorf("nil session returned ok=true (%q)", got)
	}
}

func TestDescriptionEnhancer_EmptyAgentSessionID(t *testing.T) {
	e, _ := newEnhancerWithFixture(t, fixtureBasic)
	sess := &session.Session{AgentSessionID: ""}
	if got, _, ok := e.TryGenerate(sess); ok {
		t.Errorf("empty AgentSessionID returned ok=true (%q)", got)
	}
}

func TestDescriptionEnhancer_LocatorMiss(t *testing.T) {
	// Empty sessions dir → no rollout matches → false.
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	e := newEnhancer()

	sess := &session.Session{AgentSessionID: basicUUID}
	if got, _, ok := e.TryGenerate(sess); ok {
		t.Errorf("locator miss returned ok=true (%q)", got)
	}
}

func TestDescriptionEnhancer_NoUserPrompt(t *testing.T) {
	// Fixture has developer + env_context rows only, no genuine user turn.
	e, uuid := newEnhancerWithFixture(t, fixtureNoUser)
	sess := &session.Session{AgentSessionID: uuid}
	if got, _, ok := e.TryGenerate(sess); ok {
		t.Errorf("no-user rollout returned ok=true (%q)", got)
	}
}

func TestDescriptionEnhancer_Truncation(t *testing.T) {
	// Custom fixture with a long first user prompt exercises the
	// descriptionMaxBytes budget (60 bytes) via SmartTruncate.
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	sessions := filepath.Join(root, "sessions", "2026", "07", "11")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 200-byte input — well past the 60-byte cap.
	long := strings.Repeat("a", 200)
	body := `{"type":"session_meta","payload":{"id":"` + basicUUID + `","cwd":"/tmp"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + long + `"}]}}` + "\n"
	path := filepath.Join(sessions, "rollout-2026-07-11T00-00-00-"+basicUUID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	e := newEnhancer()
	got, _, ok := e.TryGenerate(&session.Session{AgentSessionID: basicUUID})
	if !ok {
		t.Fatalf("TryGenerate = _, _, false; want ok=true")
	}
	// SmartTruncate keeps the result within maxBytes + len("…") (60 + 3).
	if len(got) > descriptionMaxBytes+len("…") {
		t.Errorf("len(got) = %d, want <= %d", len(got), descriptionMaxBytes+len("…"))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("got = %q, want an ellipsis suffix", got)
	}
}

func TestDescriptionEnhancer_ImplementsInterface(t *testing.T) {
	// Compile-time check: the enhancer must satisfy the Manager-facing
	// session.DescriptionEnhancer contract so Agent.Description() can hand
	// it out untyped.
	var _ session.DescriptionEnhancer = (*DescriptionEnhancer)(nil)
	var _ session.DescriptionEnhancer = newEnhancer()
}
