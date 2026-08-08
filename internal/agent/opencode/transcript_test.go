package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agent"
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
	fixtureOdd       = "testdata/export-odd-metadata.json"
)

// readerOver returns a reader that answers with a fixture instead of running
// opencode, so the mapping can be exercised without an install.
func readerOver(t *testing.T, path string) *TranscriptReader {
	t.Helper()
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return &TranscriptReader{export: func(_, _ string) ([]byte, error) { return doc, nil }}
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
	r := &TranscriptReader{export: func(_, _ string) ([]byte, error) { return nil, boom }}
	entries, err := r.ReadEntries("", "ses_fixture0000000000000001", "")
	if err == nil {
		t.Fatalf("a failed export answered %v and success", shape(entries))
	}
}

// TestReadEntries_PreMintedIDNeverRunsAnything covers the window every session
// passes through: until opencode reports its own id, Session.AgentSessionID is
// a UUID that names no session, and asking about it must cost nothing and read
// as "not started" rather than as a failure.
func TestReadEntries_PreMintedIDNeverRunsAnything(t *testing.T) {
	ran := false
	r := &TranscriptReader{export: func(_, _ string) ([]byte, error) {
		ran = true
		return nil, errors.New("should not have been called")
	}}
	for _, id := range []string{
		"",
		"0198f1b2-0000-7000-8000-000000000000", // the pre-minted UUID
		"ses_",
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

// TestReadEntries_AMalformedIDIsLoud is the other half of the split.
//
// An id carrying opencode's prefix but not its shape is not the pre-mint
// window — something went wrong — so it must not share that window's quiet
// answer. Empty-and-successful is the one reply this whole feature exists to
// stop a caller from reading as "the child said nothing".
func TestReadEntries_AMalformedIDIsLoud(t *testing.T) {
	ran := false
	r := &TranscriptReader{export: func(_, _ string) ([]byte, error) {
		ran = true
		return []byte(`{"messages":[]}`), nil
	}}
	for _, id := range []string{"ses_with.dot", "ses_with/slash", "ses_with space"} {
		entries, err := r.ReadEntries("", id, "")
		if err == nil {
			t.Errorf("ReadEntries(%q) = (%v, nil), want an error", id, shape(entries))
		}
	}
	if ran {
		t.Error("a malformed id was handed to a subprocess anyway")
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
	_, err := runExport("", "ses_fixture0000000000000001")
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

// TestReadEntries_WhatIsDroppedDoesNotMoveTheClock pins what content the
// reader refuses is allowed to affect: nothing.
//
// Injected content carries real timestamps, and the clock only moves forward,
// so letting any of it advance would stamp the next genuine entry with a moment
// from something the reader had just decided nobody said. The fixture puts both
// shapes 7 seconds ahead of the user turn that follows them — an injected part
// inside a message that does emit, and a whole message that emits nothing at
// all — because that is the only arrangement where the behaviours differ.
func TestReadEntries_WhatIsDroppedDoesNotMoveTheClock(t *testing.T) {
	entries := entriesFrom(t, fixtureDropped, "")
	eq(t, "dropped content", shape(entries), []string{"assistant:text", "user:text"})
	if got, want := entries[1].Timestamp, "2026-08-06T07:06:42.000Z"; got != want {
		t.Errorf("user entry = %q, want its own message's %q — dropped content dragged the clock", got, want)
	}
}

// TestNewExportCmd_TearsDownTheWholeGroup pins the teardown, which nothing
// else can see.
//
// opencode starts more processes than the one jind-ai names, and the standard
// library's cancellation reaches only the leader — so without this the 30s
// timeout would return while the work it was meant to stop carried on. No
// parse test touches it, and a real export never reaches the timeout.
func TestNewExportCmd_TearsDownTheWholeGroup(t *testing.T) {
	cmd := newExportCmd(context.Background(), "/bin/true", t.TempDir(), "ses_x", io.Discard, io.Discard)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("the child is not in its own process group, so a signal cannot reach what it started")
	}
	if cmd.Cancel == nil {
		t.Error("cancellation falls back to killing the leader only")
	}
}

// TestNewExportCmd_RunsSomewhereThatExists covers a failure that takes out
// every opencode read at once rather than one.
//
// A command inherits the daemon's working directory when Dir is empty, and the
// daemon's is wherever it was first auto-started and never changes — while
// jind-ai creates and removes worktrees under exactly that kind of path. A
// process launched from a removed directory does not start at all, so one
// deleted worktree would end opencode transcript reads for good, with an error
// that names nothing to suggest why.
func TestNewExportCmd_RunsSomewhereThatExists(t *testing.T) {
	sessionDir := t.TempDir()
	cmd := newExportCmd(context.Background(), "/bin/true", sessionDir, "ses_x", io.Discard, io.Discard)
	if cmd.Dir != sessionDir {
		t.Errorf("Dir = %q, want the session's own %q", cmd.Dir, sessionDir)
	}

	// A worktree can be removed while the session record outlives it, so the
	// session's directory is not a guarantee either.
	gone := filepath.Join(t.TempDir(), "removed")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, dir := range []string{"", gone} {
		cmd := newExportCmd(context.Background(), "/bin/true", dir, "ses_x", io.Discard, io.Discard)
		if cmd.Dir == "" || cmd.Dir == dir {
			t.Errorf("Dir = %q for input %q, want a directory that exists", cmd.Dir, dir)
		}
		if fi, err := os.Stat(cmd.Dir); err != nil || !fi.IsDir() {
			t.Errorf("Dir = %q is not a usable directory: %v", cmd.Dir, err)
		}
	}
}

// TestRunExport_StartsEvenWhenTheProcessCwdIsGone is the same thing end to
// end: the *caller's* working directory is deleted, which is the state a
// long-lived daemon actually gets into.
func TestRunExport_StartsEvenWhenTheProcessCwdIsGone(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '{\"messages\":[]}'\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(stub))

	doomed := filepath.Join(t.TempDir(), "doomed")
	if err := os.Mkdir(doomed, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// t.Chdir restores on its own and refuses to run under t.Parallel, which
	// hand-rolled Getwd/Cleanup does neither of.
	t.Chdir(doomed)
	if err := os.Remove(doomed); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := runExport(t.TempDir(), "ses_fixture0000000000000001"); err != nil {
		t.Errorf("export failed because the daemon's own cwd was gone: %v", err)
	}
}

// TestReadEntries_OddMetadataDoesNotLoseTheSession is about blast radius.
//
// opencode types tool metadata as an untyped record, so a value this reader did
// not expect is not a malformed document — but decoding it into a typed field
// makes encoding/json fail for the whole thing, and this reader reports a
// document that will not parse as a truncated read. One tool writing
// `"exit": true` would then lose the entire conversation and blame the wrong
// cause. Every row below is a shape that used to do exactly that.
func TestReadEntries_OddMetadataDoesNotLoseTheSession(t *testing.T) {
	entries := entriesFrom(t, fixtureOdd, "")
	results := map[string]transcript.Block{}
	for _, e := range entries {
		for _, b := range e.Blocks {
			if b.Kind == "tool_result" {
				results[b.ToolUseID] = b
			}
		}
	}
	if len(results) != 7 {
		t.Fatalf("got %d tool results, want all 7 — an odd value took the session with it", len(results))
	}

	for _, tc := range []struct {
		callID  string
		isError bool
		output  string
		why     string
	}{
		{"call_bool", false, "x", "a boolean exit is not a number, so jind-ai cannot tell"},
		{"call_word", false, "x", "neither is a word"},
		{"call_object", false, "x", "nor an object"},
		{"call_float", true, "x", "1.0 is a non-zero exit however it was written"},
		{"call_zerof", false, "x", "0.0 is still success"},
		{"call_numout", false, "42", "a non-string output is kept as its own JSON, not dropped"},
		{"call_nullout", false, "", "a null output is nothing, and says so"},
	} {
		b := results[tc.callID]
		if b.IsError != tc.isError {
			t.Errorf("%s: IsError = %v, want %v — %s", tc.callID, b.IsError, tc.isError, tc.why)
		}
		if b.Output != tc.output {
			t.Errorf("%s: Output = %q, want %q — %s", tc.callID, b.Output, tc.output, tc.why)
		}
	}
}

// TestReadEntries_ANullDocumentIsLoud closes the one gap in the parse guard.
//
// Every other non-object — an array, a bare string, a number, empty input —
// fails to decode and is reported. `null` is a legal value for a struct
// pointer, so it decoded cleanly into a document with no messages and came
// back as an empty conversation and a success: the precise answer this reader
// exists to stop a caller from mistaking for "the child said nothing".
func TestReadEntries_ANullDocumentIsLoud(t *testing.T) {
	for _, body := range []string{"null", " null\n", "[]", `"x"`, "123", ""} {
		r := &TranscriptReader{export: func(_, _ string) ([]byte, error) { return []byte(body), nil }}
		entries, err := r.ReadEntries("", "ses_fixture0000000000000001", "")
		if err == nil {
			t.Errorf("export output %q = (%v, nil), want an error", body, shape(entries))
		}
	}
	// A real but empty session still reads as empty and successful.
	r := &TranscriptReader{export: func(_, _ string) ([]byte, error) {
		return []byte(`{"info":{"id":"x"},"messages":[]}`), nil
	}}
	entries, err := r.ReadEntries("", "ses_fixture0000000000000001", "")
	if err != nil || len(entries) != 0 {
		t.Errorf("an empty session = (%v, %v), want no entries and no error", shape(entries), err)
	}
}

// TestReadEntries_AnUnreadableFailureDoesNotMoveTheClock is the message-level
// twin of the dropped-part rule.
//
// An error carrying neither a name nor a message produces no entry, so it must
// not advance the clock either — otherwise the next real entry inherits a
// moment from a turn nobody can read.
func TestReadEntries_AnUnreadableFailureDoesNotMoveTheClock(t *testing.T) {
	const doc = `{"messages":[
	  {"info":{"role":"assistant","time":{"created":1786000009000},
	           "error":{"name":"","data":{"message":""}}},"parts":[]},
	  {"info":{"role":"user","time":{"created":1786000002000}},
	   "parts":[{"type":"text","text":"next"}]}
	]}`
	r := &TranscriptReader{export: func(_, _ string) ([]byte, error) { return []byte(doc), nil }}
	entries, err := r.ReadEntries("", "ses_fixture0000000000000001", "")
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	eq(t, "unreadable failure", shape(entries), []string{"user:text"})
	if got, want := entries[0].Timestamp, "2026-08-06T07:06:42.000Z"; got != want {
		t.Errorf("user entry = %q, want its own message's %q — an unreadable failure dragged the clock", got, want)
	}
}

// A reader that means to stay off the polling path says so by not implementing
// the interface; this asserts the opposite direction is not true by accident.
var _ = func() any {
	if _, ok := any(&TranscriptReader{}).(agent.PollableTranscriptSource); ok {
		panic("the opencode reader declared itself cheap enough to poll; it runs a subprocess")
	}
	return nil
}()
