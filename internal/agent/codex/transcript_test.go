package codex

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/transcript"
)

const (
	fixtureEntries    = "testdata/rollout-entries.jsonl"
	fixtureGrouping   = "testdata/rollout-grouping.jsonl"
	fixtureOutShapes  = "testdata/rollout-tool-output-shapes.jsonl"
	fixtureToolErrors = "testdata/rollout-tool-errors.jsonl"
	fixtureTaskError  = "testdata/rollout-task-error.jsonl"
)

// entriesFrom parses a fixture through the same code path ReadEntries uses,
// skipping only the on-disk lookup.
func entriesFrom(t *testing.T, path, since string) []transcript.Entry {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	got, err := readEntriesFrom(bytes.NewReader(body), since)
	if err != nil {
		t.Fatalf("readEntriesFrom(%s): %v", path, err)
	}
	return got
}

// shape renders entries as "role:kind,kind" lines so a test can assert the
// grouping and ordering in one comparison instead of walking indexes.
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

func TestReadEntries_MapsEachPayloadType(t *testing.T) {
	got := entriesFrom(t, fixtureEntries, "")

	want := []transcript.Entry{
		{
			Type: "user", Timestamp: "2026-07-11T07:00:03.000Z",
			// One item, three blocks: an injection Codex added and two lines
			// the operator wrote. The injection is dropped block by block —
			// filtering whole items would lose the prompt with it — and what
			// remains joins with a newline, because the blocks are separate
			// lines of one message rather than slices of one stream the way a
			// tool output's are.
			Blocks: []transcript.Block{{Kind: "text", Text: "print the date\nand say ok"}},
		},
		{
			Type: "assistant", Timestamp: "2026-07-11T07:00:07.000Z",
			Blocks: []transcript.Block{
				{Kind: "text", Text: "I'll run it."},
				{
					Kind: "tool_use", ToolName: "exec", ToolUseID: "call_1",
					Input: []byte(`"tools.exec_command({cmd: 'date'})"`),
				},
			},
		},
		{
			Type: "user", Timestamp: "2026-07-11T07:00:08.000Z",
			Blocks: []transcript.Block{{
				Kind: "tool_result", ToolUseID: "call_1",
				Output: "Script completed\nOutput:\n2026-07-11\n",
			}},
		},
		{
			Type: "assistant", Timestamp: "2026-07-11T07:00:09.000Z",
			Blocks: []transcript.Block{{
				Kind: "tool_use", ToolName: "wait", ToolUseID: "call_2",
				Input: []byte(`"{\"seconds\":5}"`),
			}},
		},
		{
			Type: "user", Timestamp: "2026-07-11T07:00:10.000Z",
			Blocks: []transcript.Block{{
				Kind: "tool_result", ToolUseID: "call_2", Output: `{"waited":true}`,
			}},
		},
		{
			Type: "assistant", Timestamp: "2026-07-11T07:00:14.000Z",
			Blocks: []transcript.Block{
				{Kind: "thinking", Text: "the command succeeded"},
				{Kind: "text", Text: "The date is 2026-07-11."},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries mismatch\ngot  %+v\nwant %+v", got, want)
	}
}

func TestReadEntries_DropsInjectedAndInterAgentItems(t *testing.T) {
	for _, e := range entriesFrom(t, fixtureEntries, "") {
		for _, b := range e.Blocks {
			switch {
			case strings.Contains(b.Text, "REDACTED-DEVELOPER-INSTRUCTIONS"):
				t.Errorf("role=developer item leaked into the conversation: %q", b.Text)
			case strings.Contains(b.Text, "<environment_context>"):
				t.Errorf("injected role=user item leaked into the conversation: %q", b.Text)
			case strings.Contains(b.Text, "Message Type: FINAL_ANSWER"):
				t.Errorf("response_item/agent_message leaked into the conversation: %q", b.Text)
			}
		}
	}
}

func TestReadEntries_IgnoresEventMsgContent(t *testing.T) {
	// event_msg repeats what response_item already carries. Reading it for
	// content would return each utterance twice.
	got := entriesFrom(t, fixtureEntries, "")
	for _, e := range got {
		for _, b := range e.Blocks {
			if strings.Contains(b.Text, "duplicate of the assistant item above") {
				t.Fatalf("event_msg/agent_message was read as content: %q", b.Text)
			}
		}
	}
	// The user's prompt appears on both streams; it must appear once.
	n := 0
	for _, e := range got {
		for _, b := range e.Blocks {
			if strings.Contains(b.Text, "print the date") {
				n++
			}
		}
	}
	if n != 1 {
		t.Fatalf("prompt appears %d times, want exactly 1", n)
	}
}

func TestReadEntries_GroupsByRole(t *testing.T) {
	got := entriesFrom(t, fixtureGrouping, "")

	want := []string{
		"user:text",
		"assistant:thinking,text,tool_use",
		"user:tool_result",
		"assistant:text",
	}
	if !reflect.DeepEqual(shape(got), want) {
		t.Fatalf("grouping mismatch\ngot  %v\nwant %v", shape(got), want)
	}
	// The fixture holds 6 response_items. One entry per item would be 6
	// single-block entries, which is what makes `--last N` mean a different
	// thing for Codex than for Claude Code.
	if len(got) != 4 {
		t.Fatalf("got %d entries from 6 response_items, want 4", len(got))
	}
}

func TestReadEntries_EntryTimestampIsTheGroupsLastLine(t *testing.T) {
	got := entriesFrom(t, fixtureGrouping, "")
	// The assistant group spans 08:00:02 (reasoning) .. 08:00:04 (tool call).
	if want := "2026-07-11T08:00:04.000Z"; got[1].Timestamp != want {
		t.Fatalf("assistant entry timestamp = %q, want %q (the group's last line)", got[1].Timestamp, want)
	}
}

func TestReadEntries_SkipsEmptyReasoning(t *testing.T) {
	got := entriesFrom(t, fixtureEntries, "")
	thinking := 0
	for _, e := range got {
		for _, b := range e.Blocks {
			if b.Kind != "thinking" {
				continue
			}
			thinking++
			if b.Text == "" {
				t.Error("emitted a thinking block with no text")
			}
		}
	}
	// The fixture has two reasoning items; only the one with a summary counts.
	if thinking != 1 {
		t.Fatalf("got %d thinking blocks, want 1", thinking)
	}
}

func TestReadEntries_SinceExcludesEqualTimestamp(t *testing.T) {
	all := entriesFrom(t, fixtureEntries, "")
	first := all[0]
	got := entriesFrom(t, fixtureEntries, first.Timestamp)
	if len(got) == 0 {
		t.Fatal("since dropped everything")
	}
	if got[0].Timestamp == first.Timestamp {
		t.Fatalf("entry at the boundary was returned again: %q", first.Timestamp)
	}
}

func TestReadEntries_SinceResumesWithoutOverlapOrLoss(t *testing.T) {
	// The property `--since` exists for: handing back the last timestamp seen
	// must return exactly the entries after it. This is what pins the choice
	// to stamp an entry with its last line rather than its first — stamping
	// the first line leaves the rest of a multi-line group above the bound,
	// and the caller receives a partial copy of an entry it already has.
	all := entriesFrom(t, fixtureEntries, "")
	for i := range all {
		got := entriesFrom(t, fixtureEntries, all[i].Timestamp)
		want := all[i+1:]
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("since=%s (entry %d)\ngot  %v\nwant %v",
				all[i].Timestamp, i, shape(got), shape(want))
		}
	}
}

// TestReadEntries_SinceDropsAnEntrySharingTheBoundaryTimestamp records a
// limitation rather than asserting a behaviour anyone wants.
//
// `--since` is a bare timestamp, and a timestamp is not a unique key. When the
// entry after the caller's cursor carries the same timestamp as the cursor
// itself, the exclusive comparison drops it and the caller never sees it.
//
// This is a property of the shared protocol, not of this reader. Measured on
// real data, adjacent entries collide on 1 of 112 pairs across 14 rollouts,
// and on 42 of 51,681 pairs across 242 Claude Code transcripts — where the
// same comparison has always been in use. Diverging here would make `--since`
// mean one thing for Codex and another for Claude Code, which is the failure
// grouping was introduced to avoid; the fix belongs in the cursor, which is a
// protocol change. Until then this is written down so it reads as known rather
// than as an oversight.
func TestReadEntries_SinceDropsAnEntrySharingTheBoundaryTimestamp(t *testing.T) {
	const ts = "2026-07-11T13:00:00.000Z"
	body := `{"timestamp":"` + ts + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"prompt"}]}}` + "\n" +
		`{"timestamp":"` + ts + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"text":"reply in the same millisecond"}]}}` + "\n"

	all, err := readEntriesFrom(strings.NewReader(body), "")
	if err != nil {
		t.Fatalf("readEntriesFrom: %v", err)
	}
	if !reflect.DeepEqual(shape(all), []string{"user:text", "assistant:text"}) {
		t.Fatalf("full read = %v, want both entries", shape(all))
	}
	got, err := readEntriesFrom(strings.NewReader(body), all[0].Timestamp)
	if err != nil {
		t.Fatalf("readEntriesFrom: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v; this test exists because the reply is lost here — if it now "+
			"survives, the cursor was fixed and this test should assert that instead", shape(got))
	}
}

func TestReadEntries_ToolUseCarriesCallIDAndStringInput(t *testing.T) {
	got := entriesFrom(t, fixtureEntries, "")

	var uses []transcript.Block
	for _, e := range got {
		for _, b := range e.Blocks {
			if b.Kind == "tool_use" {
				uses = append(uses, b)
			}
		}
	}
	if len(uses) != 2 {
		t.Fatalf("got %d tool_use blocks, want 2", len(uses))
	}
	// call_id, not id: the ctc_/fc_ id exists only on the call side, so a
	// tool_result could never be paired back to it.
	for _, b := range uses {
		if !strings.HasPrefix(b.ToolUseID, "call_") {
			t.Errorf("ToolUseID = %q, want the call_id", b.ToolUseID)
		}
		// Codex tool input is a bare string; it must arrive as a JSON string,
		// not as an object jind-ai synthesised around it.
		if len(b.Input) == 0 || b.Input[0] != '"' {
			t.Errorf("Input = %s, want a JSON string", b.Input)
		}
	}
	// The pairing has to survive into the result blocks, which is what makes
	// `--tool` filtering possible at all.
	var results []transcript.Block
	for _, e := range got {
		for _, b := range e.Blocks {
			if b.Kind == "tool_result" {
				results = append(results, b)
			}
		}
	}
	if len(results) != 2 {
		t.Fatalf("got %d tool_result blocks, want 2", len(results))
	}
	for i := range uses {
		if uses[i].ToolUseID != results[i].ToolUseID {
			t.Errorf("call %q has no matching result (%q)", uses[i].ToolUseID, results[i].ToolUseID)
		}
	}
}

func TestReadEntries_ToolOutputShapes(t *testing.T) {
	got := entriesFrom(t, fixtureOutShapes, "")

	outputs := map[string]string{}
	for _, e := range got {
		for _, b := range e.Blocks {
			outputs[b.ToolUseID] = b.Output
		}
	}
	want := map[string]string{
		// Array elements are consecutive slices of one stream: joining them
		// with a separator would insert a blank line the tool never printed.
		"call_list":   "Script completed\nOutput:\nfirst\nsecond\n",
		"call_string": "Script running with cell ID 22\nOutput:\n",
		"call_empty":  "",
		"call_absent": "",
		"call_null":   "",
		// An unrecognised shape keeps its content rather than vanishing.
		"call_object": `{"unexpected":"shape"}`,
	}
	if !reflect.DeepEqual(outputs, want) {
		t.Fatalf("outputs mismatch\ngot  %#v\nwant %#v", outputs, want)
	}
}

func TestReadEntries_IsErrorSignals(t *testing.T) {
	got := entriesFrom(t, fixtureToolErrors, "")

	isErr := map[string]bool{}
	for _, e := range got {
		for _, b := range e.Blocks {
			if b.Kind == "tool_result" {
				isErr[b.ToolUseID] = b.IsError
			}
		}
	}
	want := map[string]bool{
		// The documented gap, pinned deliberately: a command that ran and
		// exited non-zero is still reported as "Script completed" and its exit
		// code is nowhere in the rollout. A failed build reads as success, and
		// `--errors-only` will not surface it. If a future Codex build starts
		// recording the exit status, this expectation is the thing to revisit.
		"call_nonzero": false,
		"call_harness": true,
		"call_timeout": true,
		// The harness marker only means failure as the first thing in the
		// output. A build log that quotes the phrase is not a failed call, and
		// matching it anywhere in the text would flag a successful run — the
		// direction of error that puts an orchestrator onto a bug that is not
		// there.
		"call_mentions": false,
	}
	if !reflect.DeepEqual(isErr, want) {
		t.Fatalf("is_error mismatch\ngot  %#v\nwant %#v", isErr, want)
	}
}

func TestReadEntries_TaskCompleteErrorBecomesAnEntry(t *testing.T) {
	got := entriesFrom(t, fixtureTaskError, "")

	// The session holds a prompt and no assistant reply — the turn died on a
	// usage limit. Without the failure entry this is indistinguishable from a
	// turn still being worked on, and an orchestrator waits forever.
	want := []string{"user:text", "system:text"}
	if !reflect.DeepEqual(shape(got), want) {
		t.Fatalf("shape mismatch\ngot  %v\nwant %v", shape(got), want)
	}
	failure := got[1]
	if failure.Timestamp != "2026-07-11T11:00:03.000Z" {
		t.Errorf("failure timestamp = %q, want the task_complete line's", failure.Timestamp)
	}
	text := failure.Blocks[0].Text
	// Both fields Codex recorded survive: the classifier a caller matches on
	// and the sentence a human reads.
	if !strings.Contains(text, "usage_limit_exceeded") {
		t.Errorf("failure text %q drops codex_error_info", text)
	}
	if !strings.Contains(text, "You've hit your usage limit.") {
		t.Errorf("failure text %q drops the message", text)
	}
}

func TestReadEntries_TaskCompleteWithoutErrorEmitsNothing(t *testing.T) {
	// The successful task_complete in this fixture must not become an entry.
	for _, e := range entriesFrom(t, fixtureEntries, "") {
		if e.Type == "system" {
			t.Fatalf("a successful task_complete produced a system entry: %+v", e)
		}
	}
}

func TestReadEntries_BrokenLinesAndEmptyFile(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{name: "empty file", body: "", want: nil},
		{
			name: "meta only",
			body: `{"timestamp":"2026-07-11T12:00:00.000Z","type":"session_meta","payload":{"id":"x"}}` + "\n",
			want: nil,
		},
		{
			name: "unparsable line between good ones",
			body: `{"timestamp":"2026-07-11T12:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"one"}]}}` + "\n" +
				"{not json at all" + "\n" +
				"\n" +
				`{"timestamp":"2026-07-11T12:00:03.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"text":"two"}]}}` + "\n",
			want: []string{"user:text", "assistant:text"},
		},
		{
			name: "truncated final line",
			body: `{"timestamp":"2026-07-11T12:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"one"}]}}` + "\n" +
				`{"timestamp":"2026-07-11T12:00:02.000Z","type":"response_i`,
			want: []string{"user:text"},
		},
		{
			name: "unknown payload type",
			body: `{"timestamp":"2026-07-11T12:00:01.000Z","type":"response_item","payload":{"type":"some_future_item","content":[{"text":"?"}]}}` + "\n" +
				`{"timestamp":"2026-07-11T12:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"one"}]}}` + "\n",
			want: []string{"user:text"},
		},
		{
			name: "unknown line type",
			body: `{"timestamp":"2026-07-11T12:00:01.000Z","type":"some_future_line","payload":{"type":"message","role":"user","content":[{"text":"nope"}]}}` + "\n",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readEntriesFrom(strings.NewReader(tt.body), "")
			if err != nil {
				t.Fatalf("readEntriesFrom: %v", err)
			}
			if len(got) == 0 && tt.want == nil {
				return
			}
			if !reflect.DeepEqual(shape(got), tt.want) {
				t.Fatalf("got %v, want %v", shape(got), tt.want)
			}
		})
	}
}

func TestTranscriptReader_MissingRolloutIsEmptyNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	r := NewTranscriptReader(home)

	for _, id := range []string{"", "01900000-0000-7000-8000-00000000dead"} {
		got, err := r.ReadEntries("/tmp/example", id, "")
		if err != nil {
			t.Errorf("sessionID %q: err = %v, want nil (a session that has not written yet is not a failure)", id, err)
		}
		if len(got) != 0 {
			t.Errorf("sessionID %q: got %d entries, want 0", id, len(got))
		}
	}
}

func TestTranscriptReader_ReadsStagedRollout(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codexHome)
	const uuid = "01900000-0000-7000-8000-0000000000e2"
	stageRollout(t, filepath.Join(codexHome, "sessions"), "2026/07/11", uuid, fixtureGrouping)

	r := NewTranscriptReader(home)
	// workDir is deliberately wrong: Codex locates a rollout by UUID alone, so
	// the hint must not be able to hide a transcript that exists.
	got, err := r.ReadEntries("/nowhere/at/all", uuid, "")
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	want := []string{"user:text", "assistant:thinking,text,tool_use", "user:tool_result", "assistant:text"}
	if !reflect.DeepEqual(shape(got), want) {
		t.Fatalf("got %v, want %v", shape(got), want)
	}
}

// errAfterOneLine yields one complete line and then fails, standing in for a
// disk that goes away mid-read.
type errAfterOneLine struct {
	done bool
	err  error
}

func (r *errAfterOneLine) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	line := []byte(`{"timestamp":"2026-07-11T14:00:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"one"}]}}` + "\n")
	n := copy(p, line)
	return n, nil
}

// TestReadEntries_ScanFailuresAreLoud covers the two ways a read can go wrong
// after the file opened.
//
// Both must surface as errors rather than as a short conversation. A partial
// result returned with success is the failure this whole change exists to
// remove: the caller sees fewer entries and no reason to doubt them.
func TestReadEntries_ScanFailuresAreLoud(t *testing.T) {
	t.Run("io error mid-stream", func(t *testing.T) {
		want := errors.New("disk went away")
		got, err := readEntriesFrom(&errAfterOneLine{err: want}, "")
		if err == nil {
			t.Fatalf("err = nil with %d entries; a truncated read must not look like a short conversation", len(got))
		}
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want it to wrap %v", err, want)
		}
	})

	t.Run("line over the scanner limit", func(t *testing.T) {
		// scannerMaxLine is 4 MiB. A codex tool result holding a large build
		// log is the realistic way to reach it, so this is not purely
		// theoretical defence.
		body := `{"timestamp":"2026-07-11T14:00:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"one"}]}}` + "\n" +
			`{"x":"` + strings.Repeat("a", scannerMaxLine+1) + `"}` + "\n"
		got, err := readEntriesFrom(strings.NewReader(body), "")
		if err == nil {
			t.Fatalf("err = nil with %d entries; an oversized line must not be silently dropped", len(got))
		}
	})
}

// TestTranscriptReader_OpenFailureIsNotSilentlyEmpty separates "no rollout
// yet" from "the rollout could not be opened".
//
// Only the first is allowed to answer empty. Collapsing the second into it
// turns a permissions or I/O problem into "the child did nothing", which is
// the same wrong answer, arrived at from a different direction.
func TestTranscriptReader_OpenFailureIsNotSilentlyEmpty(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codexHome)
	const uuid = "01900000-0000-7000-8000-0000000000ff"

	dir := filepath.Join(codexHome, "sessions", "2026", "07", "11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	name := filepath.Join(dir, "rollout-2026-07-11T00-00-00-"+uuid+".jsonl")

	// A symlink pointing at itself: Locator.Find matches the name, and os.Open
	// fails with ELOOP — an error that is neither "does not exist" nor
	// permission-based, so it still reproduces for a test running as root.
	if err := os.Symlink(name, name); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	got, err := NewTranscriptReader(home).ReadEntries("", uuid, "")
	if err == nil {
		t.Fatalf("os.Open failure: err = nil with %d entries; an unopenable rollout must not answer empty", len(got))
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v; ELOOP must not be reported as a missing file", err)
	}

	// A directory in the rollout's place opens fine and fails on read, which is
	// the scanner's branch rather than os.Open's. Both have to be loud.
	if err := os.Remove(name); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got, err := NewTranscriptReader(home).ReadEntries("", uuid, ""); err == nil {
		t.Fatalf("read failure: err = nil with %d entries", len(got))
	}
}

// TestReadEntries_DefensiveGuardsAgainstFormatDrift covers three guards that
// the measured corpus cannot reach — every one of its 501 lines carries a
// timestamp, and every tool call carries an input.
//
// They are here because the format is undocumented and a guard nothing checks
// is a guard that gets deleted as dead weight. Each one names the damage it
// prevents if a future Codex build stops filling a field.
func TestReadEntries_DefensiveGuardsAgainstFormatDrift(t *testing.T) {
	t.Run("a line with no timestamp survives a since filter", func(t *testing.T) {
		// Comparing "" against any since is always "at or before", so dropping
		// the guard would make every unstamped line vanish the moment a caller
		// reads incrementally.
		body := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"no timestamp"}]}}` + "\n"
		got, err := readEntriesFrom(strings.NewReader(body), "2026-07-11T00:00:00.000Z")
		if err != nil {
			t.Fatalf("readEntriesFrom: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %v, want the unstamped entry kept", shape(got))
		}
	})

	t.Run("an unstamped line does not erase its group's timestamp", func(t *testing.T) {
		// The group's timestamp is the cursor a caller sends back. Letting a
		// later blank overwrite it would reset that cursor to "", and the next
		// incremental read would return the whole conversation again.
		body := `{"timestamp":"2026-07-11T15:00:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"one"}]}}` + "\n" +
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"two"}]}}` + "\n"
		got, err := readEntriesFrom(strings.NewReader(body), "")
		if err != nil {
			t.Fatalf("readEntriesFrom: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %v, want one grouped entry", shape(got))
		}
		if got[0].Timestamp != "2026-07-11T15:00:00.000Z" {
			t.Errorf("Timestamp = %q, want the last non-empty one", got[0].Timestamp)
		}
	})

	t.Run("a tool call with no input has no Input, not an empty JSON string", func(t *testing.T) {
		// Block.Input omits itself when nil. Encoding "" instead puts a
		// meaningless `""` in the JSON, which reads as "the agent called this
		// tool with an empty argument" rather than "no argument was recorded".
		body := `{"timestamp":"2026-07-11T15:00:00.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":""}}` + "\n"
		got, err := readEntriesFrom(strings.NewReader(body), "")
		if err != nil {
			t.Fatalf("readEntriesFrom: %v", err)
		}
		if len(got) != 1 || len(got[0].Blocks) != 1 {
			t.Fatalf("got %v, want one tool_use block", shape(got))
		}
		if got[0].Blocks[0].Input != nil {
			t.Errorf("Input = %s, want nil", got[0].Blocks[0].Input)
		}
	})
}
