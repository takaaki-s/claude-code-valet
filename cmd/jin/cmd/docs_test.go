package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/takaaki-s/jind-ai/internal/agentdocs"
	"github.com/takaaki-s/jind-ai/internal/exitcode"
)

// runDocsCmd invokes the docs subcommand tree with captured I/O. The --json
// global is reset on both sides because it is a package-level variable bound
// to a persistent flag: leaving it set would silently change the shape of a
// later test's output.
func runDocsCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	jsonOutput = false
	t.Cleanup(func() {
		jsonOutput = false
		_ = rootCmd.PersistentFlags().Set("json", "false")
	})

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"docs"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

func TestDocsListNamesEveryDoc(t *testing.T) {
	out, err := runDocsCmd(t, "list")
	if err != nil {
		t.Fatalf("docs list: %v", err)
	}
	for _, d := range agentdocs.List() {
		if !strings.Contains(out, d.Name) {
			t.Errorf("listing omits %q", d.Name)
		}
		if !strings.Contains(out, d.Description) {
			t.Errorf("listing omits the description of %q", d.Name)
		}
	}
	if !strings.Contains(out, "jin docs show") {
		t.Error("listing does not tell the reader how to read a doc")
	}
}

// TestDocsBareIsList pins the shortcut: an agent typing `jin docs` gets the
// catalogue instead of a help screen that would cost it another round trip.
func TestDocsBareIsList(t *testing.T) {
	bare, err := runDocsCmd(t)
	if err != nil {
		t.Fatalf("docs: %v", err)
	}
	list, err := runDocsCmd(t, "list")
	if err != nil {
		t.Fatalf("docs list: %v", err)
	}
	if bare != list {
		t.Errorf("`jin docs` and `jin docs list` differ:\n%q\nvs\n%q", bare, list)
	}
}

func TestDocsListJSON(t *testing.T) {
	out, err := runDocsCmd(t, "list", "--json")
	if err != nil {
		t.Fatalf("docs list --json: %v", err)
	}

	var payload struct {
		Docs []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Body        string `json:"body"`
		} `json:"docs"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not valid JSON (%v): %s", err, out)
	}

	if len(payload.Docs) != len(agentdocs.List()) {
		t.Fatalf("got %d docs, want %d", len(payload.Docs), len(agentdocs.List()))
	}
	for _, d := range payload.Docs {
		if d.Name == "" || d.Title == "" || d.Description == "" {
			t.Errorf("incomplete entry: %+v", d)
		}
		// Bodies belong to `docs show`. Shipping them in the listing would
		// flood a caller's context with material it did not ask for.
		if d.Body != "" {
			t.Errorf("%s: listing carries a body", d.Name)
		}
	}

	// A wrapper object, not a bare array — so the payload can gain fields
	// later without breaking every caller's jq expression.
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("payload is not an object: %s", out)
	}
}

func TestDocsShowPrintsBodyWithoutFrontmatter(t *testing.T) {
	docs := agentdocs.List()
	if len(docs) == 0 {
		t.Fatal("no docs to test with")
	}
	name := docs[0].Name

	out, err := runDocsCmd(t, "show", name)
	if err != nil {
		t.Fatalf("docs show %s: %v", name, err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("empty output")
	}
	if strings.HasPrefix(strings.TrimSpace(out), "---") {
		t.Error("output starts with the frontmatter fence")
	}
	if strings.Contains(out, "description:") {
		t.Error("output leaks a frontmatter key")
	}
	if !strings.Contains(out, docs[0].Title) {
		t.Errorf("body does not contain the doc's title heading %q", docs[0].Title)
	}
}

func TestDocsShowUnknownName(t *testing.T) {
	_, err := runDocsCmd(t, "show", "no-such-doc")
	if err == nil {
		t.Fatal("expected an error for an unknown doc name")
	}

	var exitErr *exitcode.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error is not an ExitError: %T", err)
	}
	if exitErr.Code != exitcode.GeneralError {
		t.Errorf("exit code = %d, want %d", exitErr.Code, exitcode.GeneralError)
	}

	// The failure has to be self-correcting: the caller should not need a
	// second command to learn what it could have asked for.
	for _, d := range agentdocs.List() {
		if !strings.Contains(err.Error(), d.Name) {
			t.Errorf("error does not offer %q as an alternative: %v", d.Name, err)
		}
	}

}

// TestDocsNeedsNoDaemon is the property the whole injection design leans on:
// the context injected into every child session points at `jin docs`, so if
// these commands required the daemon, a daemon failure would take the
// documentation explaining how to fix it down with it.
//
// Pointing the socket at a path with nothing listening makes a daemon call
// fail; the commands must still succeed.
func TestDocsNeedsNoDaemon(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	if _, err := runDocsCmd(t, "list"); err != nil {
		t.Errorf("docs list failed without a daemon: %v", err)
	}
	if _, err := runDocsCmd(t, "show", agentdocs.List()[0].Name); err != nil {
		t.Errorf("docs show failed without a daemon: %v", err)
	}
}
