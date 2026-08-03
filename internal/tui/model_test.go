package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/takaaki-s/jind-ai/internal/action"
	"github.com/takaaki-s/jind-ai/internal/config"
	"github.com/takaaki-s/jind-ai/internal/daemon"
	"github.com/takaaki-s/jind-ai/internal/session"
	"github.com/takaaki-s/jind-ai/internal/testutil"
	"github.com/takaaki-s/jind-ai/internal/tmux"
)

// Force TrueColor output during tests so styling assertions (e.g. the
// presence of a background SGR sequence on viewed cards) are reliable
// regardless of the CI environment's TTY detection.
func init() {
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// --- truncateString ---

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{
			name:     "short string within limit",
			input:    "hello",
			maxWidth: 10,
			want:     "hello",
		},
		{
			name:     "string exactly at limit",
			input:    "hello",
			maxWidth: 5,
			want:     "hello",
		},
		{
			name:     "string needs truncation",
			input:    "hello world this is long",
			maxWidth: 10,
			want:     "hello w...",
		},
		{
			name:     "maxWidth 3 gets ellipsis",
			input:    "hello world",
			maxWidth: 3,
			want:     "hel",
		},
		{
			name:     "maxWidth 2 no ellipsis",
			input:    "hello",
			maxWidth: 2,
			want:     "he",
		},
		{
			name:     "maxWidth 1",
			input:    "hello",
			maxWidth: 1,
			want:     "h",
		},
		{
			name:     "empty string",
			input:    "",
			maxWidth: 10,
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

// --- truncateStringFromEnd ---

func TestTruncateStringFromEnd(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{
			name:     "short string within limit",
			input:    "hello",
			maxWidth: 10,
			want:     "hello",
		},
		{
			name:     "string exactly at limit",
			input:    "hello",
			maxWidth: 5,
			want:     "hello",
		},
		{
			name:     "string needs truncation keeps end",
			input:    "hello world",
			maxWidth: 8,
			want:     "...world",
		},
		{
			name:     "maxWidth 3 no ellipsis",
			input:    "hello world",
			maxWidth: 3,
			want:     "rld",
		},
		{
			name:     "maxWidth 2",
			input:    "hello",
			maxWidth: 2,
			want:     "lo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateStringFromEnd(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("truncateStringFromEnd(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

// --- timeAgo ---

func TestTimeAgo(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"just now", 10 * time.Second, "just now"},
		{"1 minute ago", 1 * time.Minute, "1m ago"},
		{"5 minutes ago", 5 * time.Minute, "5m ago"},
		{"59 minutes ago", 59 * time.Minute, "59m ago"},
		{"1 hour ago", 1 * time.Hour, "1h ago"},
		{"3 hours ago", 3 * time.Hour, "3h ago"},
		{"1 day ago", 24 * time.Hour, "1d ago"},
		{"5 days ago", 5 * 24 * time.Hour, "5d ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			past := time.Now().Add(-tt.offset)
			got := timeAgo(past)
			if got != tt.want {
				t.Errorf("timeAgo(now - %v) = %q, want %q", tt.offset, got, tt.want)
			}
		})
	}
}

// --- getStatusDisplay ---

// TestGetStatusDisplay pins the icon-collision bug that motivated the
// PERMISSION/UNKNOWN split: PERMISSION used to share "?" with the default
// case, and once the one-line session list started showing the icon without
// its label, the two became indistinguishable. It also exercises padIcon,
// since the list's column alignment depends on every icon padding to the
// same 2-column cell.
func TestGetStatusDisplay(t *testing.T) {
	// The table pins the vocabulary itself; the loop below pins the invariant
	// that no two entries may ever converge again.
	statuses := []struct {
		status    session.Status
		wantIcon  string
		wantLabel string
	}{
		{session.StatusThinking, "⚡", "THINKING"},
		{session.StatusPermission, "?", "PERMISSION"},
		{session.StatusRunning, "▶", "RUNNING"},
		{session.StatusCreating, "+", "CREATING"},
		{session.StatusIdle, "○", "IDLE"},
		{session.StatusStopped, "■", "STOPPED"},
		{session.StatusDeleting, "⟳", "DELETING"},
		{session.Status("bogus-status"), "·", "UNKNOWN"}, // falls through to default
	}

	seenIcons := make(map[string]session.Status)
	for _, tt := range statuses {
		status := tt.status
		icon, label, _ := getStatusDisplay(status)

		if icon != tt.wantIcon {
			t.Errorf("getStatusDisplay(%q) icon = %q, want %q", status, icon, tt.wantIcon)
		}
		if label != tt.wantLabel {
			t.Errorf("getStatusDisplay(%q) label = %q, want %q", status, label, tt.wantLabel)
		}

		if prior, collides := seenIcons[icon]; collides {
			t.Errorf("getStatusDisplay(%q) icon %q collides with %q's icon", status, icon, prior)
		}
		seenIcons[icon] = status

		padded := padIcon(icon)
		if w := lipgloss.Width(padded); w != 2 {
			t.Errorf("padIcon(getStatusDisplay(%q)) = %q, width = %d, want 2", status, padded, w)
		}
		// "⚡" (THINKING) is already 2 columns wide; padIcon must pass it
		// through rather than appending a space that would overshoot.
		if lipgloss.Width(icon) >= 2 && padded != icon {
			t.Errorf("padIcon(%q) = %q, want unchanged (already >= 2 columns)", icon, padded)
		}
	}
}

// --- wrapText ---

func TestWrapText(t *testing.T) {
	t.Run("single short line", func(t *testing.T) {
		got := wrapText("hello", 20)
		if len(got) != 1 || got[0] != "hello" {
			t.Errorf("wrapText(%q, 20) = %v, want [%q]", "hello", got, "hello")
		}
	})

	t.Run("long line wraps", func(t *testing.T) {
		input := "abcdefghij" // 10 chars
		got := wrapText(input, 4)
		// Should wrap into: "abcd", "efgh", "ij"
		if len(got) != 3 {
			t.Fatalf("wrapText(%q, 4) got %d lines, want 3: %v", input, len(got), got)
		}
		if got[0] != "abcd" {
			t.Errorf("line 0 = %q, want %q", got[0], "abcd")
		}
		if got[1] != "efgh" {
			t.Errorf("line 1 = %q, want %q", got[1], "efgh")
		}
		if got[2] != "ij" {
			t.Errorf("line 2 = %q, want %q", got[2], "ij")
		}
	})

	t.Run("zero width returns original", func(t *testing.T) {
		got := wrapText("hello", 0)
		if len(got) != 1 || got[0] != "hello" {
			t.Errorf("wrapText(%q, 0) = %v, want [%q]", "hello", got, "hello")
		}
	})

	t.Run("negative width returns original", func(t *testing.T) {
		got := wrapText("hello", -1)
		if len(got) != 1 || got[0] != "hello" {
			t.Errorf("wrapText(%q, -1) = %v, want [%q]", "hello", got, "hello")
		}
	})

	t.Run("text with newlines", func(t *testing.T) {
		input := "line1\nline2\nline3"
		got := wrapText(input, 20)
		if len(got) != 3 {
			t.Fatalf("wrapText with newlines got %d lines, want 3: %v", len(got), got)
		}
		if got[0] != "line1" || got[1] != "line2" || got[2] != "line3" {
			t.Errorf("got %v, want [line1, line2, line3]", got)
		}
	})
}

// --- padLine ---

func TestPadLine(t *testing.T) {
	t.Run("shorter string gets padded", func(t *testing.T) {
		got := padLine("hi", 5)
		if got != "hi   " {
			t.Errorf("padLine(%q, 5) = %q, want %q", "hi", got, "hi   ")
		}
	})

	t.Run("exact width no padding", func(t *testing.T) {
		got := padLine("hello", 5)
		if got != "hello" {
			t.Errorf("padLine(%q, 5) = %q, want %q", "hello", got, "hello")
		}
	})

	t.Run("longer string no change", func(t *testing.T) {
		got := padLine("hello world", 5)
		if got != "hello world" {
			t.Errorf("padLine(%q, 5) = %q, want %q", "hello world", got, "hello world")
		}
	})

	t.Run("empty string gets full padding", func(t *testing.T) {
		got := padLine("", 3)
		if got != "   " {
			t.Errorf("padLine(%q, 3) = %q, want %q", "", got, "   ")
		}
	})
}

// --- isSessionAlive ---

func TestIsSessionAlive(t *testing.T) {
	alive := []session.Status{
		session.StatusRunning,
		session.StatusThinking,
		session.StatusIdle,
		session.StatusPermission,
		session.StatusCreating,
	}
	for _, s := range alive {
		t.Run(string(s)+"_alive", func(t *testing.T) {
			if !isSessionAlive(s) {
				t.Errorf("isSessionAlive(%q) = false, want true", s)
			}
		})
	}

	dead := []session.Status{
		session.StatusStopped,
		session.Status("unknown"),
	}
	for _, s := range dead {
		name := string(s)
		if name == "" {
			name = "empty"
		}
		t.Run(name+"_not_alive", func(t *testing.T) {
			if isSessionAlive(s) {
				t.Errorf("isSessionAlive(%q) = true, want false", s)
			}
		})
	}
}

// --- helper: verify truncation lengths ---

func TestTruncateStringLengthProperties(t *testing.T) {
	// Verify the truncated result has a display width <= maxWidth
	input := "this is a longer string for testing"
	for _, maxWidth := range []int{1, 2, 3, 5, 10, 15} {
		got := truncateString(input, maxWidth)
		// For ASCII-only strings, len is the display width
		if len(got) > maxWidth {
			t.Errorf("truncateString(%q, %d) = %q (len %d), exceeds maxWidth",
				input, maxWidth, got, len(got))
		}
	}
}

func TestTruncateStringFromEndLengthProperties(t *testing.T) {
	input := "this is a longer string for testing"
	for _, maxWidth := range []int{1, 2, 3, 5, 10, 15} {
		got := truncateStringFromEnd(input, maxWidth)
		if len(got) > maxWidth {
			t.Errorf("truncateStringFromEnd(%q, %d) = %q (len %d), exceeds maxWidth",
				input, maxWidth, got, len(got))
		}
	}
}

// Verify truncateStringFromEnd keeps the end of the string
func TestTruncateStringFromEndKeepsEnd(t *testing.T) {
	got := truncateStringFromEnd("/home/user/very/long/path/to/project", 20)
	if !strings.HasSuffix(got, "to/project") {
		t.Errorf("truncateStringFromEnd should keep the end, got %q", got)
	}
	if !strings.HasPrefix(got, "...") {
		t.Errorf("truncateStringFromEnd should start with '...', got %q", got)
	}
}

// --- truncateToWidth ---

func TestTruncateToWidth(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{
			name:     "ASCII within limit",
			input:    "hello",
			maxWidth: 10,
			want:     "hello",
		},
		{
			name:     "ASCII exact width",
			input:    "hello",
			maxWidth: 5,
			want:     "hello",
		},
		{
			name:     "ASCII truncated",
			input:    "hello world",
			maxWidth: 5,
			want:     "hello",
		},
		{
			name:     "empty string",
			input:    "",
			maxWidth: 10,
			want:     "",
		},
		{
			name:     "CJK characters fit",
			input:    "\u3042\u3044\u3046",
			maxWidth: 6,
			want:     "\u3042\u3044\u3046",
		},
		{
			name:     "CJK truncated at boundary",
			input:    "\u3042\u3044\u3046",
			maxWidth: 5,
			// Each CJK char is 2 cells wide; 2 chars = 4 cells fits, 3 chars = 6 cells > 5
			want: "\u3042\u3044",
		},
		{
			name:     "mixed ASCII and CJK",
			input:    "Aあ",
			maxWidth: 3,
			// 'A'=1 + 'あ'=2 = 3, fits exactly
			want: "Aあ",
		},
		{
			name:     "mixed ASCII and CJK truncated",
			input:    "Aあい",
			maxWidth: 3,
			// 'A'=1 + 'あ'=2 = 3, 'い' would be 5 > 3
			want: "Aあ",
		},
		{
			name:     "CJK does not fit partial",
			input:    "あ",
			maxWidth: 1,
			// 'あ' is 2 cells wide, does not fit in 1
			want: "",
		},
		{
			name:     "zero width",
			input:    "hello",
			maxWidth: 0,
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateToWidth(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("truncateToWidth(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

// --- truncateFromEndToWidth ---

func TestTruncateFromEndToWidth(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{
			name:     "ASCII within limit",
			input:    "hello",
			maxWidth: 10,
			want:     "hello",
		},
		{
			name:     "ASCII exact width",
			input:    "hello",
			maxWidth: 5,
			want:     "hello",
		},
		{
			name:     "ASCII truncated keeps end",
			input:    "hello world",
			maxWidth: 5,
			want:     "world",
		},
		{
			name:     "empty string",
			input:    "",
			maxWidth: 10,
			want:     "",
		},
		{
			name:     "CJK characters fit",
			input:    "あい",
			maxWidth: 4,
			want:     "あい",
		},
		{
			name:     "CJK truncated keeps end",
			input:    "あいう",
			maxWidth: 4,
			// Each CJK char is 2 cells; from end: 'う'=2, 'い'=2+2=4 fits, 'あ'=4+2=6 > 4
			want: "いう",
		},
		{
			name:     "CJK does not fit partial",
			input:    "あ",
			maxWidth: 1,
			// 'あ' is 2 cells wide, does not fit in 1
			want: "",
		},
		{
			name:     "mixed ASCII and CJK keeps end",
			input:    "あtest",
			maxWidth: 4,
			// from end: 't'=1, 's'=2, 'e'=3, 't'=4 => "test"
			want: "test",
		},
		{
			name:     "zero width",
			input:    "hello",
			maxWidth: 0,
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateFromEndToWidth(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("truncateFromEndToWidth(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

// --- list geometry ---

// TestSessionCardTop pins the line offsets the scroll and hit-test arithmetic
// is built on: one row per session (sessionRowHeight), plus one row per fleet
// header. The height is now state-independent — a deleting session occupies
// the same single row as any other, which is what removed the old
// cardHeight/renderSession hand-sync contract.
func TestSessionCardTop(t *testing.T) {
	t.Run("single fleet", func(t *testing.T) {
		// Lines: 0 = fleet header, 1..3 = sessions 0..2.
		m := cardListModel(3)
		for idx, wantTop := range []int{1, 2, 3} {
			top, height := m.sessionCardTop(idx)
			if top != wantTop || height != sessionRowHeight {
				t.Errorf("sessionCardTop(%d) = (%d, %d), want (%d, %d)", idx, top, height, wantTop, sessionRowHeight)
			}
		}
	})

	t.Run("deleting session occupies the same single row", func(t *testing.T) {
		m := cardListModel(3)
		m.deletingIDs["0"] = true
		m.sessions[1].Status = session.StatusDeleting
		for idx, wantTop := range []int{1, 2, 3} {
			top, height := m.sessionCardTop(idx)
			if top != wantTop || height != sessionRowHeight {
				t.Errorf("sessionCardTop(%d) = (%d, %d), want (%d, %d)", idx, top, height, wantTop, sessionRowHeight)
			}
		}
	})

	t.Run("second fleet adds one header row", func(t *testing.T) {
		// DefaultFleet always renders last, so the lines are:
		// 0 = ALPHA, 1 = session 0, 2 = DEFAULT, 3 = session 1, 4 = session 2.
		m := cardListModel(3)
		m.sessions[0].Fleet = "alpha"
		m.sessions[1].Fleet = session.DefaultFleet
		m.sessions[2].Fleet = session.DefaultFleet
		for idx, wantTop := range []int{1, 3, 4} {
			top, _ := m.sessionCardTop(idx)
			if top != wantTop {
				t.Errorf("sessionCardTop(%d) top = %d, want %d", idx, top, wantTop)
			}
		}
	})

	t.Run("out of range", func(t *testing.T) {
		m := cardListModel(3)
		for _, idx := range []int{-1, 3} {
			if top, height := m.sessionCardTop(idx); top != -1 || height != 0 {
				t.Errorf("sessionCardTop(%d) = (%d, %d), want (-1, 0)", idx, top, height)
			}
		}
	})
}

func TestTotalCardLines(t *testing.T) {
	t.Run("empty list has no rows", func(t *testing.T) {
		m := cardListModel(0)
		if got := m.totalCardLines(); got != 0 {
			t.Errorf("totalCardLines() = %d, want 0 (no sessions, no fleet header)", got)
		}
	})

	t.Run("single fleet", func(t *testing.T) {
		// 1 fleet header + 3 session rows.
		m := cardListModel(3)
		if got := m.totalCardLines(); got != 4 {
			t.Errorf("totalCardLines() = %d, want 4", got)
		}
	})

	t.Run("two fleets", func(t *testing.T) {
		// 2 fleet headers + 3 session rows.
		m := cardListModel(3)
		m.sessions[0].Fleet = "alpha"
		if got := m.totalCardLines(); got != 5 {
			t.Errorf("totalCardLines() = %d, want 5", got)
		}
	})

	t.Run("agrees with the last row's bottom", func(t *testing.T) {
		m := cardListModel(6)
		top, height := m.sessionCardTop(5)
		if got := m.totalCardLines(); got != top+height {
			t.Errorf("totalCardLines() = %d, want %d (bottom of the last row)", got, top+height)
		}
	})
}

// TestListAreaLines covers how contentAreaLines is split into the three
// regions, and the D-3 boundary where the detail pane is dropped whole rather
// than shrunk.
//
// Derivation, with no notices and a valid cursor:
//
//	contentAreaLines = max(m.height-1, 3)
//	list             = contentAreaLines - listHeaderLines(2) - detailPaneLines(6)
//	detail is drawn iff list >= minListLines(5), i.e. m.height >= 14
func TestListAreaLines(t *testing.T) {
	t.Run("detail pane appears at the height threshold", func(t *testing.T) {
		threshold := 1 + listHeaderLines + detailPaneLines + minListLines // 14
		tests := []struct {
			height        int
			wantDetail    int
			wantListLines int
		}{
			// One row short: the detail pane goes away entirely and the list
			// takes every row it leaves behind (12-2 = 10), rather than the
			// detail pane shrinking to 5.
			{height: threshold - 1, wantDetail: 0, wantListLines: 10},
			{height: threshold, wantDetail: detailPaneLines, wantListLines: minListLines},
			{height: threshold + 1, wantDetail: detailPaneLines, wantListLines: minListLines + 1},
		}
		for _, tt := range tests {
			m := cardListModel(3)
			m.height = tt.height
			if got := m.detailLines(); got != tt.wantDetail {
				t.Errorf("height %d: detailLines() = %d, want %d", tt.height, got, tt.wantDetail)
			}
			if got := m.listAreaLines(); got != tt.wantListLines {
				t.Errorf("height %d: listAreaLines() = %d, want %d", tt.height, got, tt.wantListLines)
			}
		}
	})

	t.Run("notices shrink the same budget", func(t *testing.T) {
		// An error notice costs 2 rows, so height 16 with a notice lands on
		// exactly the same 13-row content area as height 14 without one.
		m := cardListModel(3)
		m.height = 16
		m.err = errors.New("boom")
		if got := m.listAreaLines(); got != minListLines {
			t.Errorf("listAreaLines() with an error notice = %d, want %d", got, minListLines)
		}
		m.warning = "hook not allowlisted"
		// Two notices (4 rows) drop the content area to 11: 11-2-6 = 3 < 5.
		if got := m.detailLines(); got != 0 {
			t.Errorf("detailLines() with two notices = %d, want 0", got)
		}
		if got := m.listAreaLines(); got != 9 {
			t.Errorf("listAreaLines() with two notices = %d, want 9", got)
		}
	})

	t.Run("regions add up to the content area", func(t *testing.T) {
		m := cardListModel(3)
		m.height = 30
		if sum := m.headerLines() + m.listAreaLines() + m.detailLines(); sum != m.contentAreaLines() {
			t.Errorf("header+list+detail = %d, want contentAreaLines() = %d", sum, m.contentAreaLines())
		}
	})

	t.Run("empty list keeps the whole content area", func(t *testing.T) {
		m := cardListModel(0)
		m.height = 30
		if got := m.headerLines(); got != 0 {
			t.Errorf("headerLines() with no sessions = %d, want 0", got)
		}
		if got := m.detailLines(); got != 0 {
			t.Errorf("detailLines() with no sessions = %d, want 0", got)
		}
		if got := m.listAreaLines(); got != m.contentAreaLines() {
			t.Errorf("listAreaLines() with no sessions = %d, want %d", got, m.contentAreaLines())
		}
	})

	t.Run("out-of-range cursor drops the detail pane", func(t *testing.T) {
		// The pane describes the session under the cursor; with no such
		// session there is nothing to draw, so the list gets the rows.
		m := cardListModel(3)
		m.height = 30
		m.cursor = len(m.sessions)
		if m.detailVisible() {
			t.Error("detailVisible() = true with an out-of-range cursor, want false")
		}
		if got := m.listAreaLines(); got != m.contentAreaLines()-listHeaderLines {
			t.Errorf("listAreaLines() = %d, want %d", got, m.contentAreaLines()-listHeaderLines)
		}
	})

	t.Run("never reports a zero-height list", func(t *testing.T) {
		for _, h := range []int{0, 1, 2, 3} {
			m := cardListModel(3)
			m.height = h
			if got := m.listAreaLines(); got < 1 {
				t.Errorf("height %d: listAreaLines() = %d, want >= 1", h, got)
			}
		}
	})
}

// --- viewport scroll ---

// cardListModel builds a model holding n session rows. Every session renders
// as exactly one line (sessionRowHeight), and the rendered list is one fleet
// header row followed by the rows, so session i sits on line 1+i and the whole
// list spans 1+n lines.
//
// Height 15+1: the pane holds 15 rows, of which the list header takes 2 and
// the detail pane 6, leaving listAreaLines() = 7 — above the minListLines
// threshold, so these models exercise the full three-region layout, and small
// enough to watch the viewport move. Pane rows map to regions as:
//
//	rows 0..1  list header      (noticeLines() + headerLines() = 2)
//	rows 2..8  list area        (listAreaLines() = 7)
//	rows 9..14 detail pane      (detailPaneLines = 6)
//
// Every geometry test below is calibrated to those numbers — if the row height
// or the region budget changes, the hand-computed constants move together.
func cardListModel(n int) Model {
	sessions := make([]session.Info, n)
	for i := range sessions {
		sessions[i] = session.Info{ID: string(rune('0' + i)), Description: "s"}
	}
	return Model{
		sessions:    sessions,
		height:      16, // contentAreaLines() → 15; 15-2-6 = 7 list rows
		deletingIDs: map[string]bool{},
	}
}

// TestAdjustScrollForCursor verifies the viewport follows the cursor.
func TestAdjustScrollForCursor(t *testing.T) {
	newModel := func(cursor int) *Model {
		m := cardListModel(10)
		m.cursor = cursor
		return &m
	}

	t.Run("cursor on first card keeps scroll at 0", func(t *testing.T) {
		m := newModel(0)
		m.adjustScrollForCursor()
		if m.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0 (cursor on first card)", m.scrollOffset)
		}
	})

	t.Run("cursor below viewport scrolls down", func(t *testing.T) {
		m := newModel(7)
		// Row 7 top = 1 (fleet header) + 7 = 8, bottom = 9. avail = 7.
		// Should scroll so bottom = scrollOffset + avail → scrollOffset = 2.
		m.adjustScrollForCursor()
		if m.scrollOffset != 2 {
			t.Errorf("scrollOffset = %d, want 2 (cursor below fold)", m.scrollOffset)
		}
	})

	t.Run("cursor at end anchors scroll to last visible page", func(t *testing.T) {
		m := newModel(9)
		m.adjustScrollForCursor()
		// Last row top = 1 + 9 = 10, height = 1, bottom = 11.
		// scrollOffset = 11 - 7 = 4. clampScroll bounds by totalCardLines(11) - avail(7) = 4.
		if m.scrollOffset != 4 {
			t.Errorf("scrollOffset = %d, want 4 (cursor at end)", m.scrollOffset)
		}
	})

	t.Run("clampScroll bounds within [0, total-avail]", func(t *testing.T) {
		m := newModel(0)
		m.scrollOffset = 999
		m.clampScroll()
		if m.scrollOffset != 4 {
			t.Errorf("clampScroll from overshoot = %d, want 4", m.scrollOffset)
		}
		m.scrollOffset = -50
		m.clampScroll()
		if m.scrollOffset != 0 {
			t.Errorf("clampScroll from negative = %d, want 0", m.scrollOffset)
		}
	})
}

// --- mouse hit-testing ---

func TestSessionIndexAtLine(t *testing.T) {
	m := cardListModel(10)

	tests := []struct {
		name    string
		line    int
		wantIdx int
		wantOK  bool
	}{
		{name: "negative line", line: -1, wantOK: false},
		{name: "fleet header row", line: 0, wantOK: false},
		{name: "first session row", line: 1, wantIdx: 0, wantOK: true},
		{name: "second session row", line: 2, wantIdx: 1, wantOK: true},
		{name: "last session row", line: 10, wantIdx: 9, wantOK: true},
		{name: "past the last row", line: 11, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, ok := m.sessionIndexAtLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("sessionIndexAtLine(%d) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if ok && idx != tt.wantIdx {
				t.Errorf("sessionIndexAtLine(%d) = %d, want %d", tt.line, idx, tt.wantIdx)
			}
		})
	}
}

func TestSessionIndexAtRow(t *testing.T) {
	// Rows 0..1 are the list header, rows 2..8 the list area (7 rows), rows
	// 9..14 the detail pane. Inside the list area, row 2 is the fleet header,
	// so row 3 is session 0.
	newModel := func() *Model {
		m := cardListModel(10)
		return &m
	}

	t.Run("row maps straight through at scroll 0", func(t *testing.T) {
		m := newModel()
		if idx, ok := m.sessionIndexAtRow(4); !ok || idx != 1 {
			t.Errorf("sessionIndexAtRow(4) = (%d, %v), want (1, true)", idx, ok)
		}
	})

	t.Run("row in the list header misses", func(t *testing.T) {
		m := newModel()
		for _, y := range []int{0, 1} {
			if _, ok := m.sessionIndexAtRow(y); ok {
				t.Errorf("sessionIndexAtRow(%d) should miss: rows 0..1 are the list header", y)
			}
		}
	})

	t.Run("row in the detail pane misses", func(t *testing.T) {
		m := newModel()
		if _, ok := m.sessionIndexAtRow(9); ok {
			t.Error("sessionIndexAtRow(9) should miss: the list area ends at row 8")
		}
	})

	t.Run("scroll offset shifts the mapping", func(t *testing.T) {
		m := newModel()
		m.scrollOffset = 4
		// Row 2 (first list row) → line 4, which is session 3.
		if idx, ok := m.sessionIndexAtRow(2); !ok || idx != 3 {
			t.Errorf("sessionIndexAtRow(2) at offset 4 = (%d, %v), want (3, true)", idx, ok)
		}
	})

	t.Run("error notice pushes the list area down", func(t *testing.T) {
		m := newModel()
		m.err = errors.New("boom")
		// Rows 0..1 are the error line + spacer, rows 2..3 the list header,
		// row 4 the fleet header, so session 0 is on row 5.
		for _, y := range []int{1, 3, 4} {
			if _, ok := m.sessionIndexAtRow(y); ok {
				t.Errorf("sessionIndexAtRow(%d) should miss while an error notice is shown", y)
			}
		}
		if idx, ok := m.sessionIndexAtRow(5); !ok || idx != 0 {
			t.Errorf("sessionIndexAtRow(5) with error = (%d, %v), want (0, true)", idx, ok)
		}
	})
}

// --- mouse input ---

func TestHandleMouseWheel(t *testing.T) {
	newModel := func() Model { return cardListModel(10) } // 11 lines total, 7 visible

	wheel := func(m Model, button tea.MouseButton) Model {
		got, _ := m.handleMouse(tea.MouseMsg{Y: 4, Button: button, Action: tea.MouseActionPress})
		return got.(Model)
	}

	t.Run("wheel down scrolls the viewport without moving the cursor", func(t *testing.T) {
		m := wheel(newModel(), tea.MouseButtonWheelDown)
		if m.scrollOffset != wheelScrollLines {
			t.Errorf("scrollOffset = %d, want %d", m.scrollOffset, wheelScrollLines)
		}
		if m.cursor != 0 {
			t.Errorf("cursor = %d, want 0 (wheel must not move the cursor)", m.cursor)
		}
	})

	t.Run("wheel up at the top stays clamped at 0", func(t *testing.T) {
		m := wheel(newModel(), tea.MouseButtonWheelUp)
		if m.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0", m.scrollOffset)
		}
	})

	t.Run("wheel down at the bottom stays clamped", func(t *testing.T) {
		m := newModel()
		m.scrollOffset = 4 // totalCardLines(11) - listAreaLines(7)
		m = wheel(m, tea.MouseButtonWheelDown)
		if m.scrollOffset != 4 {
			t.Errorf("scrollOffset = %d, want 4 (clamped at the last page)", m.scrollOffset)
		}
	})
}

func TestHandleMouseLeftClick(t *testing.T) {
	click := func(m Model, y int) Model {
		got, _ := m.handleMouse(tea.MouseMsg{Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		return got.(Model)
	}

	newModel := func() Model { return cardListModel(10) }

	t.Run("click on a row moves the cursor there", func(t *testing.T) {
		// Row 4 → line 2 (list header 2 rows, fleet header on row 2) → session 1.
		if m := click(newModel(), 4); m.cursor != 1 {
			t.Errorf("cursor = %d, want 1", m.cursor)
		}
	})

	t.Run("click on the fleet header leaves the cursor alone", func(t *testing.T) {
		m := newModel()
		m.cursor = 3
		if got := click(m, 2); got.cursor != 3 {
			t.Errorf("cursor = %d, want 3 (fleet header row is not selectable)", got.cursor)
		}
	})

	t.Run("click on the list header leaves the cursor alone", func(t *testing.T) {
		m := newModel()
		m.cursor = 3
		if got := click(m, 0); got.cursor != 3 {
			t.Errorf("cursor = %d, want 3 (list header rows are not selectable)", got.cursor)
		}
	})

	t.Run("click on the detail pane leaves the cursor alone", func(t *testing.T) {
		// Row 9 is the first detail pane row. It describes the cursor's
		// session; clicking it must not re-target anything.
		m := newModel()
		m.cursor = 3
		if got := click(m, 9); got.cursor != 3 {
			t.Errorf("cursor = %d, want 3 (the detail pane is not the list)", got.cursor)
		}
	})

	t.Run("click below the last row leaves the cursor alone", func(t *testing.T) {
		m := newModel()
		m.sessions = cardListModel(1).sessions // fleet header + 1 row = 2 lines
		m.cursor = 0
		if got := click(m, 6); got.cursor != 0 {
			t.Errorf("cursor = %d, want 0 (empty space is not selectable)", got.cursor)
		}
	})

	t.Run("click on a deleting row is ignored", func(t *testing.T) {
		m := newModel()
		m.deletingIDs = map[string]bool{"1": true}
		if got := click(m, 4); got.cursor != 0 {
			t.Errorf("cursor = %d, want 0 (deleting rows are not selectable)", got.cursor)
		}
	})

	t.Run("release and drag events do not select", func(t *testing.T) {
		for _, action := range []tea.MouseAction{tea.MouseActionRelease, tea.MouseActionMotion} {
			m := newModel()
			got, _ := m.handleMouse(tea.MouseMsg{Y: 4, Button: tea.MouseButtonLeft, Action: action})
			if got.(Model).cursor != 0 {
				t.Errorf("cursor moved on %v; only press should select", action)
			}
		}
	})

	t.Run("click dismisses a transient warning", func(t *testing.T) {
		m := newModel()
		m.warning = "hook not allowlisted"
		// The warning occupies rows 0..1 and the list header rows 2..3, so
		// row 4 is the fleet header and row 5 is session 0.
		got := click(m, 5)
		if got.warning != "" {
			t.Errorf("warning = %q, want empty after a click", got.warning)
		}
		if got.cursor != 0 {
			t.Errorf("cursor = %d, want 0", got.cursor)
		}
	})
}

// --- convertDirHistoryEntries ---

func TestConvertDirHistoryEntries(t *testing.T) {
	now := time.Now()

	t.Run("empty input returns empty", func(t *testing.T) {
		got := convertDirHistoryEntries(nil)
		if len(got) != 0 {
			t.Errorf("convertDirHistoryEntries(nil) should return empty, got %d entries", len(got))
		}
	})

	t.Run("home prefix converted to tilde in DisplayPath", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home directory")
		}
		entries := []config.DirHistoryEntry{
			{Path: home + "/myproject", LastUsedAt: now},
		}

		got := convertDirHistoryEntries(entries)

		if len(got) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(got))
		}
		if got[0].Path != home+"/myproject" {
			t.Errorf("Path should remain absolute: got %q", got[0].Path)
		}
		if got[0].DisplayPath != "~/myproject" {
			t.Errorf("DisplayPath = %q, want %q", got[0].DisplayPath, "~/myproject")
		}
	})

	t.Run("preserves LastUsedAt", func(t *testing.T) {
		entries := []config.DirHistoryEntry{
			{Path: "/a", LastUsedAt: now},
		}

		got := convertDirHistoryEntries(entries)
		if !got[0].LastUsedAt.Equal(now) {
			t.Errorf("LastUsedAt not preserved")
		}
	})
}

// --- groupSessionsByFleet ---

func TestGroupSessionsByFleet(t *testing.T) {
	now := time.Now()

	t.Run("groups by fleet name", func(t *testing.T) {
		sessions := []session.Info{
			{ID: "1", Description: "s1", Fleet: "backend", CreatedAt: now},
			{ID: "2", Description: "s2", Fleet: "frontend", CreatedAt: now},
			{ID: "3", Description: "s3", Fleet: "backend", CreatedAt: now.Add(time.Minute)},
		}

		groups := groupSessionsByFleet(sessions)
		if len(groups) != 2 {
			t.Fatalf("expected 2 groups, got %d", len(groups))
		}
		if groups[0].Name != "backend" {
			t.Errorf("first group: got %q, want %q", groups[0].Name, "backend")
		}
		if len(groups[0].Sessions) != 2 {
			t.Errorf("backend group: got %d sessions, want 2", len(groups[0].Sessions))
		}
		if groups[1].Name != "frontend" {
			t.Errorf("second group: got %q, want %q", groups[1].Name, "frontend")
		}
	})

	t.Run("default fleet is last", func(t *testing.T) {
		sessions := []session.Info{
			{ID: "1", Description: "s1", Fleet: session.DefaultFleet, CreatedAt: now},
			{ID: "2", Description: "s2", Fleet: "alpha", CreatedAt: now},
			{ID: "3", Description: "s3", Fleet: "beta", CreatedAt: now},
		}

		groups := groupSessionsByFleet(sessions)
		if len(groups) != 3 {
			t.Fatalf("expected 3 groups, got %d", len(groups))
		}
		if groups[0].Name != "alpha" {
			t.Errorf("first group: got %q, want %q", groups[0].Name, "alpha")
		}
		if groups[1].Name != "beta" {
			t.Errorf("second group: got %q, want %q", groups[1].Name, "beta")
		}
		if groups[2].Name != session.DefaultFleet {
			t.Errorf("last group: got %q, want %q", groups[2].Name, session.DefaultFleet)
		}
	})

	t.Run("sessions with default fleet group correctly", func(t *testing.T) {
		sessions := []session.Info{
			{ID: "1", Description: "s1", Fleet: session.DefaultFleet, CreatedAt: now},
			{ID: "2", Description: "s2", Fleet: session.DefaultFleet, CreatedAt: now},
		}

		groups := groupSessionsByFleet(sessions)
		if len(groups) != 1 {
			t.Fatalf("expected 1 group, got %d", len(groups))
		}
		if groups[0].Name != session.DefaultFleet {
			t.Errorf("group name: got %q, want %q", groups[0].Name, session.DefaultFleet)
		}
		if len(groups[0].Sessions) != 2 {
			t.Errorf("group sessions: got %d, want 2", len(groups[0].Sessions))
		}
	})

	t.Run("single fleet still returns one group", func(t *testing.T) {
		sessions := []session.Info{
			{ID: "1", Description: "s1", Fleet: "only", CreatedAt: now},
		}

		groups := groupSessionsByFleet(sessions)
		if len(groups) != 1 {
			t.Fatalf("expected 1 group, got %d", len(groups))
		}
	})

	t.Run("empty sessions", func(t *testing.T) {
		groups := groupSessionsByFleet(nil)
		if len(groups) != 0 {
			t.Errorf("expected 0 groups, got %d", len(groups))
		}
	})
}

// --- skipDeletingSessions ---

func TestSkipDeletingSessions(t *testing.T) {
	makeSessions := func(ids ...string) []session.Info {
		var ss []session.Info
		for _, id := range ids {
			ss = append(ss, session.Info{ID: id, Description: id})
		}
		return ss
	}

	t.Run("no deleting sessions", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c"),
			cursor:      1,
			deletingIDs: make(map[string]bool),
			height:      100,
		}
		m.skipDeletingSessions(1)
		if m.cursor != 1 {
			t.Errorf("expected cursor 1, got %d", m.cursor)
		}
	})

	t.Run("skip down over deleting session", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c"),
			cursor:      1,
			deletingIDs: map[string]bool{"b": true},
			height:      100,
		}
		m.skipDeletingSessions(1)
		if m.cursor != 2 {
			t.Errorf("expected cursor 2, got %d", m.cursor)
		}
	})

	t.Run("skip up over deleting session", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c"),
			cursor:      1,
			deletingIDs: map[string]bool{"b": true},
			height:      100,
		}
		m.skipDeletingSessions(-1)
		if m.cursor != 0 {
			t.Errorf("expected cursor 0, got %d", m.cursor)
		}
	})

	t.Run("clamp at end and fallback to opposite direction", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c"),
			cursor:      2,
			deletingIDs: map[string]bool{"c": true},
			height:      100,
		}
		m.skipDeletingSessions(1) // going down, hits end, clamp, fallback up
		if m.cursor != 1 {
			t.Errorf("expected cursor 1, got %d", m.cursor)
		}
	})

	t.Run("clamp at start and fallback to opposite direction", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c"),
			cursor:      0,
			deletingIDs: map[string]bool{"a": true},
			height:      100,
		}
		m.skipDeletingSessions(-1) // going up, hits start, clamp, fallback down
		if m.cursor != 1 {
			t.Errorf("expected cursor 1, got %d", m.cursor)
		}
	})

	t.Run("all sessions deleting stays on cursor 0", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b"),
			cursor:      0,
			deletingIDs: map[string]bool{"a": true, "b": true},
			height:      100,
		}
		m.skipDeletingSessions(1)
		// All deleting: cursor stays at clamped position (transient state)
		if m.cursor < 0 || m.cursor >= 2 {
			t.Errorf("expected cursor in range [0,1], got %d", m.cursor)
		}
	})

	t.Run("multiple deleting sessions skip all", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c", "d"),
			cursor:      1,
			deletingIDs: map[string]bool{"b": true, "c": true},
			height:      100,
		}
		m.skipDeletingSessions(1)
		if m.cursor != 3 {
			t.Errorf("expected cursor 3, got %d", m.cursor)
		}
	})
}

// --- Delete confirmation cursor skip ---

// TestDeleteConfirmMoveCursorToNextSession drives the answered-popup entry
// point (dispatchConfirmResult) rather than a key press, since the prompt now
// lives in its own tmux popup process. What it pins is unchanged: an approved
// delete greys out the target and slides the cursor off it, so the user is
// never left pointing at a session that is transitioning away.
func TestDeleteConfirmMoveCursorToNextSession(t *testing.T) {
	makeSessions := func(ids ...string) []session.Info {
		var ss []session.Info
		for _, id := range ids {
			ss = append(ss, session.Info{ID: id, Description: id})
		}
		return ss
	}

	t.Run("cursor moves to next session after delete confirm", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c"),
			cursor:      1, // on "b"
			deletingIDs: make(map[string]bool),
			height:      100,
		}
		result, _ := m.dispatchConfirmResult(ConfirmModeDelete, "b", ConfirmResultYes)
		rm := result.(Model)
		if rm.cursor == 1 {
			t.Errorf("cursor should have moved away from deleted session, got %d", rm.cursor)
		}
		if !rm.deletingIDs["b"] {
			t.Error("session 'b' should be marked as deleting")
		}
	})

	t.Run("cursor moves up when deleting last session", func(t *testing.T) {
		m := Model{
			sessions:    makeSessions("a", "b", "c"),
			cursor:      2, // on "c" (last)
			deletingIDs: make(map[string]bool),
			height:      100,
		}
		result, _ := m.dispatchConfirmResult(ConfirmModeDelete, "c", ConfirmResultYes)
		rm := result.(Model)
		if rm.cursor != 1 {
			t.Errorf("expected cursor 1 (previous session), got %d", rm.cursor)
		}
	})
}

// --- pendingCursorRestore ---

// TestPendingCursorRestore covers the "quit-then-relaunch keeps the cursor on
// the right-pane session" flow. NewModelWithTmux arms the flag from the
// restored JIN_CURRENT_SESSION; the first sessionsMsg after startup consumes
// it and slides the cursor onto the matching row. The flag is always cleared
// after the first sessionsMsg (checked once at the bottom, not per case).
func TestPendingCursorRestore(t *testing.T) {
	msg := sessionsMsg([]session.Info{
		{ID: "a", Description: "a"},
		{ID: "b", Description: "b"},
		{ID: "c", Description: "c"},
	})
	tests := []struct {
		name             string
		cursor           int
		currentSessionID string
		pending          bool
		wantCursor       int
	}{
		{"lands on the restored right-pane session", 0, "b", true, 1},
		{"flag clears when the restored session is gone", 0, "gone", true, 0},
		{"consumed flag does not clobber user cursor movement", 2, "b", false, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				cursor:               tt.cursor,
				deletingIDs:          make(map[string]bool),
				height:               100,
				currentSessionID:     tt.currentSessionID,
				pendingCursorRestore: tt.pending,
			}
			result, _ := m.updateListMode(msg)
			rm := result.(Model)
			if rm.cursor != tt.wantCursor {
				t.Errorf("cursor = %d, want %d", rm.cursor, tt.wantCursor)
			}
			if rm.pendingCursorRestore {
				t.Error("pendingCursorRestore should be cleared after sessionsMsg")
			}
		})
	}
}

// --- list header ---

// TestStatusCounts pins the three properties the header depends on: urgency
// order (PERMISSION first, so a narrow pane drops the least urgent groups),
// omission of empty statuses, and one trailing bucket for everything the
// display vocabulary does not know.
func TestStatusCounts(t *testing.T) {
	t.Run("urgency order regardless of input order", func(t *testing.T) {
		// Deliberately scrambled, and IDLE appears twice to prove counting.
		sessions := []session.Info{
			{ID: "1", Status: session.StatusIdle},
			{ID: "2", Status: session.StatusDeleting},
			{ID: "3", Status: session.StatusThinking},
			{ID: "4", Status: session.StatusStopped},
			{ID: "5", Status: session.StatusIdle},
			{ID: "6", Status: session.StatusCreating},
			{ID: "7", Status: session.StatusPermission},
			{ID: "8", Status: session.StatusRunning},
		}
		want := []statusCount{
			{session.StatusPermission, 1},
			{session.StatusThinking, 1},
			{session.StatusRunning, 1},
			{session.StatusCreating, 1},
			{session.StatusIdle, 2},
			{session.StatusStopped, 1},
			{session.StatusDeleting, 1},
		}
		got := statusCounts(sessions)
		if len(got) != len(want) {
			t.Fatalf("statusCounts() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("statusCounts()[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	})

	t.Run("statuses with no sessions are omitted", func(t *testing.T) {
		sessions := []session.Info{
			{ID: "1", Status: session.StatusIdle},
			{ID: "2", Status: session.StatusIdle},
		}
		got := statusCounts(sessions)
		want := []statusCount{{session.StatusIdle, 2}}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("statusCounts() = %v, want %v", got, want)
		}
	})

	t.Run("no sessions yields no groups", func(t *testing.T) {
		if got := statusCounts(nil); len(got) != 0 {
			t.Errorf("statusCounts(nil) = %v, want empty", got)
		}
	})

	t.Run("unrecognised statuses collapse into one trailing bucket", func(t *testing.T) {
		sessions := []session.Info{
			{ID: "1", Status: session.Status("zombie")},
			{ID: "2", Status: session.Status("")},
			{ID: "3", Status: session.StatusIdle},
		}
		got := statusCounts(sessions)
		if len(got) != 2 {
			t.Fatalf("statusCounts() = %v, want 2 groups (idle + unknown bucket)", got)
		}
		if got[0].Status != session.StatusIdle {
			t.Errorf("first group = %v, want the known status first", got[0])
		}
		bucket := got[1]
		if bucket.N != 2 {
			t.Errorf("unknown bucket N = %d, want 2 (both unrecognised statuses)", bucket.N)
		}
		// The bucket covers several distinct values, so the only thing defined
		// for its Status is how getStatusDisplay renders it.
		if _, label, _ := getStatusDisplay(bucket.Status); label != "UNKNOWN" {
			t.Errorf("unknown bucket renders as %q, want UNKNOWN", label)
		}
	})
}

// TestRenderListHeader covers the count line: its shape at a comfortable
// width, the singular wording, and what survives when the pane is too narrow
// to hold every group.
func TestRenderListHeader(t *testing.T) {
	sessions := func(perStatus map[session.Status]int) []session.Info {
		var out []session.Info
		for _, status := range []session.Status{session.StatusPermission, session.StatusThinking, session.StatusIdle} {
			for i := 0; i < perStatus[status]; i++ {
				out = append(out, session.Info{ID: fmt.Sprintf("%s-%d", status, i), Status: status})
			}
		}
		return out
	}

	t.Run("total plus one group per non-empty status", func(t *testing.T) {
		got := renderListHeader(sessions(map[session.Status]int{
			session.StatusPermission: 1,
			session.StatusThinking:   2,
			session.StatusIdle:       4,
		}), 40)
		for _, want := range []string{"7 sessions", "? 1", "⚡ 2", "○ 4"} {
			if !strings.Contains(got, want) {
				t.Errorf("renderListHeader() = %q, want it to contain %q", got, want)
			}
		}
		if w := lipgloss.Width(got); w > 40 {
			t.Errorf("rendered width = %d, want <= 40: %q", w, got)
		}
	})

	t.Run("singular total", func(t *testing.T) {
		got := renderListHeader(sessions(map[session.Status]int{session.StatusIdle: 1}), 40)
		if !strings.Contains(got, "1 session") || strings.Contains(got, "1 sessions") {
			t.Errorf("renderListHeader() = %q, want the singular %q", got, "1 session")
		}
	})

	t.Run("narrow pane drops groups from the least urgent end", func(t *testing.T) {
		all := sessions(map[session.Status]int{
			session.StatusPermission: 1,
			session.StatusThinking:   2,
			session.StatusIdle:       4,
		})
		// "7 sessions" (10) + gap 3 + "? 1" (3) = 16 fits; the next group
		// would need 3 + 4 = 7 more.
		const width = 18
		got := renderListHeader(all, width)
		if w := lipgloss.Width(got); w > width {
			t.Fatalf("rendered width = %d, want <= %d: %q", w, width, got)
		}
		// The total is never dropped: it is the one number always worth the
		// space.
		if !strings.Contains(got, "7 sessions") {
			t.Errorf("renderListHeader() = %q, want the total to survive", got)
		}
		if !strings.Contains(got, "? 1") {
			t.Errorf("renderListHeader() = %q, want PERMISSION to survive as the most urgent group", got)
		}
		for _, dropped := range []string{"⚡", "○"} {
			if strings.Contains(got, dropped) {
				t.Errorf("renderListHeader() = %q, want the less urgent %q dropped", got, dropped)
			}
		}
	})

	t.Run("a width that fits nothing but the total", func(t *testing.T) {
		got := renderListHeader(sessions(map[session.Status]int{session.StatusPermission: 3}), 10)
		if got != helpStyle.Render("3 sessions") {
			t.Errorf("renderListHeader() = %q, want the bare total", got)
		}
	})
}

// --- one ruler ---

// TestOneRuler_TruncationBoundsRenderedWidth is the regression for the defect
// that made the whole row geometry a lie: strings were cut with go-runewidth
// but the composed line was measured with lipgloss, and the two disagree.
//
// Two independent ways they disagree, both reachable in production:
//
//   - Variation-Selector-16 emoji ("✔️", "⚠️" — how an agent routinely ends a
//     message) are 1 cell to runewidth and 2 to lipgloss. A name cut to N
//     runewidth cells could emit 2N real columns, wrap the row, and make one
//     session occupy two physical rows.
//   - go-runewidth reads the East-Asian ambiguous-width table from the process
//     locale at init, so "○ ■ ▶" are 2 cells under LANG=ja_JP.UTF-8 and 1
//     under C.UTF-8, while lipgloss never follows the locale.
//
// Asserting through lipgloss.Width is the point: it is the ruler that decides
// whether a line fits the terminal, so it must be the ruler that bounds it.
func TestOneRuler_TruncationBoundsRenderedWidth(t *testing.T) {
	inputs := []struct {
		name string
		s    string
	}{
		{"VS16 emoji run", strings.Repeat("✔️", 40)},
		{"VS16 emoji mixed with ASCII", strings.Repeat("done ✔️ ", 20)},
		{"warning sign", strings.Repeat("⚠️ alert ", 20)},
		{"locale-ambiguous glyphs", strings.Repeat("○■▶·", 30)},
		{"full-width CJK", strings.Repeat("全角文字列", 30)},
		{"CJK mixed with ASCII", strings.Repeat("全角abc", 30)},
		{"plain ASCII", strings.Repeat("plain-ascii-", 30)},
	}

	for _, in := range inputs {
		t.Run(in.name, func(t *testing.T) {
			for width := 1; width <= 40; width++ {
				if got := truncateString(in.s, width); lipgloss.Width(got) > width {
					t.Errorf("truncateString(%s, %d) is %d columns wide: %q",
						in.name, width, lipgloss.Width(got), got)
				}
				if got := truncateStringFromEnd(in.s, width); lipgloss.Width(got) > width {
					t.Errorf("truncateStringFromEnd(%s, %d) is %d columns wide: %q",
						in.name, width, lipgloss.Width(got), got)
				}
			}
		})
	}
}

// TestOneRuler_RowAndPaneSurviveWideGlyphs drives the two fixed-height
// renderers with the same hostile strings, since a line that overflows there
// wraps in the terminal and silently adds a physical row — breaking
// sessionRowHeight / detailPaneLines and every offset computed from them.
func TestOneRuler_RowAndPaneSurviveWideGlyphs(t *testing.T) {
	hostile := []string{
		strings.Repeat("✔️", 40),
		strings.Repeat("⚠️ ", 30),
		strings.Repeat("○■▶", 30),
		strings.Repeat("絵文字と全角", 20),
	}

	m := Model{deletingIDs: map[string]bool{}}
	for _, s := range hostile {
		sess := session.Info{
			ID: "s", Description: s, Status: session.StatusIdle, AgentKind: s,
			RepoName: s, CurrentBranch: s, LastUserMessage: s, LastAssistantMessage: s,
		}
		for _, width := range []int{minTUIWidth - 2, 28, 38, maxTUIWidth - 2} {
			row := m.renderSession(sess, true, true, width)
			if w := lipgloss.Width(strings.TrimSuffix(row, "\n")); w > width {
				t.Errorf("renderSession at width %d produced %d columns: %q", width, w, row)
			}
			if n := strings.Count(row, "\n"); n != 1 {
				t.Errorf("renderSession at width %d produced %d newlines, want 1", width, n)
			}

			pane := m.renderDetailPane(sess, width)
			if n := detailPaneLineCount(pane); n != detailPaneLines {
				t.Errorf("renderDetailPane at width %d produced %d lines, want %d", width, n, detailPaneLines)
			}
			for i, line := range strings.Split(pane, "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Errorf("renderDetailPane at width %d: line %d is %d columns: %q", width, i, w, line)
				}
			}
		}
	}
}

// --- renderDetailPane ---

// detailPaneLineCount splits a rendered detail pane into its lines.
func detailPaneLineCount(s string) int {
	return len(strings.Split(s, "\n"))
}

// TestRenderDetailPane_FixedHeight is the load-bearing test for the detail
// pane: listAreaLines(), every scroll clamp and every mouse hit-test subtract
// detailPaneLines as a constant, so a pane that grew or shrank with its
// session's contents would silently move the list out from under the cursor.
func TestRenderDetailPane_FixedHeight(t *testing.T) {
	m := Model{deletingIDs: map[string]bool{}}
	const width = 38

	long := strings.Repeat("absurdly-long-value-", 30)

	tests := []struct {
		name string
		sess session.Info
	}{
		{
			name: "fully populated",
			sess: session.Info{
				ID: "s", Description: "plugin registry の crawler を実装して",
				Status: session.StatusThinking, AgentKind: "claude",
				RepoName: "jind-ai", CurrentBranch: "feat/registry",
				LastUserMessage: "次の task を進めて", LastAssistantMessage: "name 衝突ルールを整理しました",
			},
		},
		{name: "entirely empty", sess: session.Info{ID: "s"}},
		{name: "no messages", sess: session.Info{ID: "s", Description: "d", Status: session.StatusIdle, RepoName: "r", CurrentBranch: "b"}},
		{name: "no branch", sess: session.Info{ID: "s", Description: "d", Status: session.StatusIdle, RepoName: "r"}},
		{name: "no repo, falls back to workdir", sess: session.Info{ID: "s", Description: "d", Status: session.StatusIdle, WorkDir: "/tmp/somewhere", CurrentBranch: "b"}},
		{name: "no repo and no branch", sess: session.Info{ID: "s", Description: "d", Status: session.StatusIdle, WorkDir: "/tmp/somewhere"}},
		{name: "locked description", sess: session.Info{ID: "s", Description: "d", DescriptionLocked: true, Status: session.StatusIdle}},
		{
			name: "absurdly long everything",
			sess: session.Info{
				ID: "s", Description: long, Status: session.StatusPermission, AgentKind: long,
				RepoName: long, CurrentBranch: long, LastUserMessage: long, LastAssistantMessage: long,
			},
		},
		{
			name: "full-width CJK everywhere",
			sess: session.Info{
				ID: "s", Description: strings.Repeat("セッション", 20), Status: session.StatusRunning,
				RepoName: strings.Repeat("リポジトリ", 10), CurrentBranch: strings.Repeat("ブランチ", 10),
				LastUserMessage: strings.Repeat("質問", 40), LastAssistantMessage: strings.Repeat("回答", 40),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.renderDetailPane(tt.sess, width)
			if n := detailPaneLineCount(got); n != detailPaneLines {
				t.Errorf("renderDetailPane() produced %d lines, want exactly %d", n, detailPaneLines)
			}
			for i, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Errorf("line %d is %d columns wide, want <= %d: %q", i, w, width, line)
				}
			}
		})
	}
}

// TestRenderDetailPane_BranchPriority pins the width fight on line 4: several
// sessions on one repo is the main use, and there the repo name is identical on
// every row while the branch is the only thing telling them apart.
func TestRenderDetailPane_BranchPriority(t *testing.T) {
	m := Model{deletingIDs: map[string]bool{}}
	sess := session.Info{
		ID: "s", Description: "d", Status: session.StatusIdle,
		RepoName: "jind-ai", CurrentBranch: "feat/plugin-multi-action-dispatch",
	}

	repoBranchLine := func(width int) string {
		return strings.Split(m.renderDetailPane(sess, width), "\n")[3]
	}

	t.Run("wide enough for both", func(t *testing.T) {
		got := repoBranchLine(60)
		if !strings.Contains(got, "jind-ai") {
			t.Errorf("line = %q, want the repo present at a comfortable width", got)
		}
		if !strings.Contains(got, "feat/plugin-multi-action-dispatch") {
			t.Errorf("line = %q, want the branch present in full", got)
		}
	})

	t.Run("narrow keeps the branch tail and sacrifices the repo", func(t *testing.T) {
		const width = 30
		got := repoBranchLine(width)
		if w := lipgloss.Width(got); w > width {
			t.Fatalf("line is %d columns wide, want <= %d: %q", w, width, got)
		}
		// Truncated from the END, so the identifying tail survives: "feat/" is
		// shared by half the branches in the repo and carries nothing.
		if !strings.Contains(got, "multi-action-dispatch") {
			t.Errorf("line = %q, want the branch tail %q to survive", got, "multi-action-dispatch")
		}
		if strings.Contains(got, "jind-ai") {
			t.Errorf("line = %q, want the repo cut back to make room for the branch", got)
		}
	})

	t.Run("a repo that would only fit truncated is dropped whole", func(t *testing.T) {
		// avail 39, branch 33 columns, so the repo is offered 5 — enough for
		// "ji..." and nothing more. A truncated repo name disambiguates
		// nothing (it fits jind-ai and jind-ai-notifier alike) and this pane
		// is the only place it appears, so the columns go to the branch.
		got := repoBranchLine(40)
		if !strings.Contains(got, "feat/plugin-multi-action-dispatch") {
			t.Fatalf("line = %q, want the branch in full at this width", got)
		}
		// Check for the stub the old truncating behaviour produced rather
		// than a bare "ji", which also matches text inside the branch.
		for _, stub := range []string{"ji...", "jind-...", "jind-ai"} {
			if strings.Contains(got, stub) {
				t.Errorf("line = %q, want no repo fragment; found %q", got, stub)
			}
		}
	})

	t.Run("the workdir fallback keeps its tail", func(t *testing.T) {
		// A path is the one thing here worth truncating: its tail identifies
		// it, while its head is shared with everything under the same root.
		noRepo := session.Info{
			ID: "s", Description: "d", Status: session.StatusIdle,
			WorkDir: "/var/opt/some/deeply/nested/place/notes", CurrentBranch: "main",
		}
		got := strings.Split(m.renderDetailPane(noRepo, 30), "\n")[3]
		if !strings.Contains(got, "notes") {
			t.Errorf("line = %q, want the path tail %q to survive", got, "notes")
		}
	})

	t.Run("no branch leaves the repo the whole line", func(t *testing.T) {
		noBranch := sess
		noBranch.CurrentBranch = ""
		got := strings.Split(m.renderDetailPane(noBranch, 30), "\n")[3]
		if !strings.Contains(got, "jind-ai") {
			t.Errorf("line = %q, want the repo shown when there is no branch to compete with", got)
		}
	})
}

// TestRenderDetailPane_FollowsCursor is the spec's headline requirement: the
// detail pane describes the session under the CURSOR, while the tmux pane on
// the right keeps showing whatever the user last attached to. The two notions
// of "current" are deliberately orthogonal, and being able to read a session
// without switching to it is the entire point of the pane.
func TestRenderDetailPane_FollowsCursor(t *testing.T) {
	sessions := []session.Info{
		{ID: "a", Description: "alpha-session", Status: session.StatusIdle, Fleet: session.DefaultFleet},
		{ID: "b", Description: "bravo-session", Status: session.StatusIdle, Fleet: session.DefaultFleet},
		{ID: "c", Description: "charlie-session", Status: session.StatusIdle, Fleet: session.DefaultFleet},
	}
	newModel := func() Model {
		return Model{sessions: sessions, height: 16, width: 40, deletingIDs: map[string]bool{}}
	}

	t.Run("moving the cursor changes the described session", func(t *testing.T) {
		m := newModel()
		for i, want := range []string{"alpha-session", "bravo-session", "charlie-session"} {
			m.cursor = i
			got := m.renderListContent(38)
			if !strings.Contains(got, want) {
				t.Errorf("cursor %d: rendered content does not mention %q", i, want)
			}
			// The name appears once in its list row and once in the detail
			// pane; the other two sessions appear only in their rows.
			if n := strings.Count(got, want); n != 2 {
				t.Errorf("cursor %d: %q appears %d times, want 2 (list row + detail pane)", i, want, n)
			}
		}
	})

	t.Run("the displayed session does not steer the pane", func(t *testing.T) {
		m := newModel()
		m.cursor = 0
		// The user is attached to "charlie" on the right while the cursor sits
		// on "alpha". The pane must describe alpha.
		m.currentSessionID = "c"
		got := m.renderListContent(38)
		if n := strings.Count(got, "alpha-session"); n != 2 {
			t.Errorf("%q appears %d times, want 2 — the detail pane must follow the cursor", "alpha-session", n)
		}
		if n := strings.Count(got, "charlie-session"); n != 1 {
			t.Errorf("%q appears %d times, want 1 — the viewed session gets no detail block", "charlie-session", n)
		}
	})
}

// --- renderListContent ---

// TestRenderListContent_MatchesGeometry is the test the whole design rests on:
// it checks that what renderListContent actually EMITS agrees, row for row,
// with what the geometry functions claim. Everything else — scroll clamping,
// PageUp/PageDown, mouse hit-testing — is arithmetic derived from those
// functions, so if the renderer and the arithmetic disagree by even one row,
// every click below the disagreement lands on the wrong session.
//
// The old code kept that agreement by hand ("keep cardHeight in sync with
// renderSession"); this change replaces the contract with constants, and this
// test is what makes the replacement real rather than asserted.
func TestRenderListContent_MatchesGeometry(t *testing.T) {
	build := func(n int, fleets []string) []session.Info {
		out := make([]session.Info, n)
		for i := range out {
			out[i] = session.Info{
				ID:          fmt.Sprintf("id-%02d", i),
				Description: fmt.Sprintf("session-%02d", i),
				Status:      session.StatusIdle,
				Fleet:       fleets[i%len(fleets)],
			}
		}
		return out
	}

	for _, fleets := range [][]string{{session.DefaultFleet}, {"backend", session.DefaultFleet}} {
		for _, n := range []int{1, 2, 3, 8, 20} {
			for height := 6; height <= 26; height++ {
				for _, withNotice := range []bool{false, true} {
					for _, scroll := range []int{0, 1, 5, 999} {
						m := Model{
							sessions:     build(n, fleets),
							height:       height,
							width:        40,
							deletingIDs:  map[string]bool{},
							scrollOffset: scroll,
						}
						if withNotice {
							m.warning = "heads up"
						}
						m.cursor = min(n-1, 3)
						m.clampScroll()

						lines := strings.Split(m.renderListContent(38), "\n")
						budget := m.noticeLines() + m.headerLines() + m.listAreaLines() + m.detailLines()
						label := fmt.Sprintf("fleets=%d n=%d height=%d notice=%v scroll=%d",
							len(fleets), n, height, withNotice, scroll)

						// Never exceed the budget: an extra row pushes the
						// bottom of the content past the pane, which MaxHeight
						// then clips — silently, and from the wrong end.
						if len(lines) > budget {
							t.Fatalf("%s: rendered %d lines, geometry budgets %d (notices %d + header %d + list %d + detail %d)",
								label, len(lines), budget,
								m.noticeLines(), m.headerLines(), m.listAreaLines(), m.detailLines())
						}
						// When the detail pane is drawn the total must be
						// exact, because that is what pins the pane to the
						// bottom edge. With no detail pane the trailing blanks
						// are the pane style's job, so a short list is fine.
						if m.detailVisible() && len(lines) != budget {
							t.Fatalf("%s: detail pane visible but rendered %d lines, want exactly %d — the pane is not on the bottom edge",
								label, len(lines), budget)
						}

						// The list window must start exactly where the mouse
						// hit-test believes it starts.
						listTop := m.noticeLines() + m.headerLines()
						for idx := range m.sessions {
							top, _ := m.sessionCardTop(idx)
							row := listTop + top - m.scrollOffset
							if row < listTop || row >= listTop+m.listAreaLines() {
								continue // scrolled out of view
							}
							if !strings.Contains(lines[row], m.sessions[idx].Description) {
								t.Fatalf("%s: session %d should be drawn on row %d, got %q",
									label, idx, row, lines[row])
							}
							if got, ok := m.sessionIndexAtRow(row); !ok || got != idx {
								t.Fatalf("%s: sessionIndexAtRow(%d) = (%d, %v), want (%d, true)",
									label, row, got, ok, idx)
							}
						}

						// The detail pane must begin on the first row after the
						// list area, so it sits on the pane's bottom edge.
						if m.detailVisible() {
							ruleRow := listTop + m.listAreaLines()
							if !strings.Contains(lines[ruleRow], "─") {
								t.Fatalf("%s: detail pane rule should be on row %d, got %q",
									label, ruleRow, lines[ruleRow])
							}
							if !strings.Contains(lines[ruleRow+1], m.sessions[m.cursor].Description) {
								t.Fatalf("%s: detail pane should name the cursor's session on row %d, got %q",
									label, ruleRow+1, lines[ruleRow+1])
							}
						}
					}
				}
			}
		}
	}
}

// TestRenderListContent_NoLineExceedsWidth guards the geometry from the one
// failure it cannot see: a line wider than the pane wraps in the terminal, so
// one rendered row becomes two physical rows and every scroll and hit-test
// offset below it is off by one.
func TestRenderListContent_NoLineExceedsWidth(t *testing.T) {
	long := strings.Repeat("wide-", 40)
	cjk := strings.Repeat("全角文字列", 20)

	sessions := []session.Info{
		{ID: "a", Description: long, Status: session.StatusThinking, Fleet: "backend", RepoName: long, CurrentBranch: long, AgentKind: "claude", LastUserMessage: long, LastAssistantMessage: long},
		{ID: "b", Description: cjk, Status: session.StatusPermission, Fleet: "backend", RepoName: cjk, CurrentBranch: cjk, LastUserMessage: cjk, LastAssistantMessage: cjk},
		{ID: "c", Description: "short", DescriptionLocked: true, Status: session.Status("bogus"), Fleet: session.DefaultFleet},
		{ID: "d", Description: "deleting", Status: session.StatusDeleting, Fleet: session.DefaultFleet},
	}

	// minTUIWidth and maxTUIWidth bound the pane the outer tmux gives us; the
	// content sits inside one column of padding on each side.
	for _, paneWidth := range []int{minTUIWidth, 40, maxTUIWidth} {
		for _, height := range []int{8, 14, 16, 30} {
			for cursor := range sessions {
				m := Model{
					sessions:         sessions,
					cursor:           cursor,
					width:            paneWidth,
					height:           height,
					deletingIDs:      map[string]bool{"d": true},
					currentSessionID: "b",
					warning:          "a warning that pushes everything down",
				}
				contentWidth := paneWidth - 2
				for i, line := range strings.Split(m.renderListContent(contentWidth), "\n") {
					if w := lipgloss.Width(line); w > contentWidth {
						t.Fatalf("pane %dx%d cursor %d: line %d is %d columns wide, want <= %d: %q",
							paneWidth, height, cursor, i, w, contentWidth, line)
					}
				}
			}
		}
	}
}

// --- renderSession ---

// TestRenderSession_OneLine pins the invariant the whole list geometry rests
// on: a session row is exactly one line, whatever the session carries. It
// replaces the old hand-maintained "cardHeight matches renderSession" contract
// — there is nothing left to keep in sync, only this to keep true.
func TestRenderSession_OneLine(t *testing.T) {
	longName := strings.Repeat("very-long-session-name-", 20)
	cjkName := strings.Repeat("セッション名前", 10) // full-width: 2 columns per rune

	tests := []struct {
		name string
		m    Model
		sess session.Info
	}{
		{
			name: "plain",
			m:    Model{deletingIDs: map[string]bool{}},
			sess: session.Info{ID: "s", Description: "plain", Status: session.StatusIdle},
		},
		{
			name: "empty description",
			m:    Model{deletingIDs: map[string]bool{}},
			sess: session.Info{ID: "s", Status: session.StatusIdle},
		},
		{
			name: "deleting via the TUI's optimistic mark",
			m:    Model{deletingIDs: map[string]bool{"s": true}},
			sess: session.Info{ID: "s", Description: "going away", Status: session.StatusIdle},
		},
		{
			name: "deleting via the daemon-reported status",
			m:    Model{deletingIDs: map[string]bool{}},
			sess: session.Info{ID: "s", Description: "going away", Status: session.StatusDeleting},
		},
		{
			name: "very long name",
			m:    Model{deletingIDs: map[string]bool{}},
			sess: session.Info{ID: "s", Description: longName, Status: session.StatusThinking},
		},
		{
			name: "full-width name",
			m:    Model{deletingIDs: map[string]bool{}},
			sess: session.Info{ID: "s", Description: cjkName, Status: session.StatusThinking},
		},
		{
			name: "locked description",
			m:    Model{deletingIDs: map[string]bool{}},
			sess: session.Info{ID: "s", Description: longName, DescriptionLocked: true, Status: session.StatusIdle},
		},
		{
			name: "message fields no longer render (they moved to the detail pane)",
			m:    Model{deletingIDs: map[string]bool{}},
			sess: session.Info{
				ID: "s", Description: "chatty", Status: session.StatusIdle,
				CurrentBranch: "feat/x", LastUserMessage: "hi", LastAssistantMessage: "yo",
			},
		},
	}

	// minTUIWidth (30) is the narrowest pane the TUI allows; 40 is typical.
	for _, width := range []int{30, 40} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/width=%d", tt.name, width), func(t *testing.T) {
				got := tt.m.renderSession(tt.sess, false, false, width)
				if n := strings.Count(got, "\n"); n != 1 {
					t.Errorf("renderSession() has %d newlines, want 1: %q", n, got)
				}
				if !strings.HasSuffix(got, "\n") {
					t.Errorf("renderSession() must end with a newline: %q", got)
				}
				// Overflowing by even one column wraps the row in the
				// terminal, which would break the geometry on screen while
				// the returned string still looked like one line.
				if w := lipgloss.Width(strings.TrimSuffix(got, "\n")); w > width {
					t.Errorf("rendered width = %d, want <= %d: %q", w, width, got)
				}
			})
		}
	}
}

// TestRenderSession_Layout pins the fixed column layout: the name always
// starts at sessionRowLead so the list reads as a table, and a locked
// description keeps its '*' marker.
func TestRenderSession_Layout(t *testing.T) {
	m := Model{deletingIDs: map[string]bool{}}
	const width = 40

	t.Run("name starts at the same column whatever the icon width", func(t *testing.T) {
		// "⚡" (THINKING) is 2 columns, "○" (IDLE) is 1; padIcon absorbs the
		// difference.
		for _, status := range []session.Status{session.StatusIdle, session.StatusThinking, session.StatusPermission} {
			sess := session.Info{ID: "s", Description: "name", Status: status}
			line := strings.TrimSuffix(m.renderSession(sess, false, false, width), "\n")
			if idx := strings.Index(line, "name"); idx < 0 {
				t.Fatalf("status %q: name missing from %q", status, line)
			} else if col := lipgloss.Width(line[:idx]); col != sessionRowLead {
				t.Errorf("status %q: name starts at column %d, want %d", status, col, sessionRowLead)
			}
		}
	})

	t.Run("locked description keeps its marker", func(t *testing.T) {
		sess := session.Info{ID: "s", Description: "locked", DescriptionLocked: true, Status: session.StatusIdle}
		if got := m.renderSession(sess, false, false, width); !strings.Contains(got, "locked*") {
			t.Errorf("renderSession() = %q, want the lock marker after the name", got)
		}
	})

	t.Run("empty description gets no lock marker", func(t *testing.T) {
		sess := session.Info{ID: "s", DescriptionLocked: true, Status: session.StatusIdle}
		if got := m.renderSession(sess, false, false, width); strings.Contains(got, "*") {
			t.Errorf("renderSession() = %q, want no lock marker on an empty name", got)
		}
	})
}

// TestRenderSession_Indicators verifies the two orthogonal indicators:
//   - selected → blue '▎' cursor bar in the row's first column
//   - viewed   → subdued row background (detectable via presence of an ANSI
//     SGR bg code in the rendered output)
func TestRenderSession_Indicators(t *testing.T) {
	sess := session.Info{
		ID:          "test-id",
		Description: "test-session",
		Status:      session.StatusIdle,
	}
	m := Model{}
	width := 40

	// The SGR sequence "\x1b[48" is the prefix for any background color set
	// (48;5;n for 256-color, 48;2;R;G;B for truecolor). Its presence is a
	// reliable signal that the viewed background was applied.
	const bgSGR = "\x1b[48"

	tests := []struct {
		name       string
		selected   bool
		viewed     bool
		wantBar    bool
		wantViewBg bool
	}{
		{name: "neither", selected: false, viewed: false, wantBar: false, wantViewBg: false},
		{name: "selected only", selected: true, viewed: false, wantBar: true, wantViewBg: false},
		{name: "viewed only", selected: false, viewed: true, wantBar: false, wantViewBg: true},
		{name: "selected and viewed", selected: true, viewed: true, wantBar: true, wantViewBg: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := strings.TrimSuffix(m.renderSession(sess, tt.selected, tt.viewed, width), "\n")
			if row == "" {
				t.Fatal("renderSession returned an empty row")
			}
			hasBar := strings.Contains(row, "▎")
			if hasBar != tt.wantBar {
				t.Errorf("cursor bar present = %v, want %v (row: %q)", hasBar, tt.wantBar, row)
			}
			hasViewBg := strings.Contains(row, bgSGR)
			if hasViewBg != tt.wantViewBg {
				t.Errorf("viewed background present = %v, want %v (row: %q)", hasViewBg, tt.wantViewBg, row)
			}
			// The background must reach the end of the row: with the blank
			// spacer between cards gone, a short background would leave a
			// ragged right edge instead of a continuous band.
			if tt.wantViewBg && !strings.HasSuffix(row, "\x1b[0m") {
				t.Errorf("viewed row does not end in a styled segment: %q", row)
			}
		})
	}
}

// --- dispatchAction / currentCursorSessionID / writeCursorEnv ---
//
// Note: the tui package has no mock for *tmux.Client or *daemon.Client — both
// are concrete structs with unexported fields, and expanding an interface just
// for these tests would balloon this task well beyond R4. The tests below
// cover the routing/guard logic reachable without live clients; the real
// side-effect wiring (ZoomPane, SetEnvironment, PluginRun) is exercised by
// the manual verification steps (see 03_todo.md V-006/V-008/V-009).

// TestDispatchAction_CoreRouting_TogglePane verifies that IDTogglePane routes
// to handleTogglePane and that the tmuxClient=nil guard keeps it a safe
// no-op (the call the palette makes into an unwired Model).
func TestDispatchAction_CoreRouting_TogglePane(t *testing.T) {
	m := Model{deletingIDs: map[string]bool{}}
	next, cmd := m.dispatchAction(action.IDTogglePane)
	if cmd != nil {
		t.Errorf("expected nil Cmd from toggle-pane on unwired model, got %T", cmd)
	}
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", next)
	}
	if nm.err != nil {
		t.Errorf("expected no err, got %v", nm.err)
	}
}

// TestDispatchAction_CoreRouting_SessionFilter verifies that IDSessionFilter
// routes to handleSessionFilter and that the tmuxClient=nil guard keeps it
// a safe no-op — the palette must be able to dispatch the action even on
// an unwired Model (e.g. before the outer tmux binding has been applied).
func TestDispatchAction_CoreRouting_SessionFilter(t *testing.T) {
	m := Model{deletingIDs: map[string]bool{}}
	next, cmd := m.dispatchAction(action.IDSessionFilter)
	if cmd != nil {
		t.Errorf("expected nil Cmd from session-filter on unwired model, got %T", cmd)
	}
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", next)
	}
	if nm.err != nil {
		t.Errorf("expected no err, got %v", nm.err)
	}
}

// TestDispatchAction_PluginRouting verifies the 3-segment plugin action ID
// routes to handlePluginRun and that the m.client=nil guard prevents any
// panic / spurious m.err when the daemon is unavailable.
func TestDispatchAction_PluginRouting(t *testing.T) {
	m := Model{deletingIDs: map[string]bool{}}
	next, cmd := m.dispatchAction(action.PluginActionID("notifier", "send-dm"))
	if cmd != nil {
		t.Errorf("expected nil Cmd, got %T", cmd)
	}
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", next)
	}
	if nm.err != nil {
		t.Errorf("expected nil m.err on nil-client no-op, got %v", nm.err)
	}
}

// TestDispatchAction_LegacyTwoSegmentPluginIDIgnored guards the "silently
// ignore" contract for stale 2-segment IDs left in the tmux env by an older
// binary — the palette expects the 3-segment form now.
func TestDispatchAction_LegacyTwoSegmentPluginIDIgnored(t *testing.T) {
	sentinel := errors.New("pre-existing error")
	m := Model{deletingIDs: map[string]bool{}, err: sentinel}
	next, cmd := m.dispatchAction(action.PluginIDPrefix + "notifier")
	if cmd != nil {
		t.Errorf("expected nil Cmd, got %T", cmd)
	}
	nm := next.(Model)
	if !errors.Is(nm.err, sentinel) {
		t.Errorf("legacy 2-segment ID must not touch m.err, got %v", nm.err)
	}
}

// TestDispatchAction_UnknownID guards the "silently ignore" contract — a
// stale JIN_ACTION_ID env value must not wedge the TUI.
func TestDispatchAction_UnknownID(t *testing.T) {
	sentinel := errors.New("pre-existing error")
	m := Model{deletingIDs: map[string]bool{}, err: sentinel}
	next, cmd := m.dispatchAction("core:bogus")
	if cmd != nil {
		t.Errorf("expected nil Cmd for unknown id, got %T", cmd)
	}
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", next)
	}
	if !errors.Is(nm.err, sentinel) {
		t.Errorf("dispatchAction should not touch m.err on unknown id, got %v", nm.err)
	}
}

// TestConfirmRequestForAction is where the kill/delete routing is actually
// asserted: the confirmation moved into a tmux popup, so dispatchAction's
// only observable effect for those IDs is an env write a test Model cannot
// see (tmuxClient=nil). dispatchAction hands the action ID straight to this
// resolver, so pinning the ID→dialog mapping here pins the routing — and the
// worktree upgrade of delete, which is the part most likely to break.
func TestConfirmRequestForAction(t *testing.T) {
	plain := []session.Info{{ID: "s1", Description: "one"}}
	worktree := []session.Info{{ID: "s1", Description: "one", IsWorktree: true}}

	cases := []struct {
		name     string
		actionID string
		sessions []session.Info
		wantMode string
	}{
		{"kill", action.IDKill, plain, ConfirmModeKill},
		{"kill on a worktree session is still a plain kill", action.IDKill, worktree, ConfirmModeKill},
		{"delete", action.IDDelete, plain, ConfirmModeDelete},
		{"delete on a worktree session asks about the worktree", action.IDDelete, worktree, ConfirmModeDeleteWorktree},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{sessions: tt.sessions, cursor: 0, deletingIDs: map[string]bool{}}
			req, ok := m.confirmRequestForAction(tt.actionID)
			if !ok {
				t.Fatalf("confirmRequestForAction(%q) = not ok, want a request", tt.actionID)
			}
			if req.mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", req.mode, tt.wantMode)
			}
			if req.targetID != "s1" {
				t.Errorf("targetID = %q, want %q", req.targetID, "s1")
			}
			if req.targetDesc != "one" {
				t.Errorf("targetDesc = %q, want %q", req.targetDesc, "one")
			}
		})
	}
}

// TestConfirmRequestForAction_NoTarget covers the guards that keep a prompt
// from naming a session that is not there (or is on its way out), plus the
// non-destructive action IDs that must not resolve to a dialog at all.
func TestConfirmRequestForAction_NoTarget(t *testing.T) {
	sessions := []session.Info{{ID: "s1", Description: "one"}}
	cases := []struct {
		name        string
		actionID    string
		sessions    []session.Info
		cursor      int
		deletingIDs map[string]bool
	}{
		{"empty list", action.IDDelete, nil, 0, map[string]bool{}},
		{"cursor past end", action.IDDelete, sessions, 3, map[string]bool{}},
		{"target already deleting", action.IDDelete, sessions, 0, map[string]bool{"s1": true}},
		{"non-destructive action", action.IDRefresh, sessions, 0, map[string]bool{}},
		{"unknown action", "core:bogus", sessions, 0, map[string]bool{}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{sessions: tt.sessions, cursor: tt.cursor, deletingIDs: tt.deletingIDs}
			if req, ok := m.confirmRequestForAction(tt.actionID); ok {
				t.Errorf("confirmRequestForAction = %+v, want no request", req)
			}
		})
	}
}

// TestDispatchAction_DestructiveActionsAreSafeWhenUnwired pins the degraded
// path the palette can hit on an unwired Model: IDKill / IDDelete must reach
// handleDestructiveAction and stop at the tmuxClient=nil guard rather than
// panicking or surfacing an error. The mapping itself is covered by
// TestConfirmRequestForAction.
func TestDispatchAction_DestructiveActionsAreSafeWhenUnwired(t *testing.T) {
	for _, id := range []string{action.IDKill, action.IDDelete} {
		t.Run(id, func(t *testing.T) {
			m := Model{
				sessions:    []session.Info{{ID: "s1", Description: "one"}},
				cursor:      0,
				deletingIDs: map[string]bool{},
			}
			next, cmd := m.dispatchAction(id)
			if cmd != nil {
				t.Errorf("expected nil Cmd (the popup is opened synchronously), got %T", cmd)
			}
			nm, ok := next.(Model)
			if !ok {
				t.Fatalf("expected Model, got %T", next)
			}
			if nm.err != nil {
				t.Errorf("expected no err, got %v", nm.err)
			}
			if len(nm.deletingIDs) != 0 {
				t.Errorf("opening a confirmation must not delete anything, got %v", nm.deletingIDs)
			}
		})
	}
}

// TestDispatchAction_RefreshReturnsCmd asserts IDRefresh routes to handleRefresh
// by observing the non-nil fetchSessions Cmd it returns.
func TestDispatchAction_RefreshReturnsCmd(t *testing.T) {
	m := Model{}
	_, cmd := m.dispatchAction(action.IDRefresh)
	if cmd == nil {
		t.Fatal("expected non-nil Cmd for IDRefresh (fetchSessions)")
	}
}

// --- dispatchConfirmResult ---

// TestDispatchConfirmResult_Routing pins the mode/result table, and in
// particular the half of it that must do nothing: dispatchConfirmResult is fed
// from tmux env that can go stale, and every acting branch destroys a session.
// Cases are separated by the Cmd (issued vs not) plus the state each acting
// branch leaves behind; which flags reach daemon.Client.Delete is pinned by
// TestConfirmFlagsOnWire below.
func TestDispatchConfirmResult_Routing(t *testing.T) {
	cases := []struct {
		name            string
		mode            string
		targetID        string
		result          string
		wantCmd         bool
		wantDeleting    bool
		wantProcessing  string
		wantPendingKill string
	}{
		{name: "kill yes", mode: ConfirmModeKill, targetID: "s1", result: ConfirmResultYes,
			wantCmd: true, wantProcessing: "Stopping...", wantPendingKill: "s1"},
		{name: "delete yes", mode: ConfirmModeDelete, targetID: "s1", result: ConfirmResultYes,
			wantCmd: true, wantDeleting: true},
		{name: "delete_worktree yes (session only)", mode: ConfirmModeDeleteWorktree, targetID: "s1", result: ConfirmResultYes,
			wantCmd: true, wantDeleting: true},
		{name: "delete_worktree worktree", mode: ConfirmModeDeleteWorktree, targetID: "s1", result: ConfirmResultWorktree,
			wantCmd: true, wantDeleting: true},
		{name: "force yes", mode: ConfirmModeDeleteWorktreeForce, targetID: "s1", result: ConfirmResultForceYes,
			wantCmd: true, wantDeleting: true},
		{name: "force no falls back to session only", mode: ConfirmModeDeleteWorktreeForce, targetID: "s1", result: ConfirmResultForceNo,
			wantCmd: true, wantDeleting: true},

		// Everything below must leave the session alone.
		{name: "kill no", mode: ConfirmModeKill, targetID: "s1", result: ConfirmResultNo},
		{name: "delete no", mode: ConfirmModeDelete, targetID: "s1", result: ConfirmResultNo},
		{name: "delete_worktree no", mode: ConfirmModeDeleteWorktree, targetID: "s1", result: ConfirmResultNo},
		{name: "worktree answer to a plain delete prompt", mode: ConfirmModeDelete, targetID: "s1", result: ConfirmResultWorktree},
		{name: "force answer to a plain delete prompt", mode: ConfirmModeDelete, targetID: "s1", result: ConfirmResultForceYes},
		{name: "plain yes to a force prompt", mode: ConfirmModeDeleteWorktreeForce, targetID: "s1", result: ConfirmResultYes},
		{name: "unknown mode", mode: "delete_everything", targetID: "s1", result: ConfirmResultYes},
		{name: "unknown result", mode: ConfirmModeDelete, targetID: "s1", result: "sure"},
		{name: "empty target", mode: ConfirmModeDelete, targetID: "", result: ConfirmResultYes},
		{name: "all empty", mode: "", targetID: "", result: ""},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				sessions:    []session.Info{{ID: "s1", Description: "one", IsWorktree: true}},
				cursor:      0,
				deletingIDs: map[string]bool{},
				height:      100,
			}
			next, cmd := m.dispatchConfirmResult(tt.mode, tt.targetID, tt.result)
			nm := next.(Model)
			if (cmd != nil) != tt.wantCmd {
				t.Errorf("Cmd issued = %v, want %v", cmd != nil, tt.wantCmd)
			}
			if nm.deletingIDs["s1"] != tt.wantDeleting {
				t.Errorf("deletingIDs[s1] = %v, want %v", nm.deletingIDs["s1"], tt.wantDeleting)
			}
			if nm.processingMsg != tt.wantProcessing {
				t.Errorf("processingMsg = %q, want %q", nm.processingMsg, tt.wantProcessing)
			}
			if nm.pendingKillID != tt.wantPendingKill {
				t.Errorf("pendingKillID = %q, want %q", nm.pendingKillID, tt.wantPendingKill)
			}
		})
	}
}

// fakeDaemon is a minimal daemon speaking the real protocol over a real Unix
// socket, so a tea.Cmd built by the Model can be run for its wire effect.
// Modelled on internal/daemon's fakeServerWith; it lives here because the
// distinction this pins — which booleans reach daemon.Client.Delete — is not
// observable in the Model's own state.
type fakeDaemon struct {
	mu       sync.Mutex
	requests []daemon.Request
}

func startFakeDaemon(t *testing.T) (*fakeDaemon, *daemon.Client) {
	t.Helper()
	sock := testutil.SocketPath(t, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	d := &fakeDaemon{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			d.serve(conn)
		}
	}()
	return d, daemon.NewClient(sock)
}

func (d *fakeDaemon) serve(conn net.Conn) {
	defer conn.Close()
	var req daemon.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.mu.Unlock()

	resp := daemon.Response{ProtocolVersion: daemon.ProtocolVersion, Success: true}
	if req.Action == "list" {
		resp.Data = json.RawMessage("[]")
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

// first returns the request that opened the exchange — the kill or delete.
// (Both Cmds follow up with a "list" to refresh the model.)
func (d *fakeDaemon) first(t *testing.T) daemon.Request {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		t.Fatal("no request reached the daemon")
	}
	return d.requests[0]
}

// TestConfirmFlagsOnWire pins what each approved answer actually asks the
// daemon to destroy. removeWorktree and force are invisible in Model state —
// every delete branch looks identical from the outside — yet force is the flag
// that discards uncommitted work, so the only place the distinction survives
// is the request on the socket.
func TestConfirmFlagsOnWire(t *testing.T) {
	cases := []struct {
		name         string
		mode         string
		result       string
		wantAction   string
		wantWorktree bool
		wantForce    bool
	}{
		{"kill", ConfirmModeKill, ConfirmResultYes, "kill", false, false},
		{"delete", ConfirmModeDelete, ConfirmResultYes, "delete", false, false},
		{"worktree prompt, session only", ConfirmModeDeleteWorktree, ConfirmResultYes, "delete", false, false},
		{"worktree prompt, with worktree", ConfirmModeDeleteWorktree, ConfirmResultWorktree, "delete", true, false},
		{"force prompt, forced", ConfirmModeDeleteWorktreeForce, ConfirmResultForceYes, "delete", true, true},
		{"force prompt, declined", ConfirmModeDeleteWorktreeForce, ConfirmResultForceNo, "delete", false, false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fake, client := startFakeDaemon(t)
			m := Model{
				client:      client,
				sessions:    []session.Info{{ID: "s1", Description: "one", IsWorktree: true}},
				deletingIDs: map[string]bool{},
				height:      100,
			}

			_, cmd := m.dispatchConfirmResult(tt.mode, "s1", tt.result)
			if cmd == nil {
				t.Fatalf("%s/%s issued no Cmd", tt.mode, tt.result)
			}
			cmd()

			got := fake.first(t)
			if got.Action != tt.wantAction {
				t.Fatalf("action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.Action == "kill" {
				var kr daemon.IDRequest
				if err := json.Unmarshal(got.Data, &kr); err != nil {
					t.Fatalf("decode kill request: %v", err)
				}
				if kr.ID != "s1" {
					t.Errorf("kill ID = %q, want s1", kr.ID)
				}
				return
			}
			var dr daemon.DeleteRequest
			if err := json.Unmarshal(got.Data, &dr); err != nil {
				t.Fatalf("decode delete request: %v", err)
			}
			if dr.ID != "s1" {
				t.Errorf("delete ID = %q, want s1", dr.ID)
			}
			if dr.RemoveWorktree != tt.wantWorktree {
				t.Errorf("remove_worktree = %v, want %v", dr.RemoveWorktree, tt.wantWorktree)
			}
			if dr.ForceRemoveWorktree != tt.wantForce {
				t.Errorf("force_remove_worktree = %v, want %v", dr.ForceRemoveWorktree, tt.wantForce)
			}
		})
	}
}

// TestHandleEnvTick_ConfirmAnswerReachesDaemon walks the whole parent half of
// the handshake in one go: an answer sitting in the tmux env is drained,
// routed, and turned into the real delete on the daemon socket.
//
// The unit tests around it each pin one link (consumeEnvRequests reads the
// keys, dispatchConfirmResult picks the branch, TestConfirmFlagsOnWire pins the
// flags) but none of them pins that the links are connected: with the confirm
// block deleted from the tick, every one of them still passes and the feature
// is simply off. This test is the one that notices.
func TestHandleEnvTick_ConfirmAnswerReachesDaemon(t *testing.T) {
	fake, client := startFakeDaemon(t)
	env := map[string]string{
		EnvConfirmResult:     ConfirmResultWorktree,
		EnvConfirmMode:       ConfirmModeDeleteWorktree,
		EnvConfirmTargetID:   "s1",
		EnvConfirmTargetDesc: "one",
		// An unresolvable focus request rides along on the same tick, which is
		// what pins the confirm block's *position*: it makes the fast path below
		// it take its early return (moveCursorToSession fails on an ID the list
		// does not have). Reorder the two and the approved delete never reaches
		// the wire — the tick returns the fetch instead. Without this key the
		// fast path always succeeds and the ordering is unguarded.
		"JIN_FOCUS_SESSION": "not-in-list",
	}
	var unset []string
	m := Model{
		client:      client,
		sessions:    []session.Info{{ID: "s1", Description: "one", IsWorktree: true}},
		deletingIDs: map[string]bool{},
		height:      100,
	}

	next, cmd := m.handleEnvTick(env, func(key string) {
		unset = append(unset, key)
		delete(env, key)
	})
	if cmd == nil {
		t.Fatal("an approved delete waiting in the env produced no Cmd")
	}
	if !next.(Model).deletingIDs["s1"] {
		t.Error("deletingIDs[s1] = false, want true (the list greys the target out while the delete runs)")
	}

	// The tick re-arms itself alongside the work it dispatched, so the Cmd is
	// a Batch. Running its members is the only way to tell them apart; one of
	// them is envTickCmd's 250ms timer, which is why this test is not instant.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Cmd returned %T, want tea.BatchMsg (the re-armed tick plus the delete)", msg)
	}
	for _, c := range batch {
		c()
	}

	got := fake.first(t)
	if got.Action != "delete" {
		t.Fatalf("action on the wire = %q, want %q", got.Action, "delete")
	}
	var dr daemon.DeleteRequest
	if err := json.Unmarshal(got.Data, &dr); err != nil {
		t.Fatalf("decode delete request: %v", err)
	}
	if dr.ID != "s1" || !dr.RemoveWorktree || dr.ForceRemoveWorktree {
		t.Errorf("delete request = %+v, want id=s1 remove_worktree=true force=false", dr)
	}
	// The answer and its prompt must leave the tmux env on this same tick, or
	// the next TUI process replays the approval against whatever it finds.
	assertConsumedSet(t, unset, "JIN_FOCUS_SESSION", EnvConfirmResult, EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc)
}

// --- display-pane hand-off around delete ---
//
// These pin the state machine that drives the pane off a session being
// deleted. The tmux-side half (resetting the pane label) needs a live server
// and lives in model_tmux_e2e_test.go; see docs/gotchas.md for why both
// halves are needed.

// TestDeleteSession_MovesDisplayOffTarget pins that the pane stops claiming a
// session at the moment the delete is issued, not a poll after it lands.
func TestDeleteSession_MovesDisplayOffTarget(t *testing.T) {
	tests := []struct {
		name     string
		sessions []session.Info
		shown    string
		want     string
	}{
		{
			name:     "deleting the only session drops the pane to the placeholder",
			sessions: []session.Info{{ID: "s1", Description: "one"}},
			shown:    "s1",
			want:     placeholderSessionID,
		},
		{
			name: "deleting the shown session hands the pane to the survivor",
			sessions: []session.Info{
				{ID: "s1", Description: "one"},
				{ID: "s2", Description: "two", Status: session.StatusIdle, TmuxWindowName: "jin-s2"},
			},
			shown: "s1",
			// switchToSession is a no-op without an outer tmux client, so the
			// observable part here is only that the pane stopped claiming s1.
			want: "",
		},
		{
			name: "deleting a session the pane is not showing leaves it alone",
			sessions: []session.Info{
				{ID: "s1", Description: "one"},
				{ID: "s2", Description: "two", Status: session.StatusIdle, TmuxWindowName: "jin-s2"},
			},
			shown: "s2",
			want:  "s2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := startFakeDaemon(t)
			m := Model{
				client:           client,
				sessions:         tt.sessions,
				cursor:           0,
				deletingIDs:      map[string]bool{},
				height:           100,
				currentSessionID: tt.shown,
			}
			next, cmd := m.deleteSession("s1", false, false)
			if cmd == nil {
				t.Fatal("deleteSession issued no Cmd")
			}
			if got := next.(Model).currentSessionID; got != tt.want {
				t.Errorf("currentSessionID = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestShowCursorSession_PlaceholderWhenCursorHasNoTarget covers the two ways
// the cursor can point at nothing attachable. The second one is the reason the
// deleting check cannot be dropped: without it the pane re-attaches to the
// session the daemon is in the middle of tearing down.
func TestShowCursorSession_PlaceholderWhenCursorHasNoTarget(t *testing.T) {
	tests := []struct {
		name        string
		sessions    []session.Info
		deletingIDs map[string]bool
	}{
		{
			name:        "empty list",
			sessions:    nil,
			deletingIDs: map[string]bool{},
		},
		{
			name:        "the only session is being deleted",
			sessions:    []session.Info{{ID: "s1", Description: "one"}},
			deletingIDs: map[string]bool{"s1": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				sessions:         tt.sessions,
				cursor:           0,
				deletingIDs:      tt.deletingIDs,
				height:           100,
				currentSessionID: "s1",
			}
			m.showCursorSession(false)
			if m.currentSessionID != placeholderSessionID {
				t.Errorf("currentSessionID = %q, want %q", m.currentSessionID, placeholderSessionID)
			}
		})
	}
}

// TestDisplaysLiveSession pins the predicate the sessionsMsg tail uses to
// decide whether the pane needs re-pointing. The placeholder case is the one
// that keeps a re-attach possible: treat the sentinel as "live" and a session
// created after the list went empty would never reach the pane.
func TestDisplaysLiveSession(t *testing.T) {
	sessions := []session.Info{{ID: "s1"}, {ID: "s2"}}
	tests := []struct {
		name  string
		shown string
		want  bool
	}{
		{"showing a session still in the list", "s1", true},
		{"showing a session that vanished", "gone", false},
		{"showing the placeholder", placeholderSessionID, false},
		{"nothing decided yet", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{sessions: sessions, currentSessionID: tt.shown}
			if got := m.displaysLiveSession(); got != tt.want {
				t.Errorf("displaysLiveSession() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSessionsMsg_LastDeleteSettlesOnPlaceholder walks the tail of the flow:
// the record is gone from the list, so the grey-out clears and the pane must
// end on the placeholder rather than still naming the session that just left.
func TestSessionsMsg_LastDeleteSettlesOnPlaceholder(t *testing.T) {
	m := Model{
		sessions:         []session.Info{{ID: "s1", Description: "one"}},
		cursor:           0,
		deletingIDs:      map[string]bool{"s1": true},
		height:           100,
		currentSessionID: "s1",
	}
	next, _ := m.updateListMode(sessionsMsg(nil))
	nm := next.(Model)
	if nm.currentSessionID != placeholderSessionID {
		t.Errorf("currentSessionID = %q, want %q", nm.currentSessionID, placeholderSessionID)
	}
	if len(nm.deletingIDs) != 0 {
		t.Errorf("deletingIDs = %v, want empty (the record is gone)", nm.deletingIDs)
	}
}

// TestSessionsMsg_KillArmSurvivesStaleList is the regression this arm exists
// for. The session poll runs on its own clock, so a List() that started before
// the daemon saw the Kill delivers a snapshot where the target is still
// running. Spending the reswitch on that snapshot re-points the pane at the
// session being killed, and the sessionsMsg carrying the real outcome then
// finds nothing to act on: the killed record stays in the list, so
// displaysLiveSession() reports the pane as settled.
func TestSessionsMsg_KillArmSurvivesStaleList(t *testing.T) {
	running := []session.Info{
		{ID: "s1", Description: "one", Status: session.StatusRunning, TmuxWindowName: "sess-s1"},
	}
	m := Model{
		sessions:         running,
		cursor:           0,
		deletingIDs:      map[string]bool{},
		height:           100,
		currentSessionID: "s1",
		pendingKillID:    "s1",
	}

	next, _ := m.updateListMode(sessionsMsg(running))
	stale := next.(Model)
	if stale.pendingKillID != "s1" {
		t.Fatalf("pendingKillID = %q, want %q — a snapshot that predates the kill spent the reswitch",
			stale.pendingKillID, "s1")
	}
	if stale.currentSessionID != "s1" {
		t.Errorf("currentSessionID = %q, want %q — the pane was re-pointed off a stale snapshot",
			stale.currentSessionID, "s1")
	}

	// The kill's own List(): the record is still listed, now stopped.
	next, _ = stale.updateListMode(sessionsMsg([]session.Info{
		{ID: "s1", Description: "one", Status: session.StatusStopped},
	}))
	fresh := next.(Model)
	if fresh.pendingKillID != "" {
		t.Errorf("pendingKillID = %q, want empty — the list confirmed the kill", fresh.pendingKillID)
	}
	if fresh.currentSessionID == "s1" {
		t.Error("the pane still claims the killed session; its attach is dead underneath")
	}
}

// TestSessionsMsg_KillArmSurvivesFocusFastPath covers the other way the arm
// could be spent without the re-point it was armed for: the focus fast path
// returns from the middle of the sessionsMsg branch, so anything resolved
// above it is silently dropped on a list that arrives while a create is still
// looking for its new session.
func TestSessionsMsg_KillArmSurvivesFocusFastPath(t *testing.T) {
	m := Model{
		sessions:         []session.Info{{ID: "s1", Description: "one", Status: session.StatusRunning}},
		cursor:           0,
		deletingIDs:      map[string]bool{},
		height:           100,
		currentSessionID: "s1",
		pendingKillID:    "s1",
		focusSessionID:   "s2", // created, not in the list yet
	}
	// The same snapshot both times: what changes is that focus has given up by
	// the second one, so it is the arm that decides whether the pane moves.
	stopped := sessionsMsg([]session.Info{{ID: "s1", Description: "one", Status: session.StatusStopped}})

	next, _ := m.updateListMode(stopped)
	focused := next.(Model)
	if focused.currentSessionID != "s1" {
		t.Fatalf("currentSessionID = %q, want %q — the focus path re-pointed the pane",
			focused.currentSessionID, "s1")
	}
	if focused.pendingKillID != "s1" {
		t.Fatalf("pendingKillID = %q, want %q — the arm was spent on a list that returned early",
			focused.pendingKillID, "s1")
	}

	// Focus gave up, so the next list is free to settle the pane.
	next, _ = focused.updateListMode(stopped)
	settled := next.(Model)
	if settled.pendingKillID != "" {
		t.Errorf("pendingKillID = %q, want empty", settled.pendingKillID)
	}
	if settled.currentSessionID == "s1" {
		t.Error("the pane still claims the killed session; its attach is dead underneath")
	}
}

// TestSessionsMsg_KillOfHiddenSessionKeepsAttach pins how far force reaches.
// A kill only makes the pane *look* settled when the pane is showing the
// session that was killed; forcing on any other kill tears down a live attach
// the user is watching and rebuilds it as a visible flash.
func TestSessionsMsg_KillOfHiddenSessionKeepsAttach(t *testing.T) {
	m := Model{
		sessions: []session.Info{
			{ID: "s1", Description: "one", Status: session.StatusRunning},
			{ID: "s2", Description: "two", Status: session.StatusRunning, TmuxWindowName: "sess-s2"},
		},
		cursor:           1, // parked on the session the pane shows
		deletingIDs:      map[string]bool{},
		height:           100,
		currentSessionID: "s2",
		pendingKillID:    "s1",
	}
	next, _ := m.updateListMode(sessionsMsg([]session.Info{
		{ID: "s1", Description: "one", Status: session.StatusStopped},
		{ID: "s2", Description: "two", Status: session.StatusRunning, TmuxWindowName: "sess-s2"},
	}))
	nm := next.(Model)
	if nm.pendingKillID != "" {
		t.Errorf("pendingKillID = %q, want empty — the kill landed", nm.pendingKillID)
	}
	if nm.currentSessionID != "s2" {
		t.Errorf("currentSessionID = %q, want %q — s2's live attach was torn down over a kill of s1",
			nm.currentSessionID, "s2")
	}
}

// TestSessionsMsg_OverlappingKillsStillFreeThePane pins why force keys off the
// pane rather than off the arm. One arm slot keeps only the newest kill, so a
// second kill can confirm while the pane is still parked on the first one's
// victim — and "was this the newest kill's target?" answers no there, leaving
// the pane on a dead attach that nothing else will ever re-point.
func TestSessionsMsg_OverlappingKillsStillFreeThePane(t *testing.T) {
	both := []session.Info{
		{ID: "s1", Description: "one", Status: session.StatusStopped},
		{ID: "s2", Description: "two", Status: session.StatusStopped},
	}
	m := Model{
		sessions:         both,
		cursor:           0, // back on the first victim, which the pane still shows
		deletingIDs:      map[string]bool{},
		height:           100,
		currentSessionID: "s1",
		pendingKillID:    "s2", // the second kill overwrote the first one's arm
	}
	next, _ := m.updateListMode(sessionsMsg(both))
	nm := next.(Model)
	if nm.pendingKillID != "" {
		t.Errorf("pendingKillID = %q, want empty — the kill landed", nm.pendingKillID)
	}
	if nm.currentSessionID == "s1" {
		t.Error("the pane still claims s1, whose attach died with the kill that lost the arm slot")
	}
}

// TestSessionsMsg_KillArmClearsWhenRecordVanishes keeps the arm from
// outliving its kill in the one case where the target never reports a stopped
// status: the record left the list entirely (deleted from elsewhere).
func TestSessionsMsg_KillArmClearsWhenRecordVanishes(t *testing.T) {
	m := Model{
		sessions:         []session.Info{{ID: "s1", Description: "one", Status: session.StatusRunning}},
		cursor:           0,
		deletingIDs:      map[string]bool{},
		height:           100,
		currentSessionID: "s1",
		pendingKillID:    "s1",
	}
	next, _ := m.updateListMode(sessionsMsg(nil))
	nm := next.(Model)
	if nm.pendingKillID != "" {
		t.Errorf("pendingKillID = %q, want empty — the record is gone, nothing will ever confirm it", nm.pendingKillID)
	}
	if nm.currentSessionID != placeholderSessionID {
		t.Errorf("currentSessionID = %q, want %q", nm.currentSessionID, placeholderSessionID)
	}
}

// TestSessionsMsg_KillArmSurvivesListError covers the other order the daemon
// can answer in: the kill's own List() failed, so the outcome arrives on a
// later poll instead. The error must not disarm the pending reswitch.
func TestSessionsMsg_KillArmSurvivesListError(t *testing.T) {
	m := Model{
		sessions:         []session.Info{{ID: "s1", Description: "one", Status: session.StatusRunning}},
		cursor:           0,
		deletingIDs:      map[string]bool{},
		height:           100,
		currentSessionID: "s1",
		pendingKillID:    "s1",
	}
	next, _ := m.updateListMode(errMsg(errors.New("list failed")))
	errored := next.(Model)
	if errored.pendingKillID != "s1" {
		t.Fatalf("pendingKillID = %q, want %q — an unrelated error disarmed the reswitch",
			errored.pendingKillID, "s1")
	}

	next, _ = errored.updateListMode(sessionsMsg([]session.Info{
		{ID: "s1", Description: "one", Status: session.StatusStopped},
	}))
	if got := next.(Model).pendingKillID; got != "" {
		t.Errorf("pendingKillID = %q, want empty — the next poll confirmed the kill", got)
	}
}

// TestSessionsMsg_PlaceholderReattachesWhenSessionAppears guards the recovery
// direction: the sentinel must not be sticky, or the pane stays blank forever
// after the list has been emptied once.
func TestSessionsMsg_PlaceholderReattachesWhenSessionAppears(t *testing.T) {
	m := Model{
		cursor:           0,
		deletingIDs:      map[string]bool{},
		height:           100,
		currentSessionID: placeholderSessionID,
	}
	next, _ := m.updateListMode(sessionsMsg([]session.Info{
		{ID: "s1", Description: "one", Status: session.StatusIdle, TmuxWindowName: "jin-s1"},
	}))
	if got := next.(Model).currentSessionID; got == placeholderSessionID {
		t.Error("currentSessionID stayed on the placeholder; a new session never reaches the pane")
	}
}

func TestCurrentCursorSessionID_Cursor(t *testing.T) {
	m := Model{
		sessions: []session.Info{
			{ID: "s1", Description: "one"},
			{ID: "s2", Description: "two"},
			{ID: "s3", Description: "three"},
		},
		cursor:      1,
		deletingIDs: map[string]bool{},
	}
	if got := m.currentCursorSessionID(); got != "s2" {
		t.Errorf("cursor=1 → %q, want %q", got, "s2")
	}
}

func TestCurrentCursorSessionID_Deleting(t *testing.T) {
	m := Model{
		sessions: []session.Info{
			{ID: "s1", Description: "one"},
			{ID: "s2", Description: "two"},
		},
		cursor:      1,
		deletingIDs: map[string]bool{"s2": true},
	}
	if got := m.currentCursorSessionID(); got != "" {
		t.Errorf("cursor on deleting session → %q, want empty", got)
	}
}

func TestCurrentCursorSessionID_EmptyList(t *testing.T) {
	m := Model{
		sessions:    nil,
		cursor:      0,
		deletingIDs: map[string]bool{},
	}
	if got := m.currentCursorSessionID(); got != "" {
		t.Errorf("empty list → %q, want empty", got)
	}
}

// TestWriteCursorEnv_UpdatesTmux is a degraded guard test: with tmuxClient=nil
// (legacy mode / tests), writeCursorEnv must be a no-op. Real SetEnvironment
// wiring is covered by manual verification (03_todo.md V-009).
func TestWriteCursorEnv_UpdatesTmux(t *testing.T) {
	m := Model{
		sessions: []session.Info{
			{ID: "s1", Description: "one"},
		},
		cursor:      0,
		deletingIDs: map[string]bool{},
	}
	// Must not panic with a nil client.
	m.writeCursorEnv()
}

// Pin the tick intervals so a stray edit that lengthens envTickInterval (which
// would re-introduce the popup pickup lag this split was built to remove) or
// shortens sessionTickInterval (which would raise daemon-refetch churn) fails
// loudly instead of drifting silently.
func TestEnvTickInterval(t *testing.T) {
	if envTickInterval != 250*time.Millisecond {
		t.Errorf("envTickInterval = %v, want 250ms", envTickInterval)
	}
}

// --- confirm handshake env contract ---

// fakeEnvConsumer stands in for the envTick consume closure over a fixed env
// map: it returns each key's value once, records the read, and drops the key
// so a second read comes back empty — the same shape as the real closure,
// which unsets the key on tmux.
type fakeEnvConsumer struct {
	env      map[string]string
	consumed []string
}

func (f *fakeEnvConsumer) consume(key string) string {
	v := f.env[key]
	if v != "" {
		f.consumed = append(f.consumed, key)
		delete(f.env, key)
	}
	return v
}

func TestConsumeEnvRequests_FocusSession(t *testing.T) {
	f := &fakeEnvConsumer{env: map[string]string{"JIN_FOCUS_SESSION": "sess-xyz"}}

	req := consumeEnvRequests(f.consume)

	if req.focusSessionID != "sess-xyz" {
		t.Errorf("focusSessionID = %q, want %q", req.focusSessionID, "sess-xyz")
	}
	if req.answer.result != "" {
		t.Errorf("answer.result = %q, want empty (no confirm result in env)", req.answer.result)
	}
	if len(f.consumed) != 1 || f.consumed[0] != "JIN_FOCUS_SESSION" {
		t.Errorf("consumed = %v, want [JIN_FOCUS_SESSION]", f.consumed)
	}
}

func TestConsumeEnvRequests_EmptyEnvIsNoOp(t *testing.T) {
	f := &fakeEnvConsumer{env: map[string]string{}}

	req := consumeEnvRequests(f.consume)

	if req != (envRequests{}) {
		t.Errorf("consumeEnvRequests on an empty env = %+v, want zero value", req)
	}
	if len(f.consumed) != 0 {
		t.Errorf("consumed = %v, want none", f.consumed)
	}
}

// TestConsumeEnvRequests_ConfirmAnswer pins that an answered popup comes back
// whole and that all four JIN_CONFIRM_* keys leave the tmux env on the same
// tick. Anything left behind either re-applies on a later tick or mislabels
// the next prompt, and every result here destroys a session.
func TestConsumeEnvRequests_ConfirmAnswer(t *testing.T) {
	f := &fakeEnvConsumer{env: map[string]string{
		EnvConfirmResult:     ConfirmResultWorktree,
		EnvConfirmMode:       ConfirmModeDeleteWorktree,
		EnvConfirmTargetID:   "s1",
		EnvConfirmTargetDesc: "one",
	}}

	req := consumeEnvRequests(f.consume)

	want := confirmAnswer{mode: ConfirmModeDeleteWorktree, targetID: "s1", result: ConfirmResultWorktree}
	if req.answer != want {
		t.Errorf("answer = %+v, want %+v", req.answer, want)
	}
	assertConsumedSet(t, f.consumed, EnvConfirmResult, EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc)
}

// TestConsumeEnvRequests_FocusAndConfirmSameTick is the ordering invariant:
// the confirm answer is drained on the same pass as the focus IDs, before the
// caller's focus handling gets a chance to return early from the tick. Moving
// the confirm read after that early return would strand an approved
// destructive request in a tmux server that outlives this TUI process, and the
// next TUI would replay it.
func TestConsumeEnvRequests_FocusAndConfirmSameTick(t *testing.T) {
	f := &fakeEnvConsumer{env: map[string]string{
		"JIN_FOCUS_SESSION":  "sess-xyz",
		EnvConfirmResult:     ConfirmResultYes,
		EnvConfirmMode:       ConfirmModeDelete,
		EnvConfirmTargetID:   "s1",
		EnvConfirmTargetDesc: "one",
	}}

	req := consumeEnvRequests(f.consume)

	if req.focusSessionID != "sess-xyz" {
		t.Errorf("focusSessionID = %q, want %q", req.focusSessionID, "sess-xyz")
	}
	if req.answer.result != ConfirmResultYes {
		t.Errorf("answer.result = %q, want %q — the confirm answer must not wait for a later tick",
			req.answer.result, ConfirmResultYes)
	}
	assertConsumedSet(t, f.consumed,
		"JIN_FOCUS_SESSION", EnvConfirmResult, EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc)
	if len(f.env) != 0 {
		t.Errorf("keys left in env = %v, want none", f.env)
	}
}

// TestConsumeEnvRequests_DismissedPopup covers Ctrl+C: the popup writes no
// result, so there is nothing to act on and the prompt keys stay put for the
// next writeConfirmRequest (or the startup clear) to wipe.
func TestConsumeEnvRequests_DismissedPopup(t *testing.T) {
	f := &fakeEnvConsumer{env: map[string]string{
		EnvConfirmMode:       ConfirmModeDelete,
		EnvConfirmTargetID:   "s1",
		EnvConfirmTargetDesc: "one",
	}}

	req := consumeEnvRequests(f.consume)

	if req.answer.result != "" {
		t.Errorf("answer.result = %q, want empty — a dismissed popup approves nothing", req.answer.result)
	}
	if len(f.consumed) != 0 {
		t.Errorf("consumed = %v, want none", f.consumed)
	}
}

func assertConsumedSet(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("consumed = %v, want exactly %v", got, want)
	}
	for _, key := range want {
		if !slices.Contains(got, key) {
			t.Errorf("consumed = %v, missing %q", got, key)
		}
	}
}

// recordingEnv captures the outer-tmux env writes of the confirm handshake in
// order, standing in for *tmux.Client. It also applies them to env, so a test
// can ask what an interrupted handshake left behind on the tmux server rather
// than only what it attempted; failSet / failUnset name a key whose
// SetEnvironment / UnsetEnvironment fails, standing in for a tmux command that
// errors mid-handshake. Both halves need a failure injector: the write is
// fail-closed only if a failed *clear* also stops it, since a clear that gave
// up early is exactly how a stale key survives into the next request.
type recordingEnv struct {
	sessions  []string
	ops       []envOp
	env       map[string]string
	failSet   string
	failUnset string
}

type envOp struct {
	name  string
	value string
	unset bool
}

func (r *recordingEnv) SetEnvironment(session, name, value string) error {
	r.sessions = append(r.sessions, session)
	r.ops = append(r.ops, envOp{name: name, value: value})
	if name == r.failSet {
		return fmt.Errorf("set-environment %s: refused", name)
	}
	if r.env == nil {
		r.env = map[string]string{}
	}
	r.env[name] = value
	return nil
}

func (r *recordingEnv) UnsetEnvironment(session, name string) error {
	r.sessions = append(r.sessions, session)
	r.ops = append(r.ops, envOp{name: name, unset: true})
	if name == r.failUnset {
		return fmt.Errorf("set-environment -u %s: refused", name)
	}
	delete(r.env, name)
	return nil
}

// TestWriteConfirmRequest_ClearsEverythingFirst is the pairing guarantee: the
// whole handshake must be gone before any of this prompt's values land. An
// unconsumed answer would otherwise pair with the target being written here,
// and a dismissed prompt's leftovers (Ctrl+C writes no result, so its mode and
// target stay put by design) would survive a partial write and pair with it.
func TestWriteConfirmRequest_ClearsEverythingFirst(t *testing.T) {
	r := &recordingEnv{}

	if err := writeConfirmRequest(r, confirmRequest{mode: ConfirmModeKill, targetID: "s1", targetDesc: "one"}); err != nil {
		t.Fatalf("writeConfirmRequest: %v", err)
	}

	var cleared []string
	for _, op := range r.ops {
		if !op.unset {
			break
		}
		cleared = append(cleared, op.name)
	}
	for _, key := range []string{EnvConfirmResult, EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc} {
		if !slices.Contains(cleared, key) {
			t.Errorf("%s was not unset before the first value landed (leading unsets: %v)", key, cleared)
		}
	}
	for _, s := range r.sessions {
		if s != tmux.SessionName {
			t.Errorf("wrote to tmux session %q, want %q", s, tmux.SessionName)
		}
	}
}

// TestWriteConfirmRequest_Values pins each field to its own key. A swap here
// shows the user one session's name while dispatchConfirmResult deletes
// another, and both prompt and answer look internally consistent.
func TestWriteConfirmRequest_Values(t *testing.T) {
	r := &recordingEnv{}

	if err := writeConfirmRequest(r, confirmRequest{
		mode:       ConfirmModeDeleteWorktree,
		targetID:   "3f9c-id",
		targetDesc: "my-session",
	}); err != nil {
		t.Fatalf("writeConfirmRequest: %v", err)
	}

	want := map[string]string{
		EnvConfirmMode:       ConfirmModeDeleteWorktree,
		EnvConfirmTargetID:   "3f9c-id",
		EnvConfirmTargetDesc: "my-session",
	}
	if !maps.Equal(r.env, want) {
		t.Errorf("env after write = %v, want %v", r.env, want)
	}
}

// TestWriteConfirmRequest_PartialWriteLeavesNothingStale is the fail-closed
// property, driven through the exact sequence that made it necessary: a prompt
// for session A was dismissed with Ctrl+C, so A's mode and target are still in
// the tmux env, and the write for session B fails halfway. Without the leading
// clear (or with the errors discarded) the env ends up naming B while carrying
// A's ID, and the popup asks about a session the user can see while the answer
// destroys one they cannot.
//
// The env must come out empty, not merely free of A: the error is what stops
// openConfirmPopup from showing the popup, so whatever landed before the
// failure is a request nobody will ever answer — and one a later tick, or a
// hand-run `jin confirm-popup`, could still pick up.
func TestWriteConfirmRequest_PartialWriteLeavesNothingStale(t *testing.T) {
	for _, failKey := range []string{EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc} {
		t.Run("fails on "+failKey, func(t *testing.T) {
			r := &recordingEnv{
				env: map[string]string{
					EnvConfirmMode:       ConfirmModeDeleteWorktree,
					EnvConfirmTargetID:   "session-A",
					EnvConfirmTargetDesc: "A",
				},
				failSet: failKey,
			}

			err := writeConfirmRequest(r, confirmRequest{
				mode:       ConfirmModeDelete,
				targetID:   "session-B",
				targetDesc: "B",
			})

			if err == nil {
				t.Fatal("writeConfirmRequest = nil, want the failed SetEnvironment propagated")
			}
			if len(r.env) != 0 {
				t.Errorf("env after a failed write = %v, want empty — neither A's prompt nor B's fragments may survive", r.env)
			}
		})
	}
}

// TestWriteConfirmRequest_FailedClearWritesNothing covers the other way the
// handshake can be interrupted: the wipe itself fails. The clear is what turns
// a partial write into empty keys rather than a previous prompt's, so once it
// has failed there is no safe value left to write — writing the new mode on top
// of an unclearable old target is exactly the splice
// TestWriteConfirmRequest_PartialWriteLeavesNothingStale exists to prevent.
// Every key is still attempted, since each one left set is a usable fragment.
//
// The returned error is also what keeps openConfirmPopup from opening a popup
// over a half-cleared env: a prompt this process could not publish in full must
// not be shown.
func TestWriteConfirmRequest_FailedClearWritesNothing(t *testing.T) {
	r := &recordingEnv{
		env: map[string]string{
			EnvConfirmMode:       ConfirmModeDeleteWorktree,
			EnvConfirmTargetID:   "session-A",
			EnvConfirmTargetDesc: "A",
		},
		failUnset: EnvConfirmTargetID,
	}

	err := writeConfirmRequest(r, confirmRequest{
		mode:       ConfirmModeDelete,
		targetID:   "session-B",
		targetDesc: "B",
	})

	if err == nil {
		t.Fatal("writeConfirmRequest = nil, want the failed UnsetEnvironment propagated")
	}
	for _, op := range r.ops {
		if !op.unset {
			t.Errorf("wrote %s=%q after a failed clear, want no writes at all", op.name, op.value)
		}
	}
	unset := map[string]bool{}
	for _, op := range r.ops {
		if op.unset {
			unset[op.name] = true
		}
	}
	for _, key := range []string{EnvConfirmResult, EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc} {
		if !unset[key] {
			t.Errorf("%s was never attempted — a clear that stops at the first failure leaves fragments", key)
		}
	}
}

// TestClearConfirmEnv_UnsetsEverything guards the wipe: a key it forgets is a
// prompt or an approval that outlives whatever it was written for — the outer
// tmux server outlives the TUI process, so a survivor can be paired with a
// later request or replayed by the next run.
func TestClearConfirmEnv_UnsetsEverything(t *testing.T) {
	r := &recordingEnv{}

	if err := clearConfirmEnv(r, confirmEnvKeys); err != nil {
		t.Fatalf("clearConfirmEnv: %v", err)
	}

	unset := map[string]bool{}
	for _, op := range r.ops {
		if !op.unset {
			t.Errorf("clearConfirmEnv set %s=%q, want unsets only", op.name, op.value)
			continue
		}
		unset[op.name] = true
	}
	for _, key := range []string{EnvConfirmResult, EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc} {
		if !unset[key] {
			t.Errorf("%s was not unset", key)
		}
	}
}

// TestStaleConfirmKeys is the startup wipe's key selection: whatever the
// snapshot holds gets unset, and nothing else is touched. The narrowing is only
// safe because an absent key and an unset one are the same state — so what this
// pins is that every present key is returned, including a lone leftover.
func TestStaleConfirmKeys(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"clean startup", map[string]string{"JIN_CURRENT_SESSION": "s1"}, nil},
		{"nil env", nil, nil},
		{
			// A prompt dismissed with Ctrl+C in a previous run: no result, but
			// enough left behind for a hand-run popup to ask about it.
			name: "dismissed prompt",
			env: map[string]string{
				EnvConfirmMode:       ConfirmModeDelete,
				EnvConfirmTargetID:   "s1",
				EnvConfirmTargetDesc: "one",
			},
			want: []string{EnvConfirmMode, EnvConfirmTargetID, EnvConfirmTargetDesc},
		},
		{
			// The dangerous one: an approval the previous run never got to
			// consume, which this run's first envTick would carry out.
			name: "unconsumed answer",
			env: map[string]string{
				EnvConfirmResult:     ConfirmResultYes,
				EnvConfirmMode:       ConfirmModeKill,
				EnvConfirmTargetID:   "s1",
				EnvConfirmTargetDesc: "one",
			},
			want: confirmEnvKeys,
		},
		{"result alone", map[string]string{EnvConfirmResult: ConfirmResultYes}, []string{EnvConfirmResult}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := staleConfirmKeys(tt.env)
			if len(got) != len(tt.want) {
				t.Fatalf("staleConfirmKeys = %v, want %v", got, tt.want)
			}
			for _, key := range tt.want {
				if !slices.Contains(got, key) {
					t.Errorf("staleConfirmKeys = %v, missing %q", got, key)
				}
			}
		})
	}
}

// resolveFocusSession is the shared fast/slow path helper. These tests pin
// the three branches the envTick fast path and the sessionsMsg slow path
// both depend on: no-op when nothing is pending, cursor-align + clear on
// hit, and preserve focusSessionID on miss so the caller can retry (fast
// path) or explicitly give up (slow path).

func TestResolveFocusSession_EmptyID_ReturnsTrue(t *testing.T) {
	m := &Model{
		sessions: []session.Info{{ID: "a"}, {ID: "b"}},
		cursor:   1,
	}
	if !m.resolveFocusSession() {
		t.Errorf("resolveFocusSession() = false, want true (nothing pending)")
	}
	// Cursor must not move on the no-op path — the helper is called from
	// envTick every 250ms and any drift would silently scroll the list.
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (unchanged)", m.cursor)
	}
}

func TestResolveFocusSession_TargetInSessions_Switches(t *testing.T) {
	m := &Model{
		sessions:         []session.Info{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		focusSessionID:   "b",
		cursor:           0,
		currentSessionID: "a",
	}
	if !m.resolveFocusSession() {
		t.Fatalf("resolveFocusSession() = false, want true (target present)")
	}
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (aligned to target index)", m.cursor)
	}
	if m.focusSessionID != "" {
		t.Errorf("focusSessionID = %q, want \"\" (cleared on hit)", m.focusSessionID)
	}
	// tmuxClient is nil in this test, so switchToSession is a no-op and
	// currentSessionID stays as the forced-reset value. Pin the reset so a
	// future refactor cannot silently drop it.
	if m.currentSessionID != "" {
		t.Errorf("currentSessionID = %q, want \"\" (forced reset before switch)", m.currentSessionID)
	}
}

func TestResolveFocusSession_TargetMissing_ReturnsFalse(t *testing.T) {
	m := &Model{
		sessions: []session.Info{
			{ID: "a"},
			{ID: "b"},
		},
		focusSessionID:   "ghost",
		cursor:           1,
		currentSessionID: "b",
	}
	if m.resolveFocusSession() {
		t.Errorf("resolveFocusSession() = true, want false (target absent)")
	}
	if m.focusSessionID != "ghost" {
		t.Errorf("focusSessionID = %q, want \"ghost\" (retained for retry)", m.focusSessionID)
	}
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (unchanged on miss)", m.cursor)
	}
	if m.currentSessionID != "b" {
		t.Errorf("currentSessionID = %q, want \"b\" (unchanged on miss)", m.currentSessionID)
	}
}

func TestSessionTickInterval(t *testing.T) {
	if sessionTickInterval != 2*time.Second {
		t.Errorf("sessionTickInterval = %v, want 2s", sessionTickInterval)
	}
}

func TestEnvTickCmd_NonNil(t *testing.T) {
	if envTickCmd() == nil {
		t.Fatal("envTickCmd returned nil")
	}
}

func TestSessionTickCmd_NonNil(t *testing.T) {
	if sessionTickCmd() == nil {
		t.Fatal("sessionTickCmd returned nil")
	}
}

// --- openPopup / popupDisplayOptions ---

// TestOpenPopup_LooksUpSizeByName verifies that each core popup name
// resolves its width/height via configMgr.GetPopupSize (matching
// config.DefaultPopupSizes for a Manager with no user overrides) and that
// the subcmd is built as "<name>-popup" with underscores hyphenated to
// match the registered cobra subcommand (e.g. "session-filter-popup").
func TestOpenPopup_LooksUpSizeByName(t *testing.T) {
	configMgr, err := config.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	m := Model{configMgr: configMgr, deletingIDs: map[string]bool{}}

	defaults := config.DefaultPopupSizes()
	tests := []struct {
		name       string
		wantSubcmd string
	}{
		{"create", "create-popup"},
		{"session_filter", "session-filter-popup"},
		{"help", "help-popup"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := m.popupDisplayOptions(tt.name, " Title ")

			def := defaults[tt.name]
			wantWidth := fmt.Sprintf("%d%%", def.Width)
			wantHeight := fmt.Sprintf("%d%%", def.Height)
			if opts.Width != wantWidth {
				t.Errorf("Width = %q, want %q", opts.Width, wantWidth)
			}
			if opts.Height != wantHeight {
				t.Errorf("Height = %q, want %q", opts.Height, wantHeight)
			}
			if !strings.HasSuffix(opts.Cmd, " "+tt.wantSubcmd) {
				t.Errorf("Cmd = %q, want suffix %q", opts.Cmd, tt.wantSubcmd)
			}
			if opts.Title != " Title " {
				t.Errorf("Title = %q, want %q", opts.Title, " Title ")
			}
		})
	}
}

// TestOpenPopup_NoConfigMgr_NoOp verifies the configMgr=nil safeguard: an
// unwired Model (tmuxClient and configMgr both nil — the legacy/test path)
// must not panic when opening a popup, since popupDisplayOptions dereferences
// configMgr.GetPopupSize.
func TestOpenPopup_NoConfigMgr_NoOp(t *testing.T) {
	m := Model{deletingIDs: map[string]bool{}}
	m.openPopup("create", " New Session ")
}

// --- adoptAttachedSession / pollAttachedSessionCmd ---
//
// These run with tmuxClient=nil (the degraded-guard style used across this
// file): the tmux side-effects (SetEnvironment, @session_name) are skipped, so
// the tests observe only the in-memory state adoption. Real tmux wiring and the
// end-to-end choose-tree follow are covered by manual verification (03_todo.md
// V-001/V-002).

// TestAdoptAttachedSession covers the in-memory adoption paths: the happy
// path, cursor tracking of the display index (V-007's reachable half), the
// steady-state no-op that keeps the poll from thrashing the cursor every tick,
// unknown names (V-004), empty attach names (V-006, client dead / poll race),
// and a late msg after leaving local attach. The off-list half of V-007
// (adopted session hidden by a filter: ID adopted, cursor kept) is not
// black-box reachable while getDisplaySessions() returns m.sessions
// unfiltered; moveCursorToSession's guard covers it structurally.
func TestAdoptAttachedSession(t *testing.T) {
	sessions := []session.Info{
		{ID: "s1", TmuxWindowName: "jin-s1", Description: "one"},
		{ID: "s2", TmuxWindowName: "jin-s2", Description: "two"},
		{ID: "s3", TmuxWindowName: "jin-s3", Description: "three"},
	}
	cases := []struct {
		name        string
		attached    string
		localAttach bool
		startID     string
		startCursor int
		wantID      string
		wantCursor  int
	}{
		{"follows another session", "jin-s2", true, "s1", 0, "s2", 1},
		{"cursor tracks display index", "jin-s3", true, "s1", 0, "s3", 2},
		{"already in sync", "jin-s2", true, "s2", 1, "s2", 1},
		{"unknown name", "jin-unknown", true, "s1", 0, "s1", 0},
		{"empty attach name", "", true, "s1", 0, "s1", 0},
		{"not local attach", "jin-s2", false, "s1", 0, "s1", 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				sessions:           sessions,
				currentSessionID:   tt.startID,
				cursor:             tt.startCursor,
				displayLocalAttach: tt.localAttach,
			}
			m.adoptAttachedSession(tt.attached)
			if m.currentSessionID != tt.wantID || m.cursor != tt.wantCursor {
				t.Errorf("got id=%q cursor=%d, want id=%q cursor=%d",
					m.currentSessionID, m.cursor, tt.wantID, tt.wantCursor)
			}
		})
	}
}

// TestPollAttachedSessionCmd_Guards covers V-005: the poll Cmd is only issued
// when the display pane is locally attached with both tmux clients wired and a
// known pane ID. Building the Cmd runs no tmux command (that happens only when
// the closure fires), so the all-satisfied case can assert non-nil with
// zero-value clients; only the actual tmux round-trip is manual (03_todo.md
// V-001).
func TestPollAttachedSessionCmd_Guards(t *testing.T) {
	cases := []struct {
		name        string
		localAttach bool
		paneID      string
		outer       *tmux.Client
		inner       *tmux.Client
		wantNil     bool
	}{
		{"not local attach", false, "%1", &tmux.Client{}, &tmux.Client{}, true},
		{"empty pane id", true, "", &tmux.Client{}, &tmux.Client{}, true},
		{"nil outer client", true, "%1", nil, &tmux.Client{}, true},
		{"nil inner client", true, "%1", &tmux.Client{}, nil, true},
		{"neither", false, "", nil, nil, true},
		{"all satisfied", true, "%1", &tmux.Client{}, &tmux.Client{}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				displayLocalAttach: tt.localAttach,
				displayPaneID:      tt.paneID,
				tmuxClient:         tt.outer,
				innerTmuxClient:    tt.inner,
				deletingIDs:        map[string]bool{},
			}
			c := m.pollAttachedSessionCmd()
			if tt.wantNil && c != nil {
				t.Errorf("expected nil Cmd, got %T", c)
			}
			if !tt.wantNil && c == nil {
				t.Error("expected non-nil Cmd, got nil")
			}
		})
	}
}
