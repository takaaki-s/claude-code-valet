package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/transcript"
)

const (
	fixtureBasic     = "testdata/export-basic.json"
	fixtureTool      = "testdata/export-tool.json"
	fixtureGrouping  = "testdata/export-grouping.json"
	fixtureParallel  = "testdata/export-parallel-tools.json"
	fixtureNoise     = "testdata/export-noise.json"
	fixtureAborted   = "testdata/export-aborted.json"
	fixtureEmpty     = "testdata/export-empty.json"
	fixtureTruncated = "testdata/export-truncated.json"
	fixtureDropped   = "testdata/export-dropped-part-clock.json"
)

// readerOver returns a reader that answers with a fixture instead of running
// opencode, so the mapping can be exercised without an install.
func readerOver(t *testing.T, path string) *TranscriptReader {
	t.Helper()
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return &TranscriptReader{export: func(string) ([]byte, error) { return doc, nil }}
}

func entriesFrom(t *testing.T, path, since string) []transcript.Entry {
	t.Helper()
	entries, err := readerOver(t, path).ReadEntries("", "ses_fixture0000000000000001", since)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	return entries
}

// shape renders entries as "role:kind,kind" strings, which is the level most
// of these assertions care about.
func shape(entries []transcript.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		kinds := make([]string, 0, len(e.Blocks))
		for _, b := range e.Blocks {
			kinds = append(kinds, b.Kind)
		}
		out = append(out, e.Type+":"+strings.Join(kinds, ","))
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s\n got: %v\nwant: %v", what, got, want)
	}
}

func TestReadEntries_TextAndReasoning(t *testing.T) {
	entries := entriesFrom(t, fixtureBasic, "")
	eq(t, "basic shape", shape(entries), []string{
		"user:text",
		"assistant:thinking,text",
	})
	if got := entries[1].Blocks[0].Text; got != "pick the build command" {
		t.Errorf("reasoning became a thinking block with no text: %q", got)
	}
	// Stamped by the group's LAST block, so a caller passing this as --since
	// is not handed the group's earlier blocks again.
	if got, want := entries[1].Timestamp, "2026-08-06T07:06:42.200Z"; got != want {
		t.Errorf("entry timestamp = %q, want the group's last block %q", got, want)
	}
	// A user text part has no clock of its own; its message's creation time is
	// the honest stand-in.
	if got, want := entries[0].Timestamp, "2026-08-06T07:06:41.000Z"; got != want {
		t.Errorf("user entry timestamp = %q, want the message's own %q", got, want)
	}
}

func TestReadEntries_ToolCallSplitsIntoUseAndResult(t *testing.T) {
	entries := entriesFrom(t, fixtureTool, "")
	// The running call contributes a use with no result: an export taken
	// mid-turn returns what has committed so far, and a result block with no
	// output would read as a tool that returned nothing.
	eq(t, "tool shape", shape(entries), []string{
		"assistant:tool_use", "user:tool_result",
		"assistant:tool_use", "user:tool_result",
		"assistant:tool_use", "user:tool_result",
		"assistant:tool_use", "user:tool_result",
		"assistant:tool_use", "user:tool_result",
		"assistant:tool_use",
	})

	use := entries[0].Blocks[0]
	if use.ToolName != "bash" || use.ToolUseID != "call_ok" {
		t.Errorf("tool_use lost its identity: %+v", use)
	}
	var input map[string]any
	if err := json.Unmarshal(use.Input, &input); err != nil || input["arg"] != "value" {
		t.Errorf("tool input not preserved as structure: %s (%v)", use.Input, err)
	}
	if got := entries[1].Blocks[0].Output; got != "ok" {
		t.Errorf("completed output = %q", got)
	}
}

// TestReadEntries_ErrorsOnlyCanSeeANonZeroExit is the defect this reader had
// while its docs claimed the opposite.
//
// opencode records a shell that exited non-zero as status "completed" — all 5
// such calls in the 194-call corpus — so believing the status alone made
// `--errors-only` return none of them. metadata.exit is the real verdict.
func TestReadEntries_ErrorsOnlyCanSeeANonZeroExit(t *testing.T) {
	entries := entriesFrom(t, fixtureTool, "")
	results := map[string]transcript.Block{}
	for _, e := range entries {
		for _, b := range e.Blocks {
			if b.Kind == "tool_result" {
				results[b.ToolUseID] = b
			}
		}
	}

	for _, tc := range []struct {
		callID  string
		isError bool
		why     string
	}{
		{"call_ok", false, "exit 0 is a success and must stay one"},
		{"call_fail", true, "exit 1 while opencode still says completed — the whole point"},
		{"call_err", true, "status error needs no exit code"},
		{"call_nofield", false, "no exit field: jind-ai cannot tell, and must not claim failure"},
		{"call_null", false, "exit present but null: same, cannot tell"},
	} {
		b, ok := results[tc.callID]
		if !ok {
			t.Errorf("%s: no tool_result", tc.callID)
			continue
		}
		if b.IsError != tc.isError {
			t.Errorf("%s: IsError = %v, want %v — %s", tc.callID, b.IsError, tc.isError, tc.why)
		}
	}
}

func TestReadEntries_ConsecutiveAssistantMessagesBecomeOneEntry(t *testing.T) {
	// opencode splits one turn across a message per step. Copying messages to
	// entries 1:1 would make --last 5 mean "five steps" here and "five
	// messages" on Claude Code.
	eq(t, "grouping", shape(entriesFrom(t, fixtureGrouping, "")), []string{
		"user:text",
		"assistant:text,text,text",
	})
}

func TestReadEntries_UsageSumsTheTurnsSteps(t *testing.T) {
	entries := entriesFrom(t, fixtureGrouping, "")
	u := entries[1].Usage
	if u == nil {
		t.Fatal("assistant entry carries no usage; opencode records tokens per message")
	}
	// Two of the three steps carry tokens; the third has none and must not
	// zero the others.
	if u.InputTokens != 30 || u.OutputTokens != 3 || u.CacheReadTokens != 300 || u.CacheCreationTokens != 11 {
		t.Errorf("usage = %+v, want the sum of the steps in the group", *u)
	}
	if entries[0].Usage != nil {
		t.Errorf("user entry carries usage: %+v", *entries[0].Usage)
	}
}

// TestReadEntries_AMessageSplitAcrossEntriesIsBilledOnce covers one message
// issuing two tool calls, which is what parallel tool use looks like. Its
// blocks land in five entries, and counting its tokens into each assistant run
// reported twice what the turn spent.
func TestReadEntries_AMessageSplitAcrossEntriesIsBilledOnce(t *testing.T) {
	entries := entriesFrom(t, fixtureParallel, "")
	eq(t, "parallel tools", shape(entries), []string{
		"assistant:tool_use", "user:tool_result",
		"assistant:tool_use", "user:tool_result",
		"assistant:text",
	})

	var in, out, cr, cw int
	for _, e := range entries {
		if e.Usage == nil {
			continue
		}
		in += e.Usage.InputTokens
		out += e.Usage.OutputTokens
		cr += e.Usage.CacheReadTokens
		cw += e.Usage.CacheCreationTokens
	}
	if in != 100 || out != 10 || cr != 1000 || cw != 50 {
		t.Errorf("usage summed across entries = in %d / out %d / cache r %d w %d, want the message's own 100/10/1000/50",
			in, out, cr, cw)
	}
}

// TestReadEntries_TimestampsNeverGoBackwards pins the one correction this
// reader makes to opencode's own clock.
//
// The parallel fixture is the real shape: the first call runs 800ms and the
// second starts and returns inside that window, so the honest end times are
// out of order. Left alone they would make --since rewind, which loses entries
// as well as repeating them.
func TestReadEntries_TimestampsNeverGoBackwards(t *testing.T) {
	for _, fixture := range []string{fixtureBasic, fixtureTool, fixtureGrouping, fixtureParallel, fixtureAborted} {
		entries := entriesFrom(t, fixture, "")
		for i := 1; i < len(entries); i++ {
			if entries[i].Timestamp < entries[i-1].Timestamp {
				t.Errorf("%s: entry %d goes backwards (%s after %s)",
					fixture, i, entries[i].Timestamp, entries[i-1].Timestamp)
			}
		}
	}
	// And the correction is a carry-forward of a real value, not an invented
	// one: the out-of-order result takes the timestamp of the block before it.
	entries := entriesFrom(t, fixtureParallel, "")
	if got, want := entries[3].Timestamp, entries[2].Timestamp; got != want {
		t.Errorf("out-of-order tool result = %q, want the previous block's %q", got, want)
	}
}

func TestReadEntries_NonConversationRowsAreDropped(t *testing.T) {
	// Every row in this fixture is something an operator did not say:
	// bookkeeping, injected context, a withdrawn part, whitespace, and two
	// part types nobody has seen in the corpus.
	if entries := entriesFrom(t, fixtureNoise, ""); len(entries) != 0 {
		t.Errorf("expected nothing to be conversation, got %v", shape(entries))
	}
}

func TestReadEntries_AbortedTurnBecomesASystemEntry(t *testing.T) {
	// Without this a turn that died looks exactly like a turn still being
	// thought about, and an orchestrator waits on an answer that is not coming.
	entries := entriesFrom(t, fixtureAborted, "")
	eq(t, "aborted", shape(entries), []string{"assistant:text", "system:text"})
	if got, want := entries[1].Blocks[0].Text, "MessageAbortedError: Aborted"; got != want {
		t.Errorf("failure text = %q, want %q", got, want)
	}
}

func TestReadEntries_EmptyDocumentIsNotAnError(t *testing.T) {
	if entries := entriesFrom(t, fixtureEmpty, ""); len(entries) != 0 {
		t.Errorf("expected no entries, got %v", shape(entries))
	}
}

// TestReadEntries_TruncatedOutputIsLoud is the guard against the measured
// failure mode: exporting through a pipe came back cut at 65536 bytes on 8 of
// 10 runs. The reader takes its output from a file for that reason, and this
// pins the behaviour if a cut document ever reaches it anyway.
func TestReadEntries_TruncatedOutputIsLoud(t *testing.T) {
	_, err := readerOver(t, fixtureTruncated).ReadEntries("", "ses_fixture0000000000000001", "")
	if err == nil {
		t.Fatal("a truncated export was accepted as a short conversation")
	}
	if !strings.Contains(err.Error(), "session document") {
		t.Errorf("error does not say what was wrong: %v", err)
	}
}

func TestReadEntries_SinceIsExclusive(t *testing.T) {
	all := entriesFrom(t, fixtureGrouping, "")
	last := all[len(all)-1].Timestamp
	if got := entriesFrom(t, fixtureGrouping, last); len(got) != 0 {
		t.Errorf("--since at the last timestamp returned %v; the bound is exclusive", shape(got))
	}
	// A bound inside the conversation keeps only what came after it.
	eq(t, "since mid-conversation",
		shape(entriesFrom(t, fixtureGrouping, "2026-08-06T07:06:45.110Z")),
		[]string{"assistant:text,text"})
	// It applies to the standalone entries too, not just to blocks.
	if got := entriesFrom(t, fixtureAborted, "2026-08-06T07:06:49.100Z"); len(got) != 0 {
		t.Errorf("--since past the failure still returned %v", shape(got))
	}
}

func TestReadEntries_ExportFailureIsNotAnEmptyConversation(t *testing.T) {
	boom := errors.New("opencode: export of ses_x failed: exit status 1")
	r := &TranscriptReader{export: func(string) ([]byte, error) { return nil, boom }}
	entries, err := r.ReadEntries("", "ses_fixture0000000000000001", "")
	if err == nil {
		t.Fatalf("a failed export answered %v and success", shape(entries))
	}
}

// TestReadEntries_PreMintedIDNeverRunsAnything covers the window every session
// passes through, and the reason it must not cost a process: until opencode
// reports its own id, Session.AgentSessionID is a UUID that names no session.
func TestReadEntries_PreMintedIDNeverRunsAnything(t *testing.T) {
	ran := false
	r := &TranscriptReader{export: func(string) ([]byte, error) {
		ran = true
		return nil, errors.New("should not have been called")
	}}
	for _, id := range []string{
		"",
		"0198f1b2-0000-7000-8000-000000000000", // the pre-minted UUID
		"ses_",
		"ses_with.dot",
		"ses_with/slash",
		"session_notthisprefix",
	} {
		entries, err := r.ReadEntries("", id, "")
		if err != nil || entries != nil {
			t.Errorf("ReadEntries(%q) = (%v, %v), want (nil, nil)", id, shape(entries), err)
		}
	}
	if ran {
		t.Error("an id that cannot name a session still spawned an export")
	}
}

func TestAgent_TranscriptIsAlwaysReadable(t *testing.T) {
	// Never nil: whether the read can happen is decided per call, and the
	// answer when it cannot is an error naming the reason — not "this adapter
	// has no transcript reader", which would be untrue.
	if New().Transcript() == nil {
		t.Fatal("Transcript() is nil; opencode sessions would report as unsupported")
	}
}

// TestRunExport_ReportsAMissingBinaryRatherThanNothing covers the asymmetry
// worth naming: jind-ai launches agents through the user's login shell, which
// resolves a version manager's shims, while the daemon's own PATH may not.
func TestRunExport_ReportsAMissingBinaryRatherThanNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := runExport("ses_fixture0000000000000001")
	if err == nil {
		t.Fatal("a missing opencode binary answered with no error")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("error does not point at the cause: %v", err)
	}
}

// TestExportArgs_KeepsPure pins the flag that keeps this read off the plugin
// path. Without --pure, printing a session would load jind-ai's own status
// reporter — once per read — and no parser test would notice.
func TestExportArgs_KeepsPure(t *testing.T) {
	got := exportArgs("ses_abc")
	want := []string{"export", "--pure", "ses_abc"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("exportArgs = %v, want %v", got, want)
	}
}

// TestStderrTail_KeepsTheEndAndBoundsMemory covers both jobs of the buffer:
// the reason a failed export gives comes after the progress line opencode
// writes on every run, and a process given 30 seconds must not be able to grow
// the daemon by writing to a channel nobody reads in full.
func TestStderrTail_KeepsTheEndAndBoundsMemory(t *testing.T) {
	var short stderrTail
	fmt.Fprint(&short, "Exporting session: ses_x\n")
	if got := short.String(); got != "Exporting session: ses_x\n" {
		t.Errorf("short write = %q, want it kept whole", got)
	}

	// One write larger than the whole buffer: the end is what matters, so the
	// head has to be the part that is dropped.
	var oversized stderrTail
	fmt.Fprint(&oversized, strings.Repeat("a", exportStderrLimit)+strings.Repeat("b", exportStderrLimit))
	if got := oversized.String(); got != strings.Repeat("b", exportStderrLimit) {
		t.Errorf("one oversized write kept the head, not the tail: %q…%q", got[:8], got[len(got)-8:])
	}

	// And the same total arriving in small writes must land on the same tail.
	var many stderrTail
	for i := 0; i < exportStderrLimit; i++ {
		fmt.Fprint(&many, "a")
	}
	for i := 0; i < exportStderrLimit; i++ {
		fmt.Fprint(&many, "b")
	}
	got := many.String()
	if len(got) != exportStderrLimit {
		t.Fatalf("kept %d bytes, want the cap of %d", len(got), exportStderrLimit)
	}
	if got != strings.Repeat("b", exportStderrLimit) {
		t.Errorf("many small writes kept the head, not the tail: %q…", got[:16])
	}
}

// TestReadEntries_ADroppedPartDoesNotMoveTheClock pins what a part the reader
// refuses is allowed to affect: nothing.
//
// An injected part carries a real timestamp, and the clock only moves forward,
// so letting one advance it would stamp the next genuine entry with a moment
// from content the reader had just decided nobody said. The fixture puts an
// injected part 8 seconds ahead of the user turn that follows it, which is the
// only arrangement where the two behaviours differ.
func TestReadEntries_ADroppedPartDoesNotMoveTheClock(t *testing.T) {
	entries := entriesFrom(t, fixtureDropped, "")
	eq(t, "dropped part", shape(entries), []string{"assistant:text", "user:text"})
	if got, want := entries[1].Timestamp, "2026-08-06T07:06:42.000Z"; got != want {
		t.Errorf("user entry = %q, want its own message's %q — an injected part dragged the clock", got, want)
	}
}
