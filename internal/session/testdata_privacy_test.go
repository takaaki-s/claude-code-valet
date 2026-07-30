package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The sendverify fixtures are captured from real agent panes, which is what
// makes them worth having — and also what makes them a disclosure risk. The
// first batch was committed as whole-screen captures and carried the
// developer's name, email address, subscription tier, organisation, home
// directory and configured model, none of which the test looks at.
//
// So the rule is not "redact what we noticed" but "keep only the input area".
// A fixture is the box the prompt lands in, nothing above it and nothing
// below. That is the region the wrap-seam assertions actually read, and it is
// the only region whose bytes have to be authentic.
//
// Reviewing pane dumps by eye does not scale to 26 files, so the rule is
// enforced here rather than in review.
var fixtureBanned = []struct {
	what    string
	pattern *regexp.Regexp
}{
	// Identifiers. A fixture has no reason to carry any of these at all,
	// which is why the address rule does not exempt example.com.
	{"an email address", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)},
	{"a personal greeting", regexp.MustCompile(`(?i)welcome back`)},
	{"an organisation line", regexp.MustCompile(`(?i)'s Organization`)},
	{"a home directory", regexp.MustCompile(`/(home|Users)/[A-Za-z0-9._-]+`)},
	{"a per-user scratch path", regexp.MustCompile(`/tmp/claude-[0-9]+`)},
	{"a credential", regexp.MustCompile(`(?i)(ssh-rsa |BEGIN [A-Z ]*PRIVATE KEY|sk-[A-Za-z0-9]{16,})`)},

	// Screen furniture outside the input area. None of it is read by the
	// assertions, and it discloses the account's plan, model and spend.
	{"a subscription tier", regexp.MustCompile(`(?i)Claude (Pro|Max|Team)\b`)},
	{"a model name", regexp.MustCompile(`(?i)(Opus \d|Sonnet \d|Haiku \d|DeepSeek|GPT-|OpenCode Zen)`)},
	{"a version banner", regexp.MustCompile(`(?i)Claude Code v[0-9]`)},
	{"a status line", regexp.MustCompile(`(?i)(auto mode on|⏱|📁|\$[0-9]+\.[0-9]{2})`)},
	{"release notes chrome", regexp.MustCompile(`(?i)(/release-notes|Tips for getting started|What's new)`)},
}

func TestSendVerifyFixtures_AreInputAreaOnly(t *testing.T) {
	dir := filepath.Join("testdata", "sendverify")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures found — this guard would pass vacuously")
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			body := string(b)
			for _, banned := range fixtureBanned {
				if m := banned.pattern.FindString(body); m != "" {
					t.Errorf("fixture contains %s (%q). Capture fixtures ship verbatim: "+
						"trim the capture to the input box before committing it, rather "+
						"than redacting the line.", banned.what, m)
				}
			}
			// A whole-screen capture is how the banned content got in last
			// time. An input area is a handful of rows; a screen is dozens.
			if n := strings.Count(body, "\n"); n > 25 {
				t.Errorf("fixture is %d lines — that is a screen, not an input area. "+
					"Trim it to the box the prompt lands in.", n)
			}
			// Redaction must not move columns: several fixtures exist
			// precisely because a needle straddles a wrap seam.
			if strings.ContainsRune(body, '\r') {
				t.Errorf("fixture contains a CR — capture-pane does not emit one, so " +
					"this is an editing artifact that shifts the seams")
			}
		})
	}
}
