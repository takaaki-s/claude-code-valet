package config

import "strings"

// normalizeTmuxKey coerces both tmux bind-key notation ("M-f", "C-]", "S-Tab")
// and the more common "+"-separated form ("ctrl+f", "alt+f", "shift+tab") into
// the tmux form. Modifier names are matched case-insensitively; the trailing key
// token is preserved verbatim so tmux's own case sensitivity ("M-p" vs "M-P") is
// not smoothed away. Any input lacking a "+" is returned unchanged, so this is
// safe to fan through every outer-tmux key config value. An unknown modifier
// segment also passes through unchanged, so tmux surfaces the real error rather
// than silently binding a mangled key.
func normalizeTmuxKey(s string) string {
	if s == "" || !strings.Contains(s, "+") {
		return s
	}
	parts := strings.Split(s, "+")
	if len(parts) < 2 {
		return s
	}
	var mods []string
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "ctrl", "control", "c":
			mods = append(mods, "C-")
		case "alt", "meta", "m":
			mods = append(mods, "M-")
		case "shift", "s":
			mods = append(mods, "S-")
		default:
			return s
		}
	}
	return strings.Join(mods, "") + parts[len(parts)-1]
}

// normalizeTmuxKeys applies normalizeTmuxKey to each slice element,
// returning a fresh slice. A nil / empty input returns the input as-is so
// the getter-level nil↔empty semantics (see GetTogglePaneKeys) are not
// accidentally collapsed.
func normalizeTmuxKeys(keys []string) []string {
	if len(keys) == 0 {
		return keys
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = normalizeTmuxKey(k)
	}
	return out
}
