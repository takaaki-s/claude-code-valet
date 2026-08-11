package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// FuzzyMatch is one result row from FuzzyFilter. Index is the position in
// the caller-provided targets slice; MatchedIndexes are rune positions in
// the corresponding target that participated in the fuzzy match. For empty
// queries MatchedIndexes is nil so callers can take a fast render path.
type FuzzyMatch struct {
	Index          int
	MatchedIndexes []int
}

// FuzzyFilter runs sahilm/fuzzy.Find over targets and returns index +
// matched-rune positions per hit. An empty query short-circuits to all targets
// in their original order (MatchedIndexes nil) — sahilm/fuzzy would otherwise
// report zero results, losing the "no filter typed" ordering callers expect.
func FuzzyFilter(query string, targets []string) []FuzzyMatch {
	q := strings.TrimSpace(query)
	if q == "" {
		out := make([]FuzzyMatch, len(targets))
		for i := range targets {
			out[i] = FuzzyMatch{Index: i}
		}
		return out
	}
	hits := fuzzy.Find(q, targets)
	out := make([]FuzzyMatch, len(hits))
	for i, h := range hits {
		out[i] = FuzzyMatch{Index: h.Index, MatchedIndexes: h.MatchedIndexes}
	}
	return out
}

// BuildActionHaystack joins a palette action's Label and Description into the
// single haystack FuzzyFilter matches against. A space separates the two
// because sahilm/fuzzy stops matching across NUL bytes, which would otherwise
// drop description hits entirely. An empty Description keeps just the label.
func BuildActionHaystack(label, description string) string {
	if description == "" {
		return label
	}
	return label + " " + description
}

// RenderMatchedLine renders target runes with matchedIndexes highlighted,
// truncating with an ellipsis if the target exceeds maxWidth. Highlights past
// the truncation boundary are dropped silently.
//
// matched is expected to be sorted ascending (sahilm/fuzzy guarantees this), so
// it is walked with a two-pointer cursor instead of a lookup map, and runs of
// adjacent hits coalesce into a single style.Render call.
func RenderMatchedLine(target []rune, matched []int, maxWidth int, style lipgloss.Style, selected bool) string {
	if maxWidth <= 0 {
		return ""
	}
	// Truncate rune-wise. maxWidth here is a display width, but callers pass
	// mostly-ASCII targets, so a rune-count fallback is close enough for the
	// popup and keeps the highlight index math simple.
	visible := target
	truncated := false
	if len(visible) > maxWidth {
		if maxWidth > 3 {
			visible = visible[:maxWidth-3]
			truncated = true
		} else {
			visible = visible[:maxWidth]
		}
	}

	// Fast path: no highlights, or selected rows (which suppress the fuzzy
	// underline to avoid clashing with the cursorStyle background).
	if len(matched) == 0 || selected {
		if truncated {
			return string(visible) + "..."
		}
		return string(visible)
	}

	var b strings.Builder
	j := 0 // cursor into matched
	i := 0
	for i < len(visible) {
		// Advance j past any hits that fell outside visible.
		for j < len(matched) && matched[j] < i {
			j++
		}
		if j < len(matched) && matched[j] == i {
			// Collect one contiguous run of hits.
			runStart := i
			for j < len(matched) && matched[j] == i && i < len(visible) {
				i++
				j++
			}
			b.WriteString(style.Render(string(visible[runStart:i])))
			continue
		}
		// Collect one contiguous plain run up to the next hit (or end).
		plainStart := i
		next := len(visible)
		if j < len(matched) && matched[j] < next {
			next = matched[j]
		}
		if next > len(visible) {
			next = len(visible)
		}
		i = next
		b.WriteString(string(visible[plainStart:i]))
	}
	if truncated {
		b.WriteString("...")
	}
	return b.String()
}

// RenderMatchedSegment is a companion to RenderMatchedLine for callers that
// paint per-piece with a base style (the switch-session card, whose
// dir/branch/kind columns each get a different color). matched carries the
// caller's haystack-wide match indexes; matchOffset is where this segment
// starts in that haystack. Only indexes inside the segment participate, rebased
// to segment-local rune positions on the fly.
//
// Plain runs render under base; hit runs under base.Underline(true), so the
// fuzzy hint composes with the segment color instead of clashing with a fixed
// highlight foreground. Truncation math mirrors RenderMatchedLine.
func RenderMatchedSegment(target []rune, matched []int, matchOffset, maxWidth int, base lipgloss.Style) string {
	if maxWidth <= 0 {
		return ""
	}
	visible := target
	truncated := false
	if len(visible) > maxWidth {
		if maxWidth > 3 {
			visible = visible[:maxWidth-3]
			truncated = true
		} else {
			visible = visible[:maxWidth]
		}
	}
	// Advance j to the first haystack index that could land inside this
	// segment (matched is sorted). matchEnd caps the window on the other side.
	matchEnd := matchOffset + len(visible)
	j := 0
	for j < len(matched) && matched[j] < matchOffset {
		j++
	}
	// Fast path when no hits touch this segment.
	if j >= len(matched) || matched[j] >= matchEnd {
		if truncated {
			return base.Render(string(visible) + "...")
		}
		return base.Render(string(visible))
	}
	hlStyle := base.Underline(true)

	var b strings.Builder
	i := 0 // rune cursor within `visible`
	for i < len(visible) {
		// Skip hits before i (should be rare; only if callers pass mis-sorted).
		for j < len(matched) && matched[j]-matchOffset < i {
			j++
		}
		if j < len(matched) && matched[j]-matchOffset == i && matched[j] < matchEnd {
			runStart := i
			for j < len(matched) && matched[j] < matchEnd && matched[j]-matchOffset == i && i < len(visible) {
				i++
				j++
			}
			b.WriteString(hlStyle.Render(string(visible[runStart:i])))
			continue
		}
		plainStart := i
		next := len(visible)
		if j < len(matched) && matched[j] < matchEnd {
			if local := matched[j] - matchOffset; local < next {
				next = local
			}
		}
		if next > len(visible) {
			next = len(visible)
		}
		i = next
		b.WriteString(base.Render(string(visible[plainStart:i])))
	}
	if truncated {
		b.WriteString(base.Render("..."))
	}
	return b.String()
}
