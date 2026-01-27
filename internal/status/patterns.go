package status

// DetectedStatus represents a detected status string
type DetectedStatus string

const (
	StatusPermission DetectedStatus = "permission"
	StatusThinking   DetectedStatus = "thinking"
	StatusIdle       DetectedStatus = "idle"
	StatusError      DetectedStatus = "error"
	StatusTrust      DetectedStatus = "trust" // 初回ディレクトリtrust確認
)

// Pattern defines a status detection pattern
// Patterns are checked in order: permission > thinking > error
// If none match, status remains unchanged (not forced to idle)
type Pattern struct {
	Status   DetectedStatus
	Patterns []string
}

// DefaultPatterns returns the default Claude Code output patterns
// Based on the original shell script's state detection logic
func DefaultPatterns() []Pattern {
	return []Pattern{
		// Trust confirmation - highest priority (initial directory trust)
		// Case-sensitive matching: patterns should match Claude Code's actual text
		{
			Status: StatusTrust,
			Patterns: []string{
				"Do you trust the files in this folder",
				"Do you trust this project directory",
				"can pose security risks",
				"Yes, proceed",
				"Only use files from trusted sources",
				"only use files from trusted sources",
			},
		},
		// Permission/waiting input - high priority
		{
			Status: StatusPermission,
			Patterns: []string{
				"Do you want",
				"Would you like",
				"do you want",
				"would you like",
				"Allow",
				"Deny",
				"Yes, allow",
				"[Y/n]",
				"[y/N]",
				"approve",
				"permit",
				"許可",
			},
		},
		// Thinking/busy - Claude Code activity indicators
		// Spinner text patterns indicate thinking state
		{
			Status: StatusThinking,
			Patterns: []string{
				"esc to interrupt", // Shown during all processing
				"Computing…",      // Spinner text
				"Precipitating…",  // Spinner text
				"Thinking…",       // Spinner text
				"Processing…",     // Spinner text
				"Working…",        // Spinner text
				"Loading…",        // Spinner text
				"Reasoning…",      // Spinner text
				"Analyzing…",      // Spinner text
				"✻",               // Spinner character
			},
		},
		// Error detection
		{
			Status: StatusError,
			Patterns: []string{
				"Error:",
				"error:",
				"failed",
				"Exception",
				"panic:",
			},
		},
		// Idle detection - input prompt symbols
		// Claude Code shows "❯ " when waiting for user input
		// Note: Claude uses non-breaking space (\u00a0) after the prompt symbol
		{
			Status: StatusIdle,
			Patterns: []string{
				"❯\u00a0",  // Primary prompt: heavy right-pointing angle + non-breaking space
				"❯ ",       // Alternative: heavy right-pointing angle + regular space
				">\u00a0",  // Fallback: greater-than + non-breaking space
				"> ",       // Fallback: greater-than + regular space
				"│ > ",     // Input prompt in box (legacy)
				"│ ❯ ",     // Alternative prompt symbol in box
			},
		},
	}
}
