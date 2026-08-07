package transcript

import (
	"os"
	"reflect"
	"testing"
)

func textEntry(kind, ts, text string) Entry {
	return Entry{Type: kind, Timestamp: ts, Blocks: []Block{{Kind: "text", Text: text}}}
}

// injected and sidechain set one flag on an entry, so a table row can say which
// one differs from the row above it instead of hiding it inside a closure.
func injected(e Entry) Entry  { e.Injected = true; return e }
func sidechain(e Entry) Entry { e.Sidechain = true; return e }

func TestLastMessagesFrom(t *testing.T) {
	tests := []struct {
		name              string
		entries           []Entry
		wantUser, wantAst string
	}{
		{
			name: "picks the last of each role",
			// Two of each: with one apiece, a scan that kept the first match
			// would be indistinguishable from one that kept the last.
			entries: []Entry{
				textEntry("user", "1", "first"), textEntry("assistant", "2", "reply"),
				textEntry("user", "3", "second"), textEntry("assistant", "4", "final"),
			},
			wantUser: "second", wantAst: "final",
		},
		{
			name: "collapses whitespace so the preview is one line",
			// The previews render inside a single row; a message that arrived
			// with newlines and tabs has to come back flattened, or the row
			// breaks apart on screen.
			entries:  []Entry{textEntry("user", "1", "line one\nline two\t\tindented")},
			wantUser: "line one line two indented",
		},
		{
			name: "an injected entry is not what the operator said",
			// The measured failure: without this, the body of an invoked skill
			// becomes the session's last user message on 55 of 231 real
			// transcripts — and those bodies carry absolute paths.
			entries: []Entry{
				textEntry("user", "1", "the real prompt"),
				injected(textEntry("user", "2", "injected context the agent wrote")),
			},
			wantUser: "the real prompt",
		},
		{
			name: "a subagent's turn is not the main conversation",
			entries: []Entry{
				textEntry("assistant", "1", "main thread reply"),
				sidechain(textEntry("assistant", "2", "subagent reply")),
			},
			wantAst: "main thread reply",
		},
		{
			name: "tool results ride inside user entries and are not speech",
			entries: []Entry{
				textEntry("user", "1", "run it"),
				{Type: "user", Timestamp: "2", Blocks: []Block{{Kind: "tool_result", Output: "exit 0"}}},
			},
			wantUser: "run it",
		},
		{
			name: "thinking is not speech either",
			entries: []Entry{
				textEntry("assistant", "1", "here is the answer"),
				{Type: "assistant", Timestamp: "2", Blocks: []Block{{Kind: "thinking", Text: "let me reconsider"}}},
			},
			wantAst: "here is the answer",
		},
		{
			// The previous case only proves a thinking-only entry loses to a
			// text one. Here thinking rides alongside the text in the entry
			// that wins, which is the shape that would leak the agent's
			// reasoning into the row if the block kind stopped being checked.
			name: "thinking inside the winning entry stays out of it",
			entries: []Entry{{Type: "assistant", Timestamp: "1", Blocks: []Block{
				{Kind: "thinking", Text: "weighing the options"},
				{Kind: "text", Text: "here is the answer"},
			}}},
			wantAst: "here is the answer",
		},
		{
			name: "entries that are neither role are skipped",
			entries: []Entry{
				textEntry("user", "1", "prompt"),
				textEntry("system", "2", "turn failed"),
			},
			wantUser: "prompt",
		},
		{
			name:    "blank text does not displace a real message",
			entries: []Entry{textEntry("user", "1", "prompt"), textEntry("user", "2", "   ")},
			// A whitespace-only entry contributes nothing, so the earlier real
			// one has to survive — dropping it would blank the row.
			wantUser: "prompt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LastMessagesFrom(tt.entries)
			if got == nil {
				t.Fatal("got nil, want messages")
			}
			gotUser, gotAst := "", ""
			if got.User != nil {
				gotUser = got.User.Content
			}
			if got.Assistant != nil {
				gotAst = got.Assistant.Content
			}
			if gotUser != tt.wantUser {
				t.Errorf("user = %q, want %q", gotUser, tt.wantUser)
			}
			if gotAst != tt.wantAst {
				t.Errorf("assistant = %q, want %q", gotAst, tt.wantAst)
			}
		})
	}
}

// TestLastMessagesFrom_CarriesTheTimestamp pins parity with the file-reading
// path this one has to be interchangeable with: both produce a Message, and a
// Message without its timestamp is a different value even when nothing on the
// current screen reads it.
func TestLastMessagesFrom_CarriesTheTimestamp(t *testing.T) {
	got := LastMessagesFrom([]Entry{
		textEntry("user", "2026-08-07T01:00:00.000Z", "ask"),
		textEntry("assistant", "2026-08-07T01:00:09.000Z", "answer"),
	})
	if got == nil {
		t.Fatal("got nil, want messages")
	}
	if got.User.Timestamp != "2026-08-07T01:00:00.000Z" {
		t.Errorf("user timestamp = %q", got.User.Timestamp)
	}
	if got.Assistant.Timestamp != "2026-08-07T01:00:09.000Z" {
		t.Errorf("assistant timestamp = %q", got.Assistant.Timestamp)
	}
}

func TestLastMessagesFrom_NothingSaidYet(t *testing.T) {
	// nil rather than an empty struct: the caller leaves the row alone instead
	// of overwriting it with two blanks.
	for _, tt := range []struct {
		name    string
		entries []Entry
	}{
		{"no entries", nil},
		{"only injected", []Entry{injected(textEntry("user", "1", "x"))}},
		{"only tool traffic", []Entry{{Type: "user", Blocks: []Block{{Kind: "tool_result"}}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := LastMessagesFrom(tt.entries); got != nil {
				t.Errorf("got %+v, want nil", got)
			}
		})
	}
}

// TestReadEntries_MarksProvenance pins that the Claude Code reader labels what
// a view has to exclude — and, just as importantly, that it still returns
// those entries. `session result` has always shown every line, and narrowing
// it here would change what every existing session reports.
func TestReadEntries_MarksProvenance(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/t.jsonl"
	body := `{"type":"user","timestamp":"1","promptSource":"typed","message":{"role":"user","content":[{"type":"text","text":"typed prompt"}]}}` + "\n" +
		`{"type":"user","timestamp":"2","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"meta injection"}]}}` + "\n" +
		`{"type":"user","timestamp":"3","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"meta alongside a result"},{"type":"tool_result","content":"exit 0"}]}}` + "\n" +
		`{"type":"user","timestamp":"4","message":{"role":"user","content":[{"type":"text","text":"unstamped text-only"}]}}` + "\n" +
		`{"type":"user","timestamp":"5","message":{"role":"user","content":[{"type":"tool_result","content":"exit 0"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"6","message":{"role":"assistant","content":[{"type":"thinking","thinking":"internal reasoning"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"7","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"8","message":{"role":"assistant","content":[{"type":"text","text":"reply"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readEntries(path, "")
	if err != nil {
		t.Fatalf("readEntries: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("got %d entries, want all 8 kept", len(got))
	}
	type flags struct{ injected, sidechain bool }
	var have []flags
	for _, e := range got {
		have = append(have, flags{e.Injected, e.Sidechain})
	}
	want := []flags{
		{false, false}, // a stamped prompt is the operator's
		{true, false},  // isMeta
		{true, false},  // isMeta alone decides it: the mixed content means the
		//                 text-only rule does not fire on this one
		{true, false},  // no promptSource + text-only is the agent in the user's voice
		{false, false}, // a tool result carries no speech to attribute to anyone,
		//                 so neither rule applies and nothing is claimed
		{false, false}, // thinking is the agent's own reasoning, and it carries
		//                 text — so this is the entry that proves the second
		//                 rule reads text blocks rather than any block with text
		{false, true},  // sidechain
		{false, false}, // assistant text is never an injection
	}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("provenance = %+v, want %+v", have, want)
	}
}
