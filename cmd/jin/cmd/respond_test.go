package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// runRespondArgs drives the real command through the root, so flag parsing and
// the usage-error path are exercised rather than reimplemented. The socket is
// pointed at a path that cannot exist, which is what makes the negative
// control below meaningful: anything that clears validation fails on the
// daemon instead, and the two are told apart by the message.
func runRespondArgs(t *testing.T, args ...string) error {
	t.Helper()
	t.Setenv("JIN_SOCKET", "/nonexistent/jin-respond-test.sock")

	// Cobra keeps flag state on the command across Execute calls, so a case
	// that passed --option would leave it Changed for the next one and turn a
	// lone --text into "both were given". Reset BEFORE running rather than in
	// a Cleanup: a table that loops without subtests runs every case before
	// any Cleanup fires, which is exactly where that leak bites.
	resetRespondFlags()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"session"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		resetRespondFlags()
	})
	return rootCmd.Execute()
}

func resetRespondFlags() {
	_ = respondCmd.Flags().Set("option", "0")
	_ = respondCmd.Flags().Set("text", "")
	respondCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
}

func TestRenderRespondResultJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := renderRespondResultJSON(&buf, respondResult{
		Success: true, Session: "fix-login", Kind: "tool-permission",
	}); err != nil {
		t.Fatalf("render returned err=%v, want nil", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, buf.String())
	}
	for k, want := range map[string]any{
		"success": true,
		"session": "fix-login",
		"kind":    "tool-permission",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
}

// TestRenderRespondResultJSONOmitsEmptyKind covers a daemon too old to report
// the kind. The answer still landed, so the field goes missing rather than
// carrying an empty string that a consumer might read as a kind.
func TestRenderRespondResultJSONOmitsEmptyKind(t *testing.T) {
	var buf bytes.Buffer
	if err := renderRespondResultJSON(&buf, respondResult{Success: true, Session: "s"}); err != nil {
		t.Fatalf("render returned err=%v, want nil", err)
	}
	if strings.Contains(buf.String(), "kind") {
		t.Errorf("output carries an empty kind: %s", buf.String())
	}
}

// TestRespondFlagValidation drives the command's own argument checking. It
// runs against a socket path that cannot exist, so a case that wrongly passed
// validation would fail on the daemon instead — and the assertion on the
// message is what tells the two apart.
func TestRespondFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"neither", []string{"respond", "sess"}, "an answer is required"},
		{"both", []string{"respond", "sess", "--option", "1", "--text", "hi"}, "not both"},
		{"option zero", []string{"respond", "sess", "--option", "0"}, "between 1 and 9"},
		{"option ten", []string{"respond", "sess", "--option", "10"}, "between 1 and 9"},
		{"option negative", []string{"respond", "sess", "--option", "-1"}, "between 1 and 9"},
		{"blank text", []string{"respond", "sess", "--text", "   "}, "no verifiable content"},
		{"box-drawing text", []string{"respond", "sess", "--text", "────"}, "no verifiable content"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runRespondArgs(t, tc.args...)
			if err == nil {
				t.Fatal("command succeeded, want a validation error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestRespondValidArgsReachTheDaemon is the negative control for the table
// above: a command that rejected everything would satisfy every case there.
// These get past validation and fail on the unreachable socket instead.
func TestRespondValidArgsReachTheDaemon(t *testing.T) {
	for _, args := range [][]string{
		{"respond", "sess", "--option", "1"},
		{"respond", "sess", "--option", "9"},
		{"respond", "sess", "--text", "use bun"},
	} {
		err := runRespondArgs(t, args...)
		if err == nil {
			t.Fatalf("%v succeeded with no daemon running", args)
		}
		for _, rejected := range []string{"an answer is required", "not both", "between 1 and 9", "no verifiable content"} {
			if strings.Contains(err.Error(), rejected) {
				t.Errorf("%v was rejected by validation (%q), want it to reach the daemon", args, err)
			}
		}
	}
}

func TestDisplayBlockKind(t *testing.T) {
	tests := map[string]string{
		"tool-permission": "the permission",
		"question":        "the question",
		"":                "the",
		// An unknown kind is echoed rather than guessed at, so a newer daemon
		// talking to an older CLI still prints something true.
		"question-multi": "question-multi",
	}
	for in, want := range tests {
		if got := displayBlockKind(in); got != want {
			t.Errorf("displayBlockKind(%q) = %q, want %q", in, got, want)
		}
	}
}
